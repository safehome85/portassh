//go:build !windows

package main

import (
	"fmt"
	"io"
	"net"
	"time"
)

// -doctor на non-Windows: сетевые проверки (реестр/ConPTY — только Windows).
func runDoctor(w io.Writer, cfg config) int {
	host, port := splitRelayHostPort(cfg.relay)
	fails := 0
	ips, err := net.LookupHost(host)
	dPrint(w, "DNS резолв relay ("+host+")", dres{err == nil, dnsMsg(err, ips)})
	sshAddr := net.JoinHostPort(host, port)
	conn, derr := net.DialTimeout("tcp", sshAddr, 5*time.Second)
	if derr == nil {
		conn.Close()
	}
	dPrint(w, "TCP "+port+" до relay", dres{derr == nil, tcpMsg(derr)})
	fmt.Fprintf(w, "=== диагностика завершена: провалов %d ===\n", fails)
	return fails
}
