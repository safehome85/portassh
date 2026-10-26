package main

// Общие помощники режима -doctor (используются на всех платформах).

import (
	"fmt"
	"io"
	"strings"
)

type dres struct {
	ok  bool
	msg string
}

func dPrint(w io.Writer, name string, r dres) {
	tag := "[OK]  "
	if !r.ok {
		tag = "[FAIL] "
	}
	msg := ""
	if r.msg != "" {
		msg = " — " + r.msg
	}
	fmt.Fprintf(w, "%s %s%s\n", tag, name, msg)
}

func dInfo(w io.Writer, name, msg string) {
	fmt.Fprintf(w, "[INFO] %s%s\n", name, sepMsg(msg))
}

func sepMsg(msg string) string {
	if msg == "" {
		return ""
	}
	return " — " + msg
}

func dnsMsg(err error, ips []string) string {
	if err != nil {
		return err.Error()
	}
	if len(ips) > 2 {
		ips = ips[:2]
	}
	return " → " + strings.Join(ips, ", ")
}

func tcpMsg(err error) string {
	if err == nil {
		return "порт открыт"
	}
	s := err.Error()
	if strings.Contains(s, "refused") {
		return "connection refused — sshd на relay не отвечает"
	}
	if strings.Contains(s, "timed out") || strings.Contains(s, "timeout") {
		return "порт фильтруется файрволом/корпоративным прокси"
	}
	return s
}

func msgIf(cond bool, msg string) string {
	if cond {
		return msg
	}
	return ""
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
