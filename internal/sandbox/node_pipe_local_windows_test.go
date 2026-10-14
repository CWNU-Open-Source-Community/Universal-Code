//go:build windows

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsLocalNamedPipeNamespaceProbe(t *testing.T) {
	base := windowsTestTempDir(t)
	tools, drydock := filepath.Join(base, "tools"), filepath.Join(base, "drydock")
	if err := os.MkdirAll(tools, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(drydock, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "probe.exe"), payload, 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(filepath.Join(base, "owners")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	request := localWindowsTestRequest(t, backend, drydock, tools, "/tools", "/tools/probe.exe", []string{"-test.run=^TestWindowsLocalNamedPipeChildProbe$", "-test.v"})
	request.Manifest.Environment = []EnvironmentBinding{{Name: "TRAVERSE_BOARD_LOCAL_CHILD", Source: EnvironmentLiteral, Value: "node-pipe"}}
	request.Manifest.Resources.MemoryBytes = 512 * 1024 * 1024
	result, err := backend.Run(t.Context(), request)
	t.Logf("exit=%d cleanup=%t/%t err=%v stdout=%s stderr=%s", result.ExitCode, result.TreeReaped, result.ACLsRestored, err, result.Stdout.Data, result.Stderr.Data)
	if err != nil || result.ExitCode != 0 || !result.TreeReaped || !result.ACLsRestored || !strings.Contains(string(result.Stdout.Data), "pipe-probe-complete") {
		t.Fatal("native LPAC named-pipe probe failed")
	}
}

func TestWindowsLocalNamedPipeChildProbe(t *testing.T) {
	if os.Getenv("TRAVERSE_BOARD_LOCAL_CHILD") != "node-pipe" {
		t.Skip("synthetic LPAC child helper")
	}
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	container, err := localTokenInformation(token, localTokenAppContainerSID)
	if err != nil {
		t.Fatal(err)
	}
	sid := *(**windows.SID)(unsafe.Pointer(&container[0]))
	descriptor, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;SY)(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;" + sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	for _, prefix := range []string{`\\?\pipe\uv\`, `\\.\pipe\LOCAL\uv\`} {
		for _, exact := range []bool{false, true} {
			for _, writeDACL := range []bool{false, true} {
				name := fmt.Sprintf("%straverse-probe-%d-%t-%t", prefix, os.Getpid(), exact, writeDACL)
				pointer, err := windows.UTF16PtrFromString(name)
				if err != nil {
					t.Fatal(err)
				}
				access := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED | windows.FILE_FLAG_FIRST_PIPE_INSTANCE)
				if writeDACL {
					access |= windows.WRITE_DAC
				}
				var security *windows.SecurityAttributes
				if exact {
					security = &attributes
				}
				handle, err := windows.CreateNamedPipe(pointer, access, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 65536, 65536, 0, security)
				t.Logf("prefix=%s exact_dacl=%t write_dacl=%t handle_valid=%t err=%v", prefix, exact, writeDACL, handle != windows.InvalidHandle && handle != 0, err)
				if err == nil {
					_ = windows.CloseHandle(handle)
				}
				if strings.Contains(prefix, `\LOCAL\`) {
					if err != nil {
						t.Errorf("own AppContainer pipe namespace rejected: %v", err)
					}
				} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
					t.Errorf("global named-pipe namespace admitted: %v", err)
				}
			}
		}
	}
	runtime.KeepAlive(container)
	runtime.KeepAlive(user)
	runtime.KeepAlive(descriptor)
	t.Log("pipe-probe-complete")
}
