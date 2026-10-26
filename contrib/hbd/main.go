// hbd: лёгкий приёмщик heartbeat-строк portassh на relay.
// Слушает 127.0.0.1:8898 (доступ только через direct-tcpip из SSH-туннелей
// клиентов), каждая строка = строка реестра, пишется в online.txt (O_APPEND).
// Ни одного форка sh.
package main

import (
	"bufio"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	listen = "127.0.0.1:8898"
	file   = "/home/portassh/online.txt"
)

func valid(line string) bool {
	f := strings.Fields(line)
	if len(f) < 4 || len(f) > 9 {
		return false
	}
	for i, x := range f {
		if x == "" || strings.ContainsAny(x, "\r\n\x00") && i != 0 {
			return false
		}
	}
	// поля: host port time role [user os disk ram ver]
	if _, ok := map[string]bool{"user": true, "admin": true}[f[3]]; !ok {
		return false
	}
	return true
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
	w := bufio.NewWriter(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !valid(line) {
			continue
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			return
		}
	}
	w.Flush()
}

func main() {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}
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
