package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
	terminalruntime "cyberagent-workbench/internal/terminal"
	"cyberagent-workbench/internal/toolcontract"
)

type debugTerminalBackendStub struct {
	process *debugTerminalProcessStub
}

func (b *debugTerminalBackendStub) Name() string {
	return "debug-terminal-agent-test"
}

func (b *debugTerminalBackendStub) Available() bool {
	return true
}

func (b *debugTerminalBackendStub) Start(
	context.Context,
	terminalruntime.BackendStartRequest,
) (terminalruntime.Process, error) {
	reader, writer := io.Pipe()
	b.process = &debugTerminalProcessStub{
		reader: reader, writer: writer, wait: make(chan struct{}),
	}
	return b.process, nil
}

type debugTerminalProcessStub struct {
	mu        sync.Mutex
	reader    *io.PipeReader
	writer    *io.PipeWriter
	input     bytes.Buffer
	writes    int
	failWrite bool
	wait      chan struct{}
	closeOnce sync.Once
}

func (p *debugTerminalProcessStub) Read(data []byte) (int, error) {
	return p.reader.Read(data)
}

func (p *debugTerminalProcessStub) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes++
	if p.failWrite {
		count := len(data) / 2
		_, _ = p.input.Write(data[:count])
		return count, errors.New("simulated partial terminal write")
	}
	return p.input.Write(data)
}

func (p *debugTerminalProcessStub) Resize(int, int) error {
	return nil
}

func (p *debugTerminalProcessStub) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.wait:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (p *debugTerminalProcessStub) Close() error {
	p.closeOnce.Do(func() {
		_ = p.writer.Close()
		close(p.wait)
	})
	return nil
}

func (p *debugTerminalProcessStub) Boundary() terminalruntime.ProcessBoundary {
	return terminalruntime.ProcessBoundary{
		UserOwned: true, JobAssignedAtCreation: true,
		KillOnJobClose: true, Persistent: true,
	}
}

func (p *debugTerminalProcessStub) snapshot() (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.input.String(), p.writes
}

func (p *debugTerminalProcessStub) emit(data string) error {
	_, err := p.writer.Write([]byte(data))
	return err
}

type debugTerminalAgentFixture struct {
	state      *store.SQLiteStore
	service    *DebugTerminalAgentInputService
	manager    *terminalruntime.Manager
	process    *debugTerminalProcessStub
	run        domain.Run
	permission *RunExecutionPermissionService
	sessionID  string
}

func TestDebugTerminalAgentInputIsShortLivedPolicyCheckedAndExactlyOnce(
	t *testing.T,
) {
	fixture := newDebugTerminalAgentFixture(t, false)
	ctx := context.Background()
	if _, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "model", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("model grant error=%v code=%s", err, apperror.CodeOf(err))
	}
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	if binding.ID == "" || binding.TokenExposed || binding.TokenPersisted ||
		binding.RawInputPersisted || binding.AgentInputDefault ||
		binding.AutomaticRetryAllowed ||
		binding.PermissionMode != domain.RunExecutionPermissionFull {
		t.Fatalf("unsafe public binding: %#v", binding)
	}
	request := WriteDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       binding.ID,
		OperationKey:    "debug-terminal-operation-0001",
		Data:            []byte("go version\r"),
	}
	result, err := fixture.service.Write(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.BytesWritten != len(request.Data) || result.Replayed ||
		result.TokenExposed || result.RawInputPersisted ||
		result.AutomaticRetryAllowed {
		t.Fatalf("unexpected write result: %#v", result)
	}
	replayed, err := fixture.service.Write(ctx, request)
	if err != nil || !replayed.Replayed ||
		replayed.OperationDigest != result.OperationDigest ||
		replayed.OutputCursor != result.OutputCursor {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	input, writes := fixture.process.snapshot()
	if input != "go version\r" || writes != 1 {
		t.Fatalf("input=%q writes=%d", input, writes)
	}
	conflict := request
	conflict.Data = []byte("go env\r")
	if _, err := fixture.service.Write(ctx, conflict); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("operation conflict error=%v code=%s",
			err, apperror.CodeOf(err))
	}
	multiline := request
	multiline.OperationKey = "debug-terminal-operation-0002"
	multiline.Data = []byte("go version\rwhoami\r")
	if _, err := fixture.service.Write(ctx, multiline); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("multiline error=%v code=%s", err, apperror.CodeOf(err))
	}
	dangerous := request
	dangerous.OperationKey = "debug-terminal-operation-0003"
	dangerous.Data = []byte("mass" + "can 0.0.0.0/0\r")
	if _, err := fixture.service.Write(ctx, dangerous); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("dangerous input error=%v code=%s",
			err, apperror.CodeOf(err))
	}
	if _, err := fixture.permission.Change(ctx,
		ChangeRunExecutionPermissionRequest{
			RunID: fixture.run.ID, Mode: "ask",
			OperationKey: "debug-terminal-permission-reset-0001",
			RequestedBy:  "test_operator", Reason: "leave debug access",
		}); err != nil {
		t.Fatal(err)
	}
	stale := request
	stale.OperationKey = "debug-terminal-operation-0004"
	stale.Data = []byte("go test\r")
	if _, err := fixture.service.Write(ctx, stale); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("stale input error=%v code=%s", err, apperror.CodeOf(err))
	}
	after, afterWrites := fixture.process.snapshot()
	if after != input || afterWrites != writes {
		t.Fatalf("stale binding wrote input=%q writes=%d", after, afterWrites)
	}
	audit, err := fixture.state.ListRunEvents(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range audit {
		counts[event.Type]++
		if event.Type != events.DebugTerminalAgentInputGrantedEvent &&
			event.Type != events.DebugTerminalAgentInputPreparedEvent &&
			event.Type != events.DebugTerminalAgentInputCompletedEvent &&
			event.Type != events.DebugTerminalAgentInputRevokedEvent {
			continue
		}
		if strings.Contains(event.PayloadJSON, "go version") ||
			strings.Contains(event.PayloadJSON, "masscan") ||
			strings.Contains(strings.ToLower(event.PayloadJSON), "token") &&
				!strings.Contains(event.PayloadJSON, `"token_exposed":false`) &&
				!strings.Contains(event.PayloadJSON, `"token_persisted":false`) {
			t.Fatalf("raw input or bearer leaked into audit: %s", event.PayloadJSON)
		}
	}
	for eventType, want := range map[string]int{
		events.DebugTerminalAgentInputGrantedEvent:   1,
		events.DebugTerminalAgentInputPreparedEvent:  1,
		events.DebugTerminalAgentInputCompletedEvent: 1,
		events.DebugTerminalAgentInputRevokedEvent:   1,
	} {
		if counts[eventType] != want {
			t.Fatalf("event %s count=%d want=%d", eventType, counts[eventType], want)
		}
	}
}

func TestDebugTerminalAgentInputDisablesRetryAfterAmbiguousWrite(t *testing.T) {
	fixture := newDebugTerminalAgentFixture(t, true)
	ctx := context.Background()
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	request := WriteDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       binding.ID,
		OperationKey:    "debug-terminal-ambiguous-operation-0001",
		Data:            []byte("go test\r"),
	}
	if _, err := fixture.service.Write(ctx, request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("ambiguous write error=%v code=%s", err, apperror.CodeOf(err))
	}
	if _, err := fixture.service.Write(ctx, request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("ambiguous replay error=%v code=%s", err, apperror.CodeOf(err))
	}
	_, writes := fixture.process.snapshot()
	if writes != 1 {
		t.Fatalf("ambiguous operation executed %d times", writes)
	}
	audit, err := fixture.state.ListRunEvents(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, completed := 0, 0
	for _, event := range audit {
		if event.Type == events.DebugTerminalAgentInputPreparedEvent {
			prepared++
		}
		if event.Type == events.DebugTerminalAgentInputCompletedEvent {
			completed++
		}
	}
	if prepared != 1 || completed != 0 {
		t.Fatalf("ambiguous audit prepared=%d completed=%d", prepared, completed)
	}
}

func TestDebugTerminalAgentInputReadsBoundedOutputAndHasOneRunBinding(t *testing.T) {
	fixture := newDebugTerminalAgentFixture(t, false)
	ctx := context.Background()
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("duplicate Run binding error=%v code=%s", err, apperror.CodeOf(err))
	}
	active, found, err := fixture.service.Active(ctx, fixture.run.ID)
	if err != nil || !found || active.ID != binding.ID || active.TokenExposed {
		t.Fatalf("active binding=%#v found=%t err=%v", active, found, err)
	}
	if err := fixture.process.emit("bounded terminal output\r\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	var output DebugTerminalAgentOutputResult
	for time.Now().Before(deadline) {
		output, err = fixture.service.Read(ctx,
			ReadDebugTerminalAgentOutputRequest{
				ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
				BindingID:       binding.ID, MaxBytes: 64,
			})
		if err != nil || len(output.Data) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil || string(output.Data) != "bounded terminal output\n" ||
		output.NextCursor <= output.BaseCursor || output.Dropped {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	fixture.service.now = func() time.Time {
		return binding.ExpiresAt.Add(time.Second)
	}
	replacement, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: 2 * time.Minute,
		})
	if err != nil || replacement.ID == binding.ID {
		t.Fatalf("expired binding was not replaced: %#v err=%v", replacement, err)
	}
	if err := fixture.service.Revoke(ctx,
		RevokeDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			BindingID:       replacement.ID, RequestedBy: "test_operator",
			OperatorConfirmed: true,
		}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := fixture.service.Active(ctx, fixture.run.ID); err != nil || found {
		t.Fatalf("revoked binding found=%t err=%v", found, err)
	}
}

func TestDebugTerminalAgentInputCannotReadOutputFromBeforeGrant(t *testing.T) {
	fixture := newDebugTerminalAgentFixture(t, false)
	ctx := context.Background()
	if err := fixture.process.emit("user-only output before grant\r\n"); err != nil {
		t.Fatal(err)
	}
	waitForDebugTerminalRingData(t, fixture.manager, fixture.sessionID)
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	page, err := fixture.service.Read(ctx, ReadDebugTerminalAgentOutputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       binding.ID, Cursor: 0, MaxBytes: 1024,
	})
	if err != nil || len(page.Data) != 0 {
		t.Fatalf("pre-grant output crossed the lease fence: %q err=%v", page.Data, err)
	}
	if err := fixture.process.emit("agent-visible output after grant\r\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		page, err = fixture.service.Read(ctx, ReadDebugTerminalAgentOutputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			BindingID:       binding.ID, Cursor: 0, MaxBytes: 1024,
		})
		if err != nil || len(page.Data) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil || string(page.Data) != "agent-visible output after grant\n" {
		t.Fatalf("post-grant output=%q err=%v", page.Data, err)
	}
}

func TestDebugTerminalAgentOutputFencesOnlyAdmittedGrantOrCommand(t *testing.T) {
	for _, action := range []string{"grant", "write", "regrant", "duplicate_grant"} {
		t.Run(action, func(t *testing.T) {
			fixture := newDebugTerminalAgentFixture(t, false)
			ctx := t.Context()
			request := GrantDebugTerminalAgentInputRequest{
				ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
				RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
				RequestedBy: "test_operator", ConfirmFullAccess: true,
				ConfirmAgentTerminalInput: true, TTL: time.Minute,
			}
			var binding DebugTerminalAgentInputBinding
			var err error
			if action != "grant" {
				binding, err = fixture.service.Grant(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := "history\nold pending"
			if err := fixture.process.emit(before); err != nil {
				t.Fatal(err)
			}
			waitForDebugTerminalRingData(t, fixture.manager, fixture.sessionID)
			var cursor uint64
			switch action {
			case "regrant":
				err = fixture.service.Revoke(ctx, RevokeDebugTerminalAgentInputRequest{
					ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
					BindingID:       binding.ID, RequestedBy: "test_operator", OperatorConfirmed: true,
				})
				if err == nil {
					binding, err = fixture.service.Grant(ctx, request)
				}
			case "grant":
				binding, err = fixture.service.Grant(ctx, request)
			case "write":
				var written DebugTerminalAgentInputWriteResult
				written, err = fixture.service.Write(ctx, WriteDebugTerminalAgentInputRequest{
					ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID,
					OperationKey: "output-fence-command", Data: []byte("pwd\r"),
				})
				cursor = written.OutputCursor
			case "duplicate_grant":
				_, duplicateErr := fixture.service.Grant(ctx, request)
				if apperror.CodeOf(duplicateErr) != apperror.CodeConflict {
					t.Fatalf("duplicate grant=%v", duplicateErr)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := fixture.service.Active(ctx, fixture.run.ID); err != nil || !found {
				t.Fatalf("active binding=%t err=%v", found, err)
			}
			if err := fixture.process.emit(" finished\nnew line\n"); err != nil {
				t.Fatal(err)
			}
			var output DebugTerminalAgentOutputResult
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				output, err = fixture.service.Read(ctx, ReadDebugTerminalAgentOutputRequest{
					ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
					BindingID:       binding.ID, Cursor: cursor, MaxBytes: 1024,
				})
				if err != nil || strings.Contains(string(output.Data), "new line") {
					break
				}
				time.Sleep(time.Millisecond)
			}
			want := "new line\n"
			if action == "duplicate_grant" {
				want = "history\nold pending finished\nnew line\n"
			}
			if err != nil || string(output.Data) != want {
				t.Fatalf("output=%q, want %q err=%v", output.Data, want, err)
			}
			userOutput, err := fixture.manager.Read(fixture.sessionID, 0, 1024)
			if err != nil || string(userOutput.Data) != before+" finished\nnew line\n" {
				t.Fatalf("raw terminal changed: %q err=%v", userOutput.Data, err)
			}
		})
	}
}

func TestDebugTerminalAgentInputDropsBindingAfterTerminalClose(t *testing.T) {
	fixture := newDebugTerminalAgentFixture(t, false)
	ctx := context.Background()
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Close(binding.TerminalSessionID,
		"test_operator", true); err != nil {
		t.Fatal(err)
	}
	if active, found, err := fixture.service.Active(ctx, fixture.run.ID); err != nil ||
		found || active.ID != "" {
		t.Fatalf("closed terminal retained binding=%#v found=%t err=%v",
			active, found, err)
	}
	if fixture.service.Reconcile(ctx) != 0 {
		t.Fatal("closed terminal binding was not removed idempotently")
	}
}

func TestDebugTerminalAgentInputRevokesOnPlanPhaseAndRejectsGrant(t *testing.T) {
	fixture := newDebugTerminalAgentFixture(t, false)
	ctx := context.Background()
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlanDeliveryControlService(fixture.state).EnterPlan(ctx,
		ControlPlanModeTransitionRequest{
			Version: PlanDeliveryControlProtocolVersion, RunID: fixture.run.ID,
			OperationKey: "debug-terminal-enter-plan-0001",
			RequestedBy:  "test_operator",
		}); err != nil {
		t.Fatal(err)
	}
	if active, found, err := fixture.service.Active(ctx, fixture.run.ID); err != nil ||
		found || active.ID != "" {
		t.Fatalf("Plan phase retained binding=%#v found=%t err=%v", active, found, err)
	}
	if _, err := fixture.service.Write(ctx, WriteDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       binding.ID,
		OperationKey:    "debug-terminal-plan-write-0001",
		Data:            []byte("go version\r"),
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("Plan phase stale write error=%v code=%s", err, apperror.CodeOf(err))
	}
	if _, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("Plan phase grant error=%v code=%s", err, apperror.CodeOf(err))
	}
}

func TestDebugTerminalAgentInputDoesNotReviveAfterPlanRoundTrip(t *testing.T) {
	fixture := newDebugTerminalAgentFixture(t, false)
	ctx := context.Background()
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	runService := NewRunService(fixture.state)
	if _, err := runService.ChangePhase(ctx, ChangeRunPhaseRequest{
		RunID: fixture.run.ID, Phase: string(domain.ExecutionPhasePlan),
		OperationKey: "debug-terminal-plan-round-trip-0001",
		RequestedBy:  "test_operator", Reason: "test Plan boundary",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runService.ChangePhase(ctx, ChangeRunPhaseRequest{
		RunID: fixture.run.ID, Phase: string(domain.ExecutionPhaseDeliver),
		OperationKey: "debug-terminal-deliver-round-trip-0001",
		RequestedBy:  "test_operator", Reason: "return to Deliver",
	}); err != nil {
		t.Fatal(err)
	}
	if active, found, err := fixture.service.Active(ctx, fixture.run.ID); err != nil ||
		found || active.ID != "" {
		t.Fatalf("old mode revision revived binding=%#v found=%t err=%v", active, found, err)
	}
	if _, err := fixture.service.Write(ctx, WriteDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       binding.ID,
		OperationKey:    "debug-terminal-old-mode-write-0001",
		Data:            []byte("go version\r"),
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("old mode revision write error=%v code=%s", err, apperror.CodeOf(err))
	}
}

func TestDebugTerminalAgentInputRevokesOnWorkspaceRootDrift(t *testing.T) {
	fixture := newDebugTerminalAgentFixture(t, false)
	ctx := context.Background()
	binding, err := fixture.service.Grant(ctx,
		GrantDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			RunID:           fixture.run.ID, TerminalSessionID: fixture.sessionID,
			RequestedBy: "test_operator", ConfirmFullAccess: true,
			ConfirmAgentTerminalInput: true, TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := fixture.state.GetMission(ctx, fixture.run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := fixture.state.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	workspace.RootPath = filepath.Clean(t.TempDir())
	if err := fixture.state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if active, found, err := fixture.service.Active(ctx, fixture.run.ID); err != nil ||
		found || active.ID != "" {
		t.Fatalf("Workspace root drift retained binding=%#v found=%t err=%v", active, found, err)
	}
	if _, err := fixture.service.Write(ctx, WriteDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       binding.ID,
		OperationKey:    "debug-terminal-old-workspace-write-0001",
		Data:            []byte("go version\r"),
	}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("old Workspace root write error=%v code=%s", err, apperror.CodeOf(err))
	}
}

func newDebugTerminalAgentFixture(t *testing.T,
	failWrite bool,
) debugTerminalAgentFixture {
	return newDebugTerminalAgentFixtureWithManager(t, failWrite, false)
}

func newDebugTerminalAgentFixtureWithManager(t *testing.T,
	failWrite, native bool,
) debugTerminalAgentFixture {
	t.Helper()
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "debug-terminal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	workspace := store.WorkspaceRecord{
		ID: "workspace-debug-terminal-agent", Name: "debug-terminal-agent",
		RootPath: filepath.Clean(t.TempDir()),
	}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := NewRunService(state).Create(ctx, CreateRunRequest{
		Goal: "debug with a bounded Agent terminal lease", Profile: "code",
		WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunExecutionProfileService(state).Change(ctx,
		ChangeRunExecutionProfileRequest{
			RunID: run.ID, Profile: "local",
			OperationKey: "debug-terminal-profile-0001",
			RequestedBy:  "test_operator", Reason: "local debug terminal",
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunExecutionInteractionService(state).Change(ctx,
		ChangeRunExecutionInteractionRequest{
			RunID: run.ID, Mode: "debug", Trust: "trusted",
			OperationKey: "debug-terminal-interaction-0001",
			RequestedBy:  "test_operator", Reason: "debug interaction",
			ConfirmWorkspaceTrust: true, ConfirmDebugBoundary: true,
		}); err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
	}
	permissionService := NewRunExecutionPermissionService(state, capabilities)
	if _, err := permissionService.Change(ctx,
		ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: "full",
			OperationKey: "debug-terminal-permission-0001",
			RequestedBy:  "test_operator", Reason: "debug maximum access",
			ConfirmFull: true,
		}); err != nil {
		t.Fatal(err)
	}
	profile, err := state.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	interaction, err := state.GetRunExecutionInteraction(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := state.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	broker := executionauth.NewTerminalInputBroker()
	backend := &debugTerminalBackendStub{}
	var manager *terminalruntime.Manager
	if native {
		manager, err = terminalruntime.NewPlatformManager(broker)
	} else {
		manager, err = terminalruntime.NewManager(backend, broker)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown() })
	sessionID := "terminal-debug-agent-input"
	authorizer := executionauth.NewPolicyAuthorizer(func(checkCtx context.Context, subject executionauth.SubjectRef,
		operation toolcontract.Operation, approvalRef string,
	) (executionauth.OperationAuthority, error) {
		current, err := state.GetRunExecutionPermission(checkCtx, run.ID)
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		if subject.RunID != run.ID || subject.ActorID != "test_operator" || operation.ToolID != "user_terminal" || approvalRef != "" {
			return executionauth.OperationAuthority{}, terminalruntime.ErrTerminalDenied
		}
		generation, active := capabilities.FullAccessGeneration(current)
		raw, err := json.Marshal(struct {
			Permission domain.RunExecutionPermissionSnapshot
			Generation uint64
			Epoch      string
		}{
			current, generation, capabilities.RuntimeAuthority.RuntimeEpoch()})
		if err != nil {
			return executionauth.OperationAuthority{}, err
		}
		return executionauth.OperationAuthority{Mode: domain.ExecutionApprovalFull, BindingFingerprint: session.ContentSHA256(string(raw)),
			RuntimeAvailable: true, FullActivated: active, EffectsVerified: false}, nil
	})

	if _, err := manager.Start(ctx, terminalruntime.StartRequest{
		ID: sessionID,
		Scope: terminalruntime.SessionScope{
			WorkspaceID: workspace.ID, RunID: run.ID,
			InteractionSnapshotID:    interaction.ID,
			InteractionRevision:      interaction.Revision,
			ExecutionProfileRevision: profile.Revision,
			PermissionSnapshotID:     permission.ID,
			PermissionRevision:       permission.Revision,
			PermissionMode:           permission.Mode, Mode: interaction.Mode,
		},
		WorkspaceRoot: workspace.RootPath, Interaction: interaction,
		CurrentProfile: profile, CurrentPermission: permission, Authorizer: authorizer,
		Columns: 100, Rows: 30, RequestedBy: "test_operator",
		OperatorConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if backend.process != nil {
		backend.process.failWrite = failWrite
	}
	bridge, err := terminalruntime.NewAgentInputBridge(manager, broker)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDebugTerminalAgentInputService(
		state, bridge, policy.NewDefaultChecker(), capabilities, true)
	if err != nil {
		t.Fatal(err)
	}
	return debugTerminalAgentFixture{
		state: state, service: service, manager: manager,
		process: backend.process, run: run, permission: permissionService,
		sessionID: sessionID,
	}
}

func waitForDebugTerminalRingData(t *testing.T, manager *terminalruntime.Manager,
	sessionID string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := manager.Get(sessionID)
		if err == nil && session.OutputNextCursor > session.OutputBaseCursor {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal output was not observed")
}

func TestDebugTerminalFullRevocationCannotReviveExistingBridge(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_terminal_new_grant", true: "old_grant"}[existing], func(t *testing.T) {
			f := newDebugTerminalAgentFixture(t, false)
			request := GrantDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, RunID: f.run.ID,
				TerminalSessionID: f.sessionID, RequestedBy: "test_operator", ConfirmFullAccess: true, ConfirmAgentTerminalInput: true, TTL: time.Minute}
			var binding DebugTerminalAgentInputBinding
			var err error
			if existing {
				binding, err = f.service.Grant(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
			}
			authority := f.service.capabilities.RuntimeAuthority
			authority.RevokeRun(f.run.ID)
			if _, err = f.service.Grant(t.Context(), request); err == nil {
				t.Fatal("cold Full issued a lease")
			}
			snapshot, err := f.state.GetRunExecutionPermission(t.Context(), f.run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = authority.ActivateRunFullAccess(snapshot); err != nil {
				t.Fatal(err)
			}
			// Do not run terminal reconciliation: the original native ref must fence it.
			if _, err = f.service.Grant(t.Context(), request); err == nil {
				t.Fatal("new activation revived the old terminal")
			}
			if existing {
				if _, err = f.service.Read(t.Context(), ReadDebugTerminalAgentOutputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, MaxBytes: 64}); err == nil {
					t.Fatal("old lease read after reactivation")
				}
				if _, err = f.service.Write(t.Context(), WriteDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, OperationKey: "after-full-reactivation", Data: []byte("go version\r")}); err == nil {
					t.Fatal("old lease wrote after reactivation")
				}
			}
			if _, writes := f.process.snapshot(); writes != 0 {
				t.Fatalf("unexpected native writes=%d", writes)
			}
		})
	}
}

type debugAuditRevocationStore struct {
	DebugTerminalAgentInputStore
	kind   terminalruntime.AgentInputAuditKind
	revoke func()
}

func (s *debugAuditRevocationStore) RecordDebugTerminalAgentInputAudit(ctx context.Context, record terminalruntime.AgentInputAuditRecord) error {
	if err := s.DebugTerminalAgentInputStore.RecordDebugTerminalAgentInputAudit(ctx, record); err != nil {
		return err
	}
	if record.Kind == s.kind {
		s.revoke()
	}
	return nil
}
func TestDebugTerminalAuditRevocationPreventsPublicationAndNativeWrite(t *testing.T) {
	for _, kind := range []terminalruntime.AgentInputAuditKind{terminalruntime.AgentInputAuditGranted, terminalruntime.AgentInputAuditPrepared} {
		t.Run(string(kind), func(t *testing.T) {
			f := newDebugTerminalAgentFixture(t, false)
			f.service.store = &debugAuditRevocationStore{DebugTerminalAgentInputStore: f.state, kind: kind, revoke: func() { f.service.capabilities.RuntimeAuthority.RevokeRun(f.run.ID) }}
			binding, err := f.service.Grant(t.Context(), GrantDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
				RunID: f.run.ID, TerminalSessionID: f.sessionID, RequestedBy: "test_operator", ConfirmFullAccess: true, ConfirmAgentTerminalInput: true, TTL: time.Minute})
			if kind == terminalruntime.AgentInputAuditGranted {
				if err == nil || len(f.service.bindings) != 0 {
					t.Fatalf("revoked grant published: %+v %v", binding, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.service.Write(t.Context(), WriteDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
					BindingID: binding.ID, OperationKey: "audit-revoke-write", Data: []byte("go version\r")}); err == nil {
					t.Fatal("revocation during prepare dispatched bytes")
				}
			}
			if _, writes := f.process.snapshot(); writes != 0 {
				t.Fatalf("unexpected native writes=%d", writes)
			}
		})
	}
}

type debugCancelledReadStore struct {
	DebugTerminalAgentInputStore
	cancel context.CancelFunc
}

func (s *debugCancelledReadStore) GetRun(ctx context.Context, id string) (domain.Run, error) {
	s.cancel()
	return domain.Run{}, ctx.Err()
}
func TestDebugTerminalRequestCancellationPreservesLeaseAndPreparedFence(t *testing.T) {
	for _, stage := range []string{"read", "reconcile", "prepared"} {
		t.Run(stage, func(t *testing.T) {
			f := newDebugTerminalAgentFixture(t, false)
			binding, err := f.service.Grant(t.Context(), GrantDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
				RunID: f.run.ID, TerminalSessionID: f.sessionID, RequestedBy: "test_operator", ConfirmFullAccess: true, ConfirmAgentTerminalInput: true, TTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := WriteDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, OperationKey: "cancelled-prepared-input", Data: []byte("go version\r")}
			if stage == "prepared" {
				f.service.store = &debugAuditRevocationStore{DebugTerminalAgentInputStore: f.state, kind: terminalruntime.AgentInputAuditPrepared, revoke: cancel}
				_, err = f.service.Write(ctx, request)
			} else {
				f.service.store = &debugCancelledReadStore{DebugTerminalAgentInputStore: f.state, cancel: cancel}
				if stage == "read" {
					_, err = f.service.Read(ctx, ReadDebugTerminalAgentOutputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, MaxBytes: 64})
				} else {
					if revoked := f.service.Reconcile(ctx); revoked != 0 {
						t.Fatalf("cancelled reconcile revoked %d leases", revoked)
					}
					err = ctx.Err()
				}
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("request cancellation lost: %v", err)
			}
			f.service.store = f.state
			active, found, err := f.service.Active(t.Context(), f.run.ID)
			if err != nil || !found || active.ID != binding.ID {
				t.Fatalf("cancelled request revoked live grant: %+v %v %v", active, found, err)
			}
			if stage == "prepared" {
				if _, err := f.service.Write(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
					t.Fatalf("prepared operation was retried: %v", err)
				}
			}
			if _, writes := f.process.snapshot(); writes != 0 {
				t.Fatalf("cancelled input wrote %d times", writes)
			}
		})
	}
}

func TestDebugTerminalReadinessUsesLiveFullWithoutLegacyDebugGate(t *testing.T) {
	f := newDebugTerminalAgentFixture(t, false)
	caps := f.service.capabilities
	caps.WorkspaceSandboxEnabled = true
	service := NewRunCapabilityReadinessService(f.state, CapabilityReadinessRuntime{RunControlEnabled: true, ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: caps, LocalSandboxInstalled: true, LocalSandboxProven: true, LocalBackendReady: true})
	for _, live := range []bool{true, false} {
		if !live {
			caps.RuntimeAuthority.RevokeRun(f.run.ID)
		}
		projection, err := service.Project(t.Context(), f.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, option := range projection.Interactions {
			if option.Value == "debug" {
				found = true
				if option.RuntimeAvailable != live {
					t.Fatalf("Full live=%v debug interaction=%+v", live, option)
				}
			}
		}
		if !found {
			t.Fatal("terminal interaction readiness missing")
		}
	}
}
