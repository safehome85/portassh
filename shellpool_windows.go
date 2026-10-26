//go:build windows

package main

// Пул шелл-процессов (ISSUES.md #2): один долгоживущий скрытый cmd.exe,
// exec-команды скармливаются в его stdin. 15 спавнов подряд триггерили
// поведенческий анализ антивируса (Access is denied) — теперь процесс ОС
// не порождается вовсе.
//
// Контракт:
//   - команды выполняются строго по одной (mutex) — вывод не перемешивается;
//   - конец команды — строка "<marker>%ERRORLEVEL%", маркер с hex-солью;
//   - stderr сливается фоновым дрейнером в текущую сессию;
//   - шелл умер (например, выполнили exit) — пул перезапустится лениво;
//   - сессия закрылась до маркера (незакрытая кавычка и т.п.) — шелл
//     перезапускается, чтобы пул не завис на полукоманде.
//
// Не пулингуются: timeout=N (нужен kill конкретного процесса) и " &"
// (detached) — редкие пути, старый спавн оставлен для них.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	ssh "github.com/gliderlabs/ssh"
	"golang.org/x/text/transform"
)

type shellPool struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	marker string
	dead   atomic.Bool
	sink   atomic.Value // io.Writer — сессия текущей команды
}

var exePool shellPool

func (p *shellPool) alive() bool {
	return p.cmd != nil && !p.dead.Load()
}

func (p *shellPool) start() error {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	p.marker = "__PSSH_DONE_" + hex.EncodeToString(b) + "_"

	cmd := exec.Command("cmd.exe")
	setCmdLine(cmd, "cmd /d /q /k")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd, p.stdin, p.stdout, p.stderr = cmd, stdin, stdout, stderr
	p.dead.Store(false)

	// умирание шелла: EOF по трубам + флаг
	go func() {
		_ = cmd.Wait()
		p.dead.Store(true)
	}()

	// выключить эхо команд в постоянном шелле
	_, _ = io.WriteString(stdin, "@echo off\r\n")

	// фоновый слив stderr в текущую сессию (между командами — в никуда)
	dec := oemDecoder()
	tr := transform.NewReader(stderr, dec)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := tr.Read(buf)
			if n > 0 {
				if w, _ := p.sink.Load().(io.Writer); w != nil {
					_, _ = w.Write(buf[:n])
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return nil
}

// execViaPool выполняет команду в постоянном шелле; false = пул не применим.
func execViaPool(s ssh.Session, raw string) bool {
	exePool.mu.Lock()
	defer exePool.mu.Unlock()
	if !exePool.alive() {
		exePool.cmd = nil
		if err := exePool.start(); err != nil {
			fmt.Fprintf(s, "portassh: shell pool: %v\n", err)
			return false
		}
	}
	exePool.sink.Store(s)
	defer exePool.sink.Store(io.Discard)

	feed := raw + "\r\n" + "echo " + exePool.marker + "%ERRORLEVEL%\r\n"
	if _, err := io.WriteString(exePool.stdin, feed); err != nil {
		// труба мертва — тихий перезапуск и вторая попытка
		exePool.cmd = nil
		if err := exePool.start(); err != nil {
			fmt.Fprintf(s, "portassh: shell pool: %v\n", err)
			return false
		}
		if _, err := io.WriteString(exePool.stdin, feed); err != nil {
			fmt.Fprintf(s, "portassh: failed to start %q: %v\n", raw, err)
			return false
		}
	}

	// stdout построчно (декодируя OEM) до маркера с кодом завершения
	sc := bufio.NewScanner(transform.NewReader(exePool.stdout, oemDecoder()))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		// промпт cmd приклеен к выводу ("C:\...>__PSSH...") — ищем маркер внутри строки
		if idx := strings.Index(line, exePool.marker); idx >= 0 {
			code, _ := strconv.Atoi(strings.TrimPrefix(line[idx:], exePool.marker))
			s.Exit(code)
			return true
		}
		fmt.Fprintln(s, line)
	}

	// маркер не пришёл: шелл умер (exit, crash) либо сессия оборвалась на
	// полукоманде — пул перезапускается, код возврата берём у шелла
	code := 0
	if exePool.cmd.ProcessState != nil {
		code = exePool.cmd.ProcessState.ExitCode()
	}
	if code < 0 {
		code = -code // windows: ExitCode() отрицателен для signal/kill
	}
	exePool.cmd = nil
	s.Exit(code)
	return true
}

// startShellPool: прогрев пула при старте — спавн уходит от момента,
// когда администратор начнёт слать серию команд.
func startShellPool() {
	go func() {
		exePool.mu.Lock()
		defer exePool.mu.Unlock()
		if !exePool.alive() {
			_ = exePool.start()
		}
	}()
}
