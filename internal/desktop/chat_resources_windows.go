//go:build windows

package desktop

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func clipboardDropFiles(path string) []byte {
	name := append(utf16.Encode([]rune(path)), 0, 0)
	data := make([]byte, 20+len(name)*2)
	binary.LittleEndian.PutUint32(data, 20)     // DROPFILES.pFiles
	binary.LittleEndian.PutUint32(data[16:], 1) // DROPFILES.fWide
	for i, value := range name {
		binary.LittleEndian.PutUint16(data[20+i*2:], value)
	}
	return data
}

// CopyNativeChatFile publishes a real CF_HDROP list so Explorer and our existing
// native paste reader consume the same file. A message-only window owns the
// clipboard; OpenClipboard(NULL) cannot safely be used with EmptyClipboard.
func CopyNativeChatFile(path string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	user := windows.NewLazySystemDLL("user32.dll")
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	class, _ := windows.UTF16PtrFromString("STATIC")
	hwnd, _, _ := user.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if hwnd == 0 {
		return fmt.Errorf("could not create clipboard owner")
	}
	defer user.NewProc("DestroyWindow").Call(hwnd)
	data := clipboardDropFiles(path)
	memory, _, _ := kernel.NewProc("GlobalAlloc").Call(0x42, uintptr(len(data)))
	if memory == 0 {
		return fmt.Errorf("could not allocate clipboard file list")
	}
	owned := true
	defer func() {
		if owned {
			kernel.NewProc("GlobalFree").Call(memory)
		}
	}()
	pointer, _, _ := kernel.NewProc("GlobalLock").Call(memory)
	if pointer == 0 {
		return fmt.Errorf("could not lock clipboard file list")
	}
	// Keep the foreign allocation as a native pointer; converting a stored
	// uintptr back to a Go pointer is invalid under Go's pointer rules.
	kernel.NewProc("RtlMoveMemory").Call(pointer, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	runtime.KeepAlive(data)
	kernel.NewProc("GlobalUnlock").Call(memory)
	opened, _, _ := user.NewProc("OpenClipboard").Call(hwnd)
	if opened == 0 {
		return fmt.Errorf("clipboard is busy; try copying again")
	}
	defer user.NewProc("CloseClipboard").Call()
	cleared, _, _ := user.NewProc("EmptyClipboard").Call()
	if cleared == 0 {
		return fmt.Errorf("could not prepare clipboard")
	}
	result, _, _ := user.NewProc("SetClipboardData").Call(15, memory)
	if result == 0 {
		return fmt.Errorf("could not copy file")
	}
	owned = false // Windows owns the allocation after SetClipboardData succeeds.
	return nil
}

func OpenNativeChatFile(path, action string) error {
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if action == "open_with" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		ole := windows.NewLazySystemDLL("ole32.dll")
		hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
		if int32(hr) < 0 {
			return fmt.Errorf("could not initialize Open with dialog")
		}
		defer ole.NewProc("CoUninitialize").Call()
		info := struct {
			File  *uint16
			Class *uint16
			Flags uint32
		}{File: file, Flags: 4} // OAIF_EXEC
		hr, _, _ = windows.NewLazySystemDLL("shell32.dll").NewProc("SHOpenWithDialog").Call(0, uintptr(unsafe.Pointer(&info)))
		if int32(hr) < 0 {
			return fmt.Errorf("Open with dialog was cancelled or failed")
		}
		return nil
	}
	verb, _ := windows.UTF16PtrFromString("open")
	if action == "reveal" {
		directory, err := windows.GetWindowsDirectory()
		if err != nil {
			return err
		}
		explorer, _ := windows.UTF16PtrFromString(filepath.Join(directory, "explorer.exe"))
		args, _ := windows.UTF16PtrFromString(`/select,"` + path + `"`)
		return windows.ShellExecute(0, verb, explorer, args, nil, windows.SW_SHOWNORMAL)
	}
	// Opening executable/script attachments must go through the explicit OS
	// application chooser, never execute them via the default association.
	if strings.Contains("|.exe|.com|.bat|.cmd|.ps1|.vbs|.js|.jse|.wsf|.msi|.lnk|.url|.scr|.hta|", "|"+strings.ToLower(filepath.Ext(path))+"|") {
		return OpenNativeChatFile(path, "open_with")
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
