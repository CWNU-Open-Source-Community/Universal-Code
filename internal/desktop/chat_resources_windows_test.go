//go:build windows

package desktop

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func TestChatFileClipboardFormatRoundTripsUnicode(t *testing.T) {
	path := `C:\项目\文件 文档.pdf`
	data := clipboardDropFiles(path)
	if binary.LittleEndian.Uint32(data) != 20 || binary.LittleEndian.Uint32(data[16:]) != 1 {
		t.Fatal("invalid DROPFILES header")
	}
	var name []uint16
	for i := 20; i < len(data); i += 2 {
		name = append(name, binary.LittleEndian.Uint16(data[i:]))
	}
	if len(name) < 2 || name[len(name)-1] != 0 || name[len(name)-2] != 0 || string(utf16.Decode(name[:len(name)-2])) != path {
		t.Fatal("clipboard filename was corrupted")
	}
}

// Opt-in only: the caller must preserve/restore its clipboard around this test.
func TestChatFileNativeClipboardRoundTrip(t *testing.T) {
	if os.Getenv("UC_TEST_NATIVE_CLIPBOARD") != "1" {
		t.Skip("requires an explicit Windows clipboard verification session")
	}
	file := filepath.Join(t.TempDir(), "复制验证 文档.md")
	content := []byte("# Native clipboard\r\n中文 file bytes\n")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CopyNativeChatFile(file); err != nil {
		t.Fatal(err)
	}
	files, err := NewNativeClipboardFileReader().ReadFiles(context.Background())
	if err != nil || len(files) != 1 || files[0].Error != nil || files[0].Name != filepath.Base(file) || string(files[0].Data) != string(content) {
		t.Fatalf("clipboard readback: %#v %v", files, err)
	}
}
