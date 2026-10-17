package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const SpecialistToolsVersion = "specialist_tools.v1"
const MaxSpecialistToolRounds = 16
const MaxSpecialistToolCalls = 4

// SpecialistToolBinding is issued by Go after the current task brief has been
// prepared. It records the actor and capability ceiling, never credentials.
type SpecialistToolBinding struct {
	WorkerProfileJSON    string
	AllowedReadPathsJSON string
	Ref                  AgentAttemptRef
	SessionID            string
	WorkspaceID          string
	RootFingerprint      string
	BriefFingerprint     string
	Mode                 string
	BatchPlanID          string
	BatchOrdinal         int
	BatchGeneration      int64
}

type SpecialistToolCall struct {
	AuthorityJSON    string
	ApprovalID       string
	ApprovalSourceID string
	StartedAt        *time.Time
	Ref              AgentAttemptRef
	Round            int
	Position         int
	ModelAttempt     int
	CallID           string
	Name             string
	PayloadJSON      string
	Status           SupervisorToolCallStatus
	ResultJSON       string
	ErrorCode        string
	ChargeID         string
	CreatedAt        time.Time
	CompletedAt      *time.Time
}

type SpecialistToolRound struct {
	Round         int
	ModelAttempt  int
	AssistantText string
	Calls         []SpecialistToolCall
	// Private provider continuation data never enters public projections.
	ProviderReplay json.RawMessage `json:"-"`
}

// SpecialistToolSource names the exact durable child intent at a runtime
// boundary. AuthorityJSON is supplied by the host. An empty ChargeID is valid
// only before charging and cannot be used for a started dispatch.
type SpecialistToolSource struct {
	Ref             AgentAttemptRef
	CallID          string
	SessionID       string
	WorkspaceID     string
	Name            string
	PayloadJSON     string
	AuthorityJSON   string
	ChargeID        string
	LeaseID         string
	LeaseGeneration int64
	RequireStarted  bool
}

func SpecialistToolApprovalID(ref AgentAttemptRef, callID string) string {
	raw, _ := json.Marshal([]string{ref.RunID, ref.AgentID, ref.AttemptID, callID})
	sum := sha256.Sum256(raw)
	return "specialist-tool-" + hex.EncodeToString(sum[:])
}

func SpecialistToolApprovalFingerprint(call SpecialistToolCall) string {
	raw, _ := json.Marshal([]string{"specialist_tool_approval.v1", call.Ref.RunID,
		call.Ref.AgentID, call.Ref.AttemptID, call.CallID, call.Name, call.PayloadJSON, call.AuthorityJSON})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
