//go:build windows

package main

// Системный трей: Shell_NotifyIconW + GDI-иконки, ноль зависимостей.
// Окно — то же скрытое, что и для событий питания (power_windows.go):
// WM_APP_TRAY — колбэк иконки, WM_TIMER — обновление цвета по состоянию.
//
// Состояния (trayState): 0 = онлайн (зелёный), 1 = переподключение (жёлтый),
// 2 = ошибка (красный). -stealth отключает иконку.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	trayCallbackMsg = 0x8001 // WM_APP+1
	menuCopy        = 2001
	menuAbout       = 2002
	menuExit        = 2003
	trayTimerID     = 1

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
)

var (
	gdi32                   = syscall.NewLazyDLL("gdi32.dll")
	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSetTimer            = user32.NewProc("SetTimer")
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procSetClipboardData    = user32.NewProc("SetClipboardData")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateBitmap        = gdi32.NewProc("CreateBitmap")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procCreateSolidBrush    = gdi32.NewProc("CreateSolidBrush")
	procCreatePen           = gdi32.NewProc("CreatePen")
	procEllipse             = gdi32.NewProc("Ellipse")
	procPatBlt              = gdi32.NewProc("PatBlt")
	procCreateIconIndirect  = user32.NewProc("CreateIconIndirect")
	procGlobalAlloc         = kernel32.NewProc("GlobalAlloc")
	procGlobalLock          = kernel32.NewProc("GlobalLock")
	procGlobalUnlock        = kernel32.NewProc("GlobalUnlock")
	procGlobalFree          = kernel32.NewProc("GlobalFree")
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	State            uint32
	StateMask        uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwStateMask      uint32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

var trayHWND uintptr
var trayIcons [3]uintptr // по состояниям
var trayLastState int32 = -1

func trayCall(hwnd uintptr, msg uint32, data *notifyIconData) uintptr {
	ret, _, _ := procShellNotifyIconW.Call(uintptr(msg), uintptr(unsafe.Pointer(data)))
	return ret
}

// drawTrayIcon: цветной круг на прозрачном фоне (GDI, 32x32).
func drawTrayIcon(hwnd uintptr, r, g, b uint8) uintptr {
	hdc, _, _ := procGetDC.Call(hwnd)
	memDC, _, _ := procCreateCompatibleDC.Call(hdc)
	bmp, _, _ := procCreateCompatibleBmp.Call(hdc, 32, 32)
	oldBmp, _, _ := procSelectObject.Call(memDC, bmp)
	color := uint32(b)<<16 | uint32(g)<<8 | uint32(r)

	// фон — чёрный (маска сделает прозрачным)
	procPatBlt.Call(memDC, 0, 0, 32, 32, 0x42 /*BLACKNESS*/)
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	pen, _, _ := procCreatePen.Call(0 /*PS_SOLID*/, 1, uintptr(color))
	oldBrush, _, _ := procSelectObject.Call(memDC, brush)
	oldPen, _, _ := procSelectObject.Call(memDC, pen)
	procEllipse.Call(memDC, 3, 3, 29, 29)
	procSelectObject.Call(memDC, oldBrush)
	procSelectObject.Call(memDC, oldPen)
	procDeleteObject.Call(brush)
	procDeleteObject.Call(pen)

	// маска: всё белое (прозрачно), круг — чёрный (непрозрачно)
	maskDC, _, _ := procCreateCompatibleDC.Call(0)
	maskBmp, _, _ := procCreateBitmap.Call(32, 32, 1, 1, 0)
	oldMask, _, _ := procSelectObject.Call(maskDC, maskBmp)
	procPatBlt.Call(maskDC, 0, 0, 32, 32, 0xFF /*WHITENESS*/)
	mBrush, _, _ := procCreateSolidBrush.Call(0)
	mPen, _, _ := procCreatePen.Call(0, 1, 0)
	mOldBrush, _, _ := procSelectObject.Call(maskDC, mBrush)
	mOldPen, _, _ := procSelectObject.Call(maskDC, mPen)
	procEllipse.Call(maskDC, 3, 3, 29, 29)
	procSelectObject.Call(maskDC, mOldBrush)
	procSelectObject.Call(maskDC, mOldPen)
	procDeleteObject.Call(mBrush)
	procDeleteObject.Call(mPen)

	ii := iconInfo{FIcon: 1, HbmMask: maskBmp, HbmColor: bmp}
	hicon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))

	procSelectObject.Call(maskDC, oldMask)
	procDeleteDC.Call(maskDC)
	procDeleteObject.Call(maskBmp)
	procSelectObject.Call(memDC, oldBmp)
	procDeleteDC.Call(memDC)
	procDeleteObject.Call(bmp)
	procReleaseDC.Call(hwnd, hdc)
	return hicon
}

// trayModify: обновляет иконку и подсказку (вызывать из потока окна).
func trayModify(hwnd uintptr) {
	st := int(trayState.Load())
	if int32(st) == trayLastState {
		return
	}
	ver := buildStamp
	if n := ownVersion(); n != "" {
		ver = n
	}
	status := [3]string{"онлайн", "переподключение...", "ошибка соединения"}[st]
	tip := fmt.Sprintf("portassh ver. %s — %s (%s)", ver, status, trayHost())
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = nifIcon | nifTip
	nid.HIcon = trayIcons[st]
	tip16, _ := syscall.UTF16FromString(tip)
	copy(nid.SzTip[:], tip16)
	trayCall(hwnd, nimModify, &nid)
	trayLastState = int32(st)
}

func trayHost() string {
	h, _ := os.Hostname()
	return h
}

func trayCopyCommand(hwnd uintptr) {
	cmd, _ := trayCmd.Load().(string)
	if cmd == "" {
		cmd = connectCommand(readPersistedRelay(), readPersistedPort())
	}
	// буфер через PowerShell: нативный путь ломался об кодировки/буферы
	tmp := filepath.Join(os.TempDir(), "portassh_clip.txt")
	if err := os.WriteFile(tmp, []byte(cmd), 0o600); err != nil {
		return
	}
	ps := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("Set-Clipboard -Value (Get-Content -Raw -Encoding UTF8 '%s'); Remove-Item '%s'", tmp, tmp))
	ps.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	_ = ps.Start()
}

func readPersistedRelay() string { return defaultRelay }

func trayAbout() {
	ver := ownVersion()
	user, _, _, _, _ := telemetry()
	msg := fmt.Sprintf("portassh — туннель удалённого доступа\r\n\r\nВерсия сборки: %s\r\nМашина: %s\r\nПользователь: %s\r\nRelay: %s",
		ver, trayHost(), user, readPersistedRelay())
	pText, _ := windows.UTF16PtrFromString(msg)
	pCap, _ := windows.UTF16PtrFromString("О portassh")
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(pText)), uintptr(unsafe.Pointer(pCap)), 0x40 /*MB_ICONINFORMATION*/)
}

// trayHandleEvent: вызывается из wndproc power-окна.
func trayHandleEvent(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case trayCallbackMsg:
		switch lp & 0xFFFF {
		case 0x0202, 0x0205, 0x0206: // любой клик — меню (копирование внутри меню)
			var pt [2]int32
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt[0])))
			procSetForegroundWindow.Call(hwnd)
			menu, _, _ := procCreatePopupMenu.Call()
			pCopy, _ := windows.UTF16PtrFromString("📋 Копировать команду подключения")
			pAbout, _ := windows.UTF16PtrFromString("ℹ️ О программе")
			pExit, _ := windows.UTF16PtrFromString("✖ Отключить (выход)")
			procAppendMenuW.Call(menu, 0, menuCopy, uintptr(unsafe.Pointer(pCopy)))
			procAppendMenuW.Call(menu, 0, menuAbout, uintptr(unsafe.Pointer(pAbout)))
			procAppendMenuW.Call(menu, 0x800 /*MF_SEPARATOR*/, 0, 0)
			procAppendMenuW.Call(menu, 0, menuExit, uintptr(unsafe.Pointer(pExit)))
			sel, _, _ := procTrackPopupMenu.Call(menu, 0x0180 /*TPM_RETURNCMD|TPM_RIGHTBUTTON*/, uintptr(pt[0]), uintptr(pt[1]), 0, hwnd, 0)
			procPostMessageW.Call(hwnd, 0, 0, 0) // WM_NULL — закрыть меню
			switch sel {
			case menuCopy:
				trayCopyCommand(hwnd)
			case menuAbout:
				trayAbout()
			case menuExit:
				log.Print("tray: выход по запросу пользователя")
				// убрать иконку перед выходом
				var nid notifyIconData
				nid.CbSize = uint32(unsafe.Sizeof(nid))
				nid.HWnd = hwnd
				nid.UID = 1
				trayCall(hwnd, nimDelete, &nid)
				os.Exit(0)
			}
		}
	case 0x0113: // WM_TIMER — обновить цвет/подсказку
		trayModify(hwnd)
	}
	return 0
}

var procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
var procPostMessageW = user32.NewProc("PostMessageW")

// startTray добавляет иконку в трей (из потока power-окна).
func startTray(hwnd uintptr) {
	trayIcons[0] = drawTrayIcon(hwnd, 52, 211, 153) // зелёный
	trayIcons[1] = drawTrayIcon(hwnd, 251, 191, 36) // жёлтый
	trayIcons[2] = drawTrayIcon(hwnd, 239, 68, 68)  // красный

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = trayCallbackMsg
	nid.HIcon = trayIcons[0]
	tip, _ := syscall.UTF16FromString("portassh — запуск...")
	copy(nid.SzTip[:], tip)
	trayCall(hwnd, nimAdd, &nid)

	procSetTimer.Call(hwnd, trayTimerID, 1000, 0)
	log.Print("tray: иконка добавлена")
}
