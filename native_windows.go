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
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	ssh "github.com/gliderlabs/ssh"
	"golang.org/x/sys/windows"
)

var (
	procK32GetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	procPSGetProcessMemoryInfo  = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
)

// SIZE_T зависит от разрядности — uintptr совпадает на 386 и amd64.
type processMemoryCounters struct {
	Cb                      uint32
	PageFaultCount          uint32
	PeakWorkingSetSize      uintptr
	WorkingSetSize          uintptr
	QuotaPeakPagedPoolUsage uintptr
	QuotaPagedPoolUsage     uintptr
	QuotaNonPagedPoolUsage  uintptr
	PagefileUsage           uintptr
	PeakPagefileUsage       uintptr
}

// procWorkingSetKB: рабочий набор процесса в КБ (-1 = недоступен).
func procWorkingSetKB(pid uint32) int64 {
	h, err := windows.OpenProcess(0x0400|0x0010 /*QUERY_INFORMATION|VM_READ*/, false, pid)
	if err != nil {
		return -1
	}
	defer windows.CloseHandle(h)
	// WOW64: thunked API требует 64-битный размер структуры — буфер с запасом
	buf := make([]byte, 128)
	mc := (*processMemoryCounters)(unsafe.Pointer(&buf[0]))
	mc.Cb = uint32(len(buf))
	r, _, err := procK32GetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		log.Printf("meminfo pid=%d: %v", pid, err)
		return -1
	}
	return int64(mc.WorkingSetSize)
}

// killProcess: TerminateProcess с защитой от самоубийства и системных PID.
func killProcess(pid uint32) error {
	if pid <= 4 {
		return fmt.Errorf("системный процесс")
	}
	if pid == uint32(os.Getpid()) {
		return fmt.Errorf("это сам portassh")
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

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
			fmt.Fprintf(s, "%d %s %d\n", pe.ProcessID, windows.UTF16ToString(pe.ExeFile[:]), procWorkingSetKB(pe.ProcessID))
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
		// sys:kill <pid> — «снять задачу» из веб-панели
		if pidStr, ok := strings.CutPrefix(sub, "kill "); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
			if err != nil || pid <= 4 || uint32(pid) == uint32(os.Getpid()) {
				fmt.Fprintln(s, "sys: kill отклонён")
				s.Exit(1)
				return true
			}
			if err := killProcess(uint32(pid)); err != nil {
				fmt.Fprintf(s, "sys: kill %d: %v\n", pid, err)
				s.Exit(1)
				return true
			}
			fmt.Fprintf(s, "sys: процесс %d завершён\n", pid)
			s.Exit(0)
			return true
		}
		return false
	}
	s.Exit(0)
	return true
}
