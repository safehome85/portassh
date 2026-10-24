// portassh: portable SSH access without admin rights.
// Runs an SSH server on loopback and a reverse tunnel to the relay host.
// All credentials are baked in at build time (baked.go): run and connect, nothing to set up.
package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	gpty "github.com/aymanbagabas/go-pty"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/text/transform"
)

type config struct {
	relay               string
	relayUser           string
	relayPort           int
	localPort           int
	shell               string
	role                string // "user" или "admin"
	allowedClientPubKey string
}

const (
	roleUser  = "user"
	roleAdmin = "admin"

	defaultRPortAdmin = 2201 // админ-экземпляр держит свой порт на relay
	defaultLPortAdmin = 2223
)

func main() {
	log.SetFlags(log.Ltime)
	relay := flag.String("relay", defaultRelay, "адрес relay-сервера host[:22]")
	rport := flag.Int("rport", defaultRPort, "порт на relay (занят — берётся следующий)")
	lport := flag.Int("lport", defaultLPort, "локальный порт SSH-сервера (только 127.0.0.1)")
	shell := flag.String("shell", defaultShell(), "shell для сессий")
	admin := flag.Bool("admin", false, "поднять админ-экземпляр (окно UAC, второй туннель: relay-порт 2201)")
	elevated := flag.Bool("elevated", false, "(внутренний) экземпляр уже повышен через UAC")
	flag.Parse()

	role := roleUser
	if *admin || *elevated {
		role = roleAdmin
		*rport, *lport = applyRoleDefaults(true, *rport, *lport)
	}

	if *admin && !isElevated() {
		// перезапуск себя же через UAC; дальше работает повышенный экземпляр
		if err := elevateSelf("-elevated"); err != nil {
			log.Print(err)
			return
		}
		return
	}

	cfg := config{
		relay:               *relay,
		relayUser:           defaultRelayUser,
		relayPort:           *rport,
		localPort:           *lport,
		shell:               *shell,
		role:                role,
		allowedClientPubKey: allowedClientPubKey,
	}

	// лог в файл рядом с exe (при скрытом окне это единственный вывод)
	if lf, err := os.OpenFile(filepath.Join(exeDir(), "portassh.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, lf))
	}
	if role == roleAdmin {
		log.SetPrefix("[admin] ")
	}

	fmt.Println("=== portassh ===")
	fmt.Printf("Запущен (%s). Жду туннель через relay...\n", role)

	// рабочая папка = профиль пользователя: сюда scp/sftp кладут файлы
	if home, err := os.UserHomeDir(); err == nil {
		_ = os.Chdir(home)
	}

	go func() {
		for {
			if err := startServer(cfg); err != nil {
				log.Printf("сервер: %v; перезапуск через 5с", err)
			} else {
				log.Print("сервер остановлен; перезапуск через 5с")
			}
			time.Sleep(5 * time.Second)
		}
	}()
	relayLoop(cfg)
}

func defaultShell() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// applyRoleDefaults: админ-экземпляр по умолчанию держит свои порты,
// но явные пользовательские значения не трогает.
func applyRoleDefaults(adminOrElevated bool, rport, lport int) (int, int) {
	if adminOrElevated {
		if rport == defaultRPort {
			rport = defaultRPortAdmin
		}
		if lport == defaultLPort {
			lport = defaultLPortAdmin
		}
	}
	return rport, lport
}

// --- SSH server ---

func startServer(cfg config) error {
	log.Printf("SSH-сервер слушает %s", newServerAddr(cfg))
	return newServer(cfg).ListenAndServe()
}

func newServerAddr(cfg config) string {
	if cfg.relay == "" {
		return fmt.Sprintf("0.0.0.0:%d", cfg.localPort) // без туннеля — для локальной сети; Windows спросит про фаервол
	}
	return fmt.Sprintf("127.0.0.1:%d", cfg.localPort)
}

// samePubKey сравнивает строки authorized_keys по типу и base64 (без комментария).
func samePubKey(a, b string) bool {
	fa, fb := strings.Fields(string(a)), strings.Fields(string(b))
	return len(fa) >= 2 && len(fb) >= 2 && fa[0] == fb[0] && fa[1] == fb[1]
}

// goSafe: паника в горутине не должна убивать весь процесс (ISSUES.md п.1) —
// глотаем, пишем стек в лог, туннель и остальные сессии продолжают работать.
func goSafe(what string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic в %s: %v\n%s", what, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// safeHandler — то же для обработчиков SSH-сессий.
func safeHandler(h ssh.Handler) ssh.Handler {
	return func(s ssh.Session) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic в сессии: %v\n%s", r, debug.Stack())
				fmt.Fprintf(s, "portassh: внутренняя ошибка сессии\r\n")
			}
		}()
		h(s)
	}
}

// safeSubsys — обёртка для подсистем (sftp): тип SubsystemHandler отличается от Handler.
func safeSubsys(h ssh.SubsystemHandler) ssh.SubsystemHandler {
	return func(s ssh.Session) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic в подсистеме: %v\n%s", r, debug.Stack())
				fmt.Fprintf(s, "portassh: внутренняя ошибка подсистемы\r\n")
			}
		}()
		h(s)
	}
}

func newServer(cfg config) *ssh.Server {
	hostSigner := mustLoadBakedSigner(serverHostKeyB64)
	return &ssh.Server{
		Addr:        newServerAddr(cfg),
		Handler:     safeHandler(sessionHandler(cfg)),
		HostSigners: []ssh.Signer{hostSigner},
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": safeSubsys(sftpHandler), // scp/sftp: приём и отдача файлов без кавычечного ада
		},
		PublicKeyHandler: func(_ ssh.Context, key ssh.PublicKey) bool {
			return samePubKey(string(gossh.MarshalAuthorizedKey(key)), cfg.allowedClientPubKey)
		},
	}
}

func sftpHandler(s ssh.Session) {
	server, err := sftp.NewServer(s)
	if err != nil {
		log.Printf("sftp: %v", err)
		s.Exit(1)
		return
	}
	if err := server.Serve(); err != nil && err != io.EOF {
		log.Printf("sftp: %v", err)
	}
	_ = server.Close()
}

func sessionHandler(cfg config) ssh.Handler {
	return func(s ssh.Session) {
		if len(s.Command()) > 0 {
			runExec(s, cfg, s.RawCommand()) // сырая строка клиента — кавычки не пересобираются
			return
		}
		ptyReq, winCh, isPty := s.Pty()
		if !isPty {
			io.WriteString(s, "portassh: требуется PTY\n")
			s.Exit(1)
			return
		}
		// go-pty: на Windows это ConPTY (creack/pty Windows не поддерживает вовсе),
		// на Unix — классический pty. Resize(width, height).
		pt, err := gpty.New()
		if err != nil {
			io.WriteString(s, err.Error())
			s.Exit(1)
			return
		}
		// один-единственный Close: двойное закрытие ConPTY/пайпов недопустимо
		var closeOnce sync.Once
		closePty := func() { closeOnce.Do(func() { _ = pt.Close() }) }
		defer closePty()
		_ = pt.Resize(ptyReq.Window.Width, ptyReq.Window.Height)
		cmd := pt.Command(cfg.shell)
		cmd.Env = append(os.Environ(), "TERM="+ptyReq.Term)
		if err := cmd.Start(); err != nil {
			io.WriteString(s, err.Error())
			s.Exit(1)
			return
		}
		done := make(chan struct{})
		goSafe("pty-resize", func() {
			defer close(done)
			for win := range winCh {
				_ = pt.Resize(win.Width, win.Height)
			}
		})
		ctx := s.Context()
		goSafe("pty-ctx", func() {
			<-ctx.Done()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
		goSafe("pty-stdin", func() { _, _ = io.Copy(pt, s) })
		copyDone := make(chan struct{})
		goSafe("pty-stdout", func() {
			defer close(copyDone)
			_, _ = io.Copy(s, pt)
		})
		_ = cmd.Wait()
		// ConPTY не отдаёт EOF при выходе процесса — дозаливаем хвост и закрываем
		time.Sleep(300 * time.Millisecond)
		closePty()
		<-copyDone
	}
}

// spawnRetryDelay — пауза между попытками запуска (ISSUES.md п.2: AV-блокировки
// снимаются через 10–30 с). Переопределяется в тестах.
var spawnRetryDelay = 3 * time.Second

func runExec(s ssh.Session, cfg config, raw string) {
	// makeCmd: exec.Cmd можно запустить только один раз — пересобираем на каждую попытку.
	makeCmd := func() *exec.Cmd {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			if strings.EqualFold(cfg.shell, "cmd.exe") {
				// Сырая командная строка через SysProcAttr.CmdLine: Go не применяет
				// MSVCRT-экранирование к аргументам (ISSUES.md регрессия), и
				// квотирование клиента доходит до cmd 1:1. /d отключает AutoRun.
				cmd = exec.Command(cfg.shell)
				setCmdLine(cmd, `cmd /d /s /c "`+raw+`"`)
			} else {
				cmd = exec.Command(cfg.shell, "/c", raw)
			}
			// вывод cmd/powershell идёт в OEM-кодировке (CP866 на русской Windows) —
			// декодируем в UTF-8 на лету вместо спавна chcp
			if dec := oemDecoder(); dec != nil {
				out := transform.NewWriter(s, dec)
				cmd.Stdout, cmd.Stderr = out, out
				cmd.Stdin = s
				return cmd
			}
		} else {
			cmd = exec.Command(cfg.shell, "-c", raw)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, s
		return cmd
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		err := makeCmd().Run()
		if err == nil {
			s.Exit(0)
			return
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// команда стартовала и упала сама — ретраить бессмысленно
			s.Exit(ee.ExitCode())
			return
		}
		// даже не запустилось (антивирус, права, нет файла) — ретраим и сообщаем
		lastErr = err
		log.Printf("exec %q: попытка %d: %v", raw, attempt, err)
		if attempt < 3 {
			time.Sleep(spawnRetryDelay)
		}
	}
	fmt.Fprintf(s, "portassh: не удалось запустить %q: %v\n", raw, lastErr)
	s.Exit(127)
}

// --- reverse tunnel to relay ---

func relayLoop(cfg config) {
	port := cfg.relayPort
	delay := 5 * time.Second
	for {
		chosen, err := relayOnce(cfg, port)
		port = chosen
		if err != nil {
			log.Printf("tunnel: %v; повтор через %s", err, delay)
			time.Sleep(delay)
			// экспоненциальный backoff с потолком 30с (ISSUES.md п.5)
			if delay < 30*time.Second {
				delay *= 2
			}
			continue
		}
		delay = 5 * time.Second
		time.Sleep(5 * time.Second)
	}
}

func relayOnce(cfg config, wantPort int) (int, error) {
	signer := mustLoadBakedSigner(relayPrivKeyB64)
	hostKey, err := bakedRelayHostKey()
	if err != nil {
		return wantPort, err
	}
	addr := cfg.relay
	if !strings.Contains(addr, ":") {
		addr += ":22"
	}
	client, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:              cfg.relayUser,
		Auth:              []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback:   gossh.FixedHostKey(hostKey),
		HostKeyAlgorithms: []string{gossh.KeyAlgoED25519}, // запечён только ed25519-ключ relay
		Timeout:           10 * time.Second,
	})
	if err != nil {
		return wantPort, err
	}
	defer client.Close()

	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}()

	local := fmt.Sprintf("127.0.0.1:%d", cfg.localPort)
	var ln net.Listener
	port := wantPort
	for p := wantPort; p <= wantPort+99; p++ { // занят — пробуем следующий (до 100 машин)
		port = p
		ln, err = client.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			break
		}
	}
	if err != nil {
		return port, fmt.Errorf("remote listen: %w (запрещён проброс на relay?)", err)
	}
	defer ln.Close()
	log.Printf("tunnel: подключено к %s как %s, проброс 127.0.0.1:%d -> %s",
		addr, cfg.relayUser, port, local)

	cfg.relayPort = port
	writeConnectionFile(cfg)
	registerOnRelay(client, cfg)

	fmt.Printf("\n=== ГОТОВО ===\nПодключение с вашего компьютера:\n  ssh -o StrictHostKeyChecking=accept-new -J root@%s -p %d anyuser@localhost\n",
		cfg.relay, port)

	acceptCh := make(chan net.Conn)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				close(acceptCh)
				return
			}
			acceptCh <- c
		}
	}()
	waitCh := make(chan error, 1)
	go func() { waitCh <- client.Wait() }()
	for {
		select {
		case err := <-waitCh:
			return port, fmt.Errorf("relay закрыл соединение: %v", err)
		case c, ok := <-acceptCh:
			if !ok {
				return port, fmt.Errorf("relay закрыл соединение")
			}
			goSafe("forward", func() { forward(c, local) })
		}
	}
}

// --- delivery of connection info ---

func exeDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

func connectCommand(relay string, port int) string {
	return fmt.Sprintf("ssh -o StrictHostKeyChecking=accept-new -J root@%s -p %d anyuser@localhost", relay, port)
}

func writeConnectionFile(cfg config) {
	name := "portassh_connection.txt"
	if cfg.role == roleAdmin {
		name = "portassh_connection_admin.txt"
	}
	p := filepath.Join(exeDir(), name)
	_ = os.WriteFile(p, []byte(connectCommand(cfg.relay, cfg.relayPort)+"\n"), 0o600)
}

// registryLine: "имя_ПК порт время роль" — порт всегда поле 2 (pconnectgo совместим).
func registryLine(host string, port int, role string, when time.Time) string {
	return fmt.Sprintf("%s %d %s %s", sanitizeLine(host), port, when.Format("02.01_15:04"), role)
}

// registerOnRelay дописывает строку в ~/online.txt на relay, чтобы видеть
// список машин и их ролей одной командой со своего компьютера.
func registerOnRelay(client *gossh.Client, cfg config) {
	host, err := os.Hostname()
	if err != nil {
		return
	}
	line := registryLine(host, cfg.relayPort, cfg.role, time.Now())
	sess, err := client.NewSession()
	if err != nil {
		return
	}
	defer sess.Close()
	_ = sess.Run(fmt.Sprintf("echo '%s' >> ~/online.txt", line))
}

func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return -1
	}, s)
}

func forward(c net.Conn, target string) {
	defer c.Close()
	up, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		log.Printf("forward: %v", err)
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); c.Close(); up.Close(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); c.Close(); up.Close(); done <- struct{}{} }()
	<-done
}

// --- baked credentials ---

func mustLoadBakedSigner(b64 string) gossh.Signer {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := gossh.ParsePrivateKey(raw)
	if err != nil {
		log.Fatal(err)
	}
	return signer
}

func bakedRelayHostKey() (gossh.PublicKey, error) {
	_, _, pub, _, _, err := gossh.ParseKnownHosts([]byte(relayHostKeyLine))
	return pub, err
}
