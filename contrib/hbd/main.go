// hbd: лёгкий приёмщик heartbeat-строк portassh на relay.
// Слушает 127.0.0.1:8898 (доступ только через direct-tcpip из SSH-туннелей
// клиентов), каждая строка = строка реестра, пишется в online.txt (O_APPEND).
// Ни одного форка sh.
package main

import (
	"bufio"
	"bytes"
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

	sftp "github.com/pkg/sftp"
	ssh "golang.org/x/crypto/ssh"
)

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
)

func valid(line string) bool {
	f := strings.Fields(line)
	// поля: host port time role [user os disk ram upt ver ...]
	if len(f) < 4 || len(f) > 12 {
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
	if clientSigner == nil || clientHostPub == nil {
		http.Error(w, "client key not configured", 500)
		return
	}
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 {
		http.Error(w, "bad port", 400)
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		http.Error(w, "target unreachable: "+err.Error(), 502)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              "anyuser",
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback:   ssh.FixedHostKey(clientHostPub),
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           5 * time.Second,
	})
	if err != nil {
		http.Error(w, "ssh: "+err.Error(), 502)
		return
	}
	defer cc.Close()
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		http.Error(w, "session: "+err.Error(), 502)
		return
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
	screenMu.Lock()
	defer screenMu.Unlock()
	if r.Method == "GET" {
		lastViewer[port] = time.Now().Unix()
	}
	if r.Method == "POST" {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		line := strings.TrimSpace(string(body))
		if line != "" {
			inputQ[port] = append(inputQ[port], line)
		}
		fmt.Fprint(w, "ok")
		return
	}
	lines := inputQ[port]
	delete(inputQ, port)
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
	if time.Now().Unix()-lastViewer[port] < 5 {
		fmt.Fprint(w, "yes")
		return
	}
	fmt.Fprint(w, "no")
}

// sftpFor: SFTP-клиент цели (пути относительно домашней папки пользователя).
func sftpFor(port int) (*sftp.Client, func(), error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              "anyuser",
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback:   ssh.FixedHostKey(clientHostPub),
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           5 * time.Second,
	})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	client := ssh.NewClient(cc, chans, reqs)
	sc, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	cleanup := func() { sc.Close(); client.Close() }
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

// broadcastHandler: параллельный exec на целях через их форвардинги.
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
	for _, p := range ports {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
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

// execOnTarget: одна команда на цель (через её пул/exec), 20 с таймаут.
func execOnTarget(port int, cmd string) (int, string) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return -1, "машина недоступна: " + err.Error()
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              "anyuser",
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback:   ssh.FixedHostKey(clientHostPub),
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           5 * time.Second,
	})
	if err != nil {
		conn.Close()
		return -1, "ssh: " + err.Error()
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return -1, "session: " + err.Error()
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
		client.Close()
		out.WriteString("\n… таймаут 20 с")
		return 124, out.String()
	}
	return code, out.String()
}

type outBuf struct{ b *bytes.Buffer }

func (o *outBuf) Write(p []byte) (int, error) { return o.b.Write(p) }

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
		hbCount++
	}
	w.Flush()
	if hbCount >= compEvery {
		compact()
		hbCount = 0
	}
}

// compact: ротация online.txt — остаются последние compKeep строк.
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
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, file); err != nil {
		return
	}
	log.Printf("online.txt rotated: kept %d lines", len(lines))
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
	hostKey := flag.String("hostkey", "5.35.81.198 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFSMuHGj/PMRmzrq5bJNhkbRafhgzexXADhiECn8uKk2 portassh-host", "host key целей")
	flag.Parse()
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

	// broadcast exec + inventory + screen: HTTP на отдельном порту (localhost)
	go func() {
		http.HandleFunc("/broadcast", broadcastHandler)
		http.HandleFunc("/inventory", inventoryHandler)
		http.HandleFunc("/ls", lsHandler)
		http.HandleFunc("/putfile", putFileHandler)
		http.HandleFunc("/getfile", getFileHandler)
		http.HandleFunc("/screen", screenHandler)
		http.HandleFunc("/input", inputHandler)
		http.HandleFunc("/viewer", viewerHandler)
		http.HandleFunc("/events", eventsHandler)
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
