package llm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/redact"
)

type Outcome string

const (
	OutcomeSuccess         Outcome = "success"
	OutcomeRetryable       Outcome = "retryable"
	OutcomeRateLimited     Outcome = "rate_limited"
	OutcomeInvalidResponse Outcome = "invalid_response"
	OutcomeCancelled       Outcome = "cancelled"
	OutcomePermanent       Outcome = "permanent"
)

func (o Outcome) Valid() bool {
	switch o {
	case OutcomeSuccess, OutcomeRetryable, OutcomeRateLimited, OutcomeInvalidResponse, OutcomeCancelled, OutcomePermanent:
		return true
	default:
		return false
	}
}

func (o Outcome) Retryable() bool {
	return o == OutcomeRetryable || o == OutcomeRateLimited
}

// ProviderFailureReason is a stable, content-free classification for operator
// diagnostics. It is deliberately separate from Outcome: Outcome controls
// retry behavior, while the reason describes the safe failure category.
type ProviderFailureReason string

const (
	ProviderFailureNone                 ProviderFailureReason = "none"
	ProviderFailureNotConfigured        ProviderFailureReason = "not_configured"
	ProviderFailureAuthentication       ProviderFailureReason = "authentication"
	ProviderFailureNetwork              ProviderFailureReason = "network"
	ProviderFailureRateLimit            ProviderFailureReason = "rate_limit"
	ProviderFailureCapacity             ProviderFailureReason = "capacity"
	ProviderFailureModelNotFound        ProviderFailureReason = "model_not_found"
	ProviderFailureProtocolIncompatible ProviderFailureReason = "protocol_incompatible"
	ProviderFailureContextLimit         ProviderFailureReason = "context_limit"
	ProviderFailureOutputLimit          ProviderFailureReason = "output_limit"
	ProviderFailurePaused               ProviderFailureReason = "paused"
	ProviderFailureRefusal              ProviderFailureReason = "refusal"
)

func (r ProviderFailureReason) Valid() bool {
	switch r {
	case ProviderFailureNone, ProviderFailureNotConfigured,
		ProviderFailureAuthentication, ProviderFailureNetwork,
		ProviderFailureRateLimit, ProviderFailureCapacity,
		ProviderFailureModelNotFound, ProviderFailureProtocolIncompatible,
		ProviderFailureContextLimit, ProviderFailureOutputLimit,
		ProviderFailurePaused, ProviderFailureRefusal:
		return true
	default:
		return false
	}
}

type ProviderError struct {
	Kind       Outcome
	Reason     ProviderFailureReason
	Provider   string
	StatusCode int
	RetryAfter time.Duration
	Message    string
	Cause      error
}

func NewProviderError(kind Outcome, provider string, message string, cause error) *ProviderError {
	if !kind.Valid() || kind == OutcomeSuccess {
		kind = OutcomePermanent
	}
	return &ProviderError{
		Kind: kind, Reason: ProviderFailureNone,
		Provider: strings.TrimSpace(provider),
		Message:  redact.String(strings.TrimSpace(message)), Cause: cause,
	}
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = string(e.Kind)
	}
	if strings.TrimSpace(e.Provider) == "" {
		return message
	}
	return fmt.Sprintf("provider %s: %s", e.Provider, message)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NormalizeProviderError(provider string, err error) *ProviderError {
	if err == nil {
		return nil
	}
	var typed *ProviderError
	if errors.As(err, &typed) {
		copy := *typed
		if !copy.Kind.Valid() || copy.Kind == OutcomeSuccess {
			copy.Kind = OutcomePermanent
		}
		if strings.TrimSpace(copy.Provider) == "" {
			copy.Provider = strings.TrimSpace(provider)
		}
		if !copy.Reason.Valid() || copy.Reason == ProviderFailureNone {
			copy.Reason = defaultProviderFailureReason(copy.Kind, copy.StatusCode)
			if copy.Reason == ProviderFailureNone &&
				errors.Is(copy.Cause, context.DeadlineExceeded) {
				copy.Reason = ProviderFailureNetwork
			}
		}
		copy.Message = redact.String(strings.TrimSpace(copy.Message))
		if copy.RetryAfter < 0 {
			copy.RetryAfter = 0
		}
		return &copy
	}
	kind := OutcomePermanent
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = OutcomeCancelled
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		kind = OutcomeRetryable
	default:
		var netErr net.Error
		var urlErr *url.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			kind = OutcomeRetryable
		} else if errors.As(err, &urlErr) {
			kind = OutcomeRetryable
		}
	}
	message := "provider request failed"
	if kind == OutcomeCancelled {
		message = "provider request was cancelled"
	}
	providerError := NewProviderError(kind, provider, message, err)
	providerError.Reason = defaultProviderFailureReason(kind, 0)
	if providerError.Reason == ProviderFailureNone &&
		errors.Is(err, context.DeadlineExceeded) {
		providerError.Reason = ProviderFailureNetwork
	}
	return providerError
}

func ProviderErrorKind(err error) Outcome {
	var typed *ProviderError
	if errors.As(err, &typed) && typed != nil {
		return typed.Kind
	}
	return ""
}

func ProviderErrorReason(err error) ProviderFailureReason {
	var typed *ProviderError
	if errors.As(err, &typed) && typed != nil {
		if typed.Reason.Valid() && typed.Reason != ProviderFailureNone {
			return typed.Reason
		}
		return defaultProviderFailureReason(typed.Kind, typed.StatusCode)
	}
	return ProviderFailureNone
}

func defaultProviderFailureReason(kind Outcome, statusCode int) ProviderFailureReason {
	if statusCode == 402 {
		return ProviderFailureCapacity
	}
	switch statusCode {
	case 401, 403:
		return ProviderFailureAuthentication
	case 404:
		// A status alone cannot distinguish a missing model from a bad or
		// unsupported endpoint path. Protocol adapters may upgrade this only
		// after recognizing an exact, content-free upstream code or type.
		return ProviderFailureProtocolIncompatible
	case 429:
		return ProviderFailureRateLimit
	case 503, 529:
		return ProviderFailureCapacity
	}
	switch kind {
	case OutcomeRetryable:
		return ProviderFailureNetwork
	case OutcomeRateLimited:
		return ProviderFailureRateLimit
	case OutcomeInvalidResponse:
		return ProviderFailureProtocolIncompatible
	default:
		return ProviderFailureNone
	}
}

type ModelAttempt struct {
	SupervisorAttemptID    string
	SpecialistAttemptID    string
	Purpose                string
	CompactionSourceSHA256 string
	Number                 int
	TransportAttempt       int
	MaxAttempts            int
	ProtocolRepair         int
	ToolRound              int
	SteeringSequence       int64
	Provider               string
	Model                  string
	Outcome                Outcome
	FailureReason          ProviderFailureReason
	ErrorText              string
	RetryAfter             time.Duration
	Elapsed                time.Duration
	RetryPlanned           bool
	StreamEvents           int
	StreamBytes            int
	InputEstimate          int
	Context                *ModelContextAudit
}

const ModelPurposeContextCompaction = "context_compaction"

// MonetaryAttemptNumber keeps auxiliary source-bound reservations outside the
// legacy per-turn normal-attempt range. Invalid identities never yield a key.
func (a ModelAttempt) MonetaryAttemptNumber() int64 {
	if a.SpecialistAttemptID != "" {
		if !validSupervisorMonetaryIdentity(a.SpecialistAttemptID) || a.Number <= 0 ||
			a.SupervisorAttemptID != "" || a.Purpose != "" || a.SteeringSequence != 0 {
			return 0
		}
		digest := sha256.Sum256([]byte("specialist_model_cost.v1\x00" + a.SpecialistAttemptID + "\x00" + strconv.Itoa(a.Number)))
		return int64(binary.BigEndian.Uint64(digest[:8])&((1<<61)-1) | (1 << 61))
	}
	if a.Purpose == "" {
		if a.SupervisorAttemptID != "" {
			if !validSupervisorMonetaryIdentity(a.SupervisorAttemptID) || a.Number <= 0 {
				return 0
			}
			if a.SteeringSequence < 0 {
				return 0
			}
			if a.SteeringSequence > 0 {
				digest := sha256.Sum256([]byte("supervisor_model_cost_steering.v1\x00" +
					a.SupervisorAttemptID + "\x00" + strconv.Itoa(a.Number) + "\x00" +
					strconv.FormatInt(a.SteeringSequence, 10)))
				return int64(binary.BigEndian.Uint64(digest[:8])&((1<<61)-1) | (1 << 61))
			}
			digest := sha256.Sum256([]byte("supervisor_model_cost.v1\x00" + a.SupervisorAttemptID + "\x00" + strconv.Itoa(a.Number)))
			return int64(binary.BigEndian.Uint64(digest[:8])&((1<<61)-1) | (1 << 61))
		}
		return int64(a.Number)
	}
	if a.Purpose != ModelPurposeContextCompaction {
		return 0
	}
	digest, err := hex.DecodeString(a.CompactionSourceSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(a.CompactionSourceSHA256) != a.CompactionSourceSHA256 {
		return 0
	}
	return int64(binary.BigEndian.Uint64(digest[:8])&((1<<62)-1) | (1 << 62))
}

func validSupervisorMonetaryIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

const MaxModelContextSources = 256

type ModelContextSource struct {
	Kind           string `json:"kind"`
	SourceID       string `json:"source_id"`
	Tokens         int    `json:"tokens"`
	MemoryVersion  int64  `json:"memory_version,omitempty"`
	ContentSHA256  string `json:"content_sha256,omitempty"`
	OmissionReason string `json:"omission_reason,omitempty"`
}

type ModelContextAudit struct {
	TokenBudget     int                  `json:"token_budget"`
	EstimatedTokens int                  `json:"estimated_tokens"`
	Included        []ModelContextSource `json:"included"`
	Omitted         []ModelContextSource `json:"omitted,omitempty"`
}

func (a ModelContextAudit) Validate() error {
	if a.TokenBudget <= 0 {
		return errors.New("model context token budget must be positive")
	}
	if a.EstimatedTokens < 0 || a.EstimatedTokens > a.TokenBudget {
		return errors.New("model context token estimate must be within budget")
	}
	if len(a.Included)+len(a.Omitted) > MaxModelContextSources {
		return fmt.Errorf("model context source list exceeds %d items", MaxModelContextSources)
	}
	seen := make(map[string]struct{}, len(a.Included)+len(a.Omitted))
	includedTokens := 0
	for groupIndex, group := range [][]ModelContextSource{a.Included, a.Omitted} {
		for _, source := range group {
			if strings.TrimSpace(source.Kind) == "" || strings.TrimSpace(source.SourceID) == "" || source.Tokens <= 0 {
				return errors.New("model context source kind, id, and positive tokens are required")
			}
			if len([]rune(source.Kind)) > 64 || len([]rune(source.SourceID)) > 256 {
				return errors.New("model context source identity is too long")
			}
			if source.MemoryVersion != 0 || source.ContentSHA256 != "" {
				digest, err := hex.DecodeString(source.ContentSHA256)
				if source.Kind != "long_term_memory" || source.MemoryVersion < 1 || err != nil ||
					len(digest) != sha256.Size || strings.ToLower(source.ContentSHA256) != source.ContentSHA256 {
					return errors.New("model context memory version and digest are invalid")
				}
			}
			if groupIndex == 0 && source.OmissionReason != "" {
				return errors.New("included model context cannot have an omission reason")
			}
			switch source.OmissionReason {
			case "", "budget", "operator_excluded", "auxiliary_scope", "long_term_memory_quota",
				"required_context_budget", "project_instruction_budget":
			default:
				return errors.New("model context omission reason is invalid")
			}
			key := source.Kind + "\x00" + source.SourceID
			if _, ok := seen[key]; ok {
				return errors.New("model context source identities must be unique")
			}
			seen[key] = struct{}{}
		}
	}
	for _, source := range a.Included {
		if source.Tokens > a.TokenBudget-includedTokens {
			return errors.New("model context included source tokens exceed budget")
		}
		includedTokens += source.Tokens
	}
	if includedTokens != a.EstimatedTokens {
		return errors.New("model context included source tokens do not match estimate")
	}
	return nil
}

func (a ModelAttempt) ValidateStarted() error {
	if a.SupervisorAttemptID != "" && (a.Purpose != "" || !validSupervisorMonetaryIdentity(a.SupervisorAttemptID)) {
		return errors.New("invalid Supervisor monetary attempt identity")
	}
	switch a.Purpose {
	case "":
		if a.CompactionSourceSHA256 != "" {
			return errors.New("normal model attempt cannot carry a compaction source")
		}
	case ModelPurposeContextCompaction:
		if a.MonetaryAttemptNumber() == 0 || a.ProtocolRepair != 0 || a.ToolRound != 0 || a.TransportNumber() != 1 || a.MaxAttempts != 1 || a.RetryPlanned || a.RetryAfter != 0 || a.StreamEvents != 0 || a.StreamBytes != 0 {
			return errors.New("context compaction requires one source-bound non-streaming attempt without tools or repair")
		}
	default:
		return errors.New("unknown model attempt purpose")
	}
	if a.Number <= 0 || a.MaxAttempts <= 0 {
		return errors.New("model attempt number and transport limit must be positive")
	}
	transportAttempt := a.TransportNumber()
	if transportAttempt <= 0 || transportAttempt > a.MaxAttempts {
		return errors.New("model transport attempt must be within its limit")
	}
	if a.ProtocolRepair < 0 || a.ProtocolRepair > 1 {
		return errors.New("model protocol repair number must be zero or one")
	}
	maxToolRounds := 4
	if a.SpecialistAttemptID != "" {
		maxToolRounds = 16
	}
	if a.ToolRound < 0 || a.ToolRound > maxToolRounds {
		return fmt.Errorf("model tool round must be between zero and %d", maxToolRounds)
	}
	if a.SteeringSequence < 0 {
		return errors.New("model steering sequence cannot be negative")
	}
	if strings.TrimSpace(a.Provider) == "" || strings.TrimSpace(a.Model) == "" {
		return errors.New("model attempt provider and model are required")
	}
	if a.RetryAfter < 0 || a.Elapsed < 0 {
		return errors.New("model attempt durations cannot be negative")
	}
	if a.StreamEvents < 0 || a.StreamBytes < 0 {
		return errors.New("model stream counters cannot be negative")
	}
	if a.InputEstimate < 0 {
		return errors.New("model input estimate cannot be negative")
	}
	if a.FailureReason != "" && !a.FailureReason.Valid() {
		return errors.New("model attempt failure reason is invalid")
	}
	if a.Context != nil {
		if err := a.Context.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (a ModelAttempt) TransportNumber() int {
	if a.TransportAttempt > 0 {
		return a.TransportAttempt
	}
	return a.Number
}

func (a ModelAttempt) ValidateCompleted() error {
	if err := a.ValidateStarted(); err != nil {
		return err
	}
	if a.Outcome != OutcomeSuccess {
		return errors.New("completed model attempt requires success outcome")
	}
	if a.FailureReason != "" && a.FailureReason != ProviderFailureNone {
		return errors.New("completed model attempt cannot carry a failure reason")
	}
	return nil
}

func (a ModelAttempt) ValidateFailed() error {
	if err := a.ValidateStarted(); err != nil {
		return err
	}
	if !a.Outcome.Valid() || a.Outcome == OutcomeSuccess {
		return errors.New("failed model attempt requires a failure outcome")
	}
	if strings.TrimSpace(a.ErrorText) == "" {
		return errors.New("failed model attempt error is required")
	}
	return nil
}
