package toolgateway

import (
	"context"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/policy"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failureHookSink struct{ records []hooks.AuditRecord }

func (s *failureHookSink) RecordHookAudit(_ context.Context, r hooks.AuditRecord) error {
	s.records = append(s.records, r)
	return nil
}

func TestGatewayPostToolFailureObservesFailedCallsOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "exists.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	sink := &failureHookSink{}
	engine := hooks.NewEngine(sink)
	var registrations []hooks.Registration
	for _, event := range []hooks.Event{hooks.PostTool, hooks.PostToolFailure} {
		registrations = append(registrations, hooks.Registration{PluginID: "watch", PluginFingerprint: strings.Repeat("b", 64), Declaration: hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion, ID: string(event), Event: event, Action: hooks.ActionRecord, FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100, ToolPattern: "^read_file$"}})
	}
	if err := engine.Replace(registrations); err != nil {
		t.Fatal(err)
	}
	gateway := New(nil, policy.NewDefaultChecker()).WithLifecycleHooks(engine)
	call := ToolCall{Name: ReadFileTool, WorkspaceID: "ws-1", WorkspaceRoot: root, Arguments: map[string]string{"path": "exists.txt"}, RequestedBy: "root"}
	if _, err := gateway.Invoke(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 1 || sink.records[0].Event != hooks.PostTool {
		t.Fatalf("success events=%+v", sink.records)
	}
	call.Arguments["path"] = "missing.txt"
	if _, err := gateway.Invoke(t.Context(), call); err == nil {
		t.Fatal("missing file was successful")
	}
	if len(sink.records) != 3 || sink.records[1].Event != hooks.PostTool || sink.records[2].Event != hooks.PostToolFailure || sink.records[2].WorkspaceID != "ws-1" {
		t.Fatalf("failure events=%+v", sink.records)
	}
}
