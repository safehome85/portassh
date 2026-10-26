//go:build windows

package main

// Нативная диагностика через Win32 API (без cmd.exe и спавнов): префикс
// "sys:" в exec-команде. Для антивируса это внутренняя работа программы.
//
//	sys:whoami  — DOMAIN\user
//	sys:hostname — имя машины
//	sys:procs   — PID и имя каждого процесса
//	sys:svcs    — службы и состояния (RUNNING/STOPPED/...)
//	sys:disks   — свободно/всего по логическим дискам

import (
	"fmt"
	"os"
	"unsafe"

	ssh "github.com/gliderlabs/ssh"
	"golang.org/x/sys/windows"
)

func sysNative(s ssh.Session, sub string) bool {
	switch sub {
	case "whoami":
		fmt.Fprintf(s, "%s\\%s\n", os.Getenv("USERDOMAIN"), os.Getenv("USERNAME"))
	case "hostname":
		h, _ := os.Hostname()
		fmt.Fprintln(s, h)
	case "procs":
		snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
		if err != nil {
			fmt.Fprintf(s, "sys: %v\n", err)
			return true
		}
		defer windows.CloseHandle(snap)
		var pe windows.ProcessEntry32
		pe.Size = uint32(unsafe.Sizeof(pe))
		if err := windows.Process32First(snap, &pe); err != nil {
			fmt.Fprintf(s, "sys: %v\n", err)
			return true
		}
		for {
			fmt.Fprintf(s, "%d %s\n", pe.ProcessID, windows.UTF16ToString(pe.ExeFile[:]))
			if err := windows.Process32Next(snap, &pe); err != nil {
				break
			}
		}
	case "svcs":
		mgr, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ENUMERATE_SERVICE)
		if err != nil {
			fmt.Fprintf(s, "sys: %v\n", err)
			return true
		}
		defer windows.CloseServiceHandle(mgr)
		var needed, returned, resume uint32
		_ = windows.EnumServicesStatusEx(mgr, windows.SC_STATUS_PROCESS_INFO,
			windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL, nil, 0, &needed, &returned, &resume, nil)
		if needed == 0 {
			fmt.Fprintln(s, "sys: no services")
			return true
		}
		buf := make([]byte, needed)
		if err := windows.EnumServicesStatusEx(mgr, windows.SC_STATUS_PROCESS_INFO,
			windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL, &buf[0], needed, &needed, &returned, &resume, nil); err != nil {
			fmt.Fprintf(s, "sys: %v\n", err)
			return true
		}
		entries := (*[1024]windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0]))[:returned:returned]
		for i := range entries {
			name := windows.UTF16PtrToString(entries[i].ServiceName)
			disp := windows.UTF16PtrToString(entries[i].DisplayName)
			st := entries[i].ServiceStatusProcess.CurrentState
			state := "UNKNOWN"
			switch st {
			case windows.SERVICE_RUNNING:
				state = "RUNNING"
			case windows.SERVICE_STOPPED:
				state = "STOPPED"
			case windows.SERVICE_START_PENDING:
				state = "START_PENDING"
			case windows.SERVICE_STOP_PENDING:
				state = "STOP_PENDING"
			case windows.SERVICE_PAUSED:
				state = "PAUSED"
			}
			fmt.Fprintf(s, "%s %s %s\n", name, state, disp)
		}
	case "disks":
		bits, _ := windows.GetLogicalDrives()
		for d := 0; d < 26; d++ {
			if bits&(1<<uint(d)) == 0 {
				continue
			}
			root, _ := windows.UTF16PtrFromString(string(rune('A'+d)) + ":\\")
			var free, total, totalFree uint64
			if err := windows.GetDiskFreeSpaceEx(root, &free, &total, &totalFree); err != nil {
				continue
			}
			gb := func(b uint64) string {
				return fmt.Sprintf("%.1fGB", float64(b)/(1<<30))
			}
			fmt.Fprintf(s, "%s free=%s total=%s\n", string(rune('A'+d)), gb(free), gb(total))
		}
	default:
		return false
	}
	return true
}
