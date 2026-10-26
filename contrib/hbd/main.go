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
	listen    = "127.0.0.1:8898"
	file      = "/home/portassh/online.txt"
	compEvery = 5000 // дозаписей между ротациями
	compKeep  = 2000 // строк остаётся после ротации
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
