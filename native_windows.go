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
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	ssh "github.com/gliderlabs/ssh"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/transform"
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

// stateText: текстовое состояние службы.
func stateText(st uint32) string {
	switch st {
	case windows.SERVICE_RUNNING:
		return "RUNNING"
	case windows.SERVICE_STOPPED:
		return "STOPPED"
	case windows.SERVICE_START_PENDING:
		return "START_PENDING"
	case windows.SERVICE_STOP_PENDING:
		return "STOP_PENDING"
	case windows.SERVICE_PAUSED:
		return "PAUSED"
	}
	return "UNKNOWN"
}

// svcControl: stop/start/restart службы через OpenSCManager + ControlService.
func svcControl(action, name string) (string, error) {
	mgr, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", fmt.Errorf("SCM: %w", err)
	}
	defer windows.CloseServiceHandle(mgr)
	np, _ := windows.UTF16PtrFromString(name)
	svc, err := windows.OpenService(mgr, np, windows.SERVICE_QUERY_STATUS|windows.SERVICE_START|windows.SERVICE_STOP)
	if err != nil {
		return "", fmt.Errorf("служба %q не найдена: %w", name, err)
	}
	defer windows.CloseServiceHandle(svc)
	var st windows.SERVICE_STATUS
	waitState := func(target uint32, timeout time.Duration) error {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if err := windows.QueryServiceStatus(svc, &st); err != nil {
				return err
			}
			if st.CurrentState == target {
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return fmt.Errorf("таймаут ожидания состояния %s", stateText(target))
	}
	switch action {
	case "stop":
		if err := windows.ControlService(svc, windows.SERVICE_CONTROL_STOP, &st); err != nil {
			return "", err
		}
		err = waitState(windows.SERVICE_STOPPED, 15*time.Second)
	case "start":
		if err := windows.StartService(svc, 0, nil); err != nil {
			return "", err
		}
		err = waitState(windows.SERVICE_RUNNING, 15*time.Second)
	case "restart":
		if err := windows.ControlService(svc, windows.SERVICE_CONTROL_STOP, &st); err != nil {
			return "", err
		}
		if err = waitState(windows.SERVICE_STOPPED, 20*time.Second); err != nil {
			return "", err
		}
		if err = windows.StartService(svc, 0, nil); err != nil {
			return "", err
		}
		err = waitState(windows.SERVICE_RUNNING, 20*time.Second)
	default:
		return "", fmt.Errorf("неизвестное действие: %s", action)
	}
	if err != nil {
		return "", err
	}
	if err := windows.QueryServiceStatus(svc, &st); err != nil {
		return "", err
	}
	return stateText(st.CurrentState), nil
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

// listAutoruns: точки автозагрузки без спавнов утилит — ключи реестра
// Run/RunOnce (HKCU, HKLM, WOW6432Node) и содержимое папок Startup с четким префиксом.
func listAutoruns(s ssh.Session) {
	collect := func(rootName string, root registry.Key, sub string) {
		k, err := registry.OpenKey(root, sub, registry.QUERY_VALUE)
		if err != nil {
			return // ключа нет — не ошибка
		}
		defer k.Close()
		names, err := k.ReadValueNames(-1)
		if err != nil {
			return
		}
		for _, n := range names {
			v, _, _ := k.GetStringValue(n)
			fmt.Fprintf(s, "%s\\%s\\%s = %s\n", rootName, sub, n, v)
		}
	}
	collect("HKCU", registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`)
	collect("HKCU", registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`)
	collect("HKLM", registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`)
	collect("HKLM", registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`)
	collect("HKLM", registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run`)
	for _, item := range []struct {
		label string
		dir   string
	}{
		{"User Startup", filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Startup`)},
		{"Common Startup", filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs\Startup`)},
	} {
		if ents, err := os.ReadDir(item.dir); err == nil {
			for _, e := range ents {
				fmt.Fprintf(s, "%s\\%s\n", item.label, e.Name())
			}
		}
	}
}

// queryEvents: выборка ошибок и предупреждений из системных журналов Windows через wevtutil.
func queryEvents(s ssh.Session, args string) {
	channel := "System"
	limit := 15
	fields := strings.Fields(args)
	if len(fields) >= 1 {
		if strings.EqualFold(fields[0], "application") {
			channel = "Application"
		} else if strings.EqualFold(fields[0], "system") {
			channel = "System"
		}
	}
	if len(fields) >= 2 {
		if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	// /q: обязателен: позиционный XPath мыvtutil отбрасывает (exit 87)
	cmd := exec.Command("wevtutil", "qe", channel, fmt.Sprintf("/c:%d", limit), "/rd:true", "/f:text", "/q:*[System[(Level=1 or Level=2 or Level=3)]]")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(s, "sys: events error: %v\n", err)
		return
	}
	if len(bytes.TrimSpace(out)) == 0 {
		fmt.Fprintf(s, "sys: нет критических событий в журнале %s\n", channel)
		return
	}
	if dec := oemDecoder(); dec != nil {
		tr := transform.NewReader(bytes.NewReader(out), dec)
		if decOut, rerr := io.ReadAll(tr); rerr == nil {
			s.Write(decOut)
			return
		}
	}
	s.Write(out)
}

func sysNative(s ssh.Session, sub string) bool {
	// sys:msg <base64-utf8> — сообщение пользователю (плашка на экране)
	if b64, ok := strings.CutPrefix(sub, "msg "); ok {
		text, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if derr != nil || len(text) == 0 {
			fmt.Fprintln(s, "sys: msg: пустой текст")
			s.Exit(1)
			return true
		}
		showToast(string(text))
		fmt.Fprintln(s, "sys: сообщение показано пользователю")
		s.Exit(0)
		return true
	}
	// sys:clip <base64-utf8> — текст в буфер обмена машины (через PowerShell)
	if b64, ok := strings.CutPrefix(sub, "clip "); ok {
		text, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if derr != nil || len(text) == 0 {
			fmt.Fprintln(s, "sys: clip: пустой текст")
			s.Exit(1)
			return true
		}
		if err := setClipboardData(text); err != nil {
			fmt.Fprintf(s, "sys: clip: %v\n", err)
			s.Exit(1)
			return true
		}
		fmt.Fprintln(s, "sys: буфер обмена машины обновлён")
		s.Exit(0)
		return true
	}
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
		// экранный ввод из SSH: sys:click/dbl/rclick/press/move/release/wheel/key <args>
		for _, p := range []string{"click", "dbl", "rclick", "press", "move", "release", "wheel", "key", "msg"} {
			if sub == p || strings.HasPrefix(sub, p+" ") {
				execScreenInput(sub)
				s.Exit(0)
				return true
			}
		}
		// sys:svc-<start|stop|restart> <name> — управление службами
		if action, ok := strings.CutPrefix(sub, "svc-"); ok {
			act, name, _ := strings.Cut(action, " ")
			name = strings.TrimSpace(name)
			if act == "" || name == "" || (act != "start" && act != "stop" && act != "restart") {
				fmt.Fprintln(s, "sys: использование: sys:svc-<start|stop|restart> <имя_службы>")
				s.Exit(2)
				return true
			}
			msg, err := svcControl(act, name)
			if err != nil {
				fmt.Fprintf(s, "sys: %s %s: %v\n", act, name, err)
				s.Exit(1)
				return true
			}
			fmt.Fprintf(s, "sys: служба %s — %s\n", name, msg)
			s.Exit(0)
			return true
		}
		// sys:events <system|application> [limit]
		if evArgs, ok := strings.CutPrefix(sub, "events"); ok {
			queryEvents(s, strings.TrimSpace(evArgs))
			s.Exit(0)
			return true
		}
		// sys:autoruns — точки автозагрузки (диагностика «комп тормозит»)
		if sub == "autoruns" {
			listAutoruns(s)
			s.Exit(0)
			return true
		}
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
