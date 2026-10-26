//go:build windows

package main

// -doctor: самодиагностика для инженера на месте. Консольный вывод
// [OK]/[FAIL]/[INFO]; GUI-приложение подцепляет родительскую консоль
// (запуск из cmd) или создаёт свою (двойной клик).

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	gossh "golang.org/x/crypto/ssh"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const attachParentProcess = ^uintptr(0) // (DWORD)-1

// ensureConsole: GUI-процесс без консоли не печатает — цепляем родительскую
// или создаём свою. Возвращает writer: SSH-канал + консоль (если есть).
func ensureConsole() io.Writer {
	orig := os.Stdout
	modkernel32 := syscall.NewLazyDLL("kernel32.dll")
	attach := modkernel32.NewProc("AttachConsole")
	free := modkernel32.NewProc("FreeConsole")
	alloc := modkernel32.NewProc("AllocConsole")
	createFile := modkernel32.NewProc("CreateFileW")

	if r, _, _ := attach.Call(attachParentProcess); r == 0 {
		free.Call()
		alloc.Call()
	}
	for _, name := range []string{"CONOUT$", "CONOUT$"} {
		h, _, err := createFile.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(name))),
			0xC0000000 /*GENERIC_READ|WRITE*/, 3 /*SHARE*/, 0, 3 /*OPEN_EXISTING*/, 0, 0)
		if h == ^uintptr(0) || h == 0 {
			_ = err
			continue
		}
		if name == "CONOUT$" && h != 0 && h != ^uintptr(0) {
			f := os.NewFile(h, name)
			if f != nil {
				return io.MultiWriter(orig, f)
			}
		}
	}
	return orig
}

// runDoctor выполняет проверки и возвращает число провалов.
func runDoctor(w io.Writer, cfg config) int {
	w = ensureConsole()
	fmt.Fprintln(w, "=== portassh doctor ===")
	host, port := splitRelayHostPort(cfg.relay)
	fails := 0
	check := func(name string, r dres) {
		if !r.ok {
			fails++
		}
		dPrint(w, name, r)
	}

	// 1. DNS
	ips, err := net.LookupHost(host)
	check("DNS резолв relay ("+host+")", dres{err == nil, dnsMsg(err, ips)})

	// 2. TCP-порт sshd
	sshAddr := net.JoinHostPort(host, port)
	conn, derr := net.DialTimeout("tcp", sshAddr, 5*time.Second)
	if derr == nil {
		conn.Close()
	}
	check("TCP "+port+" до relay", dres{derr == nil, tcpMsg(derr)})

	// 3. host key + аутентификация baked-ключом (полный handshake)
	if derr == nil {
		signer := mustLoadBakedSigner(relayPrivKeyB64)
		hk, herr := bakedRelayHostKey()
		if herr != nil {
			check("Host key (baked)", dres{false, herr.Error()})
		} else {
			addr2 := net.JoinHostPort(host, port)
			c2, err2 := net.DialTimeout("tcp", addr2, 5*time.Second)
			if err2 != nil {
				check("Host key match", dres{false, "повторное соединение не удалось: " + err2.Error()})
			} else {
				cc, chans, reqs, herr2 := gossh.NewClientConn(c2, addr2, &gossh.ClientConfig{
					User:              cfg.relayUser,
					Auth:              []gossh.AuthMethod{gossh.PublicKeys(signer)},
					HostKeyCallback:   gossh.FixedHostKey(hk),
					HostKeyAlgorithms: []string{gossh.KeyAlgoED25519},
					Timeout:           5 * time.Second,
				})
				if herr2 == nil {
					cl := gossh.NewClient(cc, chans, reqs)
					cl.Close()
					check("Host key match + auth baked-ключом", dres{true, ""})
				} else if strings.Contains(strings.ToLower(herr2.Error()), "host key") {
					check("Host key match", dres{false, "НЕСОВПАДЕНИЕ — возможен MITM или это не наш relay: " + herr2.Error()})
					fails++
				} else {
					// host key проверен, ошибка дальше по стеку (auth и т.п.)
					check("Host key match", dres{true, ""})
				}
				_ = c2.Close()
			}
		}
	} else {
		dInfo(w, "Host key match", "пропущено — порт недоступен")
	}

	// 4. ConPTY / версия Windows
	v := windows.RtlGetVersion()
	if v == nil {
		check("ConPTY", dres{false, "не удалось определить версию Windows"})
	} else {
		build := v.BuildNumber
		conptyOK := build >= 18309 // 1809 — первая версия с ConPTY
		check(fmt.Sprintf("ConPTY (Windows %d.%d build %d)", v.MajorVersion, v.MinorVersion, build),
			dres{conptyOK, msgIf(!conptyOK, "устаревшая сборка — интерактивный шелл недоступен")})
		_ = build
	}

	// 5. каталог рядом с exe (лог, порт-файл)
	dir := exeDir()
	testPath := filepath.Join(dir, "portassh_doctor.tmp")
	f, ferr := os.Create(testPath)
	if ferr == nil {
		_, werr := f.WriteString("ok")
		f.Close()
		os.Remove(testPath)
		check("Каталог "+dir+" доступен для записи", dres{werr == nil, errStr(werr)})
	} else {
		check("Каталог "+dir+" доступен для записи", dres{false, ferr.Error()})
	}

	// 6. реестр HKCU (блокировки со стороны AV/политик)
	regErr := func() error {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\portassh-doctor`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		if err := k.SetDWordValue("probe", 1); err != nil {
			return err
		}
		rk, err := registry.OpenKey(registry.CURRENT_USER, `Software\portassh-doctor`, registry.QUERY_VALUE)
		if err != nil {
			return err
		}
		defer rk.Close()
		v, _, err := rk.GetIntegerValue("probe")
		if err != nil {
			return err
		}
		if v != 1 {
			return fmt.Errorf("probe value mismatch: %d", v)
		}
		if err := rk.DeleteValue("probe"); err != nil {
			return err
		}
		return registry.DeleteKey(registry.CURRENT_USER, `Software\portassh-doctor`)
	}()
	check("Запись в реестр HKCU", dres{regErr == nil, errStr(regErr)})

	// 7. антивирус (информационно)
	if av := detectAntivirus(); av != nil {
		dInfo(w, "Антивирус", av.Product+" · "+av.State)
	}

	// 8. подсказка про сохранённый порт
	if p := readPersistedPort(); p > 0 {
		dInfo(w, "Сохранённый порт", strconv.Itoa(p)+" (машина вернётся на него после сбоя)")
	}

	fmt.Fprintf(w, "=== диагностика завершена: провалов %d ===\n", fails)
	fmt.Fprintln(w, "(если вывод не видно — запускайте из cmd.exe: portassh.exe -doctor)")
	return fails
}
