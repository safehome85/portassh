//go:build windows

package main

// Реакция на сон/пробуждение ноутбука (ISSUES: Resume → мгновенный реконнект).
// Скрытое TOP-LEVEL окно (не message-only — broadcast WM_POWERBROADCAST до
// message-only окон не доходит) получает уведомления питания; по Resume
// закрываем «подвисший» SSH-клиент — relayLoop переподключается за ~2 с.

import (
	"log"
	"syscall"
	"unsafe"
)

const (
	wmQueryEndSession     = 0x0011
	wmEndSession          = 0x0016
	wmPowerBroadcast      = 0x0218
	pbtApmSuspend         = 4
	pbtApmResumeSuspend   = 6
	pbtApmResumeAutomatic = 7
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  uintptr
	LpszClassName uintptr
	HIconSm       uintptr
}

type msgW struct {
	HWnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	PtX, PtY int32
}

var powerWndProc = syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	// события трей-иконки и таймер статуса
	if msg == trayCallbackMsg || msg == 0x0113 {
		return trayHandleEvent(hwnd, msg, wp, lp)
	}
	switch msg {
	case wmQueryEndSession:
		// начало завершения Windows: быстро снимаем себя с дашборда (асинхронно,
		// ~1 RTT через живой туннель), завершению не препятствуем
		if c := activeClient.Load(); c != nil {
			goSafe("offline", func() { sendOffline(c) })
		}
		return 1 // разрешаем завершение
	case wmEndSession:
		return 0
	case wmPowerBroadcast:
		switch wp {
		case pbtApmResumeAutomatic, pbtApmResumeSuspend:
			log.Print("power: resume — forcing tunnel reconnect")
			resumeHint.Store(true)
			if c := activeClient.Load(); c != nil {
				_ = c.Close()
			}
		case pbtApmSuspend:
			if c := activeClient.Load(); c != nil {
				_ = c.Close()
			}
		}
		return 1
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wp, lp)
	return ret
})

// startPowerWatcher: одно скрытое окно на время жизни процесса.
func startPowerWatcher() {
	go func() {
		cls, _ := syscall.UTF16PtrFromString("portassh-power")
		hInst, _, _ := procGetModuleHandleW.Call(0)
		wc := wndClassExW{
			CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			LpfnWndProc:   powerWndProc,
			HInstance:     hInst,
			LpszClassName: uintptr(unsafe.Pointer(cls)),
		}
		if atom, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
			log.Printf("power: RegisterClassExW failed")
			return
		}
		hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0,
			0, 0, 0, 0, 0, 0, hInst, 0)
		if hwnd == 0 {
			log.Printf("power: CreateWindowExW: %v", err)
			return
		}
		if trayEnabled.Load() {
			startTray(hwnd)
			procSetTimer.Call(hwnd, trayTimerID, 1000, 0)
		}

		var m msgW
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if uintptr(r) == 0 || uintptr(r) == ^uintptr(0) { // WM_QUIT или ошибка
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
}
