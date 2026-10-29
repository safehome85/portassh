// hbd: лёгкий приёмщик heartbeat-строк portassh на relay.
// Слушает 127.0.0.1:8898 (доступ только через direct-tcpip из SSH-туннелей
// клиентов), каждая строка = строка реестра, пишется в online.txt (O_APPEND).
// Ни одного форка sh.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	sftp "github.com/pkg/sftp"
	ssh "golang.org/x/crypto/ssh"
)

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}


const (
	listen    = "127.0.0.1:8898"
	file      = "/home/portassh/online.txt"
	compEvery = 5000 // дозаписей между ротациями
	compKeep  = 2000 // строк остаётся после ротации
)

var (
	clientSigner  ssh.Signer
	clientHostPub ssh.PublicKey
)

var (
	hbMu    sync.Mutex
	hbCount int
	inputQ  = map[int][]string{}

	inputWaitersMu sync.Mutex
	inputWaiters   = make(map[int]chan struct{})
)

// In-memory Fleet state store
type FleetMachine struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Role     string `json:"role"`
	Time     string `json:"t"`
	User     string `json:"user,omitempty"`
	OS       string `json:"os,omitempty"`
	DiskGB   int    `json:"disk"`
	RAMLoad  int    `json:"ram"`
	Uptime   int    `json:"upt"`
	Version  string `json:"ver,omitempty"`
	LastSeen int64  `json:"ts"`
}

var (
	fleetMu    sync.RWMutex
	fleetState = make(map[string]*FleetMachine)

	sseMu      sync.Mutex
	sseClients = make(map[chan []byte]struct{})
)

func broadcastSSE(event string, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(b)))
	sseMu.Lock()
	defer sseMu.Unlock()
	for ch := range sseClients {
		select {
		case ch <- msg:
		default:
		}
	}
}

func notifyInputWaiter(port int) {
	inputWaitersMu.Lock()
	if ch, ok := inputWaiters[port]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	inputWaitersMu.Unlock()
}

// SSH Connection Pool: multiplexed sessions across long-lived ssh.Client
type pooledSSHClient struct {
	client   *ssh.Client
	lastUsed time.Time
}

var (
	sshPoolMu sync.Mutex
	sshPool   = make(map[int]*pooledSSHClient)
)

const sshPoolTTL = 45 * time.Second

func initSSHPoolReaper() {
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for range t.C {
			sshPoolMu.Lock()
			now := time.Now()
			for port, entry := range sshPool {
				if now.Sub(entry.lastUsed) > sshPoolTTL {
					_ = entry.client.Close()
					delete(sshPool, port)
				}
			}
			sshPoolMu.Unlock()
		}
	}()
}

func dialTargetSSH(port int) (*ssh.Client, error) {
	if clientSigner == nil || clientHostPub == nil {
		return nil, fmt.Errorf("client key not configured")
	}
	sshPoolMu.Lock()
	if entry, ok := sshPool[port]; ok {
		entry.lastUsed = time.Now()
		sshPoolMu.Unlock()
		return entry.client, nil
	}
	sshPoolMu.Unlock()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              "anyuser",
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback:   ssh.FixedHostKey(clientHostPub),
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           5 * time.Second,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	cl := ssh.NewClient(cc, chans, reqs)

	sshPoolMu.Lock()
	sshPool[port] = &pooledSSHClient{client: cl, lastUsed: time.Now()}
	sshPoolMu.Unlock()
	return cl, nil
}

func dropTargetSSH(port int, cl *ssh.Client) {
	sshPoolMu.Lock()
	if entry, ok := sshPool[port]; ok && (cl == nil || entry.client == cl) {
		_ = entry.client.Close()
		delete(sshPool, port)
	}
	sshPoolMu.Unlock()
}

// allowlist: /home/portassh/allowed-hosts.txt — по одному хостнейму на строку.
// Пустой/отсутствующий файл = принимать всех (обратная совместимость).
// Проверка динамическая (stat mtime): добавление хоста не требует перезапуска hbd.
var (
	allowedMu    sync.RWMutex
	allowedCache map[string]bool
	allowedMtime time.Time
	rejectedMu   sync.Mutex
	lastRejected = map[string]time.Time{}
)

func isHostAllowed(host string) bool {
	stat, err := os.Stat("/home/portassh/allowed-hosts.txt")
	if err != nil || stat.Size() == 0 {
		return true // missing or empty = allow all
	}

	allowedMu.Lock()
	if allowedCache == nil || stat.ModTime().After(allowedMtime) {
		m := make(map[string]bool)
		if b, err := os.ReadFile("/home/portassh/allowed-hosts.txt"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if h := strings.TrimSpace(l); h != "" {
					m[strings.ToLower(h)] = true
				}
			}
		}
		allowedCache = m
		allowedMtime = stat.ModTime()
	}
	allowed := len(allowedCache) == 0 || allowedCache[strings.ToLower(host)]
	allowedMu.Unlock()

	if !allowed {
		recordRejectedHost(host)
	}
	return allowed
}

func recordRejectedHost(host string) {
	rejectedMu.Lock()
	defer rejectedMu.Unlock()
	if time.Since(lastRejected[host]) < 5*time.Minute {
		return
	}
	lastRejected[host] = time.Now()
	f, err := os.OpenFile("/home/portassh/rejected-hosts.txt", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), host)
		f.Close()
	}
}

func valid(line string) bool {
	f := strings.Fields(line)
	// поля: host port time role [user os disk ram upt ver ...]
	if len(f) < 4 || len(f) > 12 {
		return false
	}
	if !isHostAllowed(f[0]) {
		return false
	}
	for i, x := range f {
		if x == "" || strings.ContainsAny(x, "\r\n\x00") && i != 0 {
			return false
		}
	}
	if _, ok := map[string]bool{"user": true, "admin": true}[f[3]]; !ok {
		return false
	}
	return true
}

// inventoryHandler: снимает подсистему "info" цели и отдаёт её JSON.
func inventoryHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	client, err := dialTargetSSH(port)
	if err != nil {
		http.Error(w, "target unreachable: "+err.Error(), 502)
		return
	}
	session, err := client.NewSession()
	if err != nil {
		dropTargetSSH(port, client)
		client, err = dialTargetSSH(port)
		if err != nil {
			http.Error(w, "target unreachable: "+err.Error(), 502)
			return
		}
		session, err = client.NewSession()
		if err != nil {
			dropTargetSSH(port, client)
			http.Error(w, "session: "+err.Error(), 502)
			return
		}
	}
	defer session.Close()
	stdout, err := session.StdoutPipe()
	if err != nil {
		http.Error(w, "pipe: "+err.Error(), 502)
		return
	}
	if err := session.RequestSubsystem("info"); err != nil {
		http.Error(w, "subsystem: "+err.Error(), 500)
		return
	}
	out, err := io.ReadAll(stdout)
	if err != nil && len(out) == 0 {
		http.Error(w, "read: "+err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(out)
}

// кадры экрана: последний кадр на порт + очередь ввода + флаг зрителя
type screenFrame struct {
	jpg  []byte
	w, h int
	ts   int64
}

var (
	screenMu     sync.Mutex
	screenFrames = map[int]screenFrame{}
	inputQueues  = map[int][]string{}
	lastViewer   = map[int]int64{}
)

func screenHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	screenMu.Lock()
	defer screenMu.Unlock()
	if r.Method == "GET" {
		lastViewer[port] = time.Now().Unix()
	}
	if r.Method == "POST" {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 3<<20))
		if err != nil || len(body) < 100 {
			http.Error(w, "bad frame", 400)
			return
		}
		fw, _ := strconv.Atoi(r.URL.Query().Get("w"))
		fh, _ := strconv.Atoi(r.URL.Query().Get("h"))
		screenFrames[port] = screenFrame{jpg: body, w: fw, h: fh, ts: time.Now().Unix()}
		fmt.Fprint(w, "ok")
		fanOutScreen(port, body)
		return
	}
	fr, ok := screenFrames[port]
	if !ok || time.Now().Unix()-fr.ts > 60 {
		http.Error(w, "no frame", 404)
		return
	}
	w2 := w.Header()
	w2.Set("Content-Type", "image/jpeg")
	w2.Set("X-Frame-W", strconv.Itoa(fr.w))
	w2.Set("X-Frame-H", strconv.Itoa(fr.h))
	w.Write(fr.jpg)
}

func inputHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	if r.Method == "POST" {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		line := strings.TrimSpace(string(body))
		if line != "" {
			screenMu.Lock()
			inputQ[port] = append(inputQ[port], line)
			screenMu.Unlock()
			notifyInputWaiter(port)
		}
		fmt.Fprint(w, "ok")
		return
	}

	screenMu.Lock()
	lastViewer[port] = time.Now().Unix()

	// Long-polling: если указан wait=1 и в очереди пусто — ждём до 25 секунд
	if r.URL.Query().Get("wait") == "1" && len(inputQ[port]) == 0 {
		ch := make(chan struct{}, 1)
		inputWaitersMu.Lock()
		inputWaiters[port] = ch
		inputWaitersMu.Unlock()

		screenMu.Unlock()
		select {
		case <-ch:
		case <-time.After(25 * time.Second):
		case <-r.Context().Done():
		}
		screenMu.Lock()

		inputWaitersMu.Lock()
		delete(inputWaiters, port)
		inputWaitersMu.Unlock()
	}

	lines := inputQ[port]
	delete(inputQ, port)
	screenMu.Unlock()
	fmt.Fprint(w, strings.Join(lines, "\n"))
}

func viewerHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	screenMu.Lock()
	defer screenMu.Unlock()
	// bootstrap: кадра ещё нет — зритель считается активным (первый кадр)
	if _, ok := screenFrames[port]; !ok {
		fmt.Fprint(w, "yes")
		return
	}
	liveMu.Lock()
	hasLive := len(liveSubs[port]) > 0
	liveMu.Unlock()
	if time.Now().Unix()-lastViewer[port] < 8 || hasLive {
		fmt.Fprint(w, "yes")
		return
	}
	fmt.Fprint(w, "no")
}

// --- live screen: WebSocket-хаб (кадры exe фан-аутом зриителям, ввод обратно) ---

var (
	liveMu     sync.Mutex
	liveSubs   = map[int]map[chan []byte]struct{}{}
	dashSecret = []byte("")
)

// scrToken: sha256("scr|<secret>|<port>|<час UTC>")[:32 hex]; принимаем текущий и предыдущий час.
func scrToken(secret []byte, port int, bucket string) string {
	h := sha256.Sum256([]byte("scr|" + string(secret) + "|" + strconv.Itoa(port) + "|" + bucket))
	return hex.EncodeToString(h[:])[:32]
}

func scrTokValid(t string, port int) bool {
	if len(dashSecret) == 0 || len(t) != 32 {
		return false
	}
	now := time.Now().UTC().Format("2006010215")
	prev := time.Now().UTC().Add(-time.Hour).Format("2006010215")
	ok := subtle.ConstantTimeCompare([]byte(t), []byte(scrToken(dashSecret, port, now))) == 1 ||
		subtle.ConstantTimeCompare([]byte(t), []byte(scrToken(dashSecret, port, prev))) == 1
	return ok
}

func screenWSHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	if !scrTokValid(r.URL.Query().Get("t"), port) {
		http.Error(w, "forbidden", 403)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	sub := make(chan []byte, 8)
	liveMu.Lock()
	if liveSubs[port] == nil {
		liveSubs[port] = map[chan []byte]struct{}{}
	}
	liveSubs[port][sub] = struct{}{}
	liveMu.Unlock()
	screenMu.Lock()
	lastViewer[port] = time.Now().Unix()
	screenMu.Unlock()

	done := make(chan struct{})
	// чтение: пинги и строки ввода
	go func() {
		defer close(done)
		for {
			mt, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				screenMu.Lock()
				lastViewer[port] = time.Now().Unix()
				line := strings.TrimSpace(string(msg))
				if line != "" && line != "ping" {
					inputQ[port] = append(inputQ[port], line)
					notifyInputWaiter(port)
				}
				screenMu.Unlock()
			}
		}
	}()
	// запись: кадры из подписки
	go func() {
		pt := time.NewTicker(10 * time.Second)
		defer pt.Stop()
		for {
			select {
			case jpg := <-sub:
				if err := ws.WriteMessage(websocket.BinaryMessage, jpg); err != nil {
					return
				}
			case <-pt.C:
				if err := ws.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	<-done
	liveMu.Lock()
	delete(liveSubs[port], sub)
	liveMu.Unlock()
	ws.Close()
}

// fanOutScreen: разослать новый кадр живым зрителям (медленных отключаем).
func fanOutScreen(port int, jpg []byte) {
	liveMu.Lock()
	defer liveMu.Unlock()
	for ch := range liveSubs[port] {
		select {
		case ch <- jpg:
		default: // подписчик не успевает — пропускаем кадр
		}
	}
}


// sftpFor: SFTP-клиент цели (пути относительно домашней папки пользователя).
func sftpFor(port int) (*sftp.Client, func(), error) {
	client, err := dialTargetSSH(port)
	if err != nil {
		return nil, nil, err
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		dropTargetSSH(port, client)
		client, err = dialTargetSSH(port)
		if err != nil {
			return nil, nil, err
		}
		sc, err = sftp.NewClient(client)
		if err != nil {
			dropTargetSSH(port, client)
			return nil, nil, err
		}
	}
	cleanup := func() { _ = sc.Close() }
	return sc, cleanup, nil
}

func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_ ()", r) {
			return r
		}
		return '_'
	}, name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

// lsHandler: список файлов домашней папки цели.
func lsHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	sc, cleanup, err := sftpFor(port)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer cleanup()
	infos, err := sc.ReadDir(".")
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	type fi struct {
		Name string `json:"n"`
		Size int64  `json:"s"`
		Dir  bool   `json:"d"`
	}
	var out []fi
	for _, i := range infos {
		out = append(out, fi{Name: i.Name(), Size: i.Size(), Dir: i.IsDir()})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// putFileHandler: приём файла (тело запроса) → Downloads цели.
func putFileHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	name := safeName(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "bad name", 400)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<20))
	if err != nil || len(body) == 0 {
		http.Error(w, "empty body", 400)
		return
	}
	sc, cleanup, err := sftpFor(port)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer cleanup()
	f, err := sc.Create("Downloads/" + name)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		http.Error(w, err.Error(), 502)
		return
	}
	f.Close()
	fmt.Fprint(w, "ok")
}

// getFileHandler: отдача файла из домашней папки или Downloads цели.
func getFileHandler(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	name := safeName(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "bad name", 400)
		return
	}
	sc, cleanup, err := sftpFor(port)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer cleanup()
	f, err := sc.Open(name)
	if err != nil {
		f, err = sc.Open("Downloads/" + name)
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}
	}
	defer f.Close()
	fi, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	if fi != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	io.Copy(w, f)
}

// broadcastHandler: параллельный exec на целях через их форвардинги с динамическим worker-пулом.
func broadcastHandler(w http.ResponseWriter, r *http.Request) {
	if clientSigner == nil || clientHostPub == nil {
		http.Error(w, "client key not configured", 500)
		return
	}
	var ports []int
	for _, p := range strings.Split(r.URL.Query().Get("ports"), ",") {
		p = strings.TrimSpace(p)
		if n, err := strconv.Atoi(p); err == nil && n >= 1024 && n <= 65535 {
			ports = append(ports, n)
		}
	}
	cmd := r.URL.Query().Get("cmd")
	if cmd == "" || len(ports) == 0 || len(cmd) > 4096 {
		http.Error(w, "bad request", 400)
		return
	}
	type res struct {
		Code int    `json:"code"`
		Out  string `json:"out"`
	}
	results := make(map[int]res, len(ports))
	var mu sync.Mutex
	var wg sync.WaitGroup
	// динамический worker pool (макс. 32 параллельных запроса)
	sem := make(chan struct{}, 32)
	for _, p := range ports {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			code, out := execOnTarget(p, cmd)
			mu.Lock()
			results[p] = res{Code: code, Out: out}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

// execOnTarget: одна команда на цель через пул постоянных SSH-соединений (20 с таймаут).
func execOnTarget(port int, cmd string) (int, string) {
	client, err := dialTargetSSH(port)
	if err != nil {
		return -1, "машина недоступна: " + err.Error()
	}
	session, err := client.NewSession()
	if err != nil {
		// при обрыве туннеля — сбрасываем и повторяем один раз
		dropTargetSSH(port, client)
		client, err = dialTargetSSH(port)
		if err != nil {
			return -1, "машина недоступна: " + err.Error()
		}
		session, err = client.NewSession()
		if err != nil {
			dropTargetSSH(port, client)
			return -1, "session: " + err.Error()
		}
	}
	defer session.Close()

	var out bytes.Buffer
	session.Stdout = &outBuf{b: &out}
	session.Stderr = &outBuf{b: &out}
	code := 0
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()
	select {
	case err := <-done:
		if ee, ok := err.(*ssh.ExitError); ok {
			code = ee.ExitStatus()
		} else if err != nil {
			code = 127
		}
	case <-time.After(20 * time.Second):
		session.Close()
		dropTargetSSH(port, client)
		out.WriteString("\n… таймаут 20 с")
		return 124, out.String()
	}
	return code, out.String()
}

type outBuf struct{ b *bytes.Buffer }

func (o *outBuf) Write(p []byte) (int, error) { return o.b.Write(p) }

func parseFleetHeartbeat(line string) *FleetMachine {
	f := strings.Fields(line)
	if len(f) < 4 {
		return nil
	}
	p, _ := strconv.Atoi(f[1])
	m := &FleetMachine{
		Host:     f[0],
		Port:     p,
		Time:     f[2],
		Role:     f[3],
		LastSeen: time.Now().Unix(),
		DiskGB:   -1,
		RAMLoad:  -1,
		Uptime:   -1,
	}
	if len(f) >= 10 {
		if f[4] != "-" {
			m.User = f[4]
		}
		if f[5] != "-" {
			m.OS = f[5]
		}
		if d, err := strconv.Atoi(f[6]); err == nil {
			m.DiskGB = d
		}
		if r, err := strconv.Atoi(f[7]); err == nil {
			m.RAMLoad = r
		}
		if u, err := strconv.Atoi(f[8]); err == nil {
			m.Uptime = u
		}
		if f[9] != "-" {
			m.Version = f[9]
		}
	}
	return m
}

func initFleetFromDisk() {
	b, err := os.ReadFile(file)
	if err != nil {
		return
	}
	fleetMu.Lock()
	defer fleetMu.Unlock()
	now := time.Now().Unix()
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if valid(l) {
			if fm := parseFleetHeartbeat(l); fm != nil {
				fm.LastSeen = now - 60
				key := fmt.Sprintf("%s|%s", fm.Host, fm.Role)
				fleetState[key] = fm
			}
		}
	}
}

func fleetHandler(w http.ResponseWriter, r *http.Request) {
	fleetMu.RLock()
	machines := make([]*FleetMachine, 0, len(fleetState))
	now := time.Now().Unix()
	for _, m := range fleetState {
		if now-m.LastSeen <= 180 {
			machines = append(machines, m)
		}
	}
	fleetMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(machines)
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, 32)
	sseMu.Lock()
	sseClients[ch] = struct{}{}
	sseMu.Unlock()

	defer func() {
		sseMu.Lock()
		delete(sseClients, ch)
		sseMu.Unlock()
	}()

	fleetMu.RLock()
	now := time.Now().Unix()
	for _, m := range fleetState {
		if now-m.LastSeen <= 180 {
			if b, err := json.Marshal(m); err == nil {
				fmt.Fprintf(w, "event: fleet\ndata: %s\n\n", string(b))
			}
		}
	}
	fleetMu.RUnlock()
	flusher.Flush()

	pt := time.NewTicker(15 * time.Second)
	defer pt.Stop()

	for {
		select {
		case msg := <-ch:
			w.Write(msg)
			flusher.Flush()
		case <-pt.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func handle(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 4096), 4096)
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Print("open: ", err)
		return
	}
	defer f.Close()
	hbMu.Lock()
	defer hbMu.Unlock()
	w := bufio.NewWriter(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !valid(line) {
			continue
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			return
		}
		if fm := parseFleetHeartbeat(line); fm != nil {
			fleetMu.Lock()
			key := fmt.Sprintf("%s|%s", fm.Host, fm.Role)
			fleetState[key] = fm
			fleetMu.Unlock()
			broadcastSSE("heartbeat", fm)
		}
		hbCount++
	}
	w.Flush()
	if hbCount >= compEvery {
		compact()
		hbCount = 0
	}
}

// compact: ротация online.txt — остаются последние compKeep строк,
// причём только хосты из allowlist (утёкшие exe не оставляют следов).
// Единственный писатель — hbd, поэтому rename безопасен.
func compact() {
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > compKeep {
		lines = lines[len(lines)-compKeep:]
	}
	if stat, err := os.Stat("/home/portassh/allowed-hosts.txt"); err == nil && stat.Size() > 0 {
		kept := lines[:0]
		for _, l := range lines {
			if f := strings.Fields(l); len(f) > 0 && isHostAllowed(f[0]) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		return
	}
	log.Printf("online.txt rotated: kept %d lines", len(lines))
}

func allowHostHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Host = r.FormValue("host")
	}
	h := strings.TrimSpace(req.Host)
	if h == "" || len(h) > 64 || strings.ContainsAny(h, " \t\r\n/\\'\";`$|&><") {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	allowedMu.Lock()
	b, _ := os.ReadFile("/home/portassh/allowed-hosts.txt")
	existing := strings.Split(string(b), "\n")
	has := false
	var cleaned []string
	for _, l := range existing {
		tr := strings.TrimSpace(l)
		if tr != "" {
			if tr == h {
				has = true
			}
			cleaned = append(cleaned, tr)
		}
	}
	if !has {
		cleaned = append(cleaned, h)
		_ = os.WriteFile("/home/portassh/allowed-hosts.txt", []byte(strings.Join(cleaned, "\n")+"\n"), 0644)
	}
	m := make(map[string]bool)
	for _, x := range cleaned {
		m[strings.ToLower(x)] = true
	}
	allowedCache = m
	allowedMtime = time.Now()
	allowedMu.Unlock()

	// Очищаем из rejected-hosts.txt
	rejectedMu.Lock()
	delete(lastRejected, h)
	if rb, err := os.ReadFile("/home/portassh/rejected-hosts.txt"); err == nil {
		var kept []string
		for _, l := range strings.Split(string(rb), "\n") {
			f := strings.Fields(l)
			if len(f) >= 3 && f[2] == h {
				continue
			}
			if strings.TrimSpace(l) != "" {
				kept = append(kept, l)
			}
		}
		_ = os.WriteFile("/home/portassh/rejected-hosts.txt", []byte(strings.Join(kept, "\n")+"\n"), 0644)
	}
	rejectedMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "host": h})
}

func rejectedHostsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	b, err := os.ReadFile("/home/portassh/rejected-hosts.txt")
	if err != nil {
		json.NewEncoder(w).Encode([]any{})
		return
	}
	allowedMu.RLock()
	cached := allowedCache
	allowedMu.RUnlock()

	type item struct {
		Host string `json:"host"`
		Time string `json:"time"`
	}
	seen := map[string]bool{}
	var res []item
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 3 {
			tStr := f[0] + " " + f[1]
			h := f[2]
			if !seen[h] && (cached == nil || !cached[h]) {
				seen[h] = true
				res = append(res, item{Host: h, Time: tStr})
			}
		}
	}
	json.NewEncoder(w).Encode(res)
}

func dismissRejectedHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Host string `json:"host"`
		All  bool   `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Host = r.FormValue("host")
		if r.FormValue("all") == "1" || r.FormValue("all") == "true" {
			req.All = true
		}
	}
	h := strings.TrimSpace(req.Host)

	rejectedMu.Lock()
	defer rejectedMu.Unlock()

	if req.All || h == "*" || h == "all" {
		lastRejected = make(map[string]time.Time)
		_ = os.WriteFile("/home/portassh/rejected-hosts.txt", []byte(""), 0644)
	} else if h != "" {
		delete(lastRejected, h)
		if rb, err := os.ReadFile("/home/portassh/rejected-hosts.txt"); err == nil {
			var kept []string
			for _, l := range strings.Split(string(rb), "\n") {
				f := strings.Fields(l)
				if len(f) >= 3 && f[2] == h {
					continue
				}
				if strings.TrimSpace(l) != "" {
					kept = append(kept, l)
				}
			}
			data := ""
			if len(kept) > 0 {
				data = strings.Join(kept, "\n") + "\n"
			}
			_ = os.WriteFile("/home/portassh/rejected-hosts.txt", []byte(data), 0644)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func main() {
	compact()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}
	keyPath := flag.String("key", "/home/portassh/portassh_client.key", "client key для exec на цели")
	hostKey := flag.String("hostkey", "", "host key целей (known_hosts-строка, обязательно)")
	dashSecretFile := flag.String("dashsecret", "", "файл с общим секретом дашборда (для /screenws токенов)")
	flag.Parse()
	if *dashSecretFile != "" {
		if b, err := os.ReadFile(*dashSecretFile); err == nil {
			dashSecret = []byte(strings.TrimSpace(string(b)))
		}
	}
	if kb, err := os.ReadFile(*keyPath); err == nil {
		if sg, err := ssh.ParsePrivateKey(kb); err == nil {
			clientSigner = sg
		} else {
			log.Print("client key: ", err)
		}
	} else {
		log.Print("client key недоступна — broadcast выключен")
	}
	if _, _, hp, _, _, err := ssh.ParseKnownHosts([]byte(*hostKey)); err == nil {
		clientHostPub = hp
	}

	initSSHPoolReaper()
	initFleetFromDisk()

	// broadcast exec + inventory + screen + fleet API + SSE: HTTP на отдельном порту (localhost)
	go func() {
		http.HandleFunc("/broadcast", broadcastHandler)
		http.HandleFunc("/inventory", inventoryHandler)
		http.HandleFunc("/ls", lsHandler)
		http.HandleFunc("/putfile", putFileHandler)
		http.HandleFunc("/getfile", getFileHandler)
		http.HandleFunc("/screen", screenHandler)
		http.HandleFunc("/input", inputHandler)
		http.HandleFunc("/viewer", viewerHandler)
		http.HandleFunc("/screenws", screenWSHandler)
		http.HandleFunc("/events", eventsHandler)
		http.HandleFunc("/api/fleet", fleetHandler)
		http.HandleFunc("/api/events", sseHandler)
		http.HandleFunc("/api/allow-host", allowHostHandler)
		http.HandleFunc("/api/rejected-hosts", rejectedHostsHandler)
		http.HandleFunc("/api/dismiss-rejected", dismissRejectedHandler)
		http.HandleFunc("/ai/config", aiConfigHandler)
		http.HandleFunc("/ai/explain-events", explainEventsHandler)
		http.HandleFunc("/ai/chat", aiChatHandler)
		log.Fatal(http.ListenAndServe("127.0.0.1:8897", nil))
	}()

	log.Print("hbd listening on ", listen)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Print("accept: ", err)
			continue
		}
		go handle(c)
	}
}
