package runactivity

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
)

const (
	ProtocolVersion = "run_activity.v1"
	MaxSourceEvents = 100
	MaxDetailRunes  = 4096
	maxLabelRunes   = 128
	maxToolNames    = 12
)

type Source string

const (
	SourceHarness  Source = "harness"
	SourceModel    Source = "model"
	SourceOperator Source = "operator"
)

type Kind string

const (
	KindHarnessStatus Kind = "harness_status"
	KindModelUpdate   Kind = "model_update"
	KindOperatorInput Kind = "operator_input"
	KindModelCall     Kind = "model_call"
	KindToolCall      Kind = "tool_call"
	KindApproval      Kind = "approval"
	KindFileChange    Kind = "file_change"
	KindPlan          Kind = "plan"
	KindDependency    Kind = "dependency"
	KindBrowser       Kind = "browser"
)

type Item struct {
	ID                    string
	Sequence              int64
	Kind                  Kind
	Source                Source
	Title                 string
	Detail                string
	Status                string
	Verifiable            bool
	InstructionAuthorized bool
	AttemptID             string
	ModelAttempt          int
	Model                 string
	ToolRound             int
	CreatedAt             time.Time
}

type Projection struct {
	Version                  string
	RunID                    string
	ThroughSequence          int64
	Truncated                bool
	PrivateReasoningIncluded bool
	Items                    []Item
}

// Build creates a display-only projection. It never forwards an event payload
// wholesale and deliberately has no mapping for provider thinking or model deltas.
func Build(runID string, source []events.Event, truncated bool) (Projection, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Projection{}, fmt.Errorf("run activity requires a run id")
	}
	if len(source) > MaxSourceEvents {
		return Projection{}, fmt.Errorf("run activity accepts at most %d source events", MaxSourceEvents)
	}
	ordered := append([]events.Event(nil), source...)
	slices.SortFunc(ordered, func(left, right events.Event) int {
		return cmp.Compare(left.Sequence, right.Sequence)
	})
	projection := Projection{
		Version: ProtocolVersion, RunID: runID, Truncated: truncated,
		PrivateReasoningIncluded: false, Items: make([]Item, 0, len(ordered)),
	}
	var previous int64
	for _, event := range ordered {
		if event.RunID != runID || event.Sequence <= 0 || event.Sequence == previous {
			return Projection{}, fmt.Errorf("run activity source events are inconsistent")
		}
		previous = event.Sequence
		if event.Sequence > projection.ThroughSequence {
			projection.ThroughSequence = event.Sequence
		}
		if item, ok := projectEvent(event); ok {
			projection.Items = append(projection.Items, item)
		}
	}
	return projection, nil
}

func projectEvent(event events.Event) (Item, bool) {
	base := Item{
		ID: event.EventID, Sequence: event.Sequence, Source: SourceHarness,
		Kind: KindHarnessStatus, Verifiable: true, CreatedAt: event.CreatedAt,
	}
	switch event.Type {
	case events.SessionMessageEvent:
		return projectMessage(event, base)
	case events.ModelStartedEvent:
		base.Kind, base.Title, base.Status = KindModelCall, "模型调用开始", "running"
		identity := modelIdentity(event.PayloadJSON)
		base.Detail = identity
		base.Model = identity
		base.AttemptID = cleanLabel(stringField(event.PayloadJSON, "attempt_id"))
		base.ModelAttempt = int(intField(event.PayloadJSON, "model_attempt"))
		base.ToolRound = int(intField(event.PayloadJSON, "tool_round"))
		if stringField(event.PayloadJSON, "purpose") == "context_compaction" {
			base.Title = "正在整理上下文"
		}
	case events.ModelPublicCommentaryEvent:
		base.Kind, base.Source, base.Title = KindModelUpdate, SourceModel, "Universal Code"
		base.Detail = stringField(event.PayloadJSON, "text")
		base.Verifiable = false
		base.AttemptID = cleanLabel(stringField(event.PayloadJSON, "attempt_id"))
		base.ModelAttempt = int(intField(event.PayloadJSON, "model_attempt"))
		base.ToolRound = int(intField(event.PayloadJSON, "tool_round"))
	case events.ModelCompletedEvent:
		base.Kind, base.Title, base.Status = KindModelCall, "模型响应完成", "completed"
		identity := modelIdentity(event.PayloadJSON)
		base.Model = identity
		base.Detail = completedModelDetail(event.PayloadJSON, identity)
		base.AttemptID = cleanLabel(stringField(event.PayloadJSON, "attempt_id"))
		base.ModelAttempt = int(intField(event.PayloadJSON, "model_attempt"))
		base.ToolRound = int(intField(event.PayloadJSON, "tool_round"))
		if stringField(event.PayloadJSON, "purpose") == "context_compaction" {
			base.Title = "上下文整理调用已结束"
		}
	case events.ModelFailedEvent:
		base.Kind, base.Title, base.Status = KindModelCall, "模型调用失败", "failed"
		identity := modelIdentity(event.PayloadJSON)
		if identity != "" {
			base.Model = identity
		}
		base.AttemptID = cleanLabel(stringField(event.PayloadJSON, "attempt_id"))
		base.ModelAttempt = int(intField(event.PayloadJSON, "model_attempt"))
		base.ToolRound = int(intField(event.PayloadJSON, "tool_round"))
		if stringField(event.PayloadJSON, "purpose") == "context_compaction" {
			base.Title = "上下文整理调用未完成"
		}
	case "session.context_compacted":
		if event.Source != "context_manager" {
			return Item{}, false
		}
		base.Title, base.Status = "上下文摘要已保存", "completed"
		base.Detail = "原始记录仍可按来源回读，可在任务上下文中查看摘要并补充纠正。"
		if stringField(event.PayloadJSON, "generation_fallback_reason") != "" {
			base.Title = "已采用原文摘录摘要"
			base.Detail = "这次未采用模型生成说明，已保存原文摘录以继续对话。原始记录仍保留。"
		}
	case events.ModelCancelRequestedEvent:
		base.Kind, base.Title, base.Status = KindModelCall, "模型取消已请求", "cancelling"
	case events.ModelCancelObservedEvent:
		base.Kind, base.Title, base.Status = KindModelCall, "模型取消已确认", "cancelled"
	case events.AgentTurnStartedEvent:
		base.Title, base.Status = "Agent 回合开始", "running"
	case events.AgentTurnCompletedEvent:
		base.Title, base.Status = "Agent 回合完成", "completed"
	case events.AgentTurnFailedEvent:
		base.Title, base.Status = "Agent 回合失败", "failed"
	case events.RunExecutionHandoffCompletedEvent:
		// A saved approval and its later execution have separate outcomes.
		// Other handoffs and successful continuations stay in the audit detail.
		if event.Source != "run_execution_handoff" ||
			stringField(event.PayloadJSON, "requested_by") != "approval_continuation" ||
			stringField(event.PayloadJSON, "status") != "failed" {
			return Item{}, false
		}
		base.Title, base.Status = "审批已保存，后续执行未完成", "failed"
		base.Detail = "审批已保存，后续执行未完成。请查看工具记录和当前任务状态；已完成的操作仍保留。"
		if stringField(event.PayloadJSON, "error_code") == "cancelled" {
			base.Title, base.Status = "审批已保存，后续执行已停止", "cancelled"
			base.Detail = "审批已保存，后续执行已停止。请查看工具记录确认已完成的操作。"
		} else if stringField(event.PayloadJSON, "error_code") == "failed_precondition" {
			switch stringField(event.PayloadJSON, "failure_stage") {
			case "empty_model_response":
				base.Title = "审批已保存，模型没有返回有效答复"
				base.Detail = "审批已保存，后续执行因模型没有返回有效答复而结束。此前已完成的操作仍保留，请查看工具记录。"
			case "tool_request_rejected":
				base.Title = "审批已保存，模型的工具请求无效"
				base.Detail = "审批已保存，后续执行中的工具请求未通过校验，该批请求尚未执行。此前已完成的操作仍保留。"
			case "invalid_model_response":
				base.Title = "审批已保存，模型答复格式无效"
				base.Detail = "审批已保存，后续执行中的模型答复未通过校验。此前已完成的操作仍保留，请查看工具记录。"
			}
		}
	case events.ThreadTurnFailedEvent:
		base.Title, base.Status = "本轮执行失败", "failed"
		base.Detail = "本轮执行失败。输入和已完成的操作已保留，可发送新消息继续。"
		if stringField(event.PayloadJSON, "error_code") == "UNAVAILABLE" {
			base.Detail = "模型服务暂时不可用。输入和已完成的操作已保留，可发送新消息继续。"
		} else if stringField(event.PayloadJSON, "error_code") == "CANCELLED" {
			base.Title, base.Status = "本轮已停止", "cancelled"
			base.Detail = "本轮已停止。已完成的修改和已受理的补充要求已保留，可发送消息继续。"
		} else if stringField(event.PayloadJSON, "error_code") == "RESOURCE_EXHAUSTED" &&
			stringField(event.PayloadJSON, "failure_stage") == domain.ThreadFailureContextWindowExceeded {
			base.Title = "当前模型的上下文空间不足"
			base.Detail = "任务上下文超出当前模型的输入窗口，本轮已结束。历史和已完成的操作已保留；请缩短本次输入，或切换到上下文窗口更大的模型后继续。"
		} else if stringField(event.PayloadJSON, "error_code") == "FAILED_PRECONDITION" {
			switch stringField(event.PayloadJSON, "failure_stage") {
			case "tool_request_rejected":
				base.Title = "模型的工具请求无效"
				base.Detail = "模型的工具请求未通过校验，该批请求尚未执行。此前已完成的操作已保留，可发送新消息继续。"
			case "empty_model_response":
				base.Title = "模型没有返回有效答复"
				base.Detail = "模型没有返回有效答复，本轮已结束。此前已完成的操作已保留，可发送新消息继续。"
			case "invalid_model_response":
				base.Title = "模型答复格式无效"
				base.Detail = "模型答复未通过校验，本轮已结束。此前已完成的操作已保留，可发送新消息继续。"
			}
		}
	case events.SupervisorToolBatchEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具调用已请求", "running"
		base.Detail = toolList(event.PayloadJSON)
	case events.SupervisorToolExecutionStartedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具执行已开始", "running"
		if tool := cleanLabel(stringField(event.PayloadJSON, "tool")); tool != "" {
			base.Detail = toolDisplayName(tool)
		}
	case events.SupervisorToolExecutionCompletedEvent:
		base.Kind, base.Title = KindToolCall, "工具执行已完成"
		base.Status = cleanStatus(stringField(event.PayloadJSON, "status"))
		if tool := cleanLabel(stringField(event.PayloadJSON, "tool")); tool != "" {
			base.Detail = toolDisplayName(tool)
		}
	case events.SupervisorToolResultEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具结果已记录",
			cleanStatus(stringField(event.PayloadJSON, "status"))
		if tool := cleanLabel(stringField(event.PayloadJSON, "tool")); tool != "" {
			base.Detail = toolDisplayName(tool)
		}
	case events.SupervisorToolCompleteEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具批次完成", "completed"
	case events.ToolProposedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具操作已提议", "pending"
		base.Detail = toolNameFromPayload(event.PayloadJSON)
	case events.ToolApprovedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具操作已批准", "approved"
		base.Detail = toolNameFromPayload(event.PayloadJSON)
	case events.ToolDeniedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具操作已拒绝", "denied"
		base.Detail = toolNameFromPayload(event.PayloadJSON)
	case events.ToolStartedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具操作开始", "running"
		base.Detail = toolNameFromPayload(event.PayloadJSON)
	case events.ToolCompletedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具操作完成", "completed"
		base.Detail = toolNameFromPayload(event.PayloadJSON)
	case events.ToolFailedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "工具操作失败", "failed"
		base.Detail = toolNameFromPayload(event.PayloadJSON)
	case events.ApprovalRequestedEvent:
		base.Kind, base.Title, base.Status = KindApproval, "等待用户审批", "pending"
	case events.ApprovalDecidedEvent:
		base.Kind, base.Title, base.Status = KindApproval, "用户审批已记录", "completed"
		if stringField(event.PayloadJSON, "mode") == "automatic" &&
			stringField(event.PayloadJSON, "reviewed_by") == "automatic_policy" {
			base.Title = "完全访问自动授权已记录"
		}
	case events.ApprovalBoundEvent:
		base.Kind, base.Title, base.Status = KindApproval, "审批已绑定到操作", "pending"
	case events.ControlledCommandProposedEvent:
		base.Kind, base.Title, base.Status = KindApproval, "受控命令已提议", "pending"
	case events.ControlledCommandProposalReviewedEvent:
		base.Kind, base.Title, base.Status = KindApproval, "受控命令审批已记录", "completed"
	case events.ControlledCommandProposalResultRecordedEvent:
		base.Kind, base.Title, base.Status = KindToolCall, "受控命令结果已记录", "completed"
	case events.FileEditProposedEvent:
		base.Kind, base.Title, base.Status = KindFileChange, "文件修改已提议", "pending"
	case events.FileEditApprovedEvent:
		base.Kind, base.Title, base.Status = KindFileChange, "文件修改已批准", "approved"
		if source := stringField(event.PayloadJSON, "authorization_source"); source == "full_access_automatic" || source == "operation_policy_automatic" {
			base.Title = "文件修改已自动授权"
		}
	case events.FileEditAppliedEvent, events.FileEditApplyCompletedEvent:
		base.Kind, base.Title, base.Status = KindFileChange, "文件修改已应用", "completed"
	case events.FileEditDeniedEvent:
		base.Kind, base.Title, base.Status = KindFileChange, "文件修改已拒绝", "denied"
	case events.FileEditFailedEvent:
		base.Kind, base.Title, base.Status = KindFileChange, "文件修改失败", "failed"
	case events.PlanDeliveryProposedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "计划方向已生成", "pending"
	case events.PlanDeliveryDirectionSelectedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "计划方向已选择", "selected"
	case events.DeliveryCheckpointRecordedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "切片交付检查点已记录", "completed"
	case events.VerificationEvidenceRecordedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "验证证据已记录", "completed"
	case events.VerificationPlanRecordedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "验证计划已记录", "completed"
	case events.VerificationPlanEvidenceAssociatedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "验证证据已关联", "completed"
	case events.VerificationSnapshotReceiptRecordedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "验证快照收据已记录", "completed"
	case events.VerificationSnapshotReviewRecordedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "验证快照已复核", "completed"
	case events.SupervisorCheckpointedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "Supervisor 检查点已记录", "completed"
	case events.WorkspaceCheckpointCreatedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "工作区检查点已创建", "completed"
	case events.WorkspaceCheckpointTransactionPreparedEvent:
		base.Kind, base.Title, base.Status = KindPlan, checkpointOperationName(event.PayloadJSON)+"已准备", "pending"
	case events.WorkspaceCheckpointTransactionCompletedEvent:
		base.Kind, base.Title, base.Status = KindPlan, checkpointOperationName(event.PayloadJSON)+"已完成", "completed"
	case events.WorkspaceCheckpointTransactionFailedEvent:
		base.Kind, base.Title, base.Status = KindPlan, checkpointOperationName(event.PayloadJSON)+"失败", "failed"
	case events.ArtifactCreatedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "交付物已记录", "completed"
	case events.ProtocolRepairStartedEvent, events.AgentProtocolRepairStartedEvent:
		base.Title, base.Status = "模型协议修复开始", "running"
	case events.ProtocolRepairCompletedEvent, events.AgentProtocolRepairCompletedEvent:
		base.Title, base.Status = "模型协议修复完成", "completed"
	case events.ProtocolRepairFailedEvent, events.AgentProtocolRepairFailedEvent:
		base.Title, base.Status = "模型协议修复失败", "failed"
	case events.SupervisorRunWaitingEvent:
		base.Title, base.Status = "Run 正在等待用户或依赖", "waiting"
	case events.SupervisorRunCompletedEvent:
		base.Title, base.Status = "Run 已完成", "completed"
	case events.SupervisorRunFailedEvent:
		base.Title, base.Status = "Run 已失败", "failed"
	case events.SupervisorLivelockDetectedEvent:
		base.Title, base.Status = "检测到无进展循环", "blocked"
	case events.RunStatusChangedEvent:
		base.Title = "Run 状态已更新"
		base.Status = cleanStatus(stringField(event.PayloadJSON, "status"))
	case events.SandboxDockerLifecyclePreparedEvent,
		events.SandboxDockerLifecycleAcquiredEvent,
		events.SandboxDockerLifecycleTakenOverEvent,
		events.SandboxDockerLifecycleActionPreparedEvent,
		events.SandboxDockerLifecycleTransitionEvent,
		events.SandboxDockerLifecycleCompletedEvent,
		events.SandboxDockerLogCapturePreparedEvent,
		events.SandboxDockerLogCaptureAcquiredEvent,
		events.SandboxDockerLogCaptureTakenOverEvent,
		events.SandboxDockerLogCaptureFailedEvent,
		events.SandboxDockerLogCaptureCompletedEvent,
		events.SandboxDockerOutputStagingPreparedEvent,
		events.SandboxDockerOutputStagingAcquiredEvent,
		events.SandboxDockerOutputStagingTakenOverEvent,
		events.SandboxDockerOutputStagingFailedEvent,
		events.SandboxDockerOutputStagingCompletedEvent,
		events.SandboxDockerOutputCommitPreparedEvent,
		events.SandboxDockerOutputCommitFailedEvent,
		events.SandboxDockerOutputCommitCompletedEvent,
		events.SandboxDockerProductAdmittedEvent,
		events.SandboxDockerProductAdmissionDeniedEvent,
		events.SandboxDockerProductLaunchBoundEvent,
		events.SandboxDockerProductCancelRequestedEvent,
		events.SandboxDockerProductCompletedEvent:
		return projectDockerEvent(event, base)
	case events.AgentInboxContextPreparedEvent:
		base.Title, base.Status = "上下文交付已准备", "pending"
	case events.AgentInboxContextCommittedEvent:
		base.Title, base.Status = "上下文交付已提交", "completed"
	case events.AgentInboxContextSupersededEvent:
		base.Title, base.Status = "陈旧上下文已替换", "superseded"
	case events.DependencyWaitRecordedEvent:
		base.Kind, base.Title, base.Status = KindDependency, "Agent 依赖等待已记录", "waiting"
		base.Detail = dependencyDetail(event.PayloadJSON)
	case events.DependencySatisfiedEvent:
		base.Kind, base.Title, base.Status = KindDependency, "Agent 依赖已满足", "satisfied"
		base.Detail = dependencyDetail(event.PayloadJSON)
	case events.DependencyFailedEvent:
		base.Kind, base.Title, base.Status = KindDependency, "Agent 依赖已失败", "failed"
		base.Detail = dependencyDetail(event.PayloadJSON)
	case events.DependencyCancelledEvent:
		base.Kind, base.Title, base.Status = KindDependency, "Agent 依赖已取消", "cancelled"
		base.Detail = dependencyDetail(event.PayloadJSON)
	case events.DependencyExpiredEvent:
		base.Kind, base.Title, base.Status = KindDependency, "Agent 依赖已超时", "expired"
		base.Detail = dependencyDetail(event.PayloadJSON)
	case events.DependencyDeadlockDetectedEvent:
		base.Kind, base.Title, base.Status = KindDependency, "检测到依赖死锁", "blocked"
		base.Detail = dependencyStallDetail(event.PayloadJSON)
	case events.DependencyLivelockDetectedEvent:
		base.Kind, base.Title, base.Status = KindDependency, "检测到依赖轮询循环", "blocked"
		base.Detail = dependencyStallDetail(event.PayloadJSON)
	case events.BrowserLaunchAttemptPreparedEvent:
		base.Kind, base.Title, base.Status = KindBrowser, "浏览器启动已准备", "pending"
	case events.BrowserLaunchLeaseRecordedEvent:
		base.Kind, base.Title, base.Status = KindBrowser, "浏览器启动租约已记录", "pending"
	case events.BrowserLaunchReviewedEvent:
		base.Kind, base.Title, base.Status = KindBrowser, "浏览器启动已复核", "completed"
	case events.BrowserRuntimeCheckpointRecordedEvent:
		base.Kind, base.Title, base.Status = KindBrowser, "浏览器运行时检查点已记录", "running"
	case events.BrowserRuntimeReceiptRecordedEvent:
		base.Kind, base.Title, base.Status = KindBrowser, "浏览器运行时收据已记录", "completed"
	case events.ChildTaskProposedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "子任务提案已记录", "pending"
		base.Detail = childTaskDetail(event.PayloadJSON)
	case events.ChildTaskReviewedEvent:
		base.Kind, base.Title = KindPlan, "子任务提案已审阅"
		base.Status = cleanStatus(stringField(event.PayloadJSON, "status"))
		base.Detail = childTaskDetail(event.PayloadJSON)
	case events.ChildTaskAdmittedEvent:
		base.Kind, base.Title, base.Status = KindPlan, "子任务已准入", "completed"
		base.Detail = childTaskDetail(event.PayloadJSON)
	default:
		return Item{}, false
	}
	base.Title = cleanLabel(base.Title)
	base.Detail = cleanDetail(base.Detail)
	base.Status = cleanStatus(base.Status)
	return base, base.Title != ""
}

func checkpointOperationName(payloadJSON string) string {
	// Mutation journals also use checkpoint transaction events. Their failure
	// describes the operation, not necessarily a restore or a failed capture.
	switch stringField(payloadJSON, "kind") {
	case "command_batch":
		return "命令操作"
	case "file_tool":
		return "文件操作"
	case "undo":
		return "工作区撤销"
	case "rewind":
		return "工作区回退"
	case "redo":
		return "工作区重做"
	case "fork":
		return "检查点分支创建"
	default:
		return "检查点操作"
	}
}

// projectDockerEvent deliberately treats the event type and a small set of
// closed-enum/count fields as the whole public contract. In particular, it
// never projects container identities, host paths, commands, log bodies, or
// operation/request/lease/owner fingerprints even if such fields are injected
// into an otherwise recognised event payload.
func projectDockerEvent(event events.Event, base Item) (Item, bool) {
	switch event.Type {
	case events.SandboxDockerLifecyclePreparedEvent:
		base.Title, base.Status = "Docker 容器准备中", "preparing"
	case events.SandboxDockerLifecycleAcquiredEvent:
		base.Title, base.Status = "Docker 容器执行权已获取", "preparing"
	case events.SandboxDockerLifecycleTakenOverEvent:
		base.Title, base.Status = "Docker 容器恢复处理中", "preparing"
	case events.SandboxDockerLifecycleActionPreparedEvent:
		switch stringField(event.PayloadJSON, "verb") {
		case "create":
			base.Title, base.Status = "正在创建 Docker 容器", "starting"
		case "start":
			base.Title, base.Status = "正在启动 Docker 容器", "starting"
		case "term", "kill":
			base.Title, base.Status = "正在停止 Docker 容器", "cancelling"
		case "delete":
			base.Title, base.Status = "正在清理 Docker 容器", "cleaning"
		default:
			return Item{}, false
		}
	case events.SandboxDockerLifecycleTransitionEvent:
		var ok bool
		base.Title, base.Status, ok = dockerLifecycleTransitionPresentation(
			stringField(event.PayloadJSON, "state"),
			stringField(event.PayloadJSON, "reason_code"))
		if !ok {
			return Item{}, false
		}
		base.Detail = dockerLifecycleReasonDetail(stringField(event.PayloadJSON, "reason_code"))
	case events.SandboxDockerLifecycleCompletedEvent:
		var ok bool
		base.Title, base.Status, ok = dockerLifecycleOutcomePresentation(
			stringField(event.PayloadJSON, "outcome"))
		if !ok {
			return Item{}, false
		}

	case events.SandboxDockerLogCapturePreparedEvent:
		base.Title, base.Status = "准备收集 Docker 日志", "preparing"
	case events.SandboxDockerLogCaptureAcquiredEvent:
		base.Title, base.Status = "正在收集 Docker 日志", "running"
	case events.SandboxDockerLogCaptureTakenOverEvent:
		base.Title, base.Status = "正在恢复 Docker 日志收集", "running"
	case events.SandboxDockerLogCaptureFailedEvent:
		base.Title, base.Status = "Docker 日志收集失败", "failed"
	case events.SandboxDockerLogCaptureCompletedEvent:
		status := stringField(event.PayloadJSON, "status")
		switch status {
		case "completed":
			base.Title, base.Status = "Docker 日志已收集", "completed"
		case "truncated_bytes", "truncated_lines", "truncated_deadline":
			base.Title, base.Status = "Docker 日志已按上限收集", "completed"
		case "invalid_stream":
			base.Title, base.Status = "Docker 日志收集失败", "failed"
		default:
			return Item{}, false
		}
		base.Detail = dockerLogCountDetail(event.PayloadJSON)

	case events.SandboxDockerOutputStagingPreparedEvent:
		base.Title, base.Status = "准备暂存 Docker 输出", "preparing"
	case events.SandboxDockerOutputStagingAcquiredEvent:
		base.Title, base.Status = "正在暂存 Docker 输出", "running"
	case events.SandboxDockerOutputStagingTakenOverEvent:
		base.Title, base.Status = "正在恢复 Docker 输出暂存", "running"
	case events.SandboxDockerOutputStagingFailedEvent:
		base.Title, base.Status = "Docker 输出暂存失败", "failed"
	case events.SandboxDockerOutputStagingCompletedEvent:
		switch stringField(event.PayloadJSON, "status") {
		case "completed":
			base.Title, base.Status = "Docker 输出已暂存", "completed"
		case "truncated_bytes":
			base.Title, base.Status = "Docker 输出已按上限暂存", "completed"
		case "invalid_archive", "rejected_path", "rejected_link", "rejected_duplicate":
			base.Title, base.Status = "Docker 输出暂存失败", "failed"
		default:
			return Item{}, false
		}
		base.Detail = dockerOutputCountDetail(event.PayloadJSON)

	case events.SandboxDockerOutputCommitPreparedEvent:
		base.Title, base.Status = "准备提交 Docker 产物", "preparing"
	case events.SandboxDockerOutputCommitFailedEvent:
		base.Title, base.Status = "Docker 产物提交失败", "failed"
	case events.SandboxDockerOutputCommitCompletedEvent:
		if stringField(event.PayloadJSON, "status") != "committed" {
			return Item{}, false
		}
		base.Title, base.Status = "Docker 产物已提交", "completed"
		base.Detail = dockerArtifactCountDetail(event.PayloadJSON)

	case events.SandboxDockerProductAdmittedEvent:
		if stringField(event.PayloadJSON, "decision") != "authorized" ||
			stringField(event.PayloadJSON, "reason_code") != "ready" ||
			stringField(event.PayloadJSON, "remediation_code") != "none" ||
			stringField(event.PayloadJSON, "network_mode") != "disabled" ||
			!boolFieldIs(event.PayloadJSON, "product_entry_enabled", true) ||
			!boolFieldIs(event.PayloadJSON, "execution_authorized", true) ||
			!boolFieldIs(event.PayloadJSON, "artifact_commit_authorized", true) {
			return Item{}, false
		}
		base.Title, base.Status = "Docker 沙箱准入已通过", "preparing"
		base.Detail = "网络已关闭，资源预算已锁定"
	case events.SandboxDockerProductAdmissionDeniedEvent:
		if stringField(event.PayloadJSON, "decision") != "denied" ||
			stringField(event.PayloadJSON, "network_mode") != "disabled" ||
			!boolFieldIs(event.PayloadJSON, "product_entry_enabled", false) ||
			!boolFieldIs(event.PayloadJSON, "execution_authorized", false) ||
			!boolFieldIs(event.PayloadJSON, "artifact_commit_authorized", false) {
			return Item{}, false
		}
		reason := stringField(event.PayloadJSON, "reason_code")
		remediation := stringField(event.PayloadJSON, "remediation_code")
		if !dockerProductDenialReason(reason) || remediation == "" || remediation == "none" {
			return Item{}, false
		}
		base.Title, base.Status = "Docker 沙箱准入已拒绝", "blocked"
		base.Detail = dockerProductDenialDetail(reason)
	case events.SandboxDockerProductLaunchBoundEvent:
		if stringField(event.PayloadJSON, "status") != "bound" {
			return Item{}, false
		}
		base.Title, base.Status = "Docker 沙箱启动已绑定", "starting"
	case events.SandboxDockerProductCancelRequestedEvent:
		if stringField(event.PayloadJSON, "status") != "requested" ||
			stringField(event.PayloadJSON, "reason_code") != "cancelled" {
			return Item{}, false
		}
		base.Title, base.Status = "Docker 沙箱取消已请求", "cancelling"
	case events.SandboxDockerProductCompletedEvent:
		cleanupComplete, ok := boolField(event.PayloadJSON, "cleanup_complete")
		if !ok || !boolFieldIs(event.PayloadJSON, "artifact_commit_authorized", true) {
			return Item{}, false
		}
		var presentationOK bool
		base.Title, base.Status, presentationOK = dockerProductOutcomePresentation(
			stringField(event.PayloadJSON, "outcome"), cleanupComplete)
		if !presentationOK {
			return Item{}, false
		}
		base.Detail = dockerProductArtifactDetail(event.PayloadJSON)
	default:
		return Item{}, false
	}
	base.Title = cleanLabel(base.Title)
	base.Detail = cleanDetail(base.Detail)
	base.Status = cleanStatus(base.Status)
	return base, base.Title != "" && base.Status != ""
}

func dockerLifecycleTransitionPresentation(state, reason string) (string, string, bool) {
	switch state {
	case "created":
		if reason != "created" && reason != "restart_recovery" {
			return "", "", false
		}
		return "Docker 容器已创建", "starting", true
	case "started":
		if reason != "started" && reason != "restart_recovery" {
			return "", "", false
		}
		return "Docker 容器运行中", "running", true
	case "exited":
		switch reason {
		case "timeout":
			return "Docker 容器运行超时", "failed", true
		case "cancelled":
			return "Docker 容器已停止", "cancelling", true
		case "natural_exit", "restart_recovery":
			return "Docker 进程已退出", "cleaning", true
		default:
			return "", "", false
		}
	case "cleaning":
		switch reason {
		case "natural_exit", "timeout", "cancelled", "restart_recovery", "cleanup_started":
		default:
			return "", "", false
		}
		return "Docker 容器清理中", "cleaning", true
	case "cleaned":
		if reason != "cleanup_completed" && reason != "restart_recovery" {
			return "", "", false
		}
		return "Docker 容器已清理", "cleaned", true
	case "failed":
		switch reason {
		case "create_failed", "start_failed", "wait_failed", "terminate_failed",
			"cleanup_failed", "transport_disabled", "transport_unsupported",
			"connection_failed", "invalid_response", "configuration_mismatch",
			"unsafe_existing_container":
		default:
			return "", "", false
		}
		return "Docker 容器操作失败", "failed", true
	default:
		return "", "", false
	}
}

func dockerLifecycleOutcomePresentation(outcome string) (string, string, bool) {
	switch outcome {
	case "natural_exit":
		return "Docker 容器生命周期已完成", "cleaned", true
	case "timed_out":
		return "Docker 容器超时后已清理", "cleaned", true
	case "cancelled":
		return "Docker 容器取消后已清理", "cleaned", true
	case "failed":
		return "Docker 容器失败后已清理", "failed", true
	default:
		return "", "", false
	}
}

func dockerProductOutcomePresentation(outcome string, cleanupComplete bool) (string, string, bool) {
	if !cleanupComplete {
		return "", "", false
	}
	switch outcome {
	case "succeeded":
		return "Docker 沙箱运行已完成并清理", "cleaned", true
	case "timed_out":
		return "Docker 沙箱运行超时并已清理", "cleaned", true
	case "cancelled":
		return "Docker 沙箱已取消并清理", "cleaned", true
	case "failed":
		return "Docker 沙箱运行失败并已清理", "failed", true
	default:
		return "", "", false
	}
}

func dockerLifecycleReasonDetail(reason string) string {
	switch reason {
	case "natural_exit":
		return "进程自然退出"
	case "timeout":
		return "已达到运行时限"
	case "cancelled":
		return "已响应取消请求"
	case "restart_recovery":
		return "重启后恢复清理"
	case "cleanup_started":
		return "已开始清理"
	case "cleanup_completed":
		return "清理已确认"
	case "create_failed":
		return "容器创建失败"
	case "start_failed":
		return "容器启动失败"
	case "wait_failed":
		return "容器状态等待失败"
	case "terminate_failed":
		return "容器停止失败"
	case "cleanup_failed":
		return "容器清理失败"
	case "transport_disabled":
		return "Docker 传输未启用"
	case "transport_unsupported":
		return "Docker 传输不受支持"
	case "connection_failed":
		return "Docker 连接失败"
	case "invalid_response":
		return "Docker 返回无效响应"
	case "configuration_mismatch":
		return "容器配置校验失败"
	case "unsafe_existing_container":
		return "发现不属于本次运行的容器"
	default:
		return ""
	}
}

func dockerProductDenialReason(reason string) bool {
	switch reason {
	case "feature_disabled", "daemon_unreachable", "api_unsupported",
		"platform_unsupported", "resource_unavailable",
		"managed_egress_unavailable", "policy_denied", "approval_required",
		"permission_denied", "budget_exhausted", "authority_changed":
		return true
	default:
		return false
	}
}

func dockerProductDenialDetail(reason string) string {
	switch reason {
	case "feature_disabled":
		return "当前进程未启用 Docker 执行能力"
	case "daemon_unreachable":
		return "本机 Docker daemon 不可达"
	case "api_unsupported":
		return "Docker API 版本不受支持"
	case "platform_unsupported":
		return "需要本机 Linux 容器运行时"
	case "resource_unavailable":
		return "Docker 资源容量不足"
	case "managed_egress_unavailable":
		return "托管网络出口尚不可用，请使用 network=none"
	case "policy_denied":
		return "当前策略拒绝该请求"
	case "approval_required":
		return "需要针对该请求的一次性审批"
	case "permission_denied":
		return "当前 Run 权限不允许 Docker 执行"
	case "budget_exhausted":
		return "Run 预算不足"
	case "authority_changed":
		return "准入依据已变化，请重新评估"
	default:
		return ""
	}
}

func dockerLogCountDetail(payloadJSON string) string {
	streams, streamsOK := boundedIntField(payloadJSON, "stream_count", 0, 2)
	bytes, bytesOK := boundedIntField(payloadJSON, "total_bytes", 0, 2*256*1024)
	lines, linesOK := boundedIntField(payloadJSON, "total_lines", 0, 2*4096)
	if !streamsOK || !bytesOK || !linesOK {
		return ""
	}
	return fmt.Sprintf("%d 个日志流 · %d bytes · %d 行", streams, bytes, lines)
}

func dockerOutputCountDetail(payloadJSON string) string {
	files, filesOK := boundedIntField(payloadJSON, "file_count", 0, 64)
	bytes, bytesOK := boundedIntField(payloadJSON, "total_bytes", 0, 16*1024*1024)
	redacted, redactedOK := boundedIntField(payloadJSON, "redacted_count", 0, 64)
	if !filesOK || !bytesOK || !redactedOK || redacted > files {
		return ""
	}
	detail := fmt.Sprintf("%d 个文件 · %d bytes", files, bytes)
	if redacted > 0 {
		detail += fmt.Sprintf(" · %d 个文件已脱敏", redacted)
	}
	return detail
}

func dockerArtifactCountDetail(payloadJSON string) string {
	count, countOK := boundedIntField(payloadJSON, "committed_count", 1, 64)
	bytes, bytesOK := boundedIntField(payloadJSON, "total_bytes", 1, 16*1024*1024)
	if !countOK || !bytesOK {
		return ""
	}
	return fmt.Sprintf("%d 个产物 · %d bytes", count, bytes)
}

func dockerProductArtifactDetail(payloadJSON string) string {
	count, countOK := boundedIntField(payloadJSON, "artifact_count", 0, 64)
	authorized, authorizedOK := boolField(payloadJSON, "artifact_commit_authorized")
	if !countOK || !authorizedOK || count > 0 && !authorized {
		return ""
	}
	if count == 0 {
		return "未提交产物"
	}
	return fmt.Sprintf("已提交 %d 个产物", count)
}

func boundedIntField(payloadJSON, key string, minimum, maximum int64) (int64, bool) {
	raw := rawField(payloadJSON, key)
	if len(raw) == 0 {
		return 0, false
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < minimum || value > maximum {
		return 0, false
	}
	return value, true
}

func boolField(payloadJSON, key string) (bool, bool) {
	raw := rawField(payloadJSON, key)
	if len(raw) == 0 {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func boolFieldIs(payloadJSON, key string, expected bool) bool {
	value, ok := boolField(payloadJSON, key)
	return ok && value == expected
}

func projectMessage(event events.Event, base Item) (Item, bool) {
	var payload struct {
		Role                  string `json:"role"`
		Content               string `json:"content"`
		SourceKind            string `json:"source_kind"`
		InstructionAuthorized bool   `json:"instruction_authorized"`
	}
	if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil {
		return Item{}, false
	}
	switch payload.Role {
	case "assistant":
		if payload.SourceKind != session.SourceModelResponse {
			return Item{}, false
		}
		base.Kind, base.Source, base.Title = KindModelUpdate, SourceModel, "Universal Code 更新"
		base.Verifiable = false
	case "user":
		if payload.SourceKind != session.SourceOperatorMessage {
			return Item{}, false
		}
		base.Kind, base.Source, base.Title = KindOperatorInput, SourceOperator, "用户消息"
		base.Verifiable = true
	default:
		return Item{}, false
	}
	base.Detail = cleanDetail(payload.Content)
	base.InstructionAuthorized = payload.InstructionAuthorized
	return base, base.Detail != ""
}

func modelIdentity(payloadJSON string) string {
	provider := cleanLabel(stringField(payloadJSON, "provider"))
	model := cleanLabel(stringField(payloadJSON, "model"))
	switch {
	case provider != "" && model != "":
		return provider + " / " + model
	case model != "":
		return model
	default:
		return provider
	}
}

func completedModelDetail(payloadJSON string, identity string) string {
	elapsed := intField(payloadJSON, "elapsed_millis")
	var usage struct {
		TotalTokens int `json:"total_tokens"`
	}
	raw := rawField(payloadJSON, "usage")
	_ = json.Unmarshal(raw, &usage)
	parts := make([]string, 0, 3)
	if identity != "" {
		parts = append(parts, identity)
	}
	if usage.TotalTokens > 0 {
		parts = append(parts, strconv.Itoa(usage.TotalTokens)+" tokens")
	}
	if elapsed > 0 {
		parts = append(parts, strconv.FormatInt(elapsed, 10)+" ms")
	}
	return strings.Join(parts, " · ")
}

func toolList(payloadJSON string) string {
	var names []string
	_ = json.Unmarshal(rawField(payloadJSON, "tools"), &names)
	if len(names) > maxToolNames {
		names = names[:maxToolNames]
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name = cleanLabel(name); name != "" {
			out = append(out, toolDisplayName(name))
		}
	}
	return strings.Join(out, "、")
}

func toolNameFromPayload(payloadJSON string) string {
	for _, key := range []string{"tool_name", "tool"} {
		if name := cleanLabel(stringField(payloadJSON, key)); name != "" {
			return toolDisplayName(name)
		}
	}
	return ""
}

func toolDisplayName(name string) string {
	switch name {
	case "read_file":
		return "读取文件"
	case "list_workspace":
		return "浏览工作区"
	case "replace_file":
		return "文件修改提案"
	case "work_item_create":
		return "创建工作项"
	case "note_create":
		return "记录记忆"
	case "plan_delivery_propose":
		return "生成交付计划"
	case "specialist_delegation_propose":
		return "提出子 Agent 委派"
	case "controlled_command_propose":
		return "提出受控命令"
	default:
		return name
	}
}

func stringField(payloadJSON string, key string) string {
	var value string
	_ = json.Unmarshal(rawField(payloadJSON, key), &value)
	return value
}

func intField(payloadJSON string, key string) int64 {
	var value int64
	_ = json.Unmarshal(rawField(payloadJSON, key), &value)
	return value
}

func rawField(payloadJSON string, key string) json.RawMessage {
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil {
		return nil
	}
	return payload[key]
}

// dependencyDetail renders a bounded source→target identity and reason
// only; raw child output never enters the dependency projection.
func dependencyDetail(payloadJSON string) string {
	source := cleanLabel(stringField(payloadJSON, "source_id"))
	target := cleanLabel(stringField(payloadJSON, "target_id"))
	reason := cleanLabel(stringField(payloadJSON, "reason"))
	parts := make([]string, 0, 3)
	if source != "" && target != "" {
		parts = append(parts, source+" → "+target)
	}
	if reason != "" {
		parts = append(parts, reason)
	}
	return strings.Join(parts, " · ")
}

// childTaskDetail bounds the child task projection to surface, tier, and
// task count. Titles, goals, and artifact hints never enter Activity.
func childTaskDetail(payloadJSON string) string {
	surface := cleanLabel(stringField(payloadJSON, "surface"))
	tier := cleanLabel(stringField(payloadJSON, "fanout_tier"))
	count, ok := boundedIntField(payloadJSON, "task_count", 1, 6)
	parts := make([]string, 0, 3)
	if surface != "" {
		parts = append(parts, surface)
	}
	if tier != "" {
		parts = append(parts, "档位 "+tier)
	}
	if ok {
		parts = append(parts, fmt.Sprintf("%d 个子任务", count))
	}
	return strings.Join(parts, " · ")
}

// dependencyStallDetail bounds the deadlock/livelock diagnosis to the
// affected edge count or polling wake count.
func dependencyStallDetail(payloadJSON string) string {
	if count, ok := boundedIntField(payloadJSON, "edge_count", 1, 1024); ok {
		return fmt.Sprintf("%d 个等待未能取得进展", count)
	}
	if count, ok := boundedIntField(payloadJSON, "wake_count", 1, 4096); ok {
		return fmt.Sprintf("同一依赖已被唤醒 %d 次", count)
	}
	return ""
}

func cleanLabel(value string) string {
	value = strings.TrimSpace(redact.String(value))
	if value == "" || !utf8.ValidString(value) {
		return ""
	}
	value = strings.Map(func(current rune) rune {
		if unicode.IsControl(current) {
			return -1
		}
		return current
	}, value)
	runes := []rune(value)
	if len(runes) > maxLabelRunes {
		runes = runes[:maxLabelRunes]
	}
	return strings.TrimSpace(string(runes))
}

func cleanDetail(value string) string {
	value = strings.TrimSpace(redact.String(value))
	if value == "" || !utf8.ValidString(value) {
		return ""
	}
	value = strings.Map(func(current rune) rune {
		if current == '\n' || current == '\t' {
			return current
		}
		if unicode.IsControl(current) {
			return '�'
		}
		return current
	}, value)
	runes := []rune(value)
	if len(runes) > MaxDetailRunes {
		runes = append(runes[:MaxDetailRunes-1], '…')
	}
	return strings.TrimSpace(string(runes))
}

func cleanStatus(value string) string {
	value = cleanLabel(value)
	switch value {
	case "approved", "blocked", "cancelled", "cancelling", "completed", "denied",
		"expired", "failed", "pending", "preparing", "running", "satisfied", "selected",
		"starting", "superseded", "waiting", "cleaning", "cleaned":
		return value
	default:
		return ""
	}
}
