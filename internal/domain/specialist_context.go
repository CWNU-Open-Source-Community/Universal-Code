package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const SpecialistResultReadName = "specialist_result_read"

// Context maintenance reads only receipts already owned by this attempt. It
// cannot acquire a workspace, parent history, or any external tool capability.
type SpecialistResultReadRequest struct {
	CallID         string `json:"call_id"`
	Part           string `json:"part"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Offset         int    `json:"offset"`
	Limit          int    `json:"limit"`
}

func (r SpecialistResultReadRequest) Validate() error {
	if r.CallID == "" || strings.TrimSpace(r.CallID) != r.CallID || len(r.CallID) > 256 ||
		(r.Part != "result" && r.Part != "arguments") || r.Offset < 0 || r.Limit < 4 || r.Limit > 8192 {
		return errors.New("invalid child receipt read range or identity")
	}
	if len(r.ExpectedSHA256) != 64 {
		return errors.New("child receipt read requires its exact SHA-256")
	}
	if _, err := hex.DecodeString(r.ExpectedSHA256); err != nil || strings.ToLower(r.ExpectedSHA256) != r.ExpectedSHA256 {
		return errors.New("invalid child receipt digest")
	}
	return nil
}

type SpecialistResultPage struct {
	Version               string                   `json:"version"`
	CallID                string                   `json:"call_id"`
	Part                  string                   `json:"part"`
	Status                SupervisorToolCallStatus `json:"status"`
	Content               string                   `json:"content"`
	ContentSHA256         string                   `json:"content_sha256"`
	Offset                int                      `json:"offset"`
	NextOffset            int                      `json:"next_offset"`
	TotalBytes            int                      `json:"total_bytes"`
	HasMore               bool                     `json:"has_more"`
	InstructionAuthorized bool                     `json:"instruction_authorized"`
}

// Only a projection watermark is mutable. Original rounds and tool receipts
// remain unchanged. One recorded provider rejection permits one smaller retry.
type SpecialistContextState struct {
	ThroughRound       int
	SourceSHA256       string
	RejectedModel      int
	RecoveryInputLimit int
}

func SpecialistContextSourceSHA256(rounds []SpecialistToolRound) string {
	// ProviderReplay is deliberately excluded by its JSON tag. Refuse replacing
	// private replay separately; the digest covers every public source field.
	raw, _ := json.Marshal(rounds)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
