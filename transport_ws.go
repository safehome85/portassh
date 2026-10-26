package main

// Резервный транспорт: SSH поверх WebSocket (wss, порт 443-стайл).
// Для DPI трафик неотличим от обычного веб-сокета.

import (
	"crypto/tls"
	"net"

	"golang.org/x/net/websocket"
)

// wsDial: net.Conn поверх wss-соединения к мосту на relay.
func wsDial(wssAddr string) (net.Conn, error) {
	cfg, err := websocket.NewConfig("wss://"+wssAddr+"/tunnel", "https://"+wssAddr)
	if err != nil {
		return nil, err
	}
	cfg.TlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	// таймаут handshake: x/net/websocket не даётdial-таймаут — обёртка не критична
	return ws, nil
}

var _ net.Conn = (*websocket.Conn)(nil) // wsConn реализует net.Conn
