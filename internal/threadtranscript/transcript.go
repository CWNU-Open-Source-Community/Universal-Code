package threadtranscript

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/webevidence"
)

const (
	ProtocolVersion  = "thread_transcript.v1"
	MaxSourceRecords = 101
	maxIdentityRunes = 256
)

type ActivityType string

const (
	TypeMessage    ActivityType = "message"
	TypeSearch     ActivityType = "search"
	TypeRead       ActivityType = "read"
	TypeEdit       ActivityType = "edit"
	TypeExecute    ActivityType = "execute"
	TypeVerify     ActivityType = "verify"
	TypeApproval   ActivityType = "approval"
	TypeCheckpoint ActivityType = "checkpoint"
	TypeDelivery   ActivityType = "delivery"
)

type Stage string

const (
	StageStarted        Stage = "started"
	StageArgumentsReady Stage = "arguments_ready"
	StageRunning        Stage = "running"
	StageResult         Stage = "result"
	StageBlocked        Stage = "blocked"
)

// Source is the immutable ordering record loaded by the store. Sequence zero
// is reserved for the Run boundary; positive values are durable Run events.
type Source struct {
	RunID                   string
	SessionID               string
	Ordinal                 int64
	PredecessorRunID        string
	PredecessorRunStatus    string
	RunStatus               string
	OperatorContent         string
	OperatorImageCount      int
	OperatorAttachmentCount int
	OperatorStatus          string
	OperatorDeliveryMode    string
	PromotedToMessageID     string
	PromotedFromMessageID   string
	// Store-verified queue/Session identity, independent of the current page.
	OperatorMessageBound bool
	Sequence             int64
	CreatedAt            time.Time
	Event                *events.Event
}

type Item struct {
	Version               string
	ID                    string
	CanonicalID           string
	RunID                 string
	RunOrdinal            int64
	Sequence              int64
	Position              int
	Type                  ActivityType
	Stage                 Stage
	Kind                  runactivity.Kind
	Source                runactivity.Source
	Title                 string
	Detail                string
	Status                string
	Verifiable            bool
	InstructionAuthorized bool
	AttemptID             string
	ModelAttempt          int
	Model                 string
	ToolRound             int
	ToolName              string
	StreamResponseID      string
	StreamItemID          string
	StreamCallID          string
	DurableCallID         string
	SourceRef             string
	BoundaryReason        string
	DeliveryMode          string
	PromotedToMessageID   string
	PromotedFromMessageID string
	WebEvidence           *WebEvidencePresentation
	Provisional           bool
	Durable               bool
	CreatedAt             time.Time
}

type WebEvidencePresentation struct {
	Version               string
	SourceID              string
	SnapshotID            string
	CitationID            string
	URL                   string
	Title                 string
	State                 string
	FetchedAt             time.Time
	StaleAt               time.Time
	Digest                string
	Robots                string
	Partial               bool
	Stale                 bool
	Citeable              bool
	Untrusted             bool
	InstructionAuthorized bool
}

func (p WebEvidencePresentation) Validate() error {
	canonical, err := webevidence.CanonicalizePublicHTTPSURL(p.URL)
	if p.Version != "web_evidence_presentation.v1" || err != nil || canonical != p.URL ||
		!validWebEvidenceIdentity(p.SourceID, false) ||
		!validWebEvidenceIdentity(p.SnapshotID, false) ||
		!validWebEvidenceIdentity(p.CitationID, true) || !validWebEvidenceTitle(p.Title) ||
		p.FetchedAt.IsZero() || p.StaleAt.Before(p.FetchedAt) ||
		!validWebEvidenceDigest(p.Digest) || !validWebEvidenceRobots(p.Robots) ||
		!p.Untrusted || p.InstructionAuthorized {
		return errors.New("web evidence transcript presentation is invalid")
	}
	switch p.State {
	case "fetched":
		if p.Partial || p.Stale || !p.Citeable {
			return errors.New("web evidence fetched state is inconsistent")
		}
	case "partial":
		if !p.Partial || p.Stale || !p.Citeable {
			return errors.New("web evidence partial state is inconsistent")
		}
	case "stale":
		if !p.Stale || !p.Citeable {
			return errors.New("web evidence stale state is inconsistent")
		}
	case "blocked", "failed":
		if p.Partial || p.Stale || p.Citeable {
			return errors.New("web evidence failure state is inconsistent")
		}
	default:
		return errors.New("web evidence transcript state is invalid")
	}
	return nil
}

func validWebEvidenceRobots(value string) bool {
	switch value {
	case "", "allowed", "blocked", "unknown", "not_checked", "not_present",
		"bypassed_disallow", "bypassed_unknown":
		return true
	default:
		return false
	}
}

func validWebEvidenceIdentity(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	if safeIdentity(value) != value {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validWebEvidenceTitle(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 1024 {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validWebEvidenceDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func Build(threadID string, source []Source) ([]Item, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || utf8.RuneCountInString(threadID) > maxIdentityRunes {
		return nil, errors.New("thread transcript requires a bounded Thread id")
	}
	if len(source) > MaxSourceRecords {
		return nil, fmt.Errorf("thread transcript accepts at most %d source records", MaxSourceRecords)
	}
	ordered := append([]Source(nil), source...)
	// The run's latest model-call attempt, so a durable reply projected from a
	// SessionMessageEvent can inherit the model identity of the call it closed.
	attemptByRun := map[string]string{}
	slices.SortFunc(ordered, func(left, right Source) int {
		if order := cmp.Compare(left.Ordinal, right.Ordinal); order != 0 {
			return order
		}
		return cmp.Compare(left.Sequence, right.Sequence)
	})
	items := make([]Item, 0, len(ordered))
	var previousOrdinal, previousSequence int64
	for index, record := range ordered {
		if err := validateSource(record); err != nil {
			return nil, err
		}
		if index > 0 && (record.Ordinal < previousOrdinal ||
			(record.Ordinal == previousOrdinal && record.Sequence <= previousSequence)) {
			return nil, errors.New("thread transcript source ordering is inconsistent")
		}
		previousOrdinal, previousSequence = record.Ordinal, record.Sequence
		if record.Sequence == 0 {
			items = append(items, projectRunBoundary(record))
			continue
		}
		if record.Event.Type == "thread.peer_message.sent" || record.Event.Type == "thread.peer_message.received" {
			items = append(items, Item{Version: ProtocolVersion, ID: record.Event.EventID, CanonicalID: record.Event.SubjectID, RunID: record.RunID, RunOrdinal: record.Ordinal, Sequence: record.Sequence, Type: TypeMessage, Stage: StageStarted, Kind: "peer_message", Source: "peer", Title: "聊天消息", SourceRef: record.Event.SubjectID, Durable: true, Verifiable: true, CreatedAt: record.CreatedAt})
			continue
		}
		if record.Event.Type == events.OperatorSteeringQueuedEvent &&
			(record.OperatorStatus == "pending" || record.OperatorStatus == "cancelled" ||
				(record.OperatorStatus == "committed" && record.OperatorMessageBound)) &&
			(strings.TrimSpace(record.OperatorContent) != "" || record.OperatorImageCount > 0 || record.OperatorAttachmentCount > 0) {
			items = append(items, projectOperatorMessage(record))
			continue
		}
		if record.Event.Type == events.SessionMessageEvent && record.OperatorMessageBound {
			// The same input is already represented at its original queue sequence.
			// Never infer this from equal text or from another item on this page.
			continue
		}
		if record.Event.Type == events.SupervisorToolBatchEvent {
			items = append(items, projectToolBatch(record)...)
			continue
		}
		projection, err := runactivity.Build(record.RunID, []events.Event{*record.Event}, false)
		if err != nil {
			return nil, err
		}
		for _, projected := range projection.Items {
			item := projectActivity(record, projected)
			// The final durable reply is a SessionMessageEvent without attempt
			// identity; inherit the run's latest model-call attempt so the web
			// narrative can attribute the reply to its producing model.
			if projected.AttemptID != "" && projected.Kind == runactivity.KindModelCall {
				attemptByRun[record.RunID] = projected.AttemptID
			}
			if item.AttemptID == "" && item.Kind == runactivity.KindModelUpdate &&
				item.Source == runactivity.SourceModel {
				item.AttemptID = attemptByRun[record.RunID]
			}
			items = append(items, item)
		}
	}
	return items, nil
}

func projectOperatorMessage(source Source) Item {
	detail := strings.TrimSpace(redact.String(source.OperatorContent))
	if utf8.RuneCountInString(detail) > runactivity.MaxDetailRunes {
		runes := []rune(detail)
		detail = string(runes[:runactivity.MaxDetailRunes-1]) + "…"
	}
	title := "用户消息已排队"
	stage := StageStarted
	instructionAuthorized := true
	if source.OperatorStatus == "cancelled" {
		title = "用户消息已取消"
		if source.PromotedToMessageID != "" {
			title = "用户消息已转为当前任务纠正"
		}
		stage = StageBlocked
		instructionAuthorized = false
	} else if source.OperatorStatus == "committed" {
		title = "用户消息"
		stage = StageResult
	}
	return Item{
		Version: ProtocolVersion, ID: source.Event.EventID,
		CanonicalID: source.Event.SubjectID, RunID: source.RunID,
		RunOrdinal: source.Ordinal, Sequence: source.Sequence, Type: TypeMessage,
		Stage: stage, Kind: runactivity.KindOperatorInput,
		Source: runactivity.SourceOperator, Title: title, Detail: detail,
		Status: source.OperatorStatus, Verifiable: true,
		InstructionAuthorized: instructionAuthorized,
		DeliveryMode:          source.OperatorDeliveryMode,
		PromotedToMessageID:   source.PromotedToMessageID, PromotedFromMessageID: source.PromotedFromMessageID,
		SourceRef: source.Event.SubjectID, Durable: true, CreatedAt: source.CreatedAt,
	}
}

func validateSource(source Source) error {
	if strings.TrimSpace(source.RunID) == "" || source.Ordinal <= 0 || source.Sequence < 0 ||
		source.CreatedAt.IsZero() {
		return errors.New("thread transcript source identity is invalid")
	}
	if source.Sequence == 0 {
		if source.Event != nil {
			return errors.New("thread transcript Run boundary contains an event")
		}
		return nil
	}
	if source.Event == nil || source.Event.RunID != source.RunID ||
		source.Event.Sequence != source.Sequence || source.Event.CreatedAt.IsZero() {
		return errors.New("thread transcript event source is inconsistent")
	}
	return nil
}

func projectRunBoundary(source Source) Item {
	detail := "初始 Run"
	reason := "initial"
	if source.PredecessorRunID != "" {
		detail = "从 Run " + source.PredecessorRunID + " 继续"
		reason = "predecessor_terminal"
		if source.PredecessorRunStatus != "" {
			detail += "；前一 Run 状态：" + source.PredecessorRunStatus
			reason += "_" + source.PredecessorRunStatus
		}
	}
	return Item{
		Version: ProtocolVersion, ID: "run-boundary:" + source.RunID,
		CanonicalID: "run:" + source.RunID, RunID: source.RunID,
		RunOrdinal: source.Ordinal, Type: TypeCheckpoint, Stage: stageForStatus(source.RunStatus),
		Kind: runactivity.KindHarnessStatus, Source: runactivity.SourceHarness,
		Title: "Run " + strconv.FormatInt(source.Ordinal, 10), Detail: detail,
		Status: source.RunStatus, BoundaryReason: reason, Verifiable: true,
		Durable: true, CreatedAt: source.CreatedAt,
	}
}

type toolBatchPayload struct {
	Tools            []string `json:"tools"`
	StreamResponseID string   `json:"stream_response_id"`
	StreamItemIDs    []string `json:"stream_item_ids"`
	StreamCallIDs    []string `json:"stream_call_ids"`
	DurableCallIDs   []string `json:"durable_call_ids"`
}

func projectToolBatch(source Source) []Item {
	var payload toolBatchPayload
	if json.Unmarshal([]byte(source.Event.PayloadJSON), &payload) != nil {
		return nil
	}
	count := len(payload.Tools)
	if len(payload.StreamItemIDs) < count {
		count = len(payload.StreamItemIDs)
	}
	if len(payload.StreamCallIDs) < count {
		count = len(payload.StreamCallIDs)
	}
	if count == 0 {
		return nil
	}
	items := make([]Item, 0, count)
	for index := 0; index < count; index++ {
		toolName := safeIdentity(payload.Tools[index])
		streamItemID := safeIdentity(payload.StreamItemIDs[index])
		streamCallID := safeIdentity(payload.StreamCallIDs[index])
		durableCallID := ""
		if index < len(payload.DurableCallIDs) {
			durableCallID = safeIdentity(payload.DurableCallIDs[index])
		}
		if toolName == "" || streamItemID == "" || streamCallID == "" {
			continue
		}
		items = append(items, Item{
			Version: ProtocolVersion, ID: source.Event.EventID + ":" + strconv.Itoa(index+1),
			CanonicalID: streamItemID, RunID: source.RunID, RunOrdinal: source.Ordinal,
			Sequence: source.Sequence, Position: index + 1, Type: classifyTool(toolName),
			Stage: StageArgumentsReady, Kind: runactivity.KindToolCall,
			Source: runactivity.SourceHarness, Title: "工具参数已就绪", Status: "pending",
			Verifiable: true, ToolName: toolName,
			StreamResponseID: safeIdentity(payload.StreamResponseID), StreamItemID: streamItemID,
			StreamCallID: streamCallID, DurableCallID: durableCallID,
			Durable: true, CreatedAt: source.CreatedAt,
		})
	}
	return items
}

func projectActivity(source Source, projected runactivity.Item) Item {
	item := Item{
		Version: ProtocolVersion, ID: projected.ID, RunID: source.RunID,
		RunOrdinal: source.Ordinal, Sequence: source.Sequence,
		Type:  classifyActivity(source.Event.Type, projected.Kind, ""),
		Stage: stageForEvent(source.Event.Type, projected.Status), Kind: projected.Kind,
		Source: projected.Source, Title: projected.Title, Detail: projected.Detail,
		Status: projected.Status, Verifiable: projected.Verifiable,
		InstructionAuthorized: projected.InstructionAuthorized,
		AttemptID:             projected.AttemptID, ModelAttempt: projected.ModelAttempt, Model: projected.Model,
		ToolRound: projected.ToolRound, Durable: true, CreatedAt: projected.CreatedAt,
	}
	if (source.Event.Type == events.ThreadTurnFailedEvent && source.Event.Source == "thread_turn") ||
		(source.Event.Type == events.RunExecutionHandoffCompletedEvent && source.Event.Source == "run_execution_handoff") {
		item.SourceRef = safeIdentity(source.Event.SubjectID)
	}
	if source.Event.Type == events.SessionMessageEvent {
		var payload struct {
			SourceRef string `json:"source_ref"`
		}
		_ = json.Unmarshal([]byte(source.Event.PayloadJSON), &payload)
		item.SourceRef = safeIdentity(payload.SourceRef)
	}
	if source.Event.Type == events.ModelPublicCommentaryEvent && source.Event.Source == "thread_branch" {
		var payload struct {
			SourceEventID string `json:"source_event_id"`
		}
		_ = json.Unmarshal([]byte(source.Event.PayloadJSON), &payload)
		if id := safeIdentity(payload.SourceEventID); id != "" {
			item.SourceRef = "branch-assistant:" + id
		}
	}
	if projected.Kind == runactivity.KindToolCall {
		var payload struct {
			Tool             string `json:"tool"`
			ToolName         string `json:"tool_name"`
			StreamResponseID string `json:"stream_response_id"`
			StreamItemID     string `json:"stream_item_id"`
			StreamCallID     string `json:"stream_call_id"`
			DurableCallID    string `json:"durable_call_id"`
			WebEvidence      *struct {
				Version               string    `json:"version"`
				SourceID              string    `json:"source_id"`
				SnapshotID            string    `json:"snapshot_id"`
				CitationID            string    `json:"citation_id"`
				URL                   string    `json:"url"`
				Title                 string    `json:"title"`
				State                 string    `json:"state"`
				FetchedAt             time.Time `json:"fetched_at"`
				StaleAt               time.Time `json:"stale_at"`
				Digest                string    `json:"digest"`
				Robots                string    `json:"robots"`
				Partial               bool      `json:"partial"`
				Stale                 bool      `json:"stale"`
				Citeable              bool      `json:"citeable"`
				Untrusted             bool      `json:"untrusted"`
				InstructionAuthorized bool      `json:"instruction_authorized"`
			} `json:"web_evidence"`
		}
		_ = json.Unmarshal([]byte(source.Event.PayloadJSON), &payload)
		item.ToolName = safeIdentity(payload.Tool)
		if item.ToolName == "" {
			item.ToolName = safeIdentity(payload.ToolName)
		}
		item.StreamResponseID = safeIdentity(payload.StreamResponseID)
		item.StreamItemID = safeIdentity(payload.StreamItemID)
		item.StreamCallID = safeIdentity(payload.StreamCallID)
		item.DurableCallID = safeIdentity(payload.DurableCallID)
		if payload.WebEvidence != nil {
			presentation := WebEvidencePresentation{
				Version: payload.WebEvidence.Version, SourceID: safeIdentity(payload.WebEvidence.SourceID),
				SnapshotID: safeIdentity(payload.WebEvidence.SnapshotID),
				CitationID: safeIdentity(payload.WebEvidence.CitationID), URL: payload.WebEvidence.URL,
				Title: payload.WebEvidence.Title, State: payload.WebEvidence.State,
				FetchedAt: payload.WebEvidence.FetchedAt, StaleAt: payload.WebEvidence.StaleAt,
				Digest: payload.WebEvidence.Digest, Robots: payload.WebEvidence.Robots,
				Partial: payload.WebEvidence.Partial,
				Stale:   payload.WebEvidence.Stale, Citeable: payload.WebEvidence.Citeable,
				Untrusted:             payload.WebEvidence.Untrusted,
				InstructionAuthorized: payload.WebEvidence.InstructionAuthorized,
			}
			if presentation.Validate() == nil {
				item.WebEvidence = &presentation
				projectWebEvidenceNarrative(&item, presentation)
			}
		}
		if item.StreamItemID != "" {
			item.CanonicalID = item.StreamItemID
		}
		item.Type = classifyActivity(source.Event.Type, projected.Kind, item.ToolName)
	}
	if item.CanonicalID == "" {
		item.CanonicalID = item.ID
	}
	return item
}

// projectWebEvidenceNarrative converts the durable tool-completion fact into
// the user-facing security outcome. A blocked or failed snapshot was persisted
// successfully, but that must never be described as a successful page fetch.
func projectWebEvidenceNarrative(item *Item, presentation WebEvidencePresentation) {
	if item == nil {
		return
	}
	item.Detail = "已记录网页证据状态"
	if presentation.Robots == "not_checked" &&
		(presentation.State == "fetched" || presentation.State == "partial") {
		item.Title = "网页已抓取（未检查 Robots）"
		item.Detail = "抓取结果可用，但未验证站点的 Robots 规则"
		item.Status = "robots_ignored"
		return
	}
	if presentation.Robots == "bypassed_disallow" &&
		(presentation.State == "fetched" || presentation.State == "partial") {
		item.Title = "Full Access 已忽略站点 Robots 限制"
		item.Detail = "站点禁止抓取；Full Access 仍继续创建了快照"
		item.Status = "robots_ignored"
		return
	}
	if presentation.Robots == "bypassed_unknown" &&
		(presentation.State == "fetched" || presentation.State == "partial") {
		item.Title = "Robots 无法验证，已按 Full Access 继续"
		item.Detail = "未能验证站点 Robots 规则；Full Access 仍继续创建了快照"
		item.Status = "robots_ignored"
		return
	}
	switch presentation.State {
	case "fetched":
		item.Title, item.Detail, item.Status = "网页已抓取", "已创建可引用的网页快照", "fetched"
	case "partial":
		item.Title, item.Detail, item.Status = "网页已部分抓取", "快照内容不完整，请谨慎引用", "partial"
	case "stale":
		item.Title, item.Detail, item.Status = "网页快照已过期", "现有快照可能不再反映当前页面", "stale"
	case "blocked":
		item.Title, item.Detail, item.Status = "网页抓取被阻止", "未获得可验证的网页内容", "blocked"
		if presentation.Robots == "blocked" {
			item.Title = "Robots 规则阻止抓取"
			item.Detail = "站点的 Robots 规则不允许本次抓取"
		}
	case "failed":
		item.Title, item.Detail, item.Status = "网页验证不可用", "未能创建可验证的网页快照", "verification_unavailable"
	}
}

func classifyActivity(eventType string, kind runactivity.Kind, toolName string) ActivityType {
	switch eventType {
	case events.VerificationEvidenceRecordedEvent, events.VerificationPlanRecordedEvent,
		events.VerificationPlanEvidenceAssociatedEvent, events.VerificationSnapshotReceiptRecordedEvent,
		events.VerificationSnapshotReviewRecordedEvent, events.BrowserRuntimeReceiptRecordedEvent:
		return TypeVerify
	case events.DeliveryCheckpointRecordedEvent, events.ArtifactCreatedEvent:
		return TypeDelivery
	case events.WorkspaceCheckpointCreatedEvent, events.WorkspaceCheckpointTransactionPreparedEvent,
		events.WorkspaceCheckpointTransactionCompletedEvent, events.WorkspaceCheckpointTransactionFailedEvent:
		return TypeCheckpoint
	}
	switch kind {
	case runactivity.KindModelUpdate, runactivity.KindOperatorInput:
		return TypeMessage
	case runactivity.KindApproval:
		return TypeApproval
	case runactivity.KindFileChange:
		return TypeEdit
	case runactivity.KindToolCall:
		return classifyTool(toolName)
	case runactivity.KindBrowser:
		return TypeExecute
	case runactivity.KindPlan:
		return TypeCheckpoint
	default:
		return TypeCheckpoint
	}
}

func classifyTool(name string) ActivityType {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "history_search":
		return TypeSearch
	case "history_read":
		return TypeRead
	case "list_workspace", "workspace_list", "workspace_glob", "workspace_grep",
		"workspace_search", "code_search", "search", "find_files",
		"github_review_evidence_list", "code_workspace_symbols", "code_document_symbols",
		"code_references", "code_implementation", "code_call_hierarchy", "code_type_hierarchy",
		"web_search":
		return TypeSearch
	case "read_file", "workspace_read", "note_get", "artifact_get",
		"github_review_evidence_read", "code_definition", "code_hover", "code_signature_help",
		"web_fetch", "web_citation":
		return TypeRead
	case "replace_file", "file_edit", "apply_patch", "workspace_restore",
		"workspace_change", "workspace_apply", "workspace_delete":
		return TypeEdit
	case "verification_record", "verification_plan", "ui_evidence", "run_tests", "code_diagnostics":
		return TypeVerify
	case "delivery_checkpoint", "artifact_create", "code_handoff", "plan_delivery_propose":
		return TypeDelivery
	case "work_item_create", "note_create", "specialist_delegation_propose",
		"child_task_propose", "controlled_command_propose", "host_command_propose",
		"one_shot_command_propose", "sandbox_docker_run_propose", "skill_candidate_propose":
		return TypeCheckpoint
	default:
		return TypeExecute
	}
}

func stageForEvent(eventType, status string) Stage {
	if stageForStatus(status) == StageBlocked {
		return StageBlocked
	}
	switch eventType {
	case events.SupervisorToolBatchEvent:
		return StageArgumentsReady
	case events.SupervisorToolExecutionStartedEvent, events.ToolStartedEvent:
		return StageRunning
	case events.ApprovalRequestedEvent, events.ControlledCommandProposedEvent,
		events.FileEditProposedEvent, events.ToolProposedEvent:
		return StageStarted
	case events.SupervisorToolExecutionCompletedEvent, events.SupervisorToolResultEvent,
		events.ToolCompletedEvent, events.SessionMessageEvent,
		events.DeliveryCheckpointRecordedEvent:
		return StageResult
	}
	return stageForStatus(status)
}

func stageForStatus(status string) Stage {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "blocked", "denied", "cancelled", "expired", "waiting_approval":
		return StageBlocked
	case "running", "preparing", "cancelling", "waiting":
		return StageRunning
	case "created", "paused", "pending", "proposed":
		return StageStarted
	default:
		return StageResult
	}
}

func safeIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxIdentityRunes {
		return ""
	}
	safe := redact.String(value)
	if safe != value {
		return "redacted"
	}
	return value
}

// Keep the item-stream dependency explicit: transcript identities and stages
// are the public projection of the provider-neutral protocol introduced by #152.
var _ = llm.ItemStreamProtocolVersion
