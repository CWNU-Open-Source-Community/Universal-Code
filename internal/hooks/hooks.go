// Package hooks implements the declarative, Go-owned lifecycle hook runtime.
// Plugin packages can contribute rules, never executable handlers or scripts.
package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ProtocolVersion      = "restricted-hooks.v1"
	MaxDeclarations      = 64
	MaxPayloadBytes      = 256 * 1024
	MaxOutputBytes       = 16 * 1024
	MaxAnnotations       = 16
	MaxAnnotationBytes   = 1024
	MaxHookTimeoutMillis = 2_000
	MaxHookIdentityRunes = 256
)

type Event string

const (
	PreTool       Event = "pre_tool"
	PostTool      Event = "post_tool"
	RunStarted    Event = "run_started"
	RunCompleted  Event = "run_completed"
	SessionOpened Event = "session_opened"
	SessionClosed Event = "session_closed"
	Compaction    Event = "compaction"
	Subagent      Event = "subagent"
	Checkpoint    Event = "checkpoint"
)

func (e Event) Valid() bool {
	switch e {
	case PreTool, PostTool, RunStarted, RunCompleted, SessionOpened, SessionClosed,
		Compaction, Subagent, Checkpoint:
		return true
	default:
		return false
	}
}

type Action string

const (
	ActionDeny     Action = "deny"
	ActionAnnotate Action = "annotate"
	ActionNarrow   Action = "narrow"
	ActionRecord   Action = "record"
)

func (a Action) Valid() bool {
	return a == ActionDeny || a == ActionAnnotate || a == ActionNarrow || a == ActionRecord
}

type FailurePolicy string

const (
	FailureContinue FailurePolicy = "continue"
	FailureDeny     FailurePolicy = "deny"
)

func (p FailurePolicy) Valid() bool { return p == FailureContinue || p == FailureDeny }

type Declaration struct {
	ProtocolVersion string        `json:"protocol_version"`
	ID              string        `json:"id"`
	Event           Event         `json:"event"`
	Action          Action        `json:"action"`
	FailurePolicy   FailurePolicy `json:"failure_policy"`
	TimeoutMillis   int           `json:"timeout_ms"`
	ToolNames       []string      `json:"tool_names,omitempty"`
	RemoveFields    []string      `json:"remove_fields,omitempty"`
	Message         string        `json:"message,omitempty"`
}

func (d Declaration) Validate() error {
	if d.ProtocolVersion != ProtocolVersion || !validIdentity(d.ID) || !d.Event.Valid() ||
		!d.Action.Valid() || !d.FailurePolicy.Valid() || d.TimeoutMillis < 1 ||
		d.TimeoutMillis > MaxHookTimeoutMillis || len(d.ToolNames) > 64 ||
		len(d.RemoveFields) > 32 || !validText(d.Message, MaxAnnotationBytes, true) {
		return errors.New("hook declaration identity, event, action, or bounds are invalid")
	}
	if d.Action == ActionDeny || d.Action == ActionAnnotate {
		if d.Message == "" {
			return errors.New("deny and annotation hooks require a message")
		}
	}
	if d.Action != ActionNarrow && len(d.RemoveFields) != 0 {
		return errors.New("only narrow hooks may remove fields")
	}
	if d.Action == ActionNarrow && (d.Event != PreTool || len(d.RemoveFields) == 0) {
		return errors.New("narrow hooks require pre_tool and at least one field")
	}
	if d.Action == ActionDeny && d.Event == PostTool {
		return errors.New("post_tool hooks cannot retroactively deny a completed call")
	}
	if d.Event == PostTool && d.FailurePolicy == FailureDeny {
		return errors.New("post_tool hooks require continue failure policy because execution is already complete")
	}
	if d.Event != PreTool && d.Event != PostTool && len(d.ToolNames) != 0 {
		return errors.New("non-tool hooks cannot declare tool filters")
	}
	if duplicatesOrInvalid(d.ToolNames, false) || duplicatesOrInvalid(d.RemoveFields, true) {
		return errors.New("hook tool names or removal fields are invalid or repeated")
	}
	return nil
}

type Registration struct {
	PluginID          string      `json:"plugin_id"`
	PluginFingerprint string      `json:"plugin_fingerprint"`
	Declaration       Declaration `json:"declaration"`
}

func (r Registration) Validate() error {
	if !validIdentity(r.PluginID) || !validDigest(r.PluginFingerprint) {
		return errors.New("hook registration plugin binding is invalid")
	}
	return r.Declaration.Validate()
}

type Input struct {
	Event       Event           `json:"event"`
	RunID       string          `json:"run_id,omitempty"`
	WorkspaceID string          `json:"workspace_id,omitempty"`
	ToolName    string          `json:"tool_name,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Depth       int             `json:"depth"`
}

func (i Input) Validate() error {
	if !i.Event.Valid() || i.Depth != 0 || !validText(i.RunID, MaxHookIdentityRunes, true) ||
		!validText(i.WorkspaceID, MaxHookIdentityRunes, true) ||
		!validText(i.ToolName, MaxHookIdentityRunes, true) || len(i.Payload) > MaxPayloadBytes {
		return errors.New("hook input is invalid or reentrant")
	}
	if len(i.Payload) > 0 && (!utf8.Valid(i.Payload) || !json.Valid(i.Payload)) {
		return errors.New("hook input payload must be bounded UTF-8 JSON")
	}
	if (i.Event == PreTool || i.Event == PostTool) && i.ToolName == "" {
		return errors.New("tool hook input requires a tool name")
	}
	return nil
}

type Annotation struct {
	PluginID string `json:"plugin_id"`
	HookID   string `json:"hook_id"`
	Message  string `json:"message"`
}

type Result struct {
	ProtocolVersion string          `json:"protocol_version"`
	Generation      string          `json:"generation"`
	Denied          bool            `json:"denied"`
	Reason          string          `json:"reason,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Annotations     []Annotation    `json:"annotations,omitempty"`
	Executed        []string        `json:"executed"`
}

// DeniedError reports a fail-closed lifecycle decision without exposing a
// plugin-controlled message to a caller, log, or model response. The detailed
// reason remains in Result for operator-owned views that explicitly choose to
// render untrusted extension text.
type DeniedError struct{}

func (DeniedError) Error() string { return "restricted lifecycle hook denied the operation" }

// ExecuteBoundary is the common adapter used by application lifecycle
// boundaries. Payload is encoded by the Go control plane, remains subject to
// the same input limit as tool hooks, and can never contain executable plugin
// behavior. A nil engine is an intentional no-op for runtimes where extensions
// are disabled.
func ExecuteBoundary(ctx context.Context, engine *Engine, input Input, payload any) (Result, error) {
	if engine == nil {
		return Result{ProtocolVersion: ProtocolVersion, Generation: emptyGeneration(),
			Executed: []string{}}, nil
	}
	if len(input.Payload) != 0 {
		return Result{}, errors.New("lifecycle boundary payload must be supplied as a Go-owned value")
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Result{}, fmt.Errorf("encode lifecycle hook payload: %w", err)
		}
		input.Payload = raw
	}
	result, err := engine.Execute(ctx, input)
	if err != nil {
		return result, err
	}
	if result.Denied {
		return result, DeniedError{}
	}
	return result, nil
}

type AuditRecord struct {
	ID                string
	PluginID          string
	PluginFingerprint string
	HookID            string
	Action            Action
	Rejected          *bool
	Event             Event
	RunID             string
	WorkspaceID       string
	ToolName          string
	Outcome           string
	CreatedAt         time.Time
}

type AuditSink interface {
	RecordHookAudit(context.Context, AuditRecord) error
}

type Engine struct {
	mu            sync.RWMutex
	registrations []Registration
	generation    string
	sink          AuditSink
	now           func() time.Time
	loader        func(context.Context) ([]Registration, error)
}

func NewEngine(sink AuditSink) *Engine {
	return &Engine{sink: sink, now: func() time.Time { return time.Now().UTC() },
		generation: emptyGeneration()}
}

// WithLoader refreshes the enabled declarative registry at each hook boundary.
// A load failure is returned to the caller so pre-operation boundaries fail
// closed instead of silently using stale or incomplete policy.
func (e *Engine) WithLoader(loader func(context.Context) ([]Registration, error)) *Engine {
	if e != nil {
		e.loader = loader
	}
	return e
}

func (e *Engine) Replace(registrations []Registration) error {
	if e == nil {
		return errors.New("hook engine is required")
	}
	if len(registrations) > MaxDeclarations {
		return fmt.Errorf("hook registry exceeds %d declarations", MaxDeclarations)
	}
	values := slices.Clone(registrations)
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return err
		}
		key := value.PluginID + "\x00" + value.Declaration.ID
		if _, found := seen[key]; found {
			return errors.New("hook registry repeats a plugin hook identity")
		}
		seen[key] = struct{}{}
	}
	sort.Slice(values, func(i, j int) bool {
		left, right := eventOrder(values[i].Declaration.Event), eventOrder(values[j].Declaration.Event)
		if left != right {
			return left < right
		}
		if values[i].PluginID != values[j].PluginID {
			return values[i].PluginID < values[j].PluginID
		}
		return values[i].Declaration.ID < values[j].Declaration.ID
	})
	raw, _ := json.Marshal(values)
	digest := sha256.Sum256(append([]byte(ProtocolVersion+"\x00"), raw...))
	e.mu.Lock()
	e.registrations = values
	e.generation = hex.EncodeToString(digest[:])
	e.mu.Unlock()
	return nil
}

func (e *Engine) Execute(ctx context.Context, input Input) (Result, error) {
	if e == nil {
		return Result{}, errors.New("hook engine is required")
	}
	if err := input.Validate(); err != nil {
		return Result{}, err
	}
	if e.loader != nil {
		registrations, err := e.loader(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("refresh restricted hooks: %w", err)
		}
		if err := e.Replace(registrations); err != nil {
			return Result{}, fmt.Errorf("refresh restricted hooks: %w", err)
		}
	}
	e.mu.RLock()
	registrations := slices.Clone(e.registrations)
	generation := e.generation
	e.mu.RUnlock()
	result := Result{ProtocolVersion: ProtocolVersion, Generation: generation,
		Payload: append(json.RawMessage(nil), input.Payload...), Executed: []string{}}
	for _, registration := range registrations {
		declaration := registration.Declaration
		if declaration.Event != input.Event || !matchesTool(declaration.ToolNames, input.ToolName) {
			continue
		}
		hookCtx, cancel := context.WithTimeout(ctx,
			time.Duration(declaration.TimeoutMillis)*time.Millisecond)
		err := e.apply(hookCtx, registration, input, &result)
		cancel()
		if err != nil {
			if declaration.FailurePolicy == FailureDeny {
				result.Denied = true
				result.Reason = "hook " + declaration.ID + " failed closed"
				_ = e.record(context.WithoutCancel(ctx), registration, input, "failed_closed")
				return result, nil
			}
			_ = e.record(context.WithoutCancel(ctx), registration, input, "failed_continue")
			continue
		}
		if err := e.record(context.WithoutCancel(ctx), registration, input, "completed"); err != nil {
			if declaration.FailurePolicy == FailureDeny {
				result.Denied = true
				result.Reason = "hook " + declaration.ID + " audit failed closed"
				return result, nil
			}
			continue
		}
		result.Executed = append(result.Executed,
			registration.PluginID+"/"+declaration.ID)
		if result.Denied {
			break
		}
		// Post-occurrence boundaries observe work that already happened; an
		// observation-output overflow there cannot retroactively fail it.
		// Cap the output and record the drop instead of failing the boundary
		// — otherwise a declaration set that exceeds the annotation, output,
		// or identity bounds (each registration individually valid) would
		// block run finalization, terminal transitions, and session
		// archiving at every fire. Capping runs after the append so the
		// result is within bounds at the end of every iteration.
		if postOccurrenceEvent(input.Event) {
			if capObservationOutput(&result) {
				_ = e.record(context.WithoutCancel(ctx), registration, input, "output_capped")
			}
		}
		// Pre-occurrence boundaries fail closed on an unbounded observation
		// output; post-occurrence boundaries were capped above before this
		// check could trip.
		if len(result.Annotations) > MaxAnnotations {
			return Result{}, errors.New("hook annotation output exceeds its limit")
		}
		encoded, _ := json.Marshal(result)
		if len(encoded) > MaxOutputBytes {
			return Result{}, errors.New("hook output exceeds its limit")
		}
	}
	return result, nil
}

// postOccurrenceEvent reports events whose boundaries observe work that has
// already happened: post_tool observes a completed tool call, run_completed
// and session_closed observe completed runs and sessions. An observation
// output overflow at these boundaries cannot retroactively fail the work —
// the same continue-only contract that keeps retroactive denial policies out
// of these events — so the engine caps the output and records the drop.
func postOccurrenceEvent(event Event) bool {
	switch event {
	case PostTool, RunCompleted, SessionClosed:
		return true
	default:
		return false
	}
}

// capObservationOutput trims a post-occurrence result until its observation
// output fits the annotation and output bounds, reporting whether anything
// was dropped. Annotations are dropped newest-first; a payload echo that
// alone exceeds the output bound is dropped next — narrow hooks are
// pre_tool-only, so a post-occurrence payload is pure observation echo; and
// an executed list that still overflows (64 registrations of max-length
// identities exceed the bound on their own) drops oldest entries last — the
// audit rows keep every hook's record, the list is only a projection. The
// caller records an output_capped outcome for the boundary fire that tripped
// the cap, so every drop is auditable.
func capObservationOutput(result *Result) bool {
	capped := false
	for len(result.Annotations) > MaxAnnotations {
		result.Annotations = result.Annotations[:MaxAnnotations]
		capped = true
	}
	for {
		encoded, err := json.Marshal(*result)
		if err != nil || len(encoded) <= MaxOutputBytes {
			return capped
		}
		if len(result.Annotations) > 0 {
			result.Annotations = result.Annotations[:len(result.Annotations)-1]
			capped = true
			continue
		}
		if len(result.Payload) != 0 {
			result.Payload = nil
			capped = true
			continue
		}
		if len(result.Executed) > 0 {
			result.Executed = result.Executed[1:]
			capped = true
			continue
		}
		return capped
	}
}

func (e *Engine) apply(ctx context.Context, registration Registration, input Input,
	result *Result,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	declaration := registration.Declaration
	switch declaration.Action {
	case ActionDeny:
		result.Denied = true
		result.Reason = declaration.Message
	case ActionAnnotate:
		result.Annotations = append(result.Annotations, Annotation{PluginID: registration.PluginID,
			HookID: declaration.ID, Message: declaration.Message})
	case ActionNarrow:
		var value map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(result.Payload))
		if err := decoder.Decode(&value); err != nil {
			return errors.New("narrow hook requires an object payload")
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("narrow hook payload contains trailing JSON")
		}
		for _, field := range declaration.RemoveFields {
			delete(value, field)
		}
		narrowed, err := json.Marshal(value)
		if err != nil || len(narrowed) > len(result.Payload) {
			return errors.New("narrow hook attempted to expand its payload")
		}
		result.Payload = narrowed
	case ActionRecord:
		// The audit record emitted by Execute is the only side effect.
	default:
		return errors.New("unsupported hook action")
	}
	return nil
}

func (e *Engine) record(ctx context.Context, registration Registration, input Input,
	outcome string,
) error {
	if e.sink == nil {
		return nil
	}
	recordCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return e.sink.RecordHookAudit(recordCtx, AuditRecord{PluginID: registration.PluginID,
		PluginFingerprint: registration.PluginFingerprint, Action: registration.Declaration.Action,
		Rejected: boolPointer(outcome == "failed_closed" || registration.Declaration.Action == ActionDeny && outcome == "completed"),
		HookID:   registration.Declaration.ID, Event: input.Event, RunID: input.RunID,
		WorkspaceID: input.WorkspaceID, ToolName: input.ToolName, Outcome: outcome,
		CreatedAt: e.now()})
}

func boolPointer(value bool) *bool { return &value }

func (e *Engine) Generation() string {
	if e == nil {
		return emptyGeneration()
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.generation
}

func matchesTool(values []string, toolName string) bool {
	return len(values) == 0 || slices.Contains(values, toolName)
}

func eventOrder(value Event) int {
	for index, current := range []Event{PreTool, PostTool, RunStarted, RunCompleted,
		SessionOpened, SessionClosed, Compaction, Subagent, Checkpoint} {
		if current == value {
			return index
		}
	}
	return 100
}

func duplicatesOrInvalid(values []string, field bool) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		valid := validText(value, MaxHookIdentityRunes, false)
		if field {
			valid = valid && !strings.ContainsAny(value, ".[]/\\")
		}
		if !valid {
			return true
		}
		if _, found := seen[value]; found {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func validIdentity(value string) bool {
	return validText(value, MaxHookIdentityRunes, false) && !strings.ContainsAny(value, "/\\")
}

func validText(value string, maxBytes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || len([]byte(value)) > maxBytes ||
		strings.ContainsRune(value, 0) || value != strings.TrimSpace(value) {
		return false
	}
	if !allowEmpty && value == "" {
		return false
	}
	for _, current := range value {
		if current != '\n' && current != '\r' && current != '\t' && unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func emptyGeneration() string {
	digest := sha256.Sum256([]byte(ProtocolVersion))
	return hex.EncodeToString(digest[:])
}
