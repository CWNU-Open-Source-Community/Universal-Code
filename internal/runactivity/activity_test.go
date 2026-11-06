package runactivity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/events"
)

func TestGeneratedCompactionActivityDoesNotRenderAuxiliaryTextAsAReply(t *testing.T) {
	now := time.Now().UTC()
	fallback := event(3, "session.context_compacted", `{"generated":false,"generation_fallback_reason":"internal provider diagnostic"}`, now)
	fallback.Source = "context_manager"
	projection, err := Build("run-1", []events.Event{
		event(1, events.ModelStartedEvent, `{"purpose":"context_compaction"}`, now),
		event(2, events.ModelCompletedEvent, `{"purpose":"context_compaction","compaction_response":"hidden handoff text"}`, now),
		fallback,
	}, false)
	if err != nil || len(projection.Items) != 3 {
		t.Fatalf("compaction activity missing: %+v %v", projection, err)
	}
	if projection.Items[0].Title != "正在整理上下文" || projection.Items[1].Title != "上下文整理调用已结束" || projection.Items[2].Title != "已采用原文摘录摘要" {
		t.Fatalf("auxiliary call was presented as ordinary answer: %+v", projection.Items)
	}
	for _, item := range projection.Items {
		if item.Source != SourceHarness || strings.Contains(item.Detail, "hidden handoff") || strings.Contains(item.Detail, "internal provider") {
			t.Fatalf("internal compaction content leaked into activity: %+v", item)
		}
	}
}

func TestBuildSeparatesPublicModelUpdatesFromHarnessEvents(t *testing.T) {
	now := time.Now().UTC()
	source := []events.Event{
		event(1, events.ModelStartedEvent,
			`{"provider":"anthropic","model":"test-model","thinking":"private"}`, now),
		event(2, events.SessionMessageEvent,
			`{"role":"assistant","content":"I inspected the files.","source_kind":"model_response","instruction_authorized":false}`, now),
		event(3, events.SupervisorToolBatchEvent,
			`{"tools":["list_workspace","read_file"],"arguments":{"secret":"do-not-show"}}`, now),
		event(4, events.SupervisorToolResultEvent,
			`{"tool":"read_file","status":"completed source-model","result":"private tool output"}`, now),
		event(5, events.ModelDeltaEvent,
			`{"thinking":"hidden chain of thought","text":"raw delta"}`, now),
	}

	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ProtocolVersion || got.PrivateReasoningIncluded ||
		got.ThroughSequence != 5 || got.Truncated || len(got.Items) != 4 {
		t.Fatalf("unexpected projection: %#v", got)
	}
	if got.Items[0].Source != SourceHarness || !got.Items[0].Verifiable ||
		got.Items[1].Source != SourceModel || got.Items[1].Verifiable ||
		got.Items[1].Kind != KindModelUpdate {
		t.Fatalf("activity provenance was not separated: %#v", got.Items)
	}
	encoded := strings.Builder{}
	for _, item := range got.Items {
		encoded.WriteString(item.Title)
		encoded.WriteString(item.Detail)
	}
	for _, forbidden := range []string{"private", "do-not-show", "chain of thought", "raw delta"} {
		if strings.Contains(encoded.String(), forbidden) {
			t.Fatalf("private event data leaked into activity: %q", encoded.String())
		}
	}
	if got.Items[2].Detail != "浏览工作区、读取文件" ||
		got.Items[3].Detail != "读取文件" || got.Items[3].Status != "" {
		t.Fatalf("tool activity was not bounded to names: %#v", got.Items)
	}
	if got.Items[0].Model != "anthropic / test-model" {
		t.Fatalf("model call record lost its provider/model identity: %#v", got.Items[0])
	}
}

func TestBuildKeepsClosedThreadFailureVisibleAfterContinuation(t *testing.T) {
	now := time.Now().UTC()
	got, err := Build("run-1", []events.Event{
		event(1, events.ThreadTurnFailedEvent, `{"error_code":"UNAVAILABLE","handoff_operation_id":"handoff-original"}`, now),
		event(2, events.SessionMessageEvent, `{"role":"user","content":"继续","source_kind":"operator_message","instruction_authorized":true}`, now),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].Title != "本轮执行失败" ||
		got.Items[0].Status != "failed" || got.Items[0].Source != SourceHarness ||
		!strings.Contains(got.Items[0].Detail, "模型服务暂时不可用") || got.Items[1].Source != SourceOperator {
		t.Fatalf("failed product turn disappeared behind the next message: %#v", got.Items)
	}
}

func TestBuildProjectsToolExecutionBoundariesWithoutPayloads(t *testing.T) {
	now := time.Now().UTC()
	source := []events.Event{
		event(1, events.SupervisorToolExecutionStartedEvent,
			`{"tool":"read_file","status":"pending","arguments":{"path":"private.md"}}`, now),
		event(2, events.SupervisorToolExecutionCompletedEvent,
			`{"tool":"read_file","status":"completed","result":"private bytes"}`, now),
	}

	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].Title != "工具执行已开始" ||
		got.Items[0].Status != "running" || got.Items[1].Title != "工具执行已完成" ||
		got.Items[1].Status != "completed" || got.Items[0].Detail != "读取文件" ||
		got.Items[1].Detail != "读取文件" {
		t.Fatalf("unexpected execution boundary projection: %#v", got.Items)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private.md") || strings.Contains(string(encoded), "private bytes") {
		t.Fatalf("tool payload leaked into public activity: %s", encoded)
	}
}

func TestBuildFailureStageCannotRelabelCancellationOrLeakDiagnostic(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct{ payload, title string }{
		{`{"error_code":"CANCELLED","failure_stage":"tool_request_rejected"}`, "本轮已停止"},
		{`{"error_code":"FAILED_PRECONDITION","failure_stage":"unknown","error":"private tool arguments"}`, "本轮执行失败"},
		{`{"error_code":"FAILED_PRECONDITION","failure_stage":"invalid_model_response","error":"private response"}`, "模型答复格式无效"},
		{`{"error_code":"FAILED_PRECONDITION"}`, "本轮执行失败"},
		{`{"error_code":"RESOURCE_EXHAUSTED","failure_stage":"context_window_exceeded"}`, "当前模型的上下文空间不足"},
		{`{"error_code":"RESOURCE_EXHAUSTED"}`, "本轮执行失败"},
		{`{"error_code":"CANCELLED","failure_stage":"context_window_exceeded"}`, "本轮已停止"},
	} {
		got, err := Build("run-1", []events.Event{event(1, events.ThreadTurnFailedEvent, tc.payload, now)}, false)
		if err != nil || len(got.Items) != 1 || got.Items[0].Title != tc.title || strings.Contains(got.Items[0].Detail, "private") {
			t.Fatalf("failure stage changed a stronger outcome or exposed raw content: %#v %v", got, err)
		}
	}
}

func TestBuildRedactsAndBoundsPublicMessages(t *testing.T) {
	secret := "sk-" + strings.Repeat("q", 30)
	content := strings.Repeat("界", MaxDetailRunes+50) + " " + secret
	source := []events.Event{event(1, events.SessionMessageEvent,
		`{"role":"user","content":`+quoted(content)+
			`,"source_kind":"operator_message","instruction_authorized":true}`, time.Now().UTC())}

	got, err := Build("run-1", source, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Source != SourceOperator ||
		!got.Items[0].InstructionAuthorized || !got.Truncated {
		t.Fatalf("unexpected operator activity: %#v", got)
	}
	if strings.Contains(got.Items[0].Detail, secret) ||
		len([]rune(got.Items[0].Detail)) > MaxDetailRunes {
		t.Fatalf("public message was not redacted and bounded: %q", got.Items[0].Detail)
	}
}

func TestBuildProjectsPublicCommentaryWithoutTrustingIt(t *testing.T) {
	secret := "sk-" + strings.Repeat("c", 30)
	source := []events.Event{event(1, events.ModelPublicCommentaryEvent,
		`{"version":"model_public_commentary.v1","run_id":"run-1","attempt_id":"attempt-1",`+
			`"model_attempt":2,"tool_round":1,"phase":"before_tools","text":"准备检查构建 `+secret+`"}`,
		time.Now().UTC())}

	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Source != SourceModel ||
		got.Items[0].Kind != KindModelUpdate || got.Items[0].Verifiable ||
		got.Items[0].AttemptID != "attempt-1" || got.Items[0].ModelAttempt != 2 ||
		got.Items[0].ToolRound != 1 || strings.Contains(got.Items[0].Detail, secret) {
		t.Fatalf("public commentary projection widened trust or leaked data: %#v", got.Items)
	}
}

func TestBuildRejectsCrossRunAndDuplicateEvents(t *testing.T) {
	now := time.Now().UTC()
	crossRun := event(1, events.ModelStartedEvent, `{}`, now)
	crossRun.RunID = "run-2"
	if _, err := Build("run-1", []events.Event{crossRun}, false); err == nil {
		t.Fatal("cross-Run activity source was accepted")
	}
	duplicate := event(1, events.ModelStartedEvent, `{}`, now)
	if _, err := Build("run-1", []events.Event{duplicate, duplicate}, false); err == nil {
		t.Fatal("duplicate event sequence was accepted")
	}
}

func TestBuildProjectsDockerProductLifecycleAndArtifactsInSequence(t *testing.T) {
	now := time.Now().UTC()
	ordered := []events.Event{
		event(1, events.SandboxDockerProductAdmittedEvent,
			`{"decision":"authorized","reason_code":"ready","remediation_code":"none",`+
				`"network_mode":"disabled","product_entry_enabled":true,`+
				`"execution_authorized":true,"artifact_commit_authorized":true}`, now),
		event(2, events.SandboxDockerProductLaunchBoundEvent,
			`{"status":"bound","lifecycle_intent_id":"intent-1","attempt_id":"attempt-1"}`, now),
		event(3, events.SandboxDockerLifecyclePreparedEvent,
			`{"resource_generation":1,"lease_generation":1}`, now),
		event(4, events.SandboxDockerLifecycleActionPreparedEvent,
			`{"ordinal":1,"verb":"start"}`, now),
		event(5, events.SandboxDockerLifecycleTransitionEvent,
			`{"ordinal":1,"state":"started","reason_code":"started"}`, now),
		event(6, events.SandboxDockerProductCancelRequestedEvent,
			`{"reason_code":"cancelled","status":"requested"}`, now),
		event(7, events.SandboxDockerLifecycleActionPreparedEvent,
			`{"ordinal":2,"verb":"term"}`, now),
		event(8, events.SandboxDockerLifecycleTransitionEvent,
			`{"ordinal":2,"state":"exited","reason_code":"cancelled"}`, now),
		event(9, events.SandboxDockerLifecycleTransitionEvent,
			`{"ordinal":3,"state":"cleaning","reason_code":"cancelled"}`, now),
		event(10, events.SandboxDockerLogCaptureCompletedEvent,
			`{"status":"completed","stream_count":2,"total_bytes":120,"total_lines":4,`+
				`"truncated":false,"utf8_violation_count":0,"redacted_segment_count":1}`, now),
		event(11, events.SandboxDockerOutputStagingCompletedEvent,
			`{"status":"completed","file_count":2,"total_bytes":240,"redacted_count":1,`+
				`"truncated":false}`, now),
		event(12, events.SandboxDockerOutputCommitCompletedEvent,
			`{"status":"committed","committed_count":2,"total_bytes":240}`, now),
		event(13, events.SandboxDockerLifecycleTransitionEvent,
			`{"ordinal":4,"state":"cleaned","reason_code":"cleanup_completed"}`, now),
		event(14, events.SandboxDockerLifecycleCompletedEvent,
			`{"outcome":"cancelled","container_removed_now":true}`, now),
		event(15, events.SandboxDockerProductCompletedEvent,
			`{"outcome":"cancelled","reason_code":"cancelled","cleanup_complete":true,`+
				`"artifact_count":2,"artifact_commit_authorized":true}`, now),
	}
	source := make([]events.Event, len(ordered))
	for index := range ordered {
		source[len(ordered)-1-index] = ordered[index]
	}

	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		sequence int64
		title    string
		status   string
	}{
		{1, "Docker 沙箱准入已通过", "preparing"},
		{2, "Docker 沙箱启动已绑定", "starting"},
		{3, "Docker 容器准备中", "preparing"},
		{4, "正在启动 Docker 容器", "starting"},
		{5, "Docker 容器运行中", "running"},
		{6, "Docker 沙箱取消已请求", "cancelling"},
		{7, "正在停止 Docker 容器", "cancelling"},
		{8, "Docker 容器已停止", "cancelling"},
		{9, "Docker 容器清理中", "cleaning"},
		{10, "Docker 日志已收集", "completed"},
		{11, "Docker 输出已暂存", "completed"},
		{12, "Docker 产物已提交", "completed"},
		{13, "Docker 容器已清理", "cleaned"},
		{14, "Docker 容器取消后已清理", "cleaned"},
		{15, "Docker 沙箱已取消并清理", "cleaned"},
	}
	if len(got.Items) != len(want) || got.ThroughSequence != 15 {
		t.Fatalf("unexpected Docker activity projection: %#v", got)
	}
	for index, expected := range want {
		item := got.Items[index]
		if item.Sequence != expected.sequence || item.Title != expected.title ||
			item.Status != expected.status || item.Source != SourceHarness ||
			item.Kind != KindHarnessStatus || !item.Verifiable {
			t.Fatalf("Docker activity %d = %#v, want sequence=%d title=%q status=%q",
				index, item, expected.sequence, expected.title, expected.status)
		}
	}
	if got.Items[9].Detail != "2 个日志流 · 120 bytes · 4 行" ||
		got.Items[10].Detail != "2 个文件 · 240 bytes · 1 个文件已脱敏" ||
		got.Items[11].Detail != "2 个产物 · 240 bytes" ||
		got.Items[14].Detail != "已提交 2 个产物" {
		t.Fatalf("Docker bounded summaries are unstable: %#v", got.Items)
	}
}

func TestBuildProjectsDockerAdmissionDenialWithoutPrivateMetadata(t *testing.T) {
	now := time.Now().UTC()
	value := event(1, events.SandboxDockerProductAdmissionDeniedEvent,
		`{"decision":"denied","reason_code":"budget_exhausted",`+
			`"remediation_code":"increase_or_free_budget","network_mode":"disabled",`+
			`"product_entry_enabled":false,"execution_authorized":false,`+
			`"artifact_commit_authorized":false,"operation_key_digest":"secret"}`, now)
	got, err := Build("run-1", []events.Event{value}, false)
	if err != nil || len(got.Items) != 1 || got.Items[0].Status != "blocked" ||
		got.Items[0].Title != "Docker 沙箱准入已拒绝" ||
		strings.Contains(got.Items[0].Detail, "secret") {
		t.Fatalf("denial projection = %#v err=%v", got.Items, err)
	}
}

func TestBuildDockerActivityNeverProjectsSensitivePayloadFields(t *testing.T) {
	now := time.Now().UTC()
	common := `,"container_id":"secret-container-id","container_name":"secret-container-name",` +
		`"host_path":"C:\\\\secret-host-path","command":"secret-command --token",` +
		`"stdout":"secret-stdout-body","stderr":"secret-stderr-body",` +
		`"operation_key_digest":"secret-operation-fingerprint",` +
		`"request_fingerprint":"secret-request-fingerprint",` +
		`"lease_id":"secret-lease-fingerprint","owner_id":"secret-owner-fingerprint"}`
	source := []events.Event{
		event(1, events.SandboxDockerLifecycleTransitionEvent,
			`{"state":"started","reason_code":"started"`+common, now),
		event(2, events.SandboxDockerLogCaptureCompletedEvent,
			`{"status":"completed","stream_count":2,"total_bytes":12,"total_lines":2`+common, now),
		event(3, events.SandboxDockerOutputStagingCompletedEvent,
			`{"status":"completed","file_count":1,"total_bytes":12,"redacted_count":0,`+
				`"entries":[{"path":"secret-output-path"}]`+common, now),
		event(4, events.SandboxDockerOutputCommitCompletedEvent,
			`{"status":"committed","committed_count":1,"total_bytes":12,`+
				`"entries":[{"path":"secret-artifact-path"}]`+common, now),
		event(5, events.SandboxDockerProductLaunchBoundEvent,
			`{"status":"bound","lifecycle_intent_id":"secret-intent-id",`+
				`"attempt_id":"secret-attempt-id"`+common, now),
	}

	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"secret-container-id", "secret-container-name", "secret-host-path",
		"secret-command", "secret-stdout-body", "secret-stderr-body",
		"secret-operation-fingerprint", "secret-request-fingerprint",
		"secret-lease-fingerprint", "secret-owner-fingerprint",
		"secret-output-path", "secret-artifact-path", "secret-intent-id",
		"secret-attempt-id",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Docker private payload field leaked into activity: %s", forbidden)
		}
	}
	if len(got.Items) != len(source) {
		t.Fatalf("recognised Docker events were lost: %#v", got.Items)
	}
}

func TestBuildProjectsDockerFailureActivities(t *testing.T) {
	now := time.Now().UTC()
	source := []events.Event{
		event(1, events.SandboxDockerLifecycleTransitionEvent,
			`{"state":"failed","reason_code":"cleanup_failed"}`, now),
		event(2, events.SandboxDockerLifecycleCompletedEvent,
			`{"outcome":"failed"}`, now),
		event(3, events.SandboxDockerLogCaptureFailedEvent, `{}`, now),
		event(4, events.SandboxDockerOutputStagingFailedEvent, `{}`, now),
		event(5, events.SandboxDockerOutputCommitFailedEvent, `{}`, now),
		event(6, events.SandboxDockerProductCompletedEvent,
			`{"outcome":"failed","reason_code":"io_failed","cleanup_complete":true,`+
				`"artifact_count":0,"artifact_commit_authorized":true}`, now),
	}

	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != len(source) {
		t.Fatalf("Docker failures were not completely projected: %#v", got.Items)
	}
	wantTitles := []string{
		"Docker 容器操作失败", "Docker 容器失败后已清理", "Docker 日志收集失败",
		"Docker 输出暂存失败", "Docker 产物提交失败", "Docker 沙箱运行失败并已清理",
	}
	for index, item := range got.Items {
		if item.Title != wantTitles[index] || item.Status != "failed" {
			t.Fatalf("Docker failure %d = %#v", index, item)
		}
	}
	if got.Items[0].Detail != "容器清理失败" || got.Items[5].Detail != "未提交产物" {
		t.Fatalf("Docker failure summaries are unstable: %#v", got.Items)
	}
}

func TestBuildDockerActivityIgnoresUnknownContractsAndUnboundedCounts(t *testing.T) {
	now := time.Now().UTC()
	source := []events.Event{
		event(1, "sandbox.docker_future_event", `{"payload":"must-not-project"}`, now),
		event(2, events.SandboxDockerLifecycleActionPreparedEvent,
			`{"verb":"exec-secret-command"}`, now),
		event(3, events.SandboxDockerLifecycleTransitionEvent,
			`{"state":"paused-secret-state","reason_code":"future"}`, now),
		event(4, events.SandboxDockerLogCaptureCompletedEvent,
			`{"status":"future-status","stream_count":2,"total_bytes":1,"total_lines":1}`, now),
		event(5, events.SandboxDockerOutputStagingCompletedEvent,
			`{"status":"future-status","file_count":1,"total_bytes":1,"redacted_count":0}`, now),
		event(6, events.SandboxDockerOutputCommitCompletedEvent,
			`{"status":"future-status","committed_count":1,"total_bytes":1}`, now),
		event(7, events.SandboxDockerProductAdmittedEvent,
			`{"decision":"authorized","network_mode":"host","product_entry_enabled":true,`+
				`"execution_authorized":true,"artifact_commit_authorized":true}`, now),
		event(8, events.SandboxDockerProductLaunchBoundEvent, `{"status":"future"}`, now),
		event(9, events.SandboxDockerProductCancelRequestedEvent,
			`{"status":"requested","reason_code":"future"}`, now),
		event(10, events.SandboxDockerProductCompletedEvent,
			`{"outcome":"future","cleanup_complete":true,"artifact_count":0,`+
				`"artifact_commit_authorized":true}`, now),
		event(11, events.SandboxDockerLogCaptureCompletedEvent,
			`{"status":"completed","stream_count":2,"total_bytes":999999999,`+
				`"total_lines":999999999}`, now),
	}

	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.ThroughSequence != 11 || len(got.Items) != 1 {
		t.Fatalf("unknown Docker contracts were projected: %#v", got)
	}
	if got.Items[0].Sequence != 11 || got.Items[0].Title != "Docker 日志已收集" ||
		got.Items[0].Detail != "" {
		t.Fatalf("unbounded Docker metadata was forwarded: %#v", got.Items[0])
	}
}

func event(sequence int64, eventType string, payload string, createdAt time.Time) events.Event {
	return events.Event{
		EventID: "evt-" + string(rune('a'+sequence)), Version: events.EnvelopeVersion,
		RunID: "run-1", MissionID: "mission-1", Sequence: sequence, Type: eventType,
		Source: "test", PayloadJSON: payload, CreatedAt: createdAt,
	}
}

func quoted(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, current := range value {
		switch current {
		case '\\', '"':
			builder.WriteByte('\\')
			builder.WriteRune(current)
		case '\n':
			builder.WriteString(`\n`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			builder.WriteRune(current)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func TestBuildProjectsDependencyWaitsWithoutRawOutput(t *testing.T) {
	now := time.Now().UTC()
	source := []events.Event{
		event(1, events.DependencyWaitRecordedEvent,
			`{"source_kind":"agent","source_id":"agent-root","target_kind":"agent","target_id":"agent-child","reason":"await child report","generation":1,"failure_policy":"fail"}`, now),
		event(2, events.DependencySatisfiedEvent,
			`{"source_kind":"agent","source_id":"agent-root","target_kind":"agent","target_id":"agent-child","reason":"target completed","generation":1,"failure_policy":"fail","private":"child raw output"}`, now),
		event(3, events.DependencyDeadlockDetectedEvent, `{"edge_ids":["edge-a"],"edge_count":1}`, now),
		event(4, events.DependencyLivelockDetectedEvent,
			`{"source_kind":"agent","source_id":"agent-root","wake_count":65}`, now),
	}
	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 4 {
		t.Fatalf("unexpected projection: %#v", got.Items)
	}
	for _, item := range got.Items {
		if item.Kind != KindDependency || !item.Verifiable {
			t.Fatalf("dependency activity has wrong provenance: %#v", item)
		}
	}
	if got.Items[0].Status != "waiting" || got.Items[0].Title != "Agent 依赖等待已记录" ||
		got.Items[0].Detail != "agent-root → agent-child · await child report" {
		t.Fatalf("wait item is wrong: %#v", got.Items[0])
	}
	if got.Items[1].Status != "satisfied" || !strings.Contains(got.Items[1].Detail, "target completed") {
		t.Fatalf("satisfied item is wrong: %#v", got.Items[1])
	}
	if strings.Contains(got.Items[1].Detail, "child raw output") {
		t.Fatal("raw child output leaked into dependency activity")
	}
	if got.Items[2].Status != "blocked" || got.Items[2].Detail != "1 个等待未能取得进展" {
		t.Fatalf("deadlock item is wrong: %#v", got.Items[2])
	}
	if got.Items[3].Status != "blocked" || got.Items[3].Detail != "同一依赖已被唤醒 65 次" {
		t.Fatalf("livelock item is wrong: %#v", got.Items[3])
	}
}

func TestBuildProjectsBrowserLifecycleAsGoObservedFacts(t *testing.T) {
	now := time.Now().UTC()
	source := []events.Event{
		event(1, events.BrowserLaunchAttemptPreparedEvent, `{}`, now),
		event(2, events.BrowserLaunchLeaseRecordedEvent, `{}`, now),
		event(3, events.BrowserLaunchReviewedEvent, `{}`, now),
		event(4, events.BrowserRuntimeCheckpointRecordedEvent, `{}`, now),
		event(5, events.BrowserRuntimeReceiptRecordedEvent, `{}`, now),
	}
	got, err := Build("run-1", source, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 5 {
		t.Fatalf("unexpected projection: %#v", got.Items)
	}
	for _, item := range got.Items {
		if item.Kind != KindBrowser || !item.Verifiable {
			t.Fatalf("browser activity has wrong provenance: %#v", item)
		}
	}
	if got.Items[0].Status != "pending" || got.Items[0].Title != "浏览器启动已准备" {
		t.Fatalf("prepared item is wrong: %#v", got.Items[0])
	}
	if got.Items[1].Status != "pending" || got.Items[1].Title != "浏览器启动租约已记录" {
		t.Fatalf("lease item is wrong: %#v", got.Items[1])
	}
	if got.Items[2].Status != "completed" || got.Items[2].Title != "浏览器启动已复核" {
		t.Fatalf("reviewed item is wrong: %#v", got.Items[2])
	}
	if got.Items[3].Status != "running" || got.Items[3].Title != "浏览器运行时检查点已记录" {
		t.Fatalf("checkpoint item is wrong: %#v", got.Items[3])
	}
	if got.Items[4].Status != "completed" || got.Items[4].Title != "浏览器运行时收据已记录" {
		t.Fatalf("receipt item is wrong: %#v", got.Items[4])
	}
}

func TestBuildCheckpointTransactionsUseRecordedOperation(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		titles  [3]string
	}{
		{"command", `{"kind":"command_batch","before_checkpoint_id":"before","after_checkpoint_id":"after","error_code":"internal"}`,
			[3]string{"命令操作已准备", "命令操作已完成", "命令操作失败"}},
		{"file", `{"kind":"file_tool"}`,
			[3]string{"文件操作已准备", "文件操作已完成", "文件操作失败"}},
		{"undo", `{"kind":"undo"}`,
			[3]string{"工作区撤销已准备", "工作区撤销已完成", "工作区撤销失败"}},
		{"rewind", `{"kind":"rewind"}`,
			[3]string{"工作区回退已准备", "工作区回退已完成", "工作区回退失败"}},
		{"redo", `{"kind":"redo"}`,
			[3]string{"工作区重做已准备", "工作区重做已完成", "工作区重做失败"}},
		{"fork", `{"kind":"fork"}`,
			[3]string{"检查点分支创建已准备", "检查点分支创建已完成", "检查点分支创建失败"}},
		{"legacy missing kind", `{}`,
			[3]string{"检查点操作已准备", "检查点操作已完成", "检查点操作失败"}},
		{"unknown kind", `{"kind":"unknown_future_operation","reason":"private input"}`,
			[3]string{"检查点操作已准备", "检查点操作已完成", "检查点操作失败"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 9, 8, 16, 31, 3, 0, time.UTC)
			source := []events.Event{
				event(1, events.WorkspaceCheckpointTransactionPreparedEvent, test.payload, now),
				event(2, events.WorkspaceCheckpointTransactionCompletedEvent, test.payload, now),
				event(3, events.WorkspaceCheckpointTransactionFailedEvent, test.payload, now),
			}
			got, err := Build("run-1", source, false)
			if err != nil || len(got.Items) != len(source) {
				t.Fatalf("projection=%+v err=%v", got, err)
			}
			statuses := [3]string{"pending", "completed", "failed"}
			for index, item := range got.Items {
				if item.Title != test.titles[index] {
					t.Errorf("event %s title=%q want=%q", source[index].Type, item.Title, test.titles[index])
				}
				if item.Status != statuses[index] || item.ID != source[index].EventID ||
					item.Sequence != source[index].Sequence || !item.CreatedAt.Equal(source[index].CreatedAt) ||
					item.Kind != KindPlan || item.Source != SourceHarness || !item.Verifiable || item.Detail != "" {
					t.Errorf("operation wording changed audit provenance or exposed payload: %+v", item)
				}
			}
		})
	}
}
