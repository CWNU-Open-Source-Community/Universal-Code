package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
)

type OperationKind string

const (
	OperationConnect   OperationKind = "connect"
	OperationDiscovery OperationKind = "discovery"
	OperationToolCall  OperationKind = "tool_call"
	OperationFileRead  OperationKind = "file_read"
	OperationFileWrite OperationKind = "file_write"
	OperationProcess   OperationKind = "process"
)

type Effect string

const (
	EffectWorkspaceRead   Effect = "workspace_read"
	EffectReversibleWrite Effect = "reversible_workspace_write"
	EffectProcess         Effect = "process_execute"
	EffectPublicNetwork   Effect = "public_network_request"
	EffectOutside         Effect = "outside_workspace"
	EffectDestructive     Effect = "destructive"
	EffectSensitiveExport Effect = "sensitive_export"
	EffectRemoteWrite     Effect = "shared_remote_write"
	EffectUnknown         Effect = "unknown"
)

type Target struct {
	Kind    string // file | directory | endpoint | process
	Locator string // exact host target reference; never a secret-bearing URL
}

// Operation is a prepared host description, NOT an authority token. The host
// verifies effects against the installed adapter and actual enforcement. Tool
// annotations cannot establish isolation, access rights, or safe effects.
type Operation struct {
	ID                    string
	Kind                  OperationKind
	ToolID                string
	Component             ComponentRef
	AdapterID             string
	AdapterRevision       string
	InputFingerprint      string // HMAC for secret-bearing inputs, not a plain secret hash
	CapabilityFingerprint string // required for a tools/call capability snapshot
	Targets               []Target
	Effects               []Effect
}

func (o Operation) Validate() error {
	if !normalized(o.ID, 256) || !normalized(o.ToolID, 256) || o.Component.Validate() != nil ||
		!normalized(o.AdapterID, 256) || !normalized(o.AdapterRevision, 256) ||
		!digest(o.InputFingerprint) || len(o.Targets) == 0 || len(o.Targets) > 256 ||
		len(o.Effects) == 0 || len(o.Effects) > 9 ||
		(o.CapabilityFingerprint != "" && !digest(o.CapabilityFingerprint)) {
		return errors.New("operation identity, input, or bounds are invalid")
	}
	switch o.Kind {
	case OperationConnect, OperationDiscovery, OperationFileRead, OperationFileWrite, OperationProcess:
	case OperationToolCall:
		if !digest(o.CapabilityFingerprint) {
			return errors.New("tool call requires a capability fingerprint")
		}
	default:
		return errors.New("operation kind is invalid")
	}
	seen := make(map[Target]bool, len(o.Targets))
	for _, target := range o.Targets {
		var valid bool
		switch target.Kind {
		case "file", "directory":
			valid = target.Locator != "" && text(target.Locator, 4096)
		case "endpoint", "process":
			valid = normalized(target.Locator, 4096)
		}
		if !valid || seen[target] {
			return errors.New("operation target is invalid or duplicated")
		}
		seen[target] = true
	}
	effects := make(map[Effect]bool, len(o.Effects))
	for _, effect := range o.Effects {
		if effects[effect] || !slices.Contains([]Effect{EffectWorkspaceRead, EffectReversibleWrite,
			EffectProcess, EffectPublicNetwork, EffectOutside, EffectDestructive,
			EffectSensitiveExport, EffectRemoteWrite, EffectUnknown}, effect) {
			return errors.New("operation effect is invalid or duplicated")
		}
		effects[effect] = true
	}
	return nil
}

// FingerprintOperation binds final inputs, adapter/capability versions, targets,
// and all effects. Sets are sorted on copies; ordered launch inputs were already
// bound by InputFingerprint. Changing a hook output requires a new decision.
func FingerprintOperation(operation Operation) (string, error) {
	if err := operation.Validate(); err != nil {
		return "", err
	}
	operation.Targets = slices.Clone(operation.Targets)
	operation.Effects = slices.Clone(operation.Effects)
	slices.SortFunc(operation.Targets, func(a, b Target) int {
		if a.Kind != b.Kind {
			return compare(a.Kind, b.Kind)
		}
		return compare(a.Locator, b.Locator)
	})
	slices.Sort(operation.Effects)
	raw, err := json.Marshal(operation)
	if err != nil {
		return "", errors.New("could not encode operation")
	}
	sum := sha256.Sum256(append([]byte("toolcontract.operation.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

func compare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// DispatchGuard is a host-injected, one-operation check-and-begin closure. The
// adapter recomputes actualFingerprint from its final frozen execution inputs.
// It is not supplied by a plugin or model, persisted, or recovered as a grant.
type DispatchGuard func(ctx context.Context, actualFingerprint string) error

type ReceiptState string

const (
	ReceiptNotDispatched  ReceiptState = "not_dispatched"
	ReceiptResultReceived ReceiptState = "result_received"
	ReceiptOutcomeUnknown ReceiptState = "outcome_unknown"
)

// Receipt annotates an existing operation ledger, not a second Run database.
// ResultReceived means an execution result was observed, not success or absence
// of partial effects. Native IsError/exit status stays separate. After dispatch,
// lost response/cancellation/crash is Unknown unless stronger evidence exists.
// Unknown must never trigger an automatic retry (including SDK MRTR retries).
type Receipt struct {
	OperationID string
	State       ReceiptState
	ErrorCode   string
}

func (r Receipt) Validate() error {
	if !normalized(r.OperationID, 256) || (r.ErrorCode != "" && !normalized(r.ErrorCode, 128)) ||
		(r.State != ReceiptNotDispatched && r.State != ReceiptResultReceived && r.State != ReceiptOutcomeUnknown) {
		return errors.New("execution receipt is invalid")
	}
	return nil
}
