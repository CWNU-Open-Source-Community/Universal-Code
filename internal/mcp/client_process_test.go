package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// A descendant keeps its own TCP connection open while retaining both output
// pipes. EOF on that connection proves it has exited, including on platforms
// where an orphan can remain visible temporarily as a zombie.
func TestMCPStdioProcessTreeHelper(t *testing.T) {
	index := slices.Index(os.Args, "mcp-process-tree-helper")
	if index < 0 {
		t.Skip("helper subprocess only")
	}
	mode, address, ready := os.Args[index+1], os.Args[index+2], os.Args[index+3]
	if mode == "child" {
		connection, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			os.Exit(2)
		}
		defer connection.Close()
		_, _ = fmt.Fprintln(connection, "ready")
		if os.WriteFile(ready, []byte("ready"), 0o600) != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, connection)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMCPStdioProcessTreeHelper$", "--",
		"mcp-process-tree-helper", "child", address, ready)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(2)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Exceed stderr retention to verify draining never blocks normal startup.
	_, _ = fmt.Fprint(os.Stderr, strings.Repeat("diagnostic ", 8192))
	if mode == "exit" {
		message, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "final",
			"params": map[string]string{"text": strings.Repeat("x", 192*1024)}})
		_, _ = fmt.Fprintln(os.Stdout, string(message))
		os.Exit(0)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if mode == "close" {
		os.Exit(0)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestStdioClientTransportClosesDescendants(t *testing.T) {
	for _, mode := range []string{"close", "stubborn", "exit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestMCPStdioProcessTreeHelper$", "--",
				"mcp-process-tree-helper", mode, listener.Addr().String(), filepath.Join(t.TempDir(), "child-ready"))
			transport := &commandClientTransport{cmd: cmd}
			var closeClient func() error
			var readMessage func(context.Context) (jsonrpc.Message, error)
			var cancel context.CancelFunc
			var connectDone chan error
			if mode == "cancel" {
				client := newSDKClient(transport)
				closeClient = client.Close
				ctx, stop := context.WithCancel(t.Context())
				cancel = stop
				defer stop()
				connectDone = make(chan error, 1)
				go func() {
					_, err := client.Discover(ctx, time.Now())
					connectDone <- err
				}()
			} else {
				connection, err := transport.Connect(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				closeClient, readMessage = connection.Close, connection.Read
			}
			defer closeClient()
			descendant, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer descendant.Close()
			if err := descendant.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(descendant)
			if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("descendant readiness = %q, %v", line, err)
			}
			switch mode {
			case "exit":
				// The root exits while the child retains stdout. Its entire final
				// message must survive the concurrent wait and tree cleanup.
				ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
				defer stop()
				message, err := readMessage(ctx)
				if err != nil {
					t.Fatal(err)
				}
				request, ok := message.(*jsonrpc.Request)
				var params struct{ Text string }
				if !ok || request.Method != "final" || json.Unmarshal(request.Params, &params) != nil || params.Text != strings.Repeat("x", 192*1024) {
					t.Fatal("root exit lost the final stdio response")
				}
				if _, err := readMessage(ctx); !errors.Is(err, io.EOF) {
					t.Fatalf("read after root exit = %v, want EOF", err)
				}
			case "cancel":
				cancel()
				select {
				case err := <-connectDone:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancelled initialize = %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled initialize did not return")
				}
			default:
				if err := closeClient(); err != nil && !errors.Is(err, os.ErrClosed) {
					t.Fatal(err)
				}
			}
			if err := descendant.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			reset := syscall.ECONNRESET
			if runtime.GOOS == "windows" {
				// WSAECONNRESET: Windows can reset a killed process's socket.
				reset = syscall.Errno(10054)
			}
			if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) && !errors.Is(err, reset) {
				t.Fatalf("descendant survived %s: %v", mode, err)
			}
		})
	}
}

func TestStdioClientTransportStartFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing executable", "rejected launch", "cancelled launch"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMCPStdioProcessTreeHelper$")
			if mode == "missing executable" {
				cmd = exec.Command(filepath.Join(t.TempDir(), "missing-executable"))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			transport := &commandClientTransport{cmd: cmd}
			var wantErr error
			if mode == "rejected launch" {
				wantErr = errors.New("launch rejected")
				transport.beforeStart = func(context.Context) error { return wantErr }
			} else if mode == "cancelled launch" {
				wantErr = context.Canceled
				transport.beforeStart = func(context.Context) error { cancel(); return nil }
			}
			if _, err := transport.Connect(ctx); err == nil || wantErr != nil && !errors.Is(err, wantErr) {
				t.Fatalf("failed launch = %v, want %v", err, wantErr)
			}
			if cmd.Process != nil {
				t.Fatal("failed launch created a process")
			}
			if _, err := cmd.Stdin.Read(make([]byte, 1)); err == nil {
				t.Fatal("failed launch retained its input pipe")
			}
			for _, output := range []io.Writer{cmd.Stdout, cmd.Stderr} {
				if _, err := output.Write([]byte("closed")); err == nil {
					t.Fatal("failed launch retained an output pipe")
				}
			}
		})
	}
}
