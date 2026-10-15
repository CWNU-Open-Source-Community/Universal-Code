package domain

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const MaxThreadMCPReferences = 4

// MCPReference is an operator-selected catalog entry, never a grant to other
// resources or tools. Arguments remain exact MCP data, including URI templates.
type MCPReference struct {
	ServerID              string          `json:"server_id"`
	CapabilityFingerprint string          `json:"capability_fingerprint"`
	Method                string          `json:"method"`
	Arguments             json.RawMessage `json:"arguments"`
}

func ValidateThreadMCPReferences(refs []MCPReference) error {
	if len(refs) > MaxThreadMCPReferences {
		return errors.New("at most four MCP references are allowed per message")
	}
	for _, ref := range refs {
		digest, err := hex.DecodeString(ref.CapabilityFingerprint)
		if !ValidAgentID(ref.ServerID) || err != nil || len(digest) != 32 ||
			ref.CapabilityFingerprint != strings.ToLower(ref.CapabilityFingerprint) ||
			(ref.Method != "resources/read" && ref.Method != "prompts/get") ||
			len(ref.Arguments) == 0 || len(ref.Arguments) > 16*1024 || !json.Valid(ref.Arguments) {
			return errors.New("MCP reference must identify a bounded resource or Prompt operation")
		}
		var arguments map[string]json.RawMessage
		if json.Unmarshal(ref.Arguments, &arguments) != nil || arguments == nil {
			return errors.New("MCP reference arguments must be an object")
		}
	}
	return nil
}

// ThreadMCPReference is the child receipt of one immutable message intent.
// An observed start without a saved result is unknown and cannot be replayed.
type ThreadMCPReference struct {
	OperationID       string
	IntentDigest      string
	Position          int
	MessageID         string
	RequestedBy       string
	Reference         MCPReference
	State             string
	Receipt           string
	ResultJSON        string
	EvidenceMessageID int64
}
