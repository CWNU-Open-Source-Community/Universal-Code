package domain

import (
	"encoding/json"
	"time"
)

const SpecialistToolsVersion = "specialist_tools.v1"
const MaxSpecialistToolRounds = 16
const MaxSpecialistToolCalls = 4

// SpecialistToolBinding is issued by Go after the current task brief has been
// prepared. It records the actor and capability ceiling, never credentials.
type SpecialistToolBinding struct {
	Ref              AgentAttemptRef
	SessionID        string
	WorkspaceID      string
	RootFingerprint  string
	BriefFingerprint string
	Mode             string
	BatchPlanID      string
	BatchOrdinal     int
	BatchGeneration  int64
}

type SpecialistToolCall struct {
	Ref          AgentAttemptRef
	Round        int
	Position     int
	ModelAttempt int
	CallID       string
	Name         string
	PayloadJSON  string
	Status       SupervisorToolCallStatus
	ResultJSON   string
	ErrorCode    string
	ChargeID     string
	CreatedAt    time.Time
	CompletedAt  *time.Time
}

type SpecialistToolRound struct {
	Round         int
	ModelAttempt  int
	AssistantText string
	Calls         []SpecialistToolCall
	// Private provider continuation data never enters public projections.
	ProviderReplay json.RawMessage `json:"-"`
}
