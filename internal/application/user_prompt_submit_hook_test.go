package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func promptTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

type promptHookAuditSink struct {
	records []hooks.AuditRecord
}

func (s *promptHookAuditSink) RecordHookAudit(_ context.Context, record hooks.AuditRecord) error {
	s.records = append(s.records, record)
	return nil
}

func promptHookEngine(t *testing.T, sink *promptHookAuditSink, action hooks.Action) *hooks.Engine {
	t.Helper()
	engine := hooks.NewEngine(sink)
	declaration := hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion,
		ID: "prompt-guard", Event: hooks.UserPromptSubmit, Action: action,
		FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100}
	if action == hooks.ActionDeny || action == hooks.ActionAnnotate {
		declaration.Message = "prompt rejected by operator hook"
	}
	if err := engine.Replace([]hooks.Registration{{PluginID: "guard-plugin",
		PluginFingerprint: promptTestDigest("plugin"), Declaration: declaration}}); err != nil {
		t.Fatal(err)
	}
	return engine
}

func openPromptHookStore(t *testing.T, name string) *store.SQLiteStore {
	t.Helper()
	base, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	return base
}

func promptHookRun(t *testing.T, base *store.SQLiteStore, goal string) domain.Run {
	t.Helper()
	ctx := t.Context()
	_, created, err := application.NewRunService(base).Create(ctx,
		application.CreateRunRequest{Goal: goal, Profile: "review", Interactive: true,
			ModelRoute: "lifecycle-test/model", Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestThreadTurnUserPromptSubmitFiresOncePerTurn(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-count.db")
	ctx := t.Context()
	created := promptHookRun(t, base, "prompt hook counting")
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionFinish, "Delivery is ready.", "done", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	execution := application.NewRunExecutionHandoffService(base, router, policy.NewDefaultChecker())
	sink := &promptHookAuditSink{}
	engine := promptHookEngine(t, sink, hooks.ActionRecord)
	turns := application.NewThreadTurnService(base,
		application.NewRunLifecycleControlService(base), execution).WithLifecycleHooks(engine)
	if _, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{
		Version:  domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(created.ID), Content: "Finish this turn.",
		OperationKey: "prompt-hook-count-key", RequestedBy: "thread_turn_test_operator"}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range sink.records {
		if record.Event == hooks.UserPromptSubmit {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("user_prompt_submit fired %d times per turn", count)
	}
}

func TestThreadTurnUserPromptSubmitDenyRejectsBeforePersistence(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-deny.db")
	ctx := t.Context()
	created := promptHookRun(t, base, "prompt hook deny")
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionFinish, "Delivery is ready.", "done", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	execution := application.NewRunExecutionHandoffService(base, router, policy.NewDefaultChecker())
	sink := &promptHookAuditSink{}
	engine := promptHookEngine(t, sink, hooks.ActionDeny)
	turns := application.NewThreadTurnService(base,
		application.NewRunLifecycleControlService(base), execution).WithLifecycleHooks(engine)
	_, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{
		Version:  domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(created.ID), Content: "Finish this turn.",
		OperationKey: "prompt-hook-deny-key", RequestedBy: "thread_turn_test_operator"})
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("denied prompt submission err=%v", err)
	}
	messages, listErr := base.ListSessionMessages(ctx, created.SessionID, true)
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, message := range messages {
		if message.Role == "user" {
			t.Fatalf("denied prompt was persisted before the boundary: %#v", message)
		}
	}
	if len(sink.records) != 1 || sink.records[0].Rejected == nil || !*sink.records[0].Rejected {
		t.Fatalf("denial audit records=%#v", sink.records)
	}
}

func TestThreadServiceSubmitFiresUserPromptSubmitOnce(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-cli.db")
	ctx := t.Context()
	created := promptHookRun(t, base, "prompt hook cli")
	sink := &promptHookAuditSink{}
	engine := promptHookEngine(t, sink, hooks.ActionRecord)
	service := application.NewThreadService(base).WithLifecycleHooks(engine)
	if _, err := service.Submit(ctx, application.SubmitThreadMessageRequest{
		Version:  domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(created.ID), Content: "Direct CLI submission.",
		OperationKey: "prompt-hook-cli-key", RequestedBy: "cli_thread_operator"}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range sink.records {
		if record.Event == hooks.UserPromptSubmit {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("CLI submission fired user_prompt_submit %d times", count)
	}
}

func TestSessionMessageSubmissionDenyRejectsBeforeQueueing(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-steering.db")
	ctx := t.Context()
	created := promptHookRun(t, base, "prompt hook steering")
	sink := &promptHookAuditSink{}
	engine := promptHookEngine(t, sink, hooks.ActionDeny)
	service := application.NewSessionMessageSubmissionService(base).WithLifecycleHooks(engine)
	_, err := service.Submit(ctx, application.SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: created.SessionID,
		Content: "correct the current task", OperationKey: "prompt-hook-steering-key",
		RequestedBy: "http_session_operator"})
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("denied steering submission err=%v", err)
	}
	if len(sink.records) != 1 || sink.records[0].Rejected == nil || !*sink.records[0].Rejected {
		t.Fatalf("denial audit records=%#v", sink.records)
	}
}

func TestSessionRunChatUserPromptSubmitDenyRejects(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-chat.db")
	ctx := context.Background()
	created := promptHookRun(t, base, "prompt hook chat")
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionFinish, "Delivery is ready.", "done", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	sink := &promptHookAuditSink{}
	engine := promptHookEngine(t, sink, hooks.ActionDeny)
	executor := application.NewSessionRunChatExecutor(base, router,
		policy.NewDefaultChecker()).WithLifecycleHooks(engine)
	sess, err := base.GetSession(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = executor.ExecuteSessionTurn(ctx, sess, "run this prompt"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("denied session chat submission err=%v", err)
	}
	if len(sink.records) != 1 || sink.records[0].Rejected == nil || !*sink.records[0].Rejected {
		t.Fatalf("session chat denial audit records=%#v", sink.records)
	}
}

func TestUserPromptSubmitScriptHookFailsClosedWithoutExecutor(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-fail.db")
	ctx := t.Context()
	created := promptHookRun(t, base, "prompt hook fail closed")
	engine := hooks.NewEngine(nil)
	if err := engine.Replace([]hooks.Registration{{PluginID: hooks.OperatorPluginID,
		PluginFingerprint: promptTestDigest("script"), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "script-guard",
			Event: hooks.UserPromptSubmit, Action: hooks.ActionScript,
			FailurePolicy: hooks.FailureDeny, TimeoutMillis: 100,
			Script: &hooks.ScriptSpec{Interpreter: "python", Path: "missing.py",
				SHA256: promptTestDigest("script")}}}}); err != nil {
		t.Fatal(err)
	}
	service := application.NewThreadService(base).WithLifecycleHooks(engine)
	_, err := service.Submit(ctx, application.SubmitThreadMessageRequest{
		Version:  domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(created.ID), Content: "run the script hook",
		OperationKey: "prompt-hook-fail-key", RequestedBy: "cli_thread_operator"})
	// The missing executor fails the script hook closed, which denies the
	// submission with the fixed policy text instead of surfacing an
	// unavailable error.
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("unavailable script executor did not fail the submission closed: err=%v", err)
	}
}

var _ = session.NewManager

type capturingScriptExecutor struct {
	invocations []hooks.ScriptInvocation
}

func (c *capturingScriptExecutor) ExecuteScript(_ context.Context,
	invocation hooks.ScriptInvocation,
) (hooks.ScriptOutput, error) {
	c.invocations = append(c.invocations, invocation)
	return hooks.ScriptOutput{ProtocolVersion: hooks.ProtocolVersion}, nil
}

func promptScriptEngine(t *testing.T, executor hooks.ScriptExecutor) *hooks.Engine {
	t.Helper()
	engine := hooks.NewEngine(nil).WithScriptExecutor(executor)
	if err := engine.Replace([]hooks.Registration{{PluginID: hooks.OperatorPluginID,
		PluginFingerprint: promptTestDigest("script"), Declaration: hooks.Declaration{
			ProtocolVersion: hooks.ProtocolVersion, ID: "prompt-capture",
			Event: hooks.UserPromptSubmit, Action: hooks.ActionScript,
			FailurePolicy: hooks.FailureContinue, TimeoutMillis: 100,
			Script: &hooks.ScriptSpec{Interpreter: "python", Path: "check.py",
				SHA256: promptTestDigest("script")}}}}); err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestUserPromptSubmitPayloadCarriesSurfaceIdentity(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-identity.db")
	ctx := t.Context()
	created := promptHookRun(t, base, "prompt hook identity")
	executor := &capturingScriptExecutor{}
	engine := promptScriptEngine(t, executor)
	service := application.NewThreadService(base).WithLifecycleHooks(engine)
	if _, err := service.Submit(ctx, application.SubmitThreadMessageRequest{
		Version:  domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(created.ID), Content: "thread surface",
		OperationKey: "prompt-hook-identity-key", RequestedBy: "cli_thread_operator"}); err != nil {
		t.Fatal(err)
	}
	if len(executor.invocations) != 1 {
		t.Fatalf("thread surface fired %d script hooks", len(executor.invocations))
	}
	var threadPayload struct {
		ThreadID  string `json:"thread_id"`
		SessionID string `json:"session_id"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(executor.invocations[0].Payload, &threadPayload); err != nil {
		t.Fatal(err)
	}
	if threadPayload.ThreadID == "" || threadPayload.SessionID != "" ||
		threadPayload.Content != "thread surface" {
		t.Fatalf("thread payload identity is wrong: %#v", threadPayload)
	}

	executor2 := &capturingScriptExecutor{}
	engine2 := promptScriptEngine(t, executor2)
	submission := application.NewSessionMessageSubmissionService(base).WithLifecycleHooks(engine2)
	if _, err := submission.Submit(ctx, application.SubmitSessionMessageRequest{
		Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: created.SessionID,
		Content: "session surface", OperationKey: "prompt-hook-identity-session-key",
		RequestedBy: "http_session_operator"}); err != nil {
		t.Fatal(err)
	}
	if len(executor2.invocations) != 1 {
		t.Fatalf("session surface fired %d script hooks", len(executor2.invocations))
	}
	var sessionPayload struct {
		ThreadID  string `json:"thread_id"`
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
	}
	if err := json.Unmarshal(executor2.invocations[0].Payload, &sessionPayload); err != nil {
		t.Fatal(err)
	}
	if sessionPayload.SessionID != created.SessionID || sessionPayload.RunID != created.ID ||
		sessionPayload.ThreadID != "" {
		t.Fatalf("session payload identity is wrong: %#v", sessionPayload)
	}
}

func TestThreadTurnFollowUpDuringActiveTurnFiresPromptBoundary(t *testing.T) {
	provider := &gatedThreadProvider{started: make(chan struct{}), release: make(chan struct{}),
		lifecycleProvider: lifecycleProvider{responses: []string{
			rootActionResponse(domain.RootActionFinish, "First complete", "complete", ""),
			rootActionResponse(domain.RootActionFinish, "Second complete", "complete", ""),
		}},
	}
	_, turns, request := threadControlFixture(t, provider)
	sink := &promptHookAuditSink{}
	turns = turns.WithLifecycleHooks(promptHookEngine(t, sink, hooks.ActionRecord))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := turns.Execute(ctx, request); done <- err }()
	awaitTurnSignal(t, provider.started)
	followUp := request
	followUp.Content, followUp.OperationKey = "Second message during active turn",
		"prompt-hook-followup-operation-0002"
	if _, err := turns.Execute(t.Context(), followUp); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	if err := awaitTurnResult(t, done); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range sink.records {
		if record.Event == hooks.UserPromptSubmit {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("follow-up during an active turn fired user_prompt_submit %d times", count)
	}
}

func TestThreadTurnUserPromptSubmitDenyLeavesNoIntentResidue(t *testing.T) {
	base := openPromptHookStore(t, "prompt-hook-residue.db")
	ctx := t.Context()
	created := promptHookRun(t, base, "prompt hook residue")
	provider := &lifecycleProvider{responses: []string{
		rootActionResponse(domain.RootActionFinish, "Delivery is ready.", "done", ""),
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	execution := application.NewRunExecutionHandoffService(base, router, policy.NewDefaultChecker())
	deny := promptHookEngine(t, &promptHookAuditSink{}, hooks.ActionDeny)
	turns := application.NewThreadTurnService(base,
		application.NewRunLifecycleControlService(base), execution).WithLifecycleHooks(deny)
	denied := application.ExecuteThreadTurnRequest{
		Version:  domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(created.ID), Content: "Finish this turn.",
		OperationKey: "prompt-hook-residue-key", RequestedBy: "thread_turn_test_operator"}
	if _, err := turns.Execute(ctx, denied); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("denied prompt submission err=%v", err)
	}
	// The same operation key must be immediately reusable: a stuck unsettled
	// intent would replay or reject instead of running the turn normally.
	sink := &promptHookAuditSink{}
	turns = turns.WithLifecycleHooks(promptHookEngine(t, sink, hooks.ActionRecord))
	confirmed, err := turns.Execute(ctx, denied)
	if err != nil || confirmed.Submission.Message.ID == "" || provider.calls != 1 {
		t.Fatalf("retry after denial did not run normally: message=%#v calls=%d err=%v",
			confirmed.Submission.Message, provider.calls, err)
	}
	if len(sink.records) != 1 || sink.records[0].Event != hooks.UserPromptSubmit {
		t.Fatalf("retry fired the boundary %#v", sink.records)
	}
}
