package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	AgentGraphProtocolVersion   = "agent_graph.v1"
	MaxAgentIdentityRunes       = 256
	MaxAgentNodesPerRun         = 3
	MaxAgentChildren            = 2
	MaxAgentDepth               = 1
	MaxAgentSkills              = 16
	MaxAgentSkillRunes          = 96
	MaxAgentStatusReasonRunes   = 4096
	MaxAgentInboxMessages       = 128
	MaxAgentMessageHistory      = 4096
	MaxAgentMessageBatch        = 32
	MaxAgentMessagePayloadBytes = 16 * 1024
	MinAgentOperationKeyBytes   = 16
	MaxAgentOperationKeyBytes   = 256
	MaxAgentGraphSnapshotBytes  = 128 * 1024
	MaxAgentGraphSnapshots      = 32
)

type AgentRole string

const (
	AgentRoleRoot       AgentRole = "root"
	AgentRoleSpecialist AgentRole = "specialist"
)

type AgentStatus string

const (
	AgentReady     AgentStatus = "ready"
	AgentRunning   AgentStatus = "running"
	AgentWaiting   AgentStatus = "waiting"
	AgentCompleted AgentStatus = "completed"
	AgentFailed    AgentStatus = "failed"
	AgentCancelled AgentStatus = "cancelled"
)

type AgentMessageKind string

const (
	AgentMessageControl      AgentMessageKind = "control"
	AgentMessageInstruction  AgentMessageKind = "instruction"
	AgentMessageResult       AgentMessageKind = "result"
	AgentMessageNotification AgentMessageKind = "notification"
)

type AgentMessageStatus string

const (
	AgentMessagePending  AgentMessageStatus = "pending"
	AgentMessageConsumed AgentMessageStatus = "consumed"
)

type AgentMessageSemantic string

const (
	AgentMessageSemanticMessage    AgentMessageSemantic = "message"
	AgentMessageSemanticWake       AgentMessageSemantic = "wake"
	AgentMessageSemanticDependency AgentMessageSemantic = "dependency"
)

type AgentDependencyState string

const (
	AgentDependencyWait      AgentDependencyState = "wait"
	AgentDependencySatisfied AgentDependencyState = "satisfied"
	AgentDependencyFailed    AgentDependencyState = "failed"
	AgentDependencyCancelled AgentDependencyState = "cancelled"
	AgentDependencyExpired   AgentDependencyState = "expired"
)

type AgentWakePayload struct {
	Reason string `json:"reason"`
}

type AgentDependencyPayload struct {
	DependencyID string               `json:"dependency_id"`
	State        AgentDependencyState `json:"state"`
	Reason       string               `json:"reason,omitempty"`
}

type AgentNode struct {
	ToolCallsUsed    int64    `json:"tool_calls_used"`
	CostUsedMicros   int64    `json:"cost_used_micros"`
	AllowedReadPaths []string `json:"allowed_read_paths,omitempty"`
	ToolCallLimit    int64    `json:"tool_call_limit,omitempty"`
	CostLimitMicros  int64    `json:"cost_limit_micros,omitempty"`
	ID               string
	RunID            string
	ParentID         string
	SessionID        string
	Role             AgentRole
	Profile          Profile
	Skills           []string
	Status           AgentStatus
	Depth            int
	ChildLimit       int
	TurnLimit        int64
	TokenLimit       int64
	TurnsUsed        int64
	TokensUsed       int64
	ActiveAttemptID  string
	StatusReason     string
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	FinishedAt       *time.Time
}

type AgentMessage struct {
	ID               string
	RunID            string
	SenderAgentID    string
	RecipientAgentID string
	Sequence         int64
	Kind             AgentMessageKind
	Semantic         AgentMessageSemantic
	PayloadJSON      string
	Status           AgentMessageStatus
	CreatedAt        time.Time
	ConsumedAt       *time.Time
}

type AgentGraphSnapshot struct {
	ID                  string
	RunID               string
	Version             int64
	ProtocolVersion     string
	RootAgentID         string
	NodeCount           int
	PendingMessageCount int
	StateJSON           string
	CreatedAt           time.Time
}

type AgentGraph struct {
	RunID           string
	RootAgentID     string
	Nodes           []AgentNode
	PendingMessages []AgentMessage
	LatestSnapshot  AgentGraphSnapshot
}

func ValidAgentRole(role AgentRole) bool {
	return role == AgentRoleRoot || role == AgentRoleSpecialist
}

func ValidAgentStatus(status AgentStatus) bool {
	switch status {
	case AgentReady, AgentRunning, AgentWaiting, AgentCompleted, AgentFailed, AgentCancelled:
		return true
	default:
		return false
	}
}

func ValidAgentMessageKind(kind AgentMessageKind) bool {
	switch kind {
	case AgentMessageControl, AgentMessageInstruction, AgentMessageResult, AgentMessageNotification:
		return true
	default:
		return false
	}
}

func ValidAgentMessageStatus(status AgentMessageStatus) bool {
	return status == AgentMessagePending || status == AgentMessageConsumed
}

func ValidAgentMessageSemantic(semantic AgentMessageSemantic) bool {
	switch semantic {
	case AgentMessageSemanticMessage, AgentMessageSemanticWake, AgentMessageSemanticDependency:
		return true
	default:
		return false
	}
}

func NormalizeAgentOperationKey(value string) (string, error) {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		len([]byte(value)) < MinAgentOperationKeyBytes || len([]byte(value)) > MaxAgentOperationKeyBytes ||
		strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("agent operation key must be normalized UTF-8 between %d and %d bytes",
			MinAgentOperationKeyBytes, MaxAgentOperationKeyBytes)
	}
	return value, nil
}

func (a AgentNode) Terminal() bool {
	return a.Status == AgentCompleted || a.Status == AgentFailed || a.Status == AgentCancelled
}

func (a AgentNode) CanTransition(to AgentStatus) bool {
	if a.Status == to {
		return true
	}
	allowed := map[AgentStatus]map[AgentStatus]bool{
		AgentReady: {
			AgentRunning: true, AgentWaiting: true, AgentCompleted: true,
			AgentFailed: true, AgentCancelled: true,
		},
		AgentRunning: {
			AgentReady: true, AgentWaiting: true, AgentCompleted: true,
			AgentFailed: true, AgentCancelled: true,
		},
		AgentWaiting: {
			AgentReady: true, AgentRunning: true, AgentFailed: true, AgentCancelled: true,
		},
	}
	return allowed[a.Status][to]
}

func (a AgentNode) Validate() error {
	if err := ValidateSpecialistReadLimits(a.AllowedReadPaths, a.ToolCallLimit, a.CostLimitMicros); err != nil {
		return err
	}
	if a.ToolCallsUsed < 0 || a.CostUsedMicros < 0 {
		return errors.New("negative child budget usage")
	}
	if !validAgentIdentity(a.ID, false) || !validAgentIdentity(a.RunID, false) {
		return errors.New("agent id and run id are required")
	}
	if !validAgentIdentity(a.SessionID, false) {
		return errors.New("agent session id is required")
	}
	if !validAgentIdentity(a.ParentID, true) || !validAgentIdentity(a.ActiveAttemptID, true) {
		return errors.New("agent parent or attempt identity is invalid")
	}
	if !ValidAgentRole(a.Role) {
		return fmt.Errorf("invalid agent role %q", a.Role)
	}
	if _, err := ParseProfile(string(a.Profile)); err != nil {
		return err
	}
	if !ValidAgentStatus(a.Status) {
		return fmt.Errorf("invalid agent status %q", a.Status)
	}
	if a.Depth < 0 || a.Depth > MaxAgentDepth {
		return fmt.Errorf("agent depth must be between 0 and %d", MaxAgentDepth)
	}
	if a.ChildLimit < 0 || a.ChildLimit > MaxAgentChildren {
		return fmt.Errorf("agent child limit must be between 0 and %d", MaxAgentChildren)
	}
	if a.Role == AgentRoleRoot {
		if strings.TrimSpace(a.ParentID) != "" || a.Depth != 0 {
			return errors.New("root agent cannot have a parent or nonzero depth")
		}
	} else if strings.TrimSpace(a.ParentID) == "" || a.Depth == 0 {
		return errors.New("specialist agent requires a parent and positive depth")
	}
	if a.TurnLimit <= 0 || a.TokenLimit < 0 || a.TurnsUsed < 0 || a.TokensUsed < 0 {
		return errors.New("agent budget counters are invalid")
	}
	if a.Version <= 0 {
		return errors.New("agent version must be positive")
	}
	normalizedSkills, err := NormalizeAgentSkills(a.Skills)
	if err != nil {
		return err
	}
	if !slices.Equal(normalizedSkills, a.Skills) {
		return errors.New("agent skills must be normalized, unique, and sorted")
	}
	if !utf8.ValidString(a.StatusReason) || strings.TrimSpace(a.StatusReason) != a.StatusReason ||
		utf8.RuneCountInString(a.StatusReason) > MaxAgentStatusReasonRunes {
		return fmt.Errorf("agent status reason must be normalized and at most %d characters", MaxAgentStatusReasonRunes)
	}
	if a.Status == AgentRunning {
		if strings.TrimSpace(a.ActiveAttemptID) == "" {
			return errors.New("running agent requires an active attempt id")
		}
	} else if strings.TrimSpace(a.ActiveAttemptID) != "" {
		return errors.New("only a running agent may have an active attempt id")
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
		return errors.New("agent timestamps are invalid")
	}
	if a.Terminal() {
		if a.FinishedAt == nil || a.FinishedAt.IsZero() || a.FinishedAt.Before(a.CreatedAt) {
			return errors.New("terminal agent requires a valid finished_at")
		}
	} else if a.FinishedAt != nil {
		return errors.New("nonterminal agent cannot have finished_at")
	}
	return nil
}

func NormalizeAgentSkills(skills []string) ([]string, error) {
	if len(skills) > MaxAgentSkills {
		return nil, fmt.Errorf("agent skills exceed %d entries", MaxAgentSkills)
	}
	unique := make(map[string]struct{}, len(skills))
	for _, raw := range skills {
		skill := strings.ToLower(strings.TrimSpace(raw))
		if skill == "" || utf8.RuneCountInString(skill) > MaxAgentSkillRunes {
			return nil, fmt.Errorf("agent skill must contain between 1 and %d characters", MaxAgentSkillRunes)
		}
		for _, char := range skill {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
				return nil, fmt.Errorf("agent skill %q contains an unsupported character", raw)
			}
		}
		unique[skill] = struct{}{}
	}
	out := make([]string, 0, len(unique))
	for skill := range unique {
		out = append(out, skill)
	}
	sort.Strings(out)
	return out, nil
}

func (m AgentMessage) Validate() error {
	if !validAgentIdentity(m.ID, false) || !validAgentIdentity(m.RunID, false) ||
		!validAgentIdentity(m.RecipientAgentID, false) || !validAgentIdentity(m.SenderAgentID, true) {
		return errors.New("agent message id, run id, and recipient are required")
	}
	if m.SenderAgentID != "" && strings.TrimSpace(m.SenderAgentID) == m.RecipientAgentID {
		return errors.New("agent cannot send a message to itself")
	}
	if m.Sequence <= 0 {
		return errors.New("agent message sequence must be positive")
	}
	if !ValidAgentMessageKind(m.Kind) {
		return fmt.Errorf("invalid agent message kind %q", m.Kind)
	}
	if !ValidAgentMessageSemantic(m.Semantic) {
		return fmt.Errorf("invalid agent message semantic %q", m.Semantic)
	}
	if m.Semantic == AgentMessageSemanticWake && m.Kind != AgentMessageControl {
		return errors.New("wake semantic requires a control message")
	}
	if m.Semantic == AgentMessageSemanticDependency && m.Kind != AgentMessageNotification {
		return errors.New("dependency semantic requires a notification message")
	}
	if m.Semantic == AgentMessageSemanticDependency && m.SenderAgentID == "" {
		return errors.New("dependency notification requires an agent sender")
	}
	if !ValidAgentMessageStatus(m.Status) {
		return fmt.Errorf("invalid agent message status %q", m.Status)
	}
	if len([]byte(m.PayloadJSON)) == 0 || len([]byte(m.PayloadJSON)) > MaxAgentMessagePayloadBytes || !json.Valid([]byte(m.PayloadJSON)) {
		return fmt.Errorf("agent message payload must be valid JSON within %d bytes", MaxAgentMessagePayloadBytes)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(m.PayloadJSON), &object); err != nil || object == nil {
		return errors.New("agent message payload must be a JSON object")
	}
	switch m.Semantic {
	case AgentMessageSemanticWake:
		if _, err := DecodeAgentWakePayload(m.PayloadJSON); err != nil {
			return err
		}
	case AgentMessageSemanticDependency:
		if _, err := DecodeAgentDependencyPayload(m.PayloadJSON); err != nil {
			return err
		}
	}
	if m.CreatedAt.IsZero() {
		return errors.New("agent message created_at is required")
	}
	if m.Status == AgentMessageConsumed {
		if m.ConsumedAt == nil || m.ConsumedAt.IsZero() || m.ConsumedAt.Before(m.CreatedAt) {
			return errors.New("consumed agent message requires a valid consumed_at")
		}
	} else if m.ConsumedAt != nil {
		return errors.New("pending agent message cannot have consumed_at")
	}
	return nil
}

func (s AgentGraphSnapshot) Validate() error {
	if !validAgentIdentity(s.ID, false) || !validAgentIdentity(s.RunID, false) ||
		!validAgentIdentity(s.RootAgentID, false) {
		return errors.New("agent graph snapshot identities are required")
	}
	if s.Version <= 0 || s.ProtocolVersion != AgentGraphProtocolVersion {
		return errors.New("agent graph snapshot version is invalid")
	}
	if s.NodeCount <= 0 || s.NodeCount > MaxAgentNodesPerRun {
		return fmt.Errorf("agent graph snapshot node count must be between 1 and %d", MaxAgentNodesPerRun)
	}
	if s.PendingMessageCount < 0 || s.PendingMessageCount > MaxAgentInboxMessages*MaxAgentNodesPerRun {
		return errors.New("agent graph snapshot pending message count is invalid")
	}
	if len([]byte(s.StateJSON)) == 0 || len([]byte(s.StateJSON)) > MaxAgentGraphSnapshotBytes || !json.Valid([]byte(s.StateJSON)) {
		return errors.New("agent graph snapshot state is invalid or too large")
	}
	if s.CreatedAt.IsZero() {
		return errors.New("agent graph snapshot timestamp is required")
	}
	return nil
}

func (g AgentGraph) Validate() error {
	if !validAgentIdentity(g.RunID, false) || !validAgentIdentity(g.RootAgentID, false) {
		return errors.New("agent graph run and root identities are required")
	}
	if len(g.Nodes) == 0 || len(g.Nodes) > MaxAgentNodesPerRun {
		return errors.New("agent graph node count is invalid")
	}
	foundRoot := false
	for _, node := range g.Nodes {
		if err := node.Validate(); err != nil {
			return err
		}
		if node.RunID != g.RunID {
			return errors.New("agent graph contains a node from another run")
		}
		if node.ID == g.RootAgentID && node.Role == AgentRoleRoot {
			foundRoot = true
		}
	}
	if !foundRoot {
		return errors.New("agent graph root was not found")
	}
	if len(g.PendingMessages) > MaxAgentInboxMessages*len(g.Nodes) {
		return errors.New("agent graph pending inbox is too large")
	}
	for _, message := range g.PendingMessages {
		if err := message.Validate(); err != nil {
			return err
		}
		if message.RunID != g.RunID || message.Status != AgentMessagePending {
			return errors.New("agent graph contains an invalid pending message")
		}
	}
	return g.LatestSnapshot.Validate()
}

func validAgentIdentity(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		utf8.RuneCountInString(value) <= MaxAgentIdentityRunes
}

func ValidAgentID(value string) bool {
	return validAgentIdentity(value, false)
}

func DecodeAgentWakePayload(payloadJSON string) (AgentWakePayload, error) {
	var payload AgentWakePayload
	if err := decodeStrictAgentPayload(payloadJSON, &payload); err != nil {
		return AgentWakePayload{}, fmt.Errorf("invalid wake payload: %w", err)
	}
	payload.Reason = strings.TrimSpace(payload.Reason)
	if payload.Reason == "" || !utf8.ValidString(payload.Reason) ||
		utf8.RuneCountInString(payload.Reason) > 1024 {
		return AgentWakePayload{}, errors.New("wake reason must contain between 1 and 1024 characters")
	}
	return payload, nil
}

func DecodeAgentDependencyPayload(payloadJSON string) (AgentDependencyPayload, error) {
	var payload AgentDependencyPayload
	if err := decodeStrictAgentPayload(payloadJSON, &payload); err != nil {
		return AgentDependencyPayload{}, fmt.Errorf("invalid dependency payload: %w", err)
	}
	payload.DependencyID = strings.TrimSpace(payload.DependencyID)
	payload.Reason = strings.TrimSpace(payload.Reason)
	if !validAgentIdentity(payload.DependencyID, false) {
		return AgentDependencyPayload{}, errors.New("dependency id is invalid")
	}
	switch payload.State {
	case AgentDependencyWait, AgentDependencySatisfied, AgentDependencyFailed,
		AgentDependencyCancelled, AgentDependencyExpired:
	default:
		return AgentDependencyPayload{}, errors.New("dependency state is invalid")
	}
	if !utf8.ValidString(payload.Reason) || utf8.RuneCountInString(payload.Reason) > 1024 {
		return AgentDependencyPayload{}, errors.New("dependency reason exceeds 1024 characters")
	}
	return payload, nil
}

func decodeStrictAgentPayload(payloadJSON string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader([]byte(payloadJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("payload does not match its schema")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("payload contains trailing data")
	}
	return nil
}
