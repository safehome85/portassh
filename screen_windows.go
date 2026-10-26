//go:build windows

package main

// Экран в браузере: GDI-захват → JPEG → кадры на hbd (127.0.0.1:8897),
// инъекция мыши/клавиатуры через SendInput. Ноль внешних утилит.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	gossh "golang.org/x/crypto/ssh"
)

var (
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procStretchBlt       = gdi32.NewProc("StretchBlt")
	procGetDIBits        = gdi32.NewProc("GetDIBits")
	procSendInput        = user32.NewProc("SendInput")
)

type bmiHeader struct {
	BiSize         uint32
	BiWidth        int32
	BiHeight       int32
	BiPlanes       uint16
	BiBitCount     uint16
	BiCompression  uint32
	BiSizeImage    uint32
	XPelsPerMeter  int32
	YPelsPerMeter  int32
	BiClrUsed      uint32
	BiClrImportant uint32
}

type bitmapinfo struct {
	Header bmiHeader
	Colors [1]uint32
}

// Размеры union INPUT зависят от разрядности — структуры ниже дают
// корректную раскладку на 386 и amd64 (ExtraInfo — якорь выравнивания).
type mouseSend struct {
	Type uint32
	Body struct {
		Dx, Dy    int32
		MouseData uint32
		Flags     uint32
		Time      uint32
		ExtraInfo uintptr
	}
}

type keybdSend struct {
	Type uint32
	Body struct {
		WVk       uint16
		Scan      uint16
		Flags     uint32
		Time      uint32
		ExtraInfo uintptr
		_         [8]byte // добор до размера union (как у MOUSEINPUT)
	}
}

const (
	inputKeyboard = 1
	mouseMove     = 0x0001
	mouseLeftDown = 0x0002
	mouseLeftUp   = 0x0004
	mouseRightDn  = 0x0008
	mouseRightUp  = 0x0010
	mouseAbsolute = 0x8000
	keyEvUnicode  = 0x0004
	keyEvKeyUp    = 0x0002
)

func screenDims() (int, int) {
	w, _, _ := procGetSystemMetrics.Call(0)
	h, _, _ := procGetSystemMetrics.Call(1)
	return int(w), int(h)
}

// captureScreenJPEG: снимок экрана, масштабирование до maxW, JPEG.
func captureScreenJPEG(maxW, quality int) ([]byte, int, int, error) {
	sw, sh := screenDims()
	if sw == 0 || sh == 0 {
		return nil, 0, 0, fmt.Errorf("экран недоступен")
	}
	w, h := sw, sh
	if maxW > 0 && w > maxW {
		w = maxW
		h = sh * maxW / sw
	}
	hdc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, hdc)
	memDC, _, _ := procCreateCompatibleDC.Call(hdc)
	defer procDeleteDC.Call(memDC)
	bmp, _, _ := procCreateCompatibleBmp.Call(hdc, uintptr(w), uintptr(h))
	oldBmp, _, _ := procSelectObject.Call(memDC, bmp)
	procStretchBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), hdc, 0, 0, uintptr(sw), uintptr(sh), 0x00CC0020 /*SRCCOPY*/)

	bmi := bitmapinfo{Header: bmiHeader{BiSize: 40, BiWidth: int32(w), BiHeight: int32(-h), BiPlanes: 1, BiBitCount: 32}}
	buf := make([]byte, w*h*4)
	procGetDIBits.Call(memDC, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bmi)), 0)
	procSelectObject.Call(memDC, oldBmp)
	procDeleteObject.Call(bmp)

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(buf); i += 4 {
		img.Pix[i] = buf[i+2]   // R (BGRA → RGBA)
		img.Pix[i+1] = buf[i+1] // G
		img.Pix[i+2] = buf[i]   // B
		img.Pix[i+3] = 255      // A
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), w, h, nil
}

// sendMouseAbs: событие мыши в координатах виртуального экрана (0..65535).
func sendMouseAbs(flags uint32, absX, absY int) {
	in := mouseSend{Type: 0}
	in.Body.Dx = int32(absX)
	in.Body.Dy = int32(absY)
	in.Body.Flags = mouseAbsolute | mouseMove | flags
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

// screenClick: клик по координатам кадра (масштабируется в экран).
func screenClick(x, y int, dbl bool) {
	imgW, imgH := lastCaptureDims()
	if imgW <= 0 || imgH <= 0 {
		return
	}
	ax := x * 65536 / imgW
	ay := y * 65536 / imgH
	sendMouseAbs(mouseLeftDown, ax, ay)
	sendMouseAbs(mouseLeftUp, ax, ay)
	if dbl {
		time.Sleep(60 * time.Millisecond)
		sendMouseAbs(mouseLeftDown, ax, ay)
		sendMouseAbs(mouseLeftUp, ax, ay)
	}
}

func screenRightClick(x, y int) {
	imgW, imgH := lastCaptureDims()
	if imgW <= 0 || imgH <= 0 {
		return
	}
	ax := x * 65536 / imgW
	ay := y * 65536 / imgH
	sendMouseAbs(mouseRightDn, ax, ay)
	sendMouseAbs(mouseRightUp, ax, ay)
}

// screenMouseAt: событие кнопки/движения в координатах кадра.
func screenMouseAt(flags uint32, x, y int) {
	imgW, imgH := lastCaptureDims()
	if imgW <= 0 || imgH <= 0 {
		return
	}
	ax := x * 65536 / imgW
	ay := y * 65536 / imgH
	if ax < 0 {
		ax = 0
	}
	if ax > 65535 {
		ax = 65535
	}
	if ay < 0 {
		ay = 0
	}
	if ay > 65535 {
		ay = 65535
	}
	sendMouseAbs(flags, ax, ay)
}

// sendWheel: прокрутка колеса (120 = один щелчок, минус = вниз).
func sendWheel(delta int) {
	in := mouseSend{Type: 0}
	in.Body.MouseData = uint32(int32(delta))
	in.Body.Flags = 0x0800 // MOUSEEVENTF_WHEEL
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

// showToast: полупрозрачная плашка с сообщением пользователю (Forms,
// автоскрытие 15 с). Текст передаётся файлом — без кавычечного ада.
func showToast(text string) {
	dir := exeDir()
	stamp := time.Now().UnixNano()
	msgFile := filepath.Join(dir, fmt.Sprintf("portassh_msg_%d.txt", stamp))
	ps1 := filepath.Join(dir, fmt.Sprintf("portassh_toast_%d.ps1", stamp))
	if err := os.WriteFile(msgFile, []byte(text), 0o600); err != nil {
		return
	}
	script := "Add-Type -AssemblyName System.Windows.Forms\r\n" +
		"Add-Type -AssemblyName System.Drawing\r\n" +
		"$f = New-Object Windows.Forms.Form\r\n" +
		"$f.FormBorderStyle = 'None'; $f.TopMost = $true; $f.Opacity = 0.92\r\n" +
		"$f.StartPosition = 'Manual'; $f.Size = New-Object Drawing.Size(560, 96)\r\n" +
		"$vs = [Windows.Forms.SystemInformation]::VirtualScreen\r\n" +
		"$f.Location = New-Object Drawing.Point(($vs.Width - 580), 40)\r\n" +
		"$f.BackColor = [Drawing.Color]::FromArgb(15, 23, 42)\r\n" +
		"$l = New-Object Windows.Forms.Label\r\n" +
		"$t = [IO.File]::ReadAllText('MSGPATH', [Text.Encoding]::UTF8)\r\n" +
		"$l.Text = $t; $l.ForeColor = 'White'; $l.Dock = 'Fill'\r\n" +
		"$l.TextAlign = 'MiddleCenter'; $l.Font = New-Object Drawing.Font('Segoe UI', 12)\r\n" +
		"$f.Controls.Add($l)\r\n" +
		"$timer = New-Object Windows.Forms.Timer; $timer.Interval = 15000\r\n" +
		"$timer.Add_Tick({ $f.Close() }); $timer.Start()\r\n" +
		"[void]$f.ShowDialog()\r\n" +
		"Remove-Item 'MSGPATH','PS1PATH'\r\n"
	script = strings.ReplaceAll(script, "MSGPATH", msgFile)
	script = strings.ReplaceAll(script, "PS1PATH", ps1)
	os.WriteFile(ps1, []byte(script), 0o600)
	ps := exec.Command("powershell", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", ps1)
	ps.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	_ = ps.Start()
}

// typeText: печать текста юникод-событиями (кириллица работает).
func typeText(text string) {
	runes := []rune(text)
	if len(runes) > 300 {
		runes = runes[:300]
	}
	for _, r := range runes {
		var down, up keybdSend
		down.Type, up.Type = inputKeyboard, inputKeyboard
		down.Body.WVk = uint16(r)
		down.Body.Flags = keyEvUnicode
		up.Body.WVk = uint16(r)
		up.Body.Flags = keyEvUnicode | keyEvKeyUp
		procSendInput.Call(1, uintptr(unsafe.Pointer(&down)), unsafe.Sizeof(down))
		procSendInput.Call(1, uintptr(unsafe.Pointer(&up)), unsafe.Sizeof(up))
	}
}

// lastCapture: размеры последнего кадра (для масштабирования координат).
var (
	capMu      sync.Mutex
	capW, capH int
)

func storeCaptureDims(w, h int) {
	capMu.Lock()
	capW, capH = w, h
	capMu.Unlock()
}

func lastCaptureDims() (int, int) {
	capMu.Lock()
	defer capMu.Unlock()
	return capW, capH
}

// screenInputExec: выполняет строку ввода из очереди hbd.
// Формат: click X Y | dbl X Y | rclick X Y | key <base64-текст>
func execScreenInput(line string) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	switch f[0] {
	case "press":
		if len(f) >= 3 {
			x, e1 := strconv.Atoi(f[1])
			y, e2 := strconv.Atoi(f[2])
			if e1 == nil && e2 == nil {
				screenMouseAt(mouseLeftDown, x, y)
			}
		}
	case "move":
		if len(f) >= 3 {
			x, e1 := strconv.Atoi(f[1])
			y, e2 := strconv.Atoi(f[2])
			if e1 == nil && e2 == nil {
				screenMouseAt(0, x, y) // движение с зажатой кнопкой
			}
		}
	case "clip":
		if len(f) >= 2 {
			if b, berr := base64.StdEncoding.DecodeString(f[1]); berr == nil && len(b) > 0 {
				_ = setClipboardData(b)
			}
		}
	case "msg":
		if len(f) >= 2 {
			if b, berr := base64.StdEncoding.DecodeString(f[1]); berr == nil && len(b) > 0 {
				showToast(string(b))
			}
		}
	case "wheel":
		if len(f) >= 2 {
			if d, err := strconv.Atoi(f[1]); err == nil {
				sendWheel(d)
			}
		}
	case "release":
		if len(f) >= 3 {
			x, e1 := strconv.Atoi(f[1])
			y, e2 := strconv.Atoi(f[2])
			if e1 == nil && e2 == nil {
				screenMouseAt(mouseLeftUp, x, y)
			}
		}
	case "click", "dbl":
		if len(f) >= 3 {
			x, e1 := strconv.Atoi(f[1])
			y, e2 := strconv.Atoi(f[2])
			if e1 == nil && e2 == nil {
				screenClick(x, y, f[0] == "dbl")
			}
		}
	case "rclick":
		if len(f) >= 3 {
			x, e1 := strconv.Atoi(f[1])
			y, e2 := strconv.Atoi(f[2])
			if e1 == nil && e2 == nil {
				screenRightClick(x, y)
			}
		}
	case "key":
		if len(f) >= 2 {
			if b, err := base64.StdEncoding.DecodeString(f[1]); err == nil {
				typeText(string(b))
			}
		}
	}
}

// screenPushLoop: пока жив туннель — кадры на hbd + выполнение ввода.
// Если просмотрщика нет (hbd отвечает no), кадр не снимается — экономия LTE.
func screenPushLoop(client *gossh.Client, cfg config, port int, stop chan struct{}) {
	hc := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				return client.Dial("tcp", "127.0.0.1:8897")
			},
		},
	}
	t := time.NewTicker(time.Duration(cfg.screenMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			// есть ли зритель?
			resp, err := hc.Get(fmt.Sprintf("http://127.0.0.1:8897/viewer?port=%d", port))
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(body), "yes") {
				continue
			}
			jpg, w, h, err := captureScreenJPEG(1280, 55)
			if err != nil {
				continue
			}
			storeCaptureDims(w, h)
			// POST кадра
			pr, err := hc.Post(fmt.Sprintf("http://127.0.0.1:8897/screen?port=%d&w=%d&h=%d", port, w, h),
				"image/jpeg", bytes.NewReader(jpg))
			if err == nil {
				io.Copy(io.Discard, pr.Body)
				pr.Body.Close()
			}
			// очередь ввода
			if ir, err := hc.Get(fmt.Sprintf("http://127.0.0.1:8897/input?port=%d", port)); err == nil {
				ib, _ := io.ReadAll(ir.Body)
				ir.Body.Close()
				for _, line := range strings.Split(string(ib), "\n") {
					if strings.TrimSpace(line) != "" {
						execScreenInput(strings.TrimSpace(line))
					}
				}
			}
		}
	}
}
