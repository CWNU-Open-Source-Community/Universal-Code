// Package hooks implements the declarative, Go-owned lifecycle hook runtime.
// Plugin packages can contribute rules, never executable handlers or scripts.
// Operator-owned script hooks are the one bounded exception: they are installed
// by the operator outside any plugin or repository surface, are hash-pinned and
// re-verified on every execution, and can only deny or annotate.
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
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ProtocolVersion         = "restricted-hooks.v1"
	MaxDeclarations         = 64
	MaxPayloadBytes         = 256 * 1024
	MaxOutputBytes          = 16 * 1024
	MaxAnnotations          = 16
	MaxAnnotationBytes      = 1024
	MaxHookTimeoutMillis    = 2_000
	MaxHookIdentityRunes    = 256
	MaxToolPatternBytes     = 256
	MaxAsyncConcurrent      = 8
	MaxAsyncAuditConcurrent = MaxDeclarations
)

type Event string

const (
	PreTool           Event = "pre_tool"
	PostTool          Event = "post_tool"
	PostToolFailure   Event = "post_tool_failure"
	RunStarted        Event = "run_started"
	RunCompleted      Event = "run_completed"
	SessionOpened     Event = "session_opened"
	SessionClosed     Event = "session_closed"
	UserPromptSubmit  Event = "user_prompt_submit"
	ApprovalRequested Event = "approval_requested"
	TurnEnd           Event = "turn_end"
	Compaction        Event = "compaction"
	PostCompaction    Event = "post_compaction"
	Subagent          Event = "subagent"
	Checkpoint        Event = "checkpoint"
)

func (e Event) Valid() bool {
	switch e {
	case PreTool, PostTool, PostToolFailure, RunStarted, RunCompleted, SessionOpened, SessionClosed,
		UserPromptSubmit, ApprovalRequested, TurnEnd, Compaction, PostCompaction, Subagent, Checkpoint:
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
	ActionScript   Action = "script"
)

func (a Action) Valid() bool {
	return a == ActionDeny || a == ActionAnnotate || a == ActionNarrow ||
		a == ActionRecord || a == ActionScript
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
	ToolPattern     string        `json:"tool_pattern,omitempty"`
	RemoveFields    []string      `json:"remove_fields,omitempty"`
	Message         string        `json:"message,omitempty"`
	Async           bool          `json:"async,omitempty"`
	Script          *ScriptSpec   `json:"script,omitempty"`
}

func (d Declaration) Validate() error {
	maxTimeout := MaxHookTimeoutMillis
	if d.Action == ActionScript {
		maxTimeout = MaxScriptTimeoutMillis
	}
	if d.ProtocolVersion != ProtocolVersion || !validIdentity(d.ID) || !d.Event.Valid() ||
		!d.Action.Valid() || !d.FailurePolicy.Valid() || d.TimeoutMillis < 1 ||
		d.TimeoutMillis > maxTimeout || len(d.ToolNames) > 64 ||
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
	if d.Action == ActionDeny && (d.Event == PostTool || d.Event == PostToolFailure) {
		return errors.New("post_tool hooks cannot retroactively deny a completed call")
	}
	if d.Async {
		// Asynchronous hooks are observation taps: the boundary returns
		// before they finish, so they can never deny, annotate the live
		// result, narrow tool arguments, or honor a fail-closed policy —
		// declaring one would silently turn fail-closed into fail-open.
		if d.Action != ActionRecord && d.Action != ActionScript {
			return errors.New("only record and script hooks may run asynchronously")
		}
		if d.FailurePolicy != FailureContinue {
			return errors.New("asynchronous hooks require the continue failure policy")
		}
	}
	if d.Action == ActionScript {
		if d.Script == nil {
			return errors.New("script hooks require a script specification")
		}
		if d.Event == PostTool || d.Event == PostToolFailure {
			return errors.New("post_tool hooks cannot run scripts because execution is already complete")
		}
		if err := d.Script.Validate(); err != nil {
			return err
		}
	}
	if d.Action != ActionScript && d.Script != nil {
		return errors.New("only script hooks may declare a script specification")
	}
	if (d.Event == PostTool || d.Event == PostToolFailure) && d.FailurePolicy == FailureDeny {
		return errors.New("post_tool hooks require continue failure policy because execution is already complete")
	}
	if d.Event == RunCompleted && d.FailurePolicy == FailureDeny {
		return errors.New("run_completed hooks require continue failure policy because the terminal transition is already decided")
	}
	if d.Event == SessionClosed && d.FailurePolicy == FailureDeny {
		return errors.New("session_closed hooks require continue failure policy because the session is already closed")
	}
	if d.Event == ApprovalRequested {
		// The approval decision belongs to the operator: hooks at this
		// boundary observe and record, they never auto-deny, annotate a
		// pending human decision, or rewrite anything. Whitelist form on
		// purpose: a future action type fails closed here instead of silently
		// gaining approval-gate authority.
		if d.Action != ActionRecord {
			return errors.New("approval_requested hooks may only record observations")
		}
	}
	if d.Event == TurnEnd {
		// The turn has already completed when this boundary fires: hooks
		// observe the outcome, they cannot deny, annotate, or narrow it.
		if d.Action != ActionRecord {
			return errors.New("turn_end hooks may only record observations")
		}
		if d.FailurePolicy == FailureDeny {
			return errors.New("turn_end hooks require continue failure policy because the turn is already complete")
		}
	}
	if d.Event == PostCompaction {
		// Compaction has already completed: hooks observe the outcome.
		if d.Action != ActionRecord {
			return errors.New("post_compaction hooks may only record observations")
		}
		if d.FailurePolicy == FailureDeny {
			return errors.New("post_compaction hooks require continue failure policy because compaction is already complete")
		}
	}
	if d.Event != PreTool && d.Event != PostTool && d.Event != PostToolFailure && len(d.ToolNames) != 0 {
		return errors.New("non-tool hooks cannot declare tool filters")
	}
	if d.Event != PreTool && d.Event != PostTool && d.Event != PostToolFailure && d.ToolPattern != "" {
		return errors.New("non-tool hooks cannot declare tool filters")
	}
	if d.ToolPattern != "" && len(d.ToolNames) != 0 {
		return errors.New("a hook declares either exact tool names or one tool pattern")
	}
	if d.ToolPattern != "" {
		if !validText(d.ToolPattern, MaxToolPatternBytes, false) {
			return errors.New("hook tool pattern is invalid or oversized")
		}
		// The web parser's trim check also strips a leading BOM; reject it
		// here so the projection and the parser agree on every byte.
		if strings.ContainsRune(d.ToolPattern, '\uFEFF') || strings.ContainsAny(d.ToolPattern, "\n\r\t") {
			return errors.New("hook tool pattern contains a byte order mark or control character")
		}
		if _, err := regexp.Compile(d.ToolPattern); err != nil {
			return fmt.Errorf("hook tool pattern does not compile: %w", err)
		}
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

// ValidateDeclarative validates a declaration that must not execute scripts.
// Plugin packages contribute declarative rules only: script hooks are
// operator-owned, and plugin manifests cannot bind the operator's script
// executor or pre-compute a digest for one.
func ValidateDeclarative(d Declaration) error {
	if d.Action == ActionScript {
		return errors.New("plugin hooks cannot execute scripts")
	}
	if d.Script != nil {
		return errors.New("plugin hooks cannot declare a script specification")
	}
	return d.Validate()
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
	if (i.Event == PreTool || i.Event == PostTool || i.Event == PostToolFailure) && i.ToolName == "" {
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
	mu              sync.RWMutex
	registrations   []Registration
	toolPatterns    []*regexp.Regexp
	generation      string
	sink            AuditSink
	now             func() time.Time
	loader          func(context.Context) ([]Registration, error)
	scriptRunner    ScriptExecutor
	asyncSlots      chan struct{}
	asyncAuditSlots chan struct{}
	droppedAudits   atomic.Uint64
}

func NewEngine(sink AuditSink) *Engine {
	return &Engine{sink: sink, now: func() time.Time { return time.Now().UTC() },
		generation: emptyGeneration(), asyncSlots: make(chan struct{}, MaxAsyncConcurrent),
		asyncAuditSlots: make(chan struct{}, MaxAsyncAuditConcurrent)}
}

// DroppedAudits reports the number of asynchronous hook audits dropped due to
// audit pool saturation under extreme load.
func (e *Engine) DroppedAudits() uint64 {
	if e == nil {
		return 0
	}
	return e.droppedAudits.Load()
}

// WithScriptExecutor wires the host-owned script execution adapter. Without it,
// script declarations fail closed at every boundary.
func (e *Engine) WithScriptExecutor(runner ScriptExecutor) *Engine {
	if e != nil {
		e.scriptRunner = runner
	}
	return e
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
		if value.Declaration.Action == ActionScript && value.PluginID != OperatorPluginID {
			return errors.New("script hooks are reserved for the operator source")
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
	// Validate already compiled and rejected every pattern, so this loop is
	// defensive depth: keep the compiled forms aligned with the sorted
	// registrations without trusting the earlier pass.
	toolPatterns := make([]*regexp.Regexp, len(values))
	for index, value := range values {
		if value.Declaration.ToolPattern != "" {
			compiled, err := regexp.Compile(value.Declaration.ToolPattern)
			if err != nil {
				return fmt.Errorf("hook tool pattern does not compile: %w", err)
			}
			toolPatterns[index] = compiled
		}
	}
	e.mu.Lock()
	e.registrations = values
	e.toolPatterns = toolPatterns
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
	toolPatterns := append([]*regexp.Regexp(nil), e.toolPatterns...)
	generation := e.generation
	e.mu.RUnlock()
	result := Result{ProtocolVersion: ProtocolVersion, Generation: generation,
		Payload: append(json.RawMessage(nil), input.Payload...), Executed: []string{}}
	for index, registration := range registrations {
		declaration := registration.Declaration
		var toolPattern *regexp.Regexp
		if index < len(toolPatterns) {
			toolPattern = toolPatterns[index]
		}
		if declaration.Event != input.Event ||
			!matchesTool(declaration.ToolNames, toolPattern, input.ToolName) {
			continue
		}
		if declaration.Async {
			e.launchAsync(input, registration)
			continue
		}
		hookCtx, cancel := context.WithTimeout(ctx,
			time.Duration(declaration.TimeoutMillis)*time.Millisecond)
		err := e.apply(hookCtx, registration, input, &result)
		cancel()
		if err != nil {
			if errors.Is(err, ErrScriptIntegrity) || declaration.FailurePolicy == FailureDeny {
				result.Denied = true
				if errors.Is(err, ErrScriptIntegrity) {
					result.Reason = "hook " + declaration.ID + " integrity verification failed"
				} else {
					result.Reason = "hook " + declaration.ID + " failed closed"
				}
				_ = e.record(context.WithoutCancel(ctx), registration, input, "failed_closed", true)
				return result, nil
			}
			_ = e.record(context.WithoutCancel(ctx), registration, input, "failed_continue", false)
			continue
		}
		rejected := declaration.Action == ActionDeny ||
			declaration.Action == ActionScript && result.Denied
		if err := e.record(context.WithoutCancel(ctx), registration, input, "completed", rejected); err != nil {
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
		if len(result.Annotations) > MaxAnnotations {
			return Result{}, errors.New("hook annotation output exceeds its limit")
		}
		// The input payload is bounded independently by Input.Validate and is
		// echoed back unchanged (narrowing only ever shrinks it); the output
		// limit governs hook-produced content only, so a legitimately large
		// tool payload does not fail every matching hook.
		hookProduced := result
		hookProduced.Payload = nil
		encoded, _ := json.Marshal(hookProduced)
		if len(encoded) > MaxOutputBytes {
			return Result{}, errors.New("hook output exceeds its limit")
		}
	}
	return result, nil
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
	case ActionScript:
		if e.scriptRunner == nil {
			return errors.New("script hook executor is unavailable")
		}
		invocation := ScriptInvocation{HookID: declaration.ID, Event: declaration.Event,
			RunID: input.RunID, WorkspaceID: input.WorkspaceID, ToolName: input.ToolName,
			Payload:      append(json.RawMessage(nil), result.Payload...),
			RelativePath: declaration.Script.Path, Interpreter: declaration.Script.Interpreter,
			Args:   slices.Clone(declaration.Script.Args),
			SHA256: declaration.Script.SHA256, TimeoutMillis: declaration.TimeoutMillis}
		output, err := e.scriptRunner.ExecuteScript(ctx, invocation)
		if err != nil {
			return err
		}
		if output.Denied {
			result.Denied = true
			result.Reason = output.Reason
		}
		for _, annotation := range output.Annotations {
			result.Annotations = append(result.Annotations, Annotation{PluginID: registration.PluginID,
				HookID: declaration.ID, Message: annotation.Message})
		}
	case ActionRecord:
		// The audit record emitted by Execute is the only side effect.
	default:
		return errors.New("unsupported hook action")
	}
	return nil
}

// launchAsync runs one observation-only hook outside the boundary. The
// boundary never waits for it: the result is discarded, only the audit ledger
// records the outcome, and an engine at its concurrency ceiling skips the run
// with a failed_continue audit row instead of blocking the caller. The audit
// row for a skipped or timed-out fire is written on its own detached context
// so a saturated engine or an expired hook deadline can never suppress it.
func (e *Engine) launchAsync(input Input, registration Registration) {
	// Copy the payload before the goroutine starts: the caller may reuse its
	// buffer after Execute returns.
	payload := append(json.RawMessage(nil), input.Payload...)
	detached := context.WithoutCancel(context.Background())
	select {
	case e.asyncSlots <- struct{}{}:
	default:
		if e.asyncAuditSlots != nil {
			select {
			case e.asyncAuditSlots <- struct{}{}:
				go func() {
					defer func() { <-e.asyncAuditSlots }()
					defer func() { _ = recover() }()
					_ = e.record(detached, registration, input, "failed_continue", false)
				}()
			default:
				e.droppedAudits.Add(1)
			}
		} else {
			e.droppedAudits.Add(1)
		}
		return
	}
	go func() {
		defer func() { <-e.asyncSlots }()
		// A panic in a background hook would otherwise take the whole host
		// down; synchronous hooks propagate to the caller instead.
		defer func() { _ = recover() }()
		asyncCtx, cancel := context.WithTimeout(detached,
			time.Duration(registration.Declaration.TimeoutMillis)*time.Millisecond)
		defer cancel()
		// The registration is a shallow copy whose slices and script pointer
		// are immutable after Replace; a concurrent registry swap never
		// mutates what this goroutine sees. The throwaway result carries the
		// bounded payload so background scripts observe the same input as
		// synchronous ones, and discards any denial an asynchronous script
		// prints: background hooks cannot block.
		throwaway := Result{Payload: append(json.RawMessage(nil), payload...)}
		err := e.apply(asyncCtx, registration, Input{Event: input.Event,
			RunID: input.RunID, WorkspaceID: input.WorkspaceID,
			ToolName: input.ToolName, Payload: payload}, &throwaway)
		outcome := "completed"
		if err != nil {
			outcome = "failed_continue"
		}
		_ = e.record(context.WithoutCancel(detached), registration, input, outcome, false)
	}()
}

func (e *Engine) record(ctx context.Context, registration Registration, input Input,
	outcome string, rejected bool,
) error {
	if e.sink == nil {
		return nil
	}
	recordCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return e.sink.RecordHookAudit(recordCtx, AuditRecord{PluginID: registration.PluginID,
		PluginFingerprint: registration.PluginFingerprint, Action: registration.Declaration.Action,
		Rejected: boolPointer(rejected),
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

func matchesTool(values []string, pattern *regexp.Regexp, toolName string) bool {
	if pattern != nil {
		return pattern.MatchString(toolName)
	}
	return len(values) == 0 || slices.Contains(values, toolName)
}

func eventOrder(value Event) int {
	for index, current := range []Event{PreTool, PostTool, PostToolFailure, RunStarted, RunCompleted,
		SessionOpened, SessionClosed, UserPromptSubmit, ApprovalRequested, TurnEnd, Compaction, PostCompaction, Subagent, Checkpoint} {
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
