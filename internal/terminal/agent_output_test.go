package terminal

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/executionauth"
)

func TestAgentOutputRedactsBeforePaging(t *testing.T) {
	manager, process, bridge, issued := agentOutputFixture(t)
	secret := "sk-" + strings.Repeat("a", 30)
	raw := "visible " + secret + "\r\nafter\r\n"
	process.emit(raw)
	waitForAgentOutputSource(t, manager, issued.Lease.Scope.TerminalSessionID, uint64(len(raw)))

	got, cursor := readAgentOutput(t, bridge, issued, 0, 1)
	if got != "visible [REDACTED:api-key]\nafter\n" {
		t.Fatalf("agent output was not redacted before pagination: %q", got)
	}
	replayed, replayCursor := readAgentOutput(t, bridge, issued, 0, 1)
	if replayed != got || replayCursor != cursor {
		t.Fatalf("replayed page changed: %q cursor=%d, want %q cursor=%d", replayed, replayCursor, got, cursor)
	}
	userPage, err := manager.Read(issued.Lease.Scope.TerminalSessionID, 0, 1024)
	if err != nil || string(userPage.Data) != raw {
		t.Fatalf("user terminal output changed: %q err=%v", userPage.Data, err)
	}
}

func TestAgentOutputRedactsAcrossProcessReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{"token", []string{"visible\x1b[3", "1m\nTOKEN=secret-", "material-12345\nnext\n"}, "visible\nTOKEN=[REDACTED:secret]\nnext\n"},
		{"bearer_newline", []string{"Bearer\n", strings.Repeat("b", 24), "\nnext\n"}, "Bearer\n[REDACTED:bearer-token]\nnext\n"},
		{"private_key", []string{"-----BE", "GIN TEST PRIVATE KEY-----\nbody-", "data\n-----END TEST PRI", "VATE KEY-----\nnext\n"}, "[REDACTED:private-key]\n\nnext\n"},
		{"assigned_private_key", []string{"PRIVATE_KEY=-----BEGIN RSA PRIVATE KEY-----\n", "private-material-12345\n-----END RSA PRIVATE KEY-----\nnext\n"}, "PRIVATE_KEY=[REDACTED:private-key]\n\nnext\n"},
		{"oversized_bearer", []string{"Bearer " + strings.Repeat(" ", 64*1024), "\nsecret-material-12345-abcd\nnext\n"}, "[REDACTED:oversized-line]\nnext\n"},
		{"utf8", []string{string([]byte("你")[:2]), string([]byte("你")[2:]) + "好\n"}, "你好\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, process, bridge, issued := agentOutputFixture(t)
			var sourceCursor, cursor uint64
			var got strings.Builder
			for _, chunk := range tc.chunks {
				process.emit(chunk)
				sourceCursor += uint64(len(chunk))
				waitForAgentOutputSource(t, manager, issued.Lease.Scope.TerminalSessionID, sourceCursor)
				// Liveness probes must not flush or discard an incomplete line.
				if _, err := bridge.Read(t.Context(), AgentReadRequest{
					Token: issued.Token, Scope: issued.Lease.Scope, Cursor: math.MaxUint64, MaxBytes: 1,
				}); err != nil {
					t.Fatal(err)
				}
				var page string
				page, cursor = readAgentOutput(t, bridge, issued, cursor, 64)
				got.WriteString(page)
			}
			if got.String() != tc.want {
				t.Fatalf("chunked output=%q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestAgentOutputFenceExcludesPendingLine(t *testing.T) {
	manager, process, bridge, issued := agentOutputFixture(t)
	before := "old complete line\nold pending line"
	process.emit(before)
	waitForAgentOutputSource(t, manager, issued.Lease.Scope.TerminalSessionID, uint64(len(before)))
	fence, err := bridge.Read(t.Context(), AgentReadRequest{
		Token: issued.Token, Scope: issued.Lease.Scope,
		Cursor: math.MaxUint64, MaxBytes: 1, Fence: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	after := " finished\nnew line\n"
	process.emit(after)
	waitForAgentOutputSource(t, manager, issued.Lease.Scope.TerminalSessionID, uint64(len(before)+len(after)))
	got, _ := readAgentOutput(t, bridge, issued, fence.Page.NextCursor, 2)
	if got != "new line\n" {
		t.Fatalf("pending output crossed fence: %q", got)
	}
}

func TestAgentOutputRetainsRedactionAcrossRingOverflow(t *testing.T) {
	manager, process, bridge, issued := agentOutputFixture(t)
	raw := strings.Repeat("ordinary output\n", MaxTerminalOutputBytes/16+2)
	raw += "TOKEN=secret-material-12345\nnext\n"
	process.emit(raw)
	waitForAgentOutputSource(t, manager, issued.Lease.Scope.TerminalSessionID, uint64(len(raw)))
	page, err := bridge.Read(t.Context(), AgentReadRequest{
		Token: issued.Token, Scope: issued.Lease.Scope, Cursor: 1, MaxBytes: 64,
	})
	if err != nil || !page.Page.Dropped || page.Page.BaseCursor <= 1 {
		t.Fatalf("old cursor not reported as dropped: %+v err=%v", page.Page, err)
	}
	got, _ := readAgentOutput(t, bridge, issued, page.Page.NextCursor, MaxTerminalOutputReadBytes)
	if strings.Contains(got, "secret-material") || !strings.HasSuffix(got, "TOKEN=[REDACTED:secret]\nnext\n") {
		t.Fatal("ring overflow lost sanitized output")
	}
}

func agentOutputFixture(t *testing.T) (*Manager, *terminalProcessStub, *AgentInputBridge, IssuedAgentInput) {
	t.Helper()
	backend := &terminalBackendStub{}
	broker := executionauth.NewTerminalInputBroker()
	manager, err := NewManager(backend, broker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown() })
	session, err := manager.Start(context.Background(), terminalStartTestRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewAgentInputBridge(manager, broker)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := bridge.Issue(context.Background(), IssueAgentInputRequest{
		SessionID: session.ID, RequestedBy: "test_operator",
		OperatorConfirmed: true, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, backend.process, bridge, issued
}

func waitForAgentOutputSource(t *testing.T, manager *Manager, sessionID string, next uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := manager.Get(sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if session.OutputNextCursor >= next {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal output did not reach the expected source cursor")
}

func readAgentOutput(t *testing.T, bridge *AgentInputBridge, issued IssuedAgentInput, cursor uint64, maxBytes int) (string, uint64) {
	t.Helper()
	var result strings.Builder
	for {
		read, err := bridge.Read(context.Background(), AgentReadRequest{
			Token: issued.Token, Scope: issued.Lease.Scope, Cursor: cursor, MaxBytes: maxBytes,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(read.Page.Data) == 0 {
			return result.String(), read.Page.NextCursor
		}
		if read.Page.NextCursor <= cursor || len(read.Page.Data) > maxBytes {
			t.Fatalf("invalid agent page: %+v", read.Page)
		}
		result.Write(read.Page.Data)
		cursor = read.Page.NextCursor
	}
}
