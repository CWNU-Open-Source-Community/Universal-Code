//go:build windows

package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCommandServiceListenerHelper(t *testing.T) {
	address := os.Getenv("COMMAND_SERVICE_HELPER")
	if address == "" {
		return
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("http://" + listener.Addr().String() + "/")
	_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ready") }))
}

func TestCommandServiceReadinessWindowsOwnedListener(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
				Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess, Executable: executable,
				Arguments: []string{"-test.run=^TestCommandServiceListenerHelper$"}, WorkingDirectory: ".",
				Environment: []CommandRuntimeEnvironment{{Name: "COMMAND_SERVICE_HELPER", Value: address}},
				StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 10000,
				Output:  CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes, ArtifactBytes: MinCommandRuntimeInlineBytes},
				Network: CommandRuntimeNetworkHost, Credentials: CommandRuntimeCredentialsNone, Purpose: "Verify owned service readiness",
			}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			process, err := newPlatformCommandRuntimeStarter().Start(t.Context(), CommandRuntimeScope{}, resolved)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Kill(); _, _ = process.Wait(); _ = process.Close() })
			go io.Copy(io.Discard, process.Stderr())
			reader := bufio.NewReader(process.Stdout())
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			go io.Copy(io.Discard, reader)
			url := strings.TrimSpace(line)
			manager, id := readinessTestManager(url, process)
			result, err := manager.CheckOwnedCommandService(t.Context(), id, url, func(context.Context) error { return nil })
			if err != nil || !result.Verified {
				t.Fatalf("owned native listener: %+v %v", result, err)
			}
			foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("contacted unrelated service") }))
			defer foreign.Close()
			manager.entries[id.ID].record.Stdout += "\n" + foreign.URL
			result, err = manager.CheckOwnedCommandService(t.Context(), id, foreign.URL, func(context.Context) error { return nil })
			if err != nil || result.Verified || result.Reason != "listener_not_owned" {
				t.Fatalf("foreign listener: %+v %v", result, err)
			}
		})
	}
}
