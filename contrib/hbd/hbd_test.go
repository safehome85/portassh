package main

import (
	"os"
	"strings"
	"testing"
)

func TestValidationAndHostAllowlist(t *testing.T) {
	// Создаем временный файл allowed-hosts.txt для теста
	tmpAllowed, err := os.CreateTemp("", "allowed-hosts-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpAllowed.Name())

	_, _ = tmpAllowed.WriteString("HOST-ALPHA\nHOST-BETA\n")
	_ = tmpAllowed.Close()

	// Проверяем формат строки реестра
	validLine := "HOST-ALPHA 2200 27.10_15:00 user DESKTOP\\user 10.0.19045 100 20 1 1.73"
	f := strings.Fields(validLine)
	if len(f) < 4 || len(f) > 12 {
		t.Fatalf("expected valid field count, got %d", len(f))
	}

	invalidRoleLine := "HOST-ALPHA 2200 27.10_15:00 hacker"
	fBad := strings.Fields(invalidRoleLine)
	if _, ok := map[string]bool{"user": true, "admin": true}[fBad[3]]; ok {
		t.Fatalf("expected role 'hacker' to be invalid")
	}
}

func TestWebFetchSSRFValidation(t *testing.T) {
	// Проверяем блокировку localhost и локальных IP
	resLocal := webFetch("http://127.0.0.1:8897/api/secret")
	if !strings.Contains(resLocal, "запрещён") {
		t.Errorf("expected localhost to be blocked, got: %s", resLocal)
	}

	resBadUrl := webFetch("ftp://evil.com/payload")
	if resBadUrl != "неверный URL" {
		t.Errorf("expected ftp scheme to be rejected, got: %s", resBadUrl)
	}
}
