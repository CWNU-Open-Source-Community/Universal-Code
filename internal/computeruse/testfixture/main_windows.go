//go:build windows

// This standalone harmless fixture is for opt-in live computer-use checks.
// Build it separately so the executor can distinguish it from its own host.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var user = windows.NewLazySystemDLL("user32.dll")
var register = user.NewProc("RegisterClassExW")
var create = user.NewProc("CreateWindowExW")
var def = user.NewProc("DefWindowProcW")
var show = user.NewProc("ShowWindow")
var getMessage = user.NewProc("GetMessageW")
var translate = user.NewProc("TranslateMessage")
var dispatch = user.NewProc("DispatchMessageW")
var postQuit = user.NewProc("PostQuitMessage")
var send = user.NewProc("SendMessageW")
var setText = user.NewProc("SetWindowTextW")
var getText = user.NewProc("GetWindowTextW")
var getRect = user.NewProc("GetWindowRect")
var setTimer = user.NewProc("SetTimer")
var setDPI = user.NewProc("SetProcessDpiAwarenessContext")

type point struct{ X, Y int32 }
type message struct {
	HWND    uintptr
	ID      uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   point
	Private uint32
}
type windowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type rect struct{ Left, Top, Right, Bottom int32 }

var edit, status, button uintptr
var clicks int
var statePath string
var previous []byte
var visualChallenge bool
var challengeCode, finishMarker string
var targetClicks int

func wide(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}
func save(hwnd uintptr) {
	if statePath == "" {
		return
	}
	buf := make([]uint16, 32768)
	getText.Call(edit, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	var windowRect, editRect, buttonRect rect
	getRect.Call(hwnd, uintptr(unsafe.Pointer(&windowRect)))
	getRect.Call(edit, uintptr(unsafe.Pointer(&editRect)))
	getRect.Call(button, uintptr(unsafe.Pointer(&buttonRect)))
	line, _, _ := send.Call(edit, 0xce, 0, 0) // EM_GETFIRSTVISIBLELINE
	v := struct {
		PID              int    `json:"pid"`
		HWND             string `json:"hwnd"`
		Text             string `json:"text"`
		Clicks           int    `json:"clicks"`
		FirstVisibleLine int    `json:"first_visible_line"`
		WindowRect       rect   `json:"window_rect"`
		EditRect         rect   `json:"edit_rect"`
		ButtonRect       rect   `json:"button_rect"`
		ChallengeCode    string `json:"challenge_code,omitempty"`
		FinishMarker     string `json:"finish_marker,omitempty"`
		TargetClicks     int    `json:"target_clicks,omitempty"`
	}{os.Getpid(), fmt.Sprintf("0x%x", hwnd), windows.UTF16ToString(buf), clicks, int(line), windowRect, editRect, buttonRect, challengeCode, finishMarker, targetClicks}
	data, _ := json.MarshalIndent(v, "", "  ")
	if !bytes.Equal(data, previous) {
		if os.WriteFile(statePath, data, 0600) == nil {
			previous = data
		}
	}
}
func main() {
	flag.StringVar(&statePath, "state", "", "Optional JSON state output path for the test runner")
	flag.BoolVar(&visualChallenge, "visual-challenge", false, "Display randomized tasks for a real vision model; state output is only for independent verification")
	flag.Parse()
	buttonX, statusX := 24, 184
	header := "Harmless test fixture. Edit text, click the button, and scroll the text area."
	if visualChallenge {
		var random [7]byte
		if _, err := rand.Read(random[:]); err != nil {
			panic(err)
		}
		challengeCode = strings.ToUpper(hex.EncodeToString(random[:3]))
		finishMarker = strings.ToUpper(hex.EncodeToString(random[3:6]))
		targetClicks = 2 + int(random[6]%3)
		header = fmt.Sprintf("Visual task code: %s     Click target: %d", challengeCode, targetClicks)
		if random[6]&4 != 0 {
			buttonX, statusX = 454, 24
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	setDPI.Call(^uintptr(3))
	instance, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	if instance == 0 {
		panic(err)
	}
	proc := windows.NewCallback(func(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
		switch msg {
		case 0x111:
			if uint16(wparam) == 2 && uint16(wparam>>16) == 0 {
				clicks++
				label := wide(fmt.Sprintf("Clicks: %d", clicks))
				setText.Call(status, uintptr(unsafe.Pointer(label)))
			}
			if edit != 0 {
				save(hwnd)
			}
		case 0x113:
			save(hwnd)
		case 2:
			postQuit.Call(0)
			return 0
		}
		r, _, _ := def.Call(hwnd, uintptr(msg), wparam, lparam)
		return r
	})
	class := wide("UniversalCodeComputerUseFixtureV1")
	wc := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Proc: proc, Instance: uintptr(instance), Background: 6, Name: class}
	if ok, _, err := register.Call(uintptr(unsafe.Pointer(&wc))); ok == 0 {
		panic(err)
	}
	title := wide("Universal Code Computer Use Fixture")
	hwnd, _, err := create.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0x00cf0000, 180, 140, 640, 460, 0, 0, uintptr(instance), 0)
	if hwnd == 0 {
		panic(err)
	}
	child := func(class, text string, style uintptr, x, y, w, h, id int) uintptr {
		c, t := wide(class), wide(text)
		handle, _, _ := create.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)), 0x50000000|style, uintptr(x), uintptr(y), uintptr(w), uintptr(h), hwnd, uintptr(id), uintptr(instance), 0)
		return handle
	}
	child("STATIC", header, 0, 24, 16, 580, 30, 0)
	lines := []string{}
	for i := 1; i <= 80; i++ {
		lines = append(lines, fmt.Sprintf("Fixture line %02d", i))
	}
	if visualChallenge {
		lines[len(lines)-1] += " | Finish marker: " + finishMarker
	}
	edit = child("EDIT", strings.Join(lines, "\r\n"), 0x00200000|0x00000004|0x00000040|0x00800000, 24, 52, 570, 260, 1)
	button = child("BUTTON", "Count click", 0, buttonX, 332, 140, 36, 2)
	status = child("STATIC", "Clicks: 0", 0, statusX, 340, 300, 30, 3)
	setTimer.Call(hwnd, 1, 200, 0)
	show.Call(hwnd, 5)
	save(hwnd)
	var m message
	for {
		r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			break
		}
		translate.Call(uintptr(unsafe.Pointer(&m)))
		dispatch.Call(uintptr(unsafe.Pointer(&m)))
	}
}
