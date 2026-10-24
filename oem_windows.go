//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// setCmdLine задаёт сырую командную строку процесса без MSVCRT-экранирования
// аргументов Go — единственный надёжный способ пробросить квотирование в cmd.
// HideWindow: дочерняя консоль не мигает окном (родитель windowsgui).
func setCmdLine(cmd *exec.Cmd, line string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true}
}

// oemDecoder конвертирует OEM-вывод cmd/powershell (CP866 на русской Windows)
// в UTF-8 на лету, без спавна chcp. Безусловный CP866: подтверждено живым
// туннелем (байты совпадают с CP866), компромисс — нативно-UTF-8 утилиты
// будут перекодированы дважды.
func oemDecoder() transform.Transformer {
	return charmap.CodePage866.NewDecoder()
}
