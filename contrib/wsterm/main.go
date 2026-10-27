// wsterm: web-терминал для portassh — WebSocket ↔ SSH PTY на целевую машину.
// Живёт на relay (127.0.0.1:8898), за nginx basic-auth дашборда.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"
)

var (
	listen   = flag.String("listen", "127.0.0.1:8896", "listen address (8897 занят HTTP API hbd, 8898 — его heartbeat)")
	keyPath  = flag.String("key", "", "client private key accepted by targets")
	hostKey  = flag.String("hostkey", "", "target embedded server host key line")
	tlsCert  = flag.String("cert", "", "TLS certificate (включает HTTPS на этом же порту)")
	tlsKey   = flag.String("tlskey", "", "TLS key")
	htpasswd = flag.String("htpasswd", "", "htpasswd файл для basic-auth (bcrypt-строки $2a$/$2y$)")
)

func basicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || !checkHtpasswd(*htpasswd, user, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="portassh"`)
			http.Error(w, "401", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func checkHtpasswd(path, user, pass string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) != 2 || parts[0] != user {
			continue
		}
		if !strings.HasPrefix(parts[1], "$2") {
			continue // apr1-строки для nginx — их пропускаем
		}
		if bcrypt.CompareHashAndPassword([]byte(parts[1]), []byte(pass)) == nil {
			return true
		}
	}
	return false
}

type wsMsg struct {
	Type string `json:"t"`
	Cols int    `json:"c"`
	Rows int    `json:"r"`
}

const pageHTML = `<!DOCTYPE html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>portassh — терминал</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.min.css">
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js"></script>
<style>html,body{margin:0;height:100%;background:#12151c}#t{height:100%}</style>
</head><body><div id="t"></div>
<script>
const PORT = new URLSearchParams(location.search).get('port') || '2200';
const term = new Terminal({cursorBlink:true, fontSize:14, theme:{background:'#12151c'}});
term.open(document.getElementById('t'));
term.writeln('\x1b[36mportassh — подключение к машине (порт ' + PORT + ')...\x1b[0m');
const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
const wsURL = proto + location.host + '/ws?port=' + PORT;
term.writeln('\x1b[90mws-url: ' + wsURL + '\x1b[0m');
let ws;
try {
  ws = new WebSocket(wsURL);
} catch (e) {
  term.writeln('\x1b[31mWebSocket error: ' + e + '\x1b[0m');
}
ws.binaryType = 'arraybuffer';
ws.onopen = () => { term.writeln('\x1b[32m— канал открыт —\x1b[0m'); term.focus(); };
ws.onerror = (e) => term.writeln('\r\n\x1b[31mws onerror\x1b[0m');
ws.onmessage = (e) => term.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data));
ws.onclose = (e) => term.writeln('\r\n\x1b[31m— сессия закрыта — код ' + e.code + (e.reason ? ': ' + e.reason : '') + ' —\x1b[0m');
term.onData((d) => { if (ws.readyState === 1) ws.send(d); });
term.onResize(({cols, rows}) => { if (ws.readyState === 1) ws.send(JSON.stringify({t:'r', c:cols, r:rows})); });
window.addEventListener('resize', () => term.resize(Math.floor(innerWidth/9), Math.floor(innerHeight/18)));
</script></body></html>`

type wsWriter struct{ ws *websocket.Conn }

func (w wsWriter) Write(p []byte) (int, error) {
	if err := websocket.Message.Send(w.ws, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func bridge(ws *websocket.Conn, port string, signer ssh.Signer, hostPub ssh.PublicKey) {
	addr := net.JoinHostPort("127.0.0.1", port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		fmt.Fprintf(ws, "portassh: target %s недоступна: %v\r\n", port, err)
		return
	}
	defer conn.Close()
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              "anyuser",
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   ssh.FixedHostKey(hostPub),
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(ws, "portassh: ssh: %v\r\n", err)
		return
	}
	defer cc.Close()
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		fmt.Fprintf(ws, "portassh: session: %v\r\n", err)
		return
	}
	defer session.Close()
	if err := session.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		fmt.Fprintf(ws, "portassh: pty: %v\r\n", err)
		return
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return
	}
	session.Stderr = wsWriter{ws}
	if err := session.Shell(); err != nil {
		fmt.Fprintf(ws, "portassh: shell: %v\r\n", err)
		return
	}

	done := make(chan struct{}, 1)
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(wsWriter{ws}, stdout)
	}()
	go func() {
		for range time.Tick(30 * time.Second) {
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				_ = ws.Close()
				return
			}
		}
	}()
	for {
		var data []byte
		if err := websocket.Message.Receive(ws, &data); err != nil || len(data) == 0 {
			break
		}
		if len(data) > 0 && data[0] == '{' {
			var m wsMsg
			if json.Unmarshal(data, &m) == nil && m.Type == "r" && m.Cols > 0 && m.Rows > 0 {
				_ = session.WindowChange(m.Rows, m.Cols)
				continue
			}
		}
		if _, err := stdin.Write(data); err != nil {
			break
		}
	}
	_ = session.Close()
	<-done
}

func main() {
	flag.Parse()
	keyBytes, err := os.ReadFile(*keyPath)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		log.Fatal(err)
	}
	_, _, hostPub, _, _, err := ssh.ParseKnownHosts([]byte(*hostKey))
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/wsterm", basicAuth(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/wsterm") && !strings.HasSuffix(r.URL.Path, "/wsterm/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, pageHTML)
	}))
	// origin-check x/net/websocket отключён: доступ закрывает nginx basic-auth
	wsServer := websocket.Server{
		Handler: websocket.Handler(func(ws *websocket.Conn) {
			bridge(ws, ws.Request().URL.Query().Get("port"), signer, hostPub)
		}),
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
	}
	http.Handle("/ws", basicAuth(wsServer.ServeHTTP))
	// SSH-туннель: WS ↔ локальный sshd (резервный транспорт portassh).
	// Под basic-auth, как /ws: клиент шлёт Authorization сама (transport_ws.go).
	http.Handle("/tunnel", basicAuth(websocket.Server{
		Handler: websocket.Handler(func(ws *websocket.Conn) {
			up, err := net.DialTimeout("tcp", "127.0.0.1:22", 5*time.Second)
			if err != nil {
				fmt.Fprintf(ws, "sshd unreachable: %v", err)
				return
			}
			defer up.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(ws, up); done <- struct{}{} }()
			go func() { io.Copy(up, ws); done <- struct{}{} }()
			<-done
		}),
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
	}.ServeHTTP))
	log.Printf("wsterm listening on %s", *listen)
	if *tlsCert != "" {
		log.Fatal(http.ListenAndServeTLS(*listen, *tlsCert, *tlsKey, nil))
	}
	log.Fatal(http.ListenAndServe(*listen, nil))
}
