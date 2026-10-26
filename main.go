// portassh: portable SSH access for Windows machines without admin rights.
// Runs an SSH server on loopback and a reverse tunnel to a relay host.
// All credentials are baked in at build time (baked.go): run and connect, nothing to set up.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gpty "github.com/aymanbagabas/go-pty"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/text/transform"
)

type config struct {
	relay                 string
	relayUser             string
	relayPort             int
	localPort             int
	shell                 string
	role                  string // "user" or "admin"
	allowedClientPubKeys  []string
	allowedClientPassword string
	noUpdate              bool
}

var buildStamp = "dev" // переопределяется при сборке: -ldflags "-X portassh.buildStamp=..."

// activeClient — текущий SSH-клиент к relay: обработчик событий питания
// (power_windows.go) закрывает его при Resume/Sleep вместо ожидания TCP-таймаута.
var (
	activeClient atomic.Pointer[gossh.Client]
	resumeHint   atomic.Bool // true сразу после Resume: реконнект за 2 с, без backoff
)

const (
	roleUser  = "user"
	roleAdmin = "admin"

	defaultRPortAdmin = 2201 // admin instance keeps its own relay port
	defaultLPortAdmin = 2223

	// self-update: build + its sha256 are uploaded here on the relay
	updateVersionPath = "/home/portassh/updates/version.txt"
	updateExePath     = "/home/portassh/updates/portassh.exe"
)

func main() {
	log.SetFlags(log.Ltime)
	relay := flag.String("relay", defaultRelay, "relay server address host[:22]")
	rport := flag.Int("rport", defaultRPort, "port on the relay (if busy, the next one is taken)")
	lport := flag.Int("lport", defaultLPort, "local port of the embedded SSH server (loopback only)")
	shell := flag.String("shell", defaultShell(), "shell for sessions")
	admin := flag.Bool("admin", false, "start an admin instance (UAC prompt, second tunnel: relay port 2201)")
	elevated := flag.Bool("elevated", false, "(internal) instance already elevated via UAC")
	noUpdate := flag.Bool("noupdate", false, "disable self-update checks")
	flag.Parse()
	cfgNoUpdate := *noUpdate

	role := roleUser
	if *admin || *elevated {
		role = roleAdmin
		*rport, *lport = applyRoleDefaults(true, *rport, *lport)
	}

	if *admin && !isElevated() {
		// relaunch self with UAC; the elevated instance takes over from here
		if err := elevateSelf("-elevated"); err != nil {
			log.Print(err)
			return
		}
		return
	}

	cfg := config{
		relay:                 *relay,
		relayUser:             defaultRelayUser,
		relayPort:             *rport,
		localPort:             *lport,
		shell:                 *shell,
		role:                  role,
		allowedClientPubKeys:  allowedClientPubKeys,
		allowedClientPassword: allowedClientPassword,
		noUpdate:              cfgNoUpdate,
	}

	// log next to the exe (with a hidden window this is the only visible output)
	if lf, err := os.OpenFile(filepath.Join(exeDir(), "portassh.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, lf))
	}
	if role == roleAdmin {
		log.SetPrefix("[admin] ")
	}

	fmt.Println("=== portassh ===")
	fmt.Printf("Started (%s) build %s. Waiting for the relay tunnel...\n", role, buildStamp)

	// working directory = user profile: this is where scp/sftp files land
	if home, err := os.UserHomeDir(); err == nil {
		_ = os.Chdir(home)
	}

	// remove the previous-version backup after a self-update
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}

	go func() {
		for {
			if err := startServer(cfg); err != nil {
				log.Printf("server: %v; restarting in 5s", err)
			} else {
				log.Print("server stopped; restarting in 5s")
			}
			time.Sleep(5 * time.Second)
		}
	}()
	if cfg.relay != "" {
		startPowerWatcher()
		relayLoop(cfg)
		return
	}
	select {} // LAN mode: only the local server
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

// applyRoleDefaults: the admin instance keeps its own default ports,
// but explicit user-provided values are left untouched.
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
	log.Printf("SSH server listening on %s", newServerAddr(cfg))
	return newServer(cfg).ListenAndServe()
}

func newServerAddr(cfg config) string {
	if cfg.relay == "" {
		return fmt.Sprintf("0.0.0.0:%d", cfg.localPort) // no tunnel: LAN mode, Windows will ask about the firewall
	}
	return fmt.Sprintf("127.0.0.1:%d", cfg.localPort)
}

// samePubKey compares authorized_keys lines by type and base64 (ignoring comments).
func samePubKey(a, b string) bool {
	fa, fb := strings.Fields(string(a)), strings.Fields(string(b))
	return len(fa) >= 2 && len(fb) >= 2 && fa[0] == fb[0] && fa[1] == fb[1]
}

// goSafe: a panic in a goroutine must never kill the whole process
// (ISSUES.md #1) — swallow it, log the stack, keep the tunnel and other
// sessions running.
func goSafe(what string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in %s: %v\n%s", what, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// safeHandler — same for SSH session handlers.
func safeHandler(h ssh.Handler) ssh.Handler {
	return func(s ssh.Session) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in session: %v\n%s", r, debug.Stack())
				fmt.Fprintf(s, "portassh: internal session error\r\n")
			}
		}()
		h(s)
	}
}

// safeSubsys — wrapper for subsystem handlers (sftp): SubsystemHandler is a different type.
func safeSubsys(h ssh.SubsystemHandler) ssh.SubsystemHandler {
	return func(s ssh.Session) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in subsystem: %v\n%s", r, debug.Stack())
				fmt.Fprintf(s, "portassh: internal subsystem error\r\n")
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
			"sftp": safeSubsys(sftpHandler), // scp/sftp: file transfer without the quoting hell
		},
		PublicKeyHandler: func(_ ssh.Context, key ssh.PublicKey) bool {
			line := string(gossh.MarshalAuthorizedKey(key))
			for _, want := range cfg.allowedClientPubKeys {
				if samePubKey(line, want) {
					return true
				}
			}
			return false
		},
		// парольный вход для машин пользователя, у которых нет зашитого ключа
		PasswordHandler: func(_ ssh.Context, pass string) bool {
			want := strings.TrimSpace(cfg.allowedClientPassword)
			return want != "" && pass == want
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
			runExec(s, cfg, s.RawCommand()) // the client's raw string — quotes are never rebuilt
			return
		}
		ptyReq, winCh, isPty := s.Pty()
		if !isPty {
			io.WriteString(s, "portassh: a PTY is required\n")
			s.Exit(1)
			return
		}
		// go-pty: ConPTY on Windows (creack/pty has no Windows support at all),
		// classic pty on Unix. Resize(width, height).
		pt, err := gpty.New()
		if err != nil {
			io.WriteString(s, err.Error())
			s.Exit(1)
			return
		}
		// a single Close only: double-closing the ConPTY/pipes is not allowed
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
		// ConPTY never signals EOF on process exit — drain the tail, then close
		time.Sleep(300 * time.Millisecond)
		closePty()
		<-copyDone
	}
}

// spawnRetryDelay — pause between spawn attempts (ISSUES.md #2: antivirus
// blocks clear after 10-30 s). Overridden in tests.
var spawnRetryDelay = 3 * time.Second

// splitExecOpts разбирает опциональные режимы запуска:
//
//	timeout=N <cmd> — убить команду через N секунд (exit 124)
//	<cmd> &         — запустить в фоне и вернуться сразу (GUI/UAC-зависания)
func splitExecOpts(raw string) (timeout time.Duration, detached bool, cmd string) {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, " &") {
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "&"))
		detached = true
	}
	if secs, rest, ok := strings.Cut(raw, " "); ok && strings.HasPrefix(secs, "timeout=") {
		if n, err := strconv.Atoi(strings.TrimPrefix(secs, "timeout=")); err == nil && n > 0 {
			// фон побеждает: detached-процесс не отслеживается, таймаут не применим
			return time.Duration(n) * time.Second, detached, strings.TrimSpace(rest)
		}
	}
	return 0, detached, raw
}

func runExec(s ssh.Session, cfg config, raw string) {
	timeout, detached, raw := splitExecOpts(raw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
	}

	// makeCmd: an exec.Cmd can only be Run once — rebuild for every attempt.
	makeCmd := func() *exec.Cmd {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			if strings.EqualFold(cfg.shell, "cmd.exe") {
				// Raw command line via SysProcAttr.CmdLine: Go does not apply
				// MSVCRT escaping to it (ISSUES.md regression), so the client's
				// quoting reaches cmd 1:1. /d disables registry AutoRun.
				cmd = exec.CommandContext(ctx, cfg.shell)
				setCmdLine(cmd, `cmd /d /s /c "`+raw+`"`)
			} else {
				cmd = exec.CommandContext(ctx, cfg.shell, "/c", raw)
			}
			// cmd/powershell write their output in the OEM encoding (CP866 on
			// Russian Windows) — decode to UTF-8 on the fly instead of spawning chcp
			if dec := oemDecoder(); dec != nil {
				out := transform.NewWriter(s, dec)
				cmd.Stdout, cmd.Stderr = out, out
				cmd.Stdin = s
				return cmd
			}
		} else {
			cmd = exec.CommandContext(ctx, cfg.shell, "-c", raw)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, s
		return cmd
	}

	// фоновый запуск: не привязываем ввод-вывод к сессии — она закроется сразу
	if detached {
		cmd := makeCmd()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		if err := startDetached(cmd); err != nil {
			fmt.Fprintf(s, "portassh: failed to start %q: %v\n", raw, err)
			s.Exit(127)
			return
		}
		fmt.Fprintf(s, "portassh: started in background (pid %d)\n", cmd.Process.Pid)
		s.Exit(0)
		return
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		err := makeCmd().Run()
		if err == nil {
			s.Exit(0)
			return
		}
		if timeout > 0 && ctx.Err() != nil {
			fmt.Fprintf(s, "portassh: timeout after %s: %q\n", timeout, raw)
			s.Exit(124)
			return
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// the command started and failed on its own — no point retrying
			s.Exit(ee.ExitCode())
			return
		}
		// it never even started (antivirus, permissions, missing file) — retry and report
		lastErr = err
		log.Printf("exec %q: attempt %d: %v", raw, attempt, err)
		if attempt < 3 {
			time.Sleep(spawnRetryDelay)
		}
	}
	fmt.Fprintf(s, "portassh: failed to start %q: %v\n", raw, lastErr)
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
			log.Printf("tunnel: %v; retrying in %s", err, delay)
			if resumeHint.Swap(false) {
				// пробуждение: коннект гарантированно мёртв, не копим backoff
				delay = 2 * time.Second
			}
			time.Sleep(delay)
			// exponential backoff capped at 30 s (ISSUES.md #5)
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
	// TCP keepalive 15 с: мёртвый после сна/обрыва сокет закрывается сам за ~45 с
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return wantPort, err
	}
	cc, chans, reqs, err := gossh.NewClientConn(conn, addr, &gossh.ClientConfig{
		User:              cfg.relayUser,
		Auth:              []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback:   gossh.FixedHostKey(hostKey),
		HostKeyAlgorithms: []string{gossh.KeyAlgoED25519}, // only the baked ed25519 relay key
	})
	if err != nil {
		_ = conn.Close()
		return wantPort, err
	}
	client := gossh.NewClient(cc, chans, reqs)
	defer client.Close()
	activeClient.Store(client)
	defer activeClient.Store(nil)

	// DPD: keepalive с жёстким локальным таймаутом 5 с. SendRequest без него
	// висит минутами на «тихом» обрыве (смена Wi-Fi ↔ LTE, сон без события).
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			done := make(chan error, 1)
			go func() {
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					// соединение умерло (сон, обрыв сети) — не ждём TCP-таймаут
					log.Printf("tunnel: keepalive failed: %v", err)
					resumeHint.Store(true) // коннект гарантированно мёртв
					_ = client.Close()
					return
				}
			case <-time.After(5 * time.Second):
				log.Print("tunnel: keepalive DPD timeout (5s) — closing dead connection")
				resumeHint.Store(true)
				_ = client.Close() // разблокирует и подвисший SendRequest
				return
			}
		}
	}()

	local := fmt.Sprintf("127.0.0.1:%d", cfg.localPort)
	var ln net.Listener
	port := wantPort
	if persisted := readPersistedPort(); persisted > 0 && persisted != wantPort && wantPort == 2200 {
		// машина со своим историческим портом: начинаем с него, а не с 2200
		port = persisted
	}
	for p := port; p <= port+99; p++ { // busy — try the next one (up to 100 machines)
		port = p
		ln, err = client.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			break
		}
		if p == readPersistedPort() {
			// «призрачный» порт прошлой сессии этой же машины — освобождаем
			if reclaimGhost(client, p) {
				if ln, err = client.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
					break
				}
			}
		}
	}
	if err != nil {
		return port, fmt.Errorf("remote listen: %w (port forwarding disabled on the relay?)", err)
	}
	defer ln.Close()
	savePersistedPort(port)
	log.Printf("tunnel: connected to %s as %s, forwarding 127.0.0.1:%d -> %s",
		addr, cfg.relayUser, port, local)

	cfg.relayPort = port
	writeConnectionFile(cfg)
	registerOnRelay(client, cfg)

	// heartbeat: перерегистрация раз в минуту, пока жива SSH-сессия с relay.
	// По свежести записей в реестре дашборд отличает активные машины от выключенных.
	hbStop := make(chan struct{})
	defer close(hbStop)
	goSafe("heartbeat", func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				registerOnRelay(client, cfg)
			case <-hbStop:
				return
			}
		}
	})
	if !cfg.noUpdate {
		goSafe("update-check", func() {
			for {
				if err := selfUpdate(client, cfg); err != nil {
					log.Printf("update: %v", err)
				}
				select {
				case <-time.After(30 * time.Minute):
				case <-hbStop:
					return
				}
			}
		})
	}

	fmt.Printf("\n=== READY ===\nConnect from your computer:\n  %s\n", connectCommand(cfg.relay, port))

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
			return port, fmt.Errorf("relay closed the connection: %v", err)
		case c, ok := <-acceptCh:
			if !ok {
				return port, fmt.Errorf("relay closed the connection")
			}
			goSafe("forward", func() { forward(c, local) })
		}
	}
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

// --- connection info delivery ---

func exeDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

func connectCommand(relay string, port int) string {
	return fmt.Sprintf("ssh -i portassh_client.key -o StrictHostKeyChecking=accept-new -J support@%s -p %d anyuser@localhost", relay, port)
}

// persistedPortPath: файл рядом с exe хранит последний удачный relay-порт,
// чтобы машина после рестарта и «призрачных» сбоев вернулась на свой порт.
func persistedPortPath() string { return filepath.Join(exeDir(), "portassh_port.txt") }

func readPersistedPort() int {
	b, err := os.ReadFile(persistedPortPath())
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if n < 1024 || n > 65535 {
		return 0
	}
	return n
}

func savePersistedPort(port int) {
	if port < 1024 {
		return
	}
	_ = os.WriteFile(persistedPortPath(), []byte(strconv.Itoa(port)+"\n"), 0o600)
}

// reclaimGhost: при аварийном разрыве sshd на relay держит порт занятым
// (зависший post-auth процесс нашего же uid). Убиваем его и освобождаем порт.
func reclaimGhost(client *gossh.Client, port int) bool {
	sess, err := client.NewSession()
	if err != nil {
		return false
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(fmt.Sprintf(
		"ss -tlnp | grep ':%d ' | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1", port))
	if err != nil {
		return false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	if pid <= 1 {
		return false // порт свободен или слушает чужой процесс
	}
	kill, err := client.NewSession()
	if err != nil {
		return false
	}
	defer kill.Close()
	if err := kill.Run("kill " + strconv.Itoa(pid)); err != nil {
		return false
	}
	time.Sleep(500 * time.Millisecond) // sshd освобождает порт не мгновенно
	log.Printf("tunnel: reclaimed ghost port %d (stale session pid %d)", port, pid)
	return true
}

func writeConnectionFile(cfg config) {
	name := "portassh_connection.txt"
	if cfg.role == roleAdmin {
		name = "portassh_connection_admin.txt"
	}
	p := filepath.Join(exeDir(), name)
	_ = os.WriteFile(p, []byte(connectCommand(cfg.relay, cfg.relayPort)+"\n"), 0o600)
}

// registryLine: "PC_NAME port time role" — the port is always field 2
// (keeps pconnectgo compatible).
func registryLine(host string, port int, role string, when time.Time) string {
	return fmt.Sprintf("%s %d %s %s", sanitizeLine(host), port, when.Format("02.01_15:04"), role)
}

// registerOnRelay appends a line to ~/online.txt on the relay so the list of
// machines and their roles can be checked with a single command.
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
	// umask 022: реестр читается веб-дашбордом на том же VPS
	_ = sess.Run(fmt.Sprintf("umask 022 && echo '%s' >> ~/online.txt", line))
}

// sendOffline: штатное завершение Windows (reboot/logoff) — убираем свои
// строки из реестра, чтобы дашборд сразу показал машину выключенной,
// а не «активной до истечения heartbeat-окна».
func sendOffline(client *gossh.Client) {
	host, err := os.Hostname()
	if err != nil {
		return
	}
	host = sanitizeLine(host)
	if host == "" {
		return
	}
	sess, err := client.NewSession()
	if err != nil {
		return
	}
	defer sess.Close()
	_ = sess.Run(fmt.Sprintf("sed -i '/^%s /d' ~/online.txt", host))
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

// --- self-update ---
// A new build is placed on the relay in /home/portassh/updates/ (portassh.exe
// + version.txt containing its sha256). The instance compares the hash of its
// own exe, downloads the new version through the tunnel and restarts itself.

func fetchViaSSH(client *gossh.Client, path string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	return sess.Output("cat " + path)
}

func selfUpdate(client *gossh.Client, cfg config) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	cur, err := os.ReadFile(exePath)
	if err != nil {
		return err
	}
	curSum := sha256.Sum256(cur)

	ver, err := fetchViaSSH(client, updateVersionPath)
	if err != nil {
		return err
	}
	remoteSum := strings.TrimSpace(string(ver))
	if remoteSum == hex.EncodeToString(curSum[:]) {
		return nil // up to date
	}
	bin, err := fetchViaSSH(client, updateExePath)
	if err != nil {
		return err
	}
	binSum := sha256.Sum256(bin)
	if remoteSum != hex.EncodeToString(binSum[:]) {
		return errors.New("downloaded build hash mismatch, skipping")
	}

	newP, oldP := exePath+".new", exePath+".old"
	if err := os.WriteFile(newP, bin, 0o755); err != nil {
		return err
	}
	_ = os.Remove(oldP)
	if err := os.Rename(exePath, oldP); err != nil {
		return err
	}
	if err := os.Rename(newP, exePath); err != nil {
		_ = os.Rename(oldP, exePath) // rollback
		return err
	}

	cmd := exec.Command(exePath, os.Args[1:]...)
	startDetached(cmd)
	log.Printf("update: new build started, restarting")
	os.Exit(0)
	return nil
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
