package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	ItemStreamProtocolVersion = "llm.item_stream.v1"
	MaxItemStreamEvents       = 16 * 1024
	MaxItemStreamIdentity     = 256
)

type StreamEventType string

const (
	StreamResponseStarted        StreamEventType = "response_started"
	StreamOutputItemStarted      StreamEventType = "output_item_started"
	StreamTextDelta              StreamEventType = "text_delta"
	StreamToolCallStarted        StreamEventType = "tool_call_started"
	StreamToolArgumentDelta      StreamEventType = "tool_argument_delta"
	StreamToolCallCompleted      StreamEventType = "tool_call_completed"
	StreamToolExecutionStarted   StreamEventType = "tool_execution_started"
	StreamToolExecutionCompleted StreamEventType = "tool_execution_completed"
	StreamOutputItemCompleted    StreamEventType = "output_item_completed"
	StreamResponseCompleted      StreamEventType = "response_completed"
	StreamResponseFailed         StreamEventType = "response_failed"
	StreamResponseCancelled      StreamEventType = "response_cancelled"
)

func (t StreamEventType) Valid() bool {
	switch t {
	case StreamResponseStarted, StreamOutputItemStarted, StreamTextDelta,
		StreamToolCallStarted, StreamToolArgumentDelta, StreamToolCallCompleted,
		StreamToolExecutionStarted, StreamToolExecutionCompleted,
		StreamOutputItemCompleted, StreamResponseCompleted, StreamResponseFailed,
		StreamResponseCancelled:
		return true
	default:
		return false
	}
}

type StreamItemType string

const (
	StreamItemMessage  StreamItemType = "message"
	StreamItemToolCall StreamItemType = "tool_call"
)

func (t StreamItemType) Valid() bool {
	return t == StreamItemMessage || t == StreamItemToolCall
}

type StreamItemStatus string

const (
	StreamItemInProgress         StreamItemStatus = "in_progress"
	StreamItemReadyForValidation StreamItemStatus = "ready_for_validation"
	StreamItemCompleted          StreamItemStatus = "completed"
	StreamItemFailed             StreamItemStatus = "failed"
	StreamItemCancelled          StreamItemStatus = "cancelled"
)

func (s StreamItemStatus) Valid() bool {
	switch s {
	case StreamItemInProgress, StreamItemReadyForValidation, StreamItemCompleted,
		StreamItemFailed, StreamItemCancelled:
		return true
	default:
		return false
	}
}

type StreamGranularity string

const (
	StreamGranularityDelta    StreamGranularity = "item_delta"
	StreamGranularityComplete StreamGranularity = "item_complete"
)

func (g StreamGranularity) Valid() bool {
	return g == StreamGranularityDelta || g == StreamGranularityComplete
}

// StreamEvent is the provider-neutral, ordered in-memory protocol. Text and
// argument deltas, and the completed call payload, deliberately have json:"-"
// tags so an accidental marshal cannot expose raw model output or arguments.
// Public and durable projections use OutputItem and StreamBoundary instead.
type StreamEvent struct {
	Version     string            `json:"version"`
	Sequence    int               `json:"sequence"`
	Type        StreamEventType   `json:"type"`
	ResponseID  string            `json:"response_id"`
	ItemID      string            `json:"item_id,omitempty"`
	CallID      string            `json:"call_id,omitempty"`
	ItemType    StreamItemType    `json:"item_type,omitempty"`
	ItemStatus  StreamItemStatus  `json:"item_status,omitempty"`
	ToolName    string            `json:"tool_name,omitempty"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Granularity StreamGranularity `json:"granularity,omitempty"`
	Provisional bool              `json:"provisional"`
	Durable     bool              `json:"durable"`
	Outcome     Outcome           `json:"outcome,omitempty"`

	TextDelta     string    `json:"-"`
	ArgumentDelta string    `json:"-"`
	CompletedCall *ToolCall `json:"-"`
	Usage         *Usage    `json:"-"`
	// DecodeMillis is measured within the provider adapter, before application
	// persistence or tool execution. Private generation contributes only time.
	DecodeMillis *int64 `json:"-"`
}

// OutputItem is the content-free item projection shared by completed model
// responses, old Session history, public live snapshots, and the durable
// Supervisor ledger. Tool arguments and private reasoning are never fields.
type OutputItem struct {
	ResponseID    string           `json:"response_id"`
	ID            string           `json:"id"`
	Type          StreamItemType   `json:"type"`
	Status        StreamItemStatus `json:"status"`
	CallID        string           `json:"call_id,omitempty"`
	DurableCallID string           `json:"durable_call_id,omitempty"`
	ToolName      string           `json:"tool_name,omitempty"`
	ArgumentBytes int              `json:"argument_bytes,omitempty"`
	Provisional   bool             `json:"provisional"`
	Durable       bool             `json:"durable"`
}

func (i OutputItem) Validate() error {
	if err := validateStreamIdentity(i.ResponseID, "response"); err != nil {
		return err
	}
	if err := validateStreamIdentity(i.ID, "item"); err != nil {
		return err
	}
	if !i.Type.Valid() || !i.Status.Valid() || i.ArgumentBytes < 0 ||
		i.ArgumentBytes > MaxProviderToolPayloadSize || (i.Provisional && i.Durable) ||
		(i.Durable && i.Status != StreamItemCompleted) {
		return errors.New("output item state is invalid")
	}
	if i.Type == StreamItemMessage {
		if i.CallID != "" || i.DurableCallID != "" || i.ToolName != "" || i.ArgumentBytes != 0 {
			return errors.New("message output item contains tool metadata")
		}
		if i.Status == StreamItemReadyForValidation {
			return errors.New("message output item cannot await tool validation")
		}
		return nil
	}
	if err := validateStreamIdentity(i.CallID, "call"); err != nil {
		return err
	}
	if i.DurableCallID != "" {
		if err := validateStreamIdentity(i.DurableCallID, "durable call"); err != nil {
			return err
		}
		if !i.Durable || i.Status != StreamItemCompleted {
			return errors.New("durable call identity requires a completed durable item")
		}
	}
	if err := validateToolName(i.ToolName); err != nil {
		return err
	}
	return nil
}

// StreamBoundary is the only item-stream shape written into model.delta. It
// carries identities and lifecycle state, never text, arguments, usage, raw
// provider payloads, credentials, or reasoning.
type StreamBoundary struct {
	Sequence    int               `json:"sequence"`
	Type        StreamEventType   `json:"type"`
	ItemID      string            `json:"item_id,omitempty"`
	CallID      string            `json:"call_id,omitempty"`
	ItemType    StreamItemType    `json:"item_type,omitempty"`
	ItemStatus  StreamItemStatus  `json:"item_status,omitempty"`
	ToolName    string            `json:"tool_name,omitempty"`
	Granularity StreamGranularity `json:"granularity,omitempty"`
	Outcome     Outcome           `json:"outcome,omitempty"`
}

func (b StreamBoundary) Validate(responseID string) error {
	if b.Sequence <= 0 || b.Sequence > MaxItemStreamEvents || !b.Type.Valid() {
		return errors.New("stream boundary sequence or type is invalid")
	}
	switch b.Type {
	case StreamResponseStarted:
		if b.ItemID != "" || b.CallID != "" || b.ItemType != "" || b.ItemStatus != "" ||
			b.ToolName != "" || !b.Granularity.Valid() || b.Outcome != "" {
			return errors.New("response start boundary contains item metadata")
		}
	case StreamResponseCompleted:
		if b.ItemID != "" || b.CallID != "" || b.ItemType != "" || b.ItemStatus != "" ||
			b.ToolName != "" || b.Granularity != "" || b.Outcome != OutcomeSuccess {
			return errors.New("response completion boundary is invalid")
		}
	case StreamResponseFailed:
		if b.ItemID != "" || b.CallID != "" || b.ItemType != "" || b.ItemStatus != "" ||
			b.ToolName != "" || b.Granularity != "" || !b.Outcome.Valid() || b.Outcome == OutcomeSuccess ||
			b.Outcome == OutcomeCancelled {
			return errors.New("response failure boundary is invalid")
		}
	case StreamResponseCancelled:
		if b.ItemID != "" || b.CallID != "" || b.ItemType != "" || b.ItemStatus != "" ||
			b.ToolName != "" || b.Granularity != "" || b.Outcome != OutcomeCancelled {
			return errors.New("response cancellation boundary is invalid")
		}
	case StreamOutputItemStarted:
		if err := validateStreamIdentity(b.ItemID, "item"); err != nil {
			return err
		}
		if !b.ItemType.Valid() || b.ItemStatus != StreamItemInProgress || b.Granularity != "" || b.Outcome != "" ||
			b.CallID != "" || b.ToolName != "" {
			return errors.New("output item start boundary is invalid")
		}
	case StreamOutputItemCompleted:
		if err := validateStreamIdentity(b.ItemID, "item"); err != nil {
			return err
		}
		if !b.ItemType.Valid() || b.ItemStatus != StreamItemCompleted || b.Granularity != "" || b.Outcome != "" {
			return errors.New("output item boundary is invalid")
		}
		if b.ItemType == StreamItemMessage {
			if b.CallID != "" || b.ToolName != "" {
				return errors.New("message boundary contains tool metadata")
			}
		} else if err := validateBoundaryTool(b); err != nil {
			return err
		}
	case StreamToolCallStarted, StreamToolCallCompleted,
		StreamToolExecutionStarted, StreamToolExecutionCompleted:
		if err := validateStreamIdentity(b.ItemID, "item"); err != nil {
			return err
		}
		if b.ItemType != StreamItemToolCall || !b.ItemStatus.Valid() || b.Granularity != "" || b.Outcome != "" {
			return errors.New("tool boundary state is invalid")
		}
		if err := validateBoundaryTool(b); err != nil {
			return err
		}
	default:
		return errors.New("delta-bearing event cannot be persisted as a boundary")
	}
	return validateStreamIdentity(responseID, "stream boundary response")
}

func validateBoundaryTool(boundary StreamBoundary) error {
	if err := validateStreamIdentity(boundary.CallID, "call"); err != nil {
		return err
	}
	return validateToolName(boundary.ToolName)
}

func BoundaryForEvent(event StreamEvent) (StreamBoundary, bool) {
	switch event.Type {
	case StreamResponseStarted, StreamOutputItemStarted, StreamToolCallStarted,
		StreamToolCallCompleted, StreamToolExecutionStarted, StreamToolExecutionCompleted,
		StreamOutputItemCompleted, StreamResponseCompleted, StreamResponseFailed,
		StreamResponseCancelled:
		return StreamBoundary{Sequence: event.Sequence, Type: event.Type, ItemID: event.ItemID,
			CallID: event.CallID, ItemType: event.ItemType, ItemStatus: event.ItemStatus,
			ToolName: SafeStreamToolName(event.ToolName), Granularity: event.Granularity,
			Outcome: event.Outcome}, true
	default:
		return StreamBoundary{}, false
	}
}

// SafeStreamToolName keeps content-free projections valid without allowing a
// provider-controlled name that resembles a credential to enter public or
// durable state.
func SafeStreamToolName(value string) string {
	if value == "" {
		return ""
	}
	safe := redact.String(value)
	if safe != value {
		return "redacted_tool"
	}
	return safe
}

func StableStreamID(kind string, parts ...string) string {
	hash := sha256.New()
	var size [8]byte
	values := append([]string{"llm.item_stream.identity.v1", strings.TrimSpace(kind)}, parts...)
	for _, value := range values {
		binary.BigEndian.PutUint64(size[:], uint64(len([]byte(value))))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	prefix := "stream"
	switch strings.TrimSpace(kind) {
	case "response":
		prefix = "resp"
	case "item":
		prefix = "item"
	case "call":
		prefix = "call"
	}
	return prefix + "_" + hex.EncodeToString(hash.Sum(nil))[:24]
}

// CompleteOutputItems converts a validated response into its durable,
// content-free item projection and binds streamed calls to the deterministic
// Go-issued call IDs used by the Supervisor tool ledger.
func CompleteOutputItems(responseID string, text string, calls []ToolCall,
	items []OutputItem,
) ([]ToolCall, []OutputItem, error) {
	if err := validateStreamIdentity(responseID, "completed response"); err != nil {
		return nil, nil, err
	}
	normalized, err := NormalizeToolCalls(calls)
	if err != nil {
		return nil, nil, err
	}
	if len(items) > MaxProviderOutputItems {
		return nil, nil, errors.New("completed response contains too many output items")
	}
	if len(items) == 0 {
		projected := make([]OutputItem, 0, len(normalized)+1)
		position := 0
		if text != "" || len(normalized) == 0 {
			position++
			projected = append(projected, OutputItem{ResponseID: responseID,
				ID:   StableStreamID("item", responseID, strconv.Itoa(position)),
				Type: StreamItemMessage, Status: StreamItemCompleted, Durable: true})
		}
		for index := range normalized {
			position++
			if normalized[index].StreamResponseID == "" {
				normalized[index].StreamResponseID = responseID
				normalized[index].StreamItemID = StableStreamID("item", responseID,
					strconv.Itoa(position))
				normalized[index].StreamCallID = StableStreamID("call", responseID,
					strconv.Itoa(index+1))
			}
			if normalized[index].StreamResponseID != responseID {
				return nil, nil, errors.New("completed tool call response identity changed")
			}
			projected = append(projected, OutputItem{ResponseID: responseID,
				ID: normalized[index].StreamItemID, Type: StreamItemToolCall,
				Status: StreamItemCompleted, CallID: normalized[index].StreamCallID,
				DurableCallID: normalized[index].ID, ToolName: normalized[index].Name,
				ArgumentBytes: len(normalized[index].Arguments), Durable: true})
		}
		if len(projected) == 0 {
			return nil, nil, errors.New("completed response contains no output items")
		}
		return normalized, projected, nil
	}
	projected := append([]OutputItem(nil), items...)
	byItem := make(map[string]int, len(projected))
	for index := range projected {
		if projected[index].ResponseID != responseID || projected[index].Status != StreamItemCompleted {
			return nil, nil, errors.New("completed output item does not match its response")
		}
		if _, exists := byItem[projected[index].ID]; exists {
			return nil, nil, errors.New("completed output item id was reused")
		}
		projected[index].Provisional = false
		projected[index].Durable = true
		projected[index].DurableCallID = ""
		byItem[projected[index].ID] = index
	}
	seenCalls := make(map[string]struct{}, len(normalized))
	for index := range normalized {
		call := normalized[index]
		if call.StreamResponseID != responseID || call.StreamItemID == "" || call.StreamCallID == "" {
			return nil, nil, errors.New("completed tool call omitted its stream identities")
		}
		itemIndex, exists := byItem[call.StreamItemID]
		if !exists || projected[itemIndex].Type != StreamItemToolCall ||
			projected[itemIndex].CallID != call.StreamCallID ||
			projected[itemIndex].ToolName != call.Name {
			return nil, nil, errors.New("completed tool call does not match its output item")
		}
		if _, exists := seenCalls[call.StreamCallID]; exists {
			return nil, nil, errors.New("completed stream call id was reused")
		}
		seenCalls[call.StreamCallID] = struct{}{}
		projected[itemIndex].DurableCallID = call.ID
		// The durable projection reports the normalized, policy-safe payload
		// size rather than the provisional provider byte count.
		projected[itemIndex].ArgumentBytes = len(call.Arguments)
	}
	for _, item := range projected {
		if err := item.Validate(); err != nil {
			return nil, nil, err
		}
		if item.Type == StreamItemToolCall && item.DurableCallID == "" {
			return nil, nil, errors.New("completed tool item has no durable call binding")
		}
	}
	return normalized, projected, nil
}

type itemStreamMode uint8

const (
	itemStreamUnknown itemStreamMode = iota
	itemStreamLegacy
	itemStreamExplicit
)

type streamAccumulatorItem struct {
	rawID         string
	id            string
	typeName      StreamItemType
	status        StreamItemStatus
	rawCallID     string
	callID        string
	toolOrdinal   int
	toolName      string
	argumentBytes int
	argumentParts []string
	argumentDelta bool
	callComplete  bool
	itemComplete  bool
}

// ItemStreamAccumulator validates an explicit provider stream or projects a
// legacy ChatChunk stream into the same protocol. It replaces provider-owned
// identities with stable attempt-owned identities before any public or durable
// consumer sees them.
type ItemStreamAccumulator struct {
	responseID string
	provider   string
	model      string
	mode       itemStreamMode

	rawSequence int
	sequence    int
	rawResponse string
	rawModel    string
	started     bool
	terminal    bool
	succeeded   bool
	granularity StreamGranularity
	textBytes   int
	textTail    []byte

	items          map[string]*streamAccumulatorItem
	itemOrder      []*streamAccumulatorItem
	legacyText     *streamAccumulatorItem
	completedCalls []ToolCall
}

func NewItemStreamAccumulator(responseID string, provider string, model string) (*ItemStreamAccumulator, error) {
	responseID = strings.TrimSpace(responseID)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if err := validateStreamIdentity(responseID, "response"); err != nil {
		return nil, err
	}
	if provider == "" || model == "" || !utf8.ValidString(provider) || !utf8.ValidString(model) ||
		len([]rune(provider)) > MaxItemStreamIdentity || len([]rune(model)) > MaxItemStreamIdentity {
		return nil, errors.New("item stream provider and model are required and bounded")
	}
	return &ItemStreamAccumulator{responseID: responseID, provider: provider, model: model,
		items: map[string]*streamAccumulatorItem{}}, nil
}

func (a *ItemStreamAccumulator) Consume(chunk ChatChunk) (ChatChunk, []StreamEvent, error) {
	if a == nil {
		return ChatChunk{}, nil, errors.New("item stream accumulator is required")
	}
	// A malformed compatibility chunk must not partially advance the stream.
	// Providers may place several item events in one wire chunk, so validation is
	// transactional and Abort can always append a contiguous failure terminal.
	candidate := a.clone()
	normalized, events, err := candidate.consume(chunk)
	if err != nil {
		return ChatChunk{}, nil, err
	}
	*a = *candidate
	return normalized, events, nil
}

func (a *ItemStreamAccumulator) consume(chunk ChatChunk) (ChatChunk, []StreamEvent, error) {
	if a.terminal {
		return ChatChunk{}, nil, errors.New("item stream emitted a chunk after its terminal event")
	}
	if err := a.validateCompatibilityIdentity(chunk, true); err != nil {
		return ChatChunk{}, nil, err
	}
	if len(chunk.Events) == 0 {
		compatibilityData := chunk.Text != "" || chunk.Done || len(chunk.ToolCalls) != 0 ||
			chunk.Usage != nil || chunk.Err != nil || chunk.Provider != "" || chunk.Model != ""
		if a.mode == itemStreamExplicit && compatibilityData {
			return ChatChunk{}, nil, errors.New("explicit item stream chunk omitted its events")
		}
		if a.mode == itemStreamUnknown && !compatibilityData {
			return chunk, nil, nil
		}
		if a.mode == itemStreamUnknown {
			a.mode = itemStreamLegacy
		}
		if a.mode == itemStreamLegacy {
			return a.consumeLegacy(chunk)
		}
		return chunk, nil, nil
	}
	if a.mode == itemStreamLegacy {
		return ChatChunk{}, nil, errors.New("provider mixed legacy chunks with explicit item events")
	}
	a.mode = itemStreamExplicit
	events := make([]StreamEvent, 0, len(chunk.Events))
	text := strings.Builder{}
	var completedUsage *Usage
	terminalType := StreamEventType("")
	for _, raw := range chunk.Events {
		event, err := a.consumeExplicit(raw)
		if err != nil {
			return ChatChunk{}, nil, err
		}
		events = append(events, event)
		if event.Type == StreamTextDelta {
			_, _ = text.WriteString(event.TextDelta)
		}
		if event.Type == StreamToolCallCompleted && event.CompletedCall != nil {
			a.completedCalls = append(a.completedCalls, cloneToolCall(*event.CompletedCall))
		}
		if event.Type == StreamResponseCompleted && event.Usage != nil {
			usage := *event.Usage
			completedUsage = &usage
		}
		if event.Type == StreamResponseCompleted || event.Type == StreamResponseFailed ||
			event.Type == StreamResponseCancelled {
			terminalType = event.Type
		}
	}
	if err := a.validateCompatibilityIdentity(chunk, false); err != nil {
		return ChatChunk{}, nil, err
	}
	if text.String() != chunk.Text {
		return ChatChunk{}, nil, errors.New("item stream text events do not match their compatibility chunk")
	}
	if chunk.Done {
		if chunk.Err != nil || terminalType != StreamResponseCompleted {
			return ChatChunk{}, nil, errors.New("item stream success terminal does not match its compatibility chunk")
		}
		if !a.succeeded || completedUsage == nil || chunk.Usage == nil || *completedUsage != *chunk.Usage {
			return ChatChunk{}, nil, errors.New("item stream completion does not match its compatibility chunk")
		}
		if !sameToolCalls(a.completedCalls, chunk.ToolCalls) {
			return ChatChunk{}, nil, errors.New("item stream completed calls do not match their compatibility chunk")
		}
		annotated, err := a.annotateFinalCalls(chunk.ToolCalls)
		if err != nil {
			return ChatChunk{}, nil, err
		}
		chunk.ToolCalls = annotated
	} else if chunk.Err != nil {
		if terminalType != StreamResponseFailed && terminalType != StreamResponseCancelled {
			return ChatChunk{}, nil, errors.New("item stream failure omitted its matching terminal event")
		}
	} else if terminalType != "" {
		return ChatChunk{}, nil, errors.New("item stream terminal event was not reflected by its compatibility chunk")
	}
	chunk.Events = events
	return chunk, events, nil
}

func (a *ItemStreamAccumulator) consumeExplicit(raw StreamEvent) (StreamEvent, error) {
	if raw.Version != ItemStreamProtocolVersion || !raw.Type.Valid() || raw.Sequence != a.rawSequence+1 ||
		raw.Sequence > MaxItemStreamEvents || !raw.Provisional || raw.Durable {
		return StreamEvent{}, errors.New("provider item stream envelope is invalid")
	}
	if strings.TrimSpace(raw.Provider) != a.provider || strings.TrimSpace(raw.Model) == "" ||
		!utf8.ValidString(raw.Model) || len([]rune(raw.Model)) > MaxItemStreamIdentity {
		return StreamEvent{}, errors.New("provider item stream returned an invalid provider or model identity")
	}
	if err := validateStreamIdentity(raw.ResponseID, "provider response"); err != nil {
		return StreamEvent{}, err
	}
	if a.terminal {
		return StreamEvent{}, errors.New("provider item stream emitted an event after its terminal event")
	}
	a.rawSequence++
	a.sequence++
	event := raw
	event.Sequence = a.sequence
	event.ResponseID = a.responseID
	switch raw.Type {
	case StreamResponseStarted:
		if a.started || !raw.Granularity.Valid() || raw.ItemID != "" || raw.CallID != "" ||
			raw.ItemType != "" || raw.ItemStatus != "" || raw.ToolName != "" || raw.Outcome != "" ||
			raw.TextDelta != "" || raw.ArgumentDelta != "" || raw.CompletedCall != nil || raw.Usage != nil {
			return StreamEvent{}, errors.New("provider response start event is invalid")
		}
		a.started = true
		a.rawResponse = raw.ResponseID
		a.rawModel = strings.TrimSpace(raw.Model)
		a.granularity = raw.Granularity
	case StreamOutputItemStarted:
		if err := a.requireStartedRawResponse(raw); err != nil {
			return StreamEvent{}, err
		}
		if !raw.ItemType.Valid() || raw.ItemStatus != StreamItemInProgress || raw.CallID != "" ||
			raw.ToolName != "" || raw.Outcome != "" || raw.TextDelta != "" ||
			raw.ArgumentDelta != "" || raw.CompletedCall != nil || raw.Usage != nil {
			return StreamEvent{}, errors.New("provider output item start event is invalid")
		}
		if err := validateStreamIdentity(raw.ItemID, "provider item"); err != nil {
			return StreamEvent{}, err
		}
		if _, exists := a.items[raw.ItemID]; exists {
			return StreamEvent{}, errors.New("provider output item id was reused")
		}
		if len(a.itemOrder) >= MaxProviderOutputItems {
			return StreamEvent{}, errors.New("provider output item count exceeds its limit")
		}
		item := &streamAccumulatorItem{rawID: raw.ItemID,
			id:       StableStreamID("item", a.responseID, strconv.Itoa(len(a.itemOrder)+1)),
			typeName: raw.ItemType, status: StreamItemInProgress}
		if raw.ItemType == StreamItemToolCall {
			item.toolOrdinal = len(a.toolItems()) + 1
		}
		a.items[raw.ItemID] = item
		a.itemOrder = append(a.itemOrder, item)
		event.ItemID = item.id
	case StreamTextDelta:
		item, err := a.activeItem(raw, StreamItemMessage)
		if err != nil {
			return StreamEvent{}, err
		}
		if raw.CallID != "" || raw.ToolName != "" || raw.ItemStatus != StreamItemInProgress ||
			raw.Outcome != "" || raw.TextDelta == "" || raw.ArgumentDelta != "" ||
			raw.CompletedCall != nil || raw.Usage != nil {
			return StreamEvent{}, errors.New("provider text delta event is invalid")
		}
		if err := a.acceptText(raw.TextDelta); err != nil {
			return StreamEvent{}, err
		}
		event.ItemID = item.id
	case StreamToolCallStarted:
		item, err := a.activeItem(raw, StreamItemToolCall)
		if err != nil {
			return StreamEvent{}, err
		}
		if item.rawCallID != "" || raw.ItemStatus != StreamItemInProgress || raw.Outcome != "" ||
			raw.TextDelta != "" || raw.ArgumentDelta != "" || raw.CompletedCall != nil || raw.Usage != nil {
			return StreamEvent{}, errors.New("provider tool call start event is invalid")
		}
		if err := validateStreamIdentity(raw.CallID, "provider call"); err != nil {
			return StreamEvent{}, err
		}
		if a.itemForRawCall(raw.CallID) != nil {
			return StreamEvent{}, errors.New("provider tool call id was reused")
		}
		if err := validateToolName(raw.ToolName); err != nil {
			return StreamEvent{}, err
		}
		item.rawCallID = raw.CallID
		item.callID = StableStreamID("call", a.responseID, strconv.Itoa(item.toolOrdinal))
		item.toolName = raw.ToolName
		event.ItemID, event.CallID = item.id, item.callID
	case StreamToolArgumentDelta:
		item, err := a.activeTool(raw)
		if err != nil {
			return StreamEvent{}, err
		}
		if raw.ItemStatus != StreamItemInProgress || raw.ToolName != item.toolName || raw.Outcome != "" ||
			raw.TextDelta != "" || raw.ArgumentDelta == "" || raw.CompletedCall != nil ||
			raw.Usage != nil || !utf8.ValidString(raw.ArgumentDelta) ||
			item.argumentBytes > MaxProviderToolPayloadSize-len(raw.ArgumentDelta) {
			return StreamEvent{}, errors.New("provider tool argument delta event is invalid")
		}
		item.argumentDelta = true
		item.argumentBytes += len(raw.ArgumentDelta)
		item.argumentParts = append(item.argumentParts, raw.ArgumentDelta)
		event.ItemID, event.CallID = item.id, item.callID
	case StreamToolCallCompleted:
		item, err := a.activeTool(raw)
		if err != nil {
			return StreamEvent{}, err
		}
		if item.callComplete || raw.ItemStatus != StreamItemReadyForValidation || raw.ToolName != item.toolName ||
			raw.Outcome != "" || raw.TextDelta != "" || raw.ArgumentDelta != "" ||
			raw.CompletedCall == nil || raw.Usage != nil {
			return StreamEvent{}, errors.New("provider tool call completion event is invalid")
		}
		call, err := NormalizeToolCall(*raw.CompletedCall)
		if err != nil || call.ID != item.rawCallID || call.Name != item.toolName {
			return StreamEvent{}, errors.New("provider completed tool call does not match its started item")
		}
		if item.argumentDelta && !bytes.Equal(bytes.TrimSpace([]byte(strings.Join(item.argumentParts, ""))),
			bytes.TrimSpace(call.Arguments)) {
			return StreamEvent{}, errors.New("provider completed tool arguments do not match their deltas")
		}
		call.StreamResponseID, call.StreamItemID, call.StreamCallID = a.responseID, item.id, item.callID
		if !item.argumentDelta {
			item.argumentBytes = len(call.Arguments)
		}
		item.callComplete = true
		item.status = StreamItemReadyForValidation
		event.ItemID, event.CallID = item.id, item.callID
		event.CompletedCall = &call
	case StreamOutputItemCompleted:
		item, err := a.activeItem(raw, raw.ItemType)
		if err != nil {
			return StreamEvent{}, err
		}
		if item.itemComplete || raw.ItemStatus != StreamItemCompleted || raw.Outcome != "" ||
			raw.TextDelta != "" || raw.ArgumentDelta != "" || raw.CompletedCall != nil || raw.Usage != nil {
			return StreamEvent{}, errors.New("provider output item completion event is invalid")
		}
		if item.typeName == StreamItemToolCall {
			if !item.callComplete || raw.CallID != item.rawCallID || raw.ToolName != item.toolName {
				return StreamEvent{}, errors.New("provider completed a tool item before its call")
			}
			event.CallID, event.ToolName = item.callID, item.toolName
		} else if raw.CallID != "" || raw.ToolName != "" {
			return StreamEvent{}, errors.New("provider message completion contains tool metadata")
		}
		item.itemComplete = true
		item.status = StreamItemCompleted
		event.ItemID = item.id
	case StreamResponseCompleted:
		if err := a.requireStartedRawResponse(raw); err != nil {
			return StreamEvent{}, err
		}
		if len(a.itemOrder) == 0 || len(a.textTail) != 0 || raw.Outcome != OutcomeSuccess || raw.Usage == nil ||
			raw.ItemID != "" || raw.CallID != "" || raw.ItemType != "" || raw.ItemStatus != "" ||
			raw.ToolName != "" || raw.TextDelta != "" || raw.ArgumentDelta != "" ||
			raw.CompletedCall != nil {
			return StreamEvent{}, errors.New("provider response completion event is invalid")
		}
		for _, item := range a.itemOrder {
			if !item.itemComplete {
				return StreamEvent{}, errors.New("provider response completed with an unfinished output item")
			}
		}
		if err := raw.Usage.Validate(); err != nil {
			return StreamEvent{}, errors.New("provider response completion usage is invalid")
		}
		a.terminal, a.succeeded = true, true
	case StreamResponseFailed, StreamResponseCancelled:
		if err := a.requireStartedRawResponse(raw); err != nil {
			return StreamEvent{}, err
		}
		if raw.ItemID != "" || raw.CallID != "" || raw.ItemType != "" || raw.ItemStatus != "" ||
			raw.ToolName != "" || raw.TextDelta != "" || raw.ArgumentDelta != "" ||
			raw.CompletedCall != nil || raw.Usage != nil {
			return StreamEvent{}, errors.New("provider response failure event contains item data")
		}
		if raw.Type == StreamResponseCancelled {
			if raw.Outcome != OutcomeCancelled {
				return StreamEvent{}, errors.New("provider response cancellation outcome is invalid")
			}
		} else if !raw.Outcome.Valid() || raw.Outcome == OutcomeSuccess || raw.Outcome == OutcomeCancelled {
			return StreamEvent{}, errors.New("provider response failure outcome is invalid")
		}
		a.failItems(raw.Type == StreamResponseCancelled)
		a.terminal = true
	case StreamToolExecutionStarted, StreamToolExecutionCompleted:
		return StreamEvent{}, errors.New("provider layer cannot emit tool execution events")
	default:
		return StreamEvent{}, errors.New("unsupported provider item stream event")
	}
	return event, nil
}

func (a *ItemStreamAccumulator) consumeLegacy(chunk ChatChunk) (ChatChunk, []StreamEvent, error) {
	if a.terminal {
		return ChatChunk{}, nil, errors.New("legacy stream emitted a chunk after its terminal chunk")
	}
	events := make([]StreamEvent, 0, 8+len(chunk.ToolCalls)*4)
	if !a.started {
		a.started = true
		a.granularity = StreamGranularityComplete
		events = append(events, a.synthetic(StreamEvent{Type: StreamResponseStarted,
			Granularity: StreamGranularityComplete}))
	}
	if chunk.Text != "" {
		if err := a.acceptText(chunk.Text); err != nil {
			return ChatChunk{}, nil, err
		}
		if a.legacyText == nil {
			a.legacyText = a.newSyntheticItem(StreamItemMessage)
			events = append(events, a.syntheticItem(StreamEvent{Type: StreamOutputItemStarted,
				ItemStatus: StreamItemInProgress}, a.legacyText))
		}
		event := a.syntheticItem(StreamEvent{Type: StreamTextDelta,
			ItemStatus: StreamItemInProgress, TextDelta: chunk.Text}, a.legacyText)
		events = append(events, event)
	}
	if chunk.Err != nil {
		outcome := NormalizeProviderError(a.provider, chunk.Err).Kind
		events = append(events, a.abortSynthetic(outcome))
		chunk.Events = events
		return chunk, events, nil
	}
	if !chunk.Done {
		chunk.Events = events
		return chunk, events, nil
	}
	if chunk.Usage == nil {
		return ChatChunk{}, nil, errors.New("legacy final stream chunk omitted usage")
	}
	if err := chunk.Usage.Validate(); err != nil {
		return ChatChunk{}, nil, errors.New("legacy final stream chunk returned invalid usage")
	}
	if len(a.textTail) != 0 {
		return ChatChunk{}, nil, errors.New("legacy final stream text ended with incomplete UTF-8")
	}
	if a.legacyText != nil && !a.legacyText.itemComplete {
		a.legacyText.itemComplete = true
		a.legacyText.status = StreamItemCompleted
		events = append(events, a.syntheticItem(StreamEvent{Type: StreamOutputItemCompleted,
			ItemStatus: StreamItemCompleted}, a.legacyText))
	}
	calls, err := NormalizeToolCalls(chunk.ToolCalls)
	if err != nil {
		return ChatChunk{}, nil, err
	}
	for index := range calls {
		item := a.newSyntheticItem(StreamItemToolCall)
		item.rawCallID = calls[index].ID
		item.callID = StableStreamID("call", a.responseID, strconv.Itoa(len(a.toolItems())))
		item.toolName = calls[index].Name
		events = append(events, a.syntheticItem(StreamEvent{Type: StreamOutputItemStarted,
			ItemStatus: StreamItemInProgress}, item))
		events = append(events, a.syntheticItem(StreamEvent{Type: StreamToolCallStarted,
			ItemStatus: StreamItemInProgress, CallID: item.callID, ToolName: item.toolName}, item))
		calls[index].StreamResponseID, calls[index].StreamItemID, calls[index].StreamCallID =
			a.responseID, item.id, item.callID
		item.argumentBytes = len(calls[index].Arguments)
		item.callComplete = true
		item.status = StreamItemReadyForValidation
		call := calls[index]
		events = append(events, a.syntheticItem(StreamEvent{Type: StreamToolCallCompleted,
			ItemStatus: StreamItemReadyForValidation, CallID: item.callID, ToolName: item.toolName,
			CompletedCall: &call}, item))
		item.itemComplete = true
		item.status = StreamItemCompleted
		events = append(events, a.syntheticItem(StreamEvent{Type: StreamOutputItemCompleted,
			ItemStatus: StreamItemCompleted, CallID: item.callID, ToolName: item.toolName}, item))
	}
	usage := *chunk.Usage
	events = append(events, a.synthetic(StreamEvent{Type: StreamResponseCompleted,
		Outcome: OutcomeSuccess, Usage: &usage}))
	a.terminal, a.succeeded = true, true
	chunk.ToolCalls = calls
	chunk.Events = events
	return chunk, events, nil
}

func (a *ItemStreamAccumulator) Abort(outcome Outcome) []StreamEvent {
	if a == nil || a.terminal {
		return nil
	}
	events := make([]StreamEvent, 0, 2)
	if !a.started {
		a.started = true
		a.granularity = StreamGranularityComplete
		events = append(events, a.synthetic(StreamEvent{Type: StreamResponseStarted,
			Granularity: StreamGranularityComplete}))
	}
	events = append(events, a.abortSynthetic(outcome))
	return events
}

func (a *ItemStreamAccumulator) clone() *ItemStreamAccumulator {
	copy := &ItemStreamAccumulator{
		responseID: a.responseID, provider: a.provider, model: a.model, mode: a.mode,
		rawSequence: a.rawSequence, sequence: a.sequence, rawResponse: a.rawResponse,
		rawModel: a.rawModel, started: a.started, terminal: a.terminal,
		succeeded: a.succeeded, granularity: a.granularity, textBytes: a.textBytes,
		textTail:       append([]byte(nil), a.textTail...),
		items:          make(map[string]*streamAccumulatorItem, len(a.items)),
		itemOrder:      make([]*streamAccumulatorItem, 0, len(a.itemOrder)),
		completedCalls: a.completedCalls[:len(a.completedCalls):len(a.completedCalls)],
	}
	for _, item := range a.itemOrder {
		cloned := &streamAccumulatorItem{
			rawID: item.rawID, id: item.id, typeName: item.typeName, status: item.status,
			rawCallID: item.rawCallID, callID: item.callID, toolName: item.toolName,
			toolOrdinal:   item.toolOrdinal,
			argumentBytes: item.argumentBytes, argumentDelta: item.argumentDelta,
			argumentParts: item.argumentParts[:len(item.argumentParts):len(item.argumentParts)],
			callComplete:  item.callComplete, itemComplete: item.itemComplete,
		}
		copy.items[cloned.rawID] = cloned
		copy.itemOrder = append(copy.itemOrder, cloned)
		if item == a.legacyText {
			copy.legacyText = cloned
		}
	}
	return copy
}

func (a *ItemStreamAccumulator) acceptText(text string) error {
	if a.textBytes > MaxModelOutputBytes-len(text) {
		return errors.New("item stream text exceeds its limit")
	}
	pending := make([]byte, 0, len(a.textTail)+len(text))
	pending = append(pending, a.textTail...)
	pending = append(pending, text...)
	for len(pending) != 0 {
		if !utf8.FullRune(pending) {
			if len(pending) >= utf8.UTFMax {
				return errors.New("item stream text contains invalid UTF-8")
			}
			a.textTail = append(a.textTail[:0], pending...)
			a.textBytes += len(text)
			return nil
		}
		r, size := utf8.DecodeRune(pending)
		if r == utf8.RuneError && size == 1 {
			return errors.New("item stream text contains invalid UTF-8")
		}
		pending = pending[size:]
	}
	a.textTail = a.textTail[:0]
	a.textBytes += len(text)
	return nil
}

func (a *ItemStreamAccumulator) validateCompatibilityIdentity(chunk ChatChunk,
	allowLegacyModel bool,
) error {
	provider := strings.TrimSpace(chunk.Provider)
	if chunk.Provider != provider || (provider != "" && provider != a.provider) {
		return errors.New("item stream compatibility provider identity changed")
	}
	model := strings.TrimSpace(chunk.Model)
	if chunk.Model != model {
		return errors.New("item stream compatibility model identity is not normalized")
	}
	if model == "" {
		return nil
	}
	if a.rawModel == "" && allowLegacyModel {
		a.rawModel = model
	}
	if a.rawModel == "" || model != a.rawModel {
		return errors.New("item stream compatibility model identity changed")
	}
	return nil
}

func (a *ItemStreamAccumulator) abortSynthetic(outcome Outcome) StreamEvent {
	if !outcome.Valid() || outcome == OutcomeSuccess {
		outcome = OutcomePermanent
	}
	kind := StreamResponseFailed
	if outcome == OutcomeCancelled {
		kind = StreamResponseCancelled
	}
	a.failItems(kind == StreamResponseCancelled)
	a.terminal = true
	return a.synthetic(StreamEvent{Type: kind, Outcome: outcome})
}

func (a *ItemStreamAccumulator) ResponseID() string {
	if a == nil {
		return ""
	}
	return a.responseID
}

func (a *ItemStreamAccumulator) Sequence() int {
	if a == nil {
		return 0
	}
	return a.sequence
}

func (a *ItemStreamAccumulator) Items() []OutputItem {
	if a == nil {
		return nil
	}
	out := make([]OutputItem, 0, len(a.itemOrder))
	for _, item := range a.itemOrder {
		projected := OutputItem{ResponseID: a.responseID, ID: item.id, Type: item.typeName,
			Status: item.status, CallID: item.callID, ToolName: item.toolName,
			ArgumentBytes: item.argumentBytes, Provisional: !a.succeeded, Durable: false}
		if projected.Status == StreamItemCompleted && a.succeeded {
			projected.Provisional = false
		}
		out = append(out, projected)
	}
	return out
}

func (a *ItemStreamAccumulator) annotateFinalCalls(calls []ToolCall) ([]ToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	out := make([]ToolCall, len(calls))
	for index := range calls {
		item := a.itemForRawCall(calls[index].ID)
		if item == nil || !item.callComplete {
			return nil, errors.New("final compatibility tool call has no completed item")
		}
		out[index] = cloneToolCall(calls[index])
		out[index].StreamResponseID, out[index].StreamItemID, out[index].StreamCallID =
			a.responseID, item.id, item.callID
	}
	return out, nil
}

func (a *ItemStreamAccumulator) requireStartedRawResponse(raw StreamEvent) error {
	if !a.started || raw.ResponseID != a.rawResponse || strings.TrimSpace(raw.Model) != a.rawModel ||
		raw.Granularity != "" {
		return errors.New("provider item event does not match its started response")
	}
	return nil
}

func (a *ItemStreamAccumulator) activeItem(raw StreamEvent,
	want StreamItemType,
) (*streamAccumulatorItem, error) {
	if err := a.requireStartedRawResponse(raw); err != nil {
		return nil, err
	}
	item, exists := a.items[raw.ItemID]
	if !exists || item.typeName != want || item.itemComplete || raw.ItemType != want {
		return nil, errors.New("provider item event references an inactive output item")
	}
	return item, nil
}

func (a *ItemStreamAccumulator) activeTool(raw StreamEvent) (*streamAccumulatorItem, error) {
	item, err := a.activeItem(raw, StreamItemToolCall)
	if err != nil {
		return nil, err
	}
	if item.rawCallID == "" || raw.CallID != item.rawCallID {
		return nil, errors.New("provider tool event changed its call identity")
	}
	return item, nil
}

func (a *ItemStreamAccumulator) newSyntheticItem(kind StreamItemType) *streamAccumulatorItem {
	raw := "legacy/" + strconv.Itoa(len(a.itemOrder)+1)
	item := &streamAccumulatorItem{rawID: raw,
		id:       StableStreamID("item", a.responseID, strconv.Itoa(len(a.itemOrder)+1)),
		typeName: kind, status: StreamItemInProgress}
	a.items[raw] = item
	a.itemOrder = append(a.itemOrder, item)
	return item
}

func (a *ItemStreamAccumulator) synthetic(template StreamEvent) StreamEvent {
	a.sequence++
	template.Version = ItemStreamProtocolVersion
	template.Sequence = a.sequence
	template.ResponseID = a.responseID
	template.Provider = a.provider
	template.Model = a.model
	template.Provisional = true
	return template
}

func (a *ItemStreamAccumulator) syntheticItem(template StreamEvent,
	item *streamAccumulatorItem,
) StreamEvent {
	template.ItemID = item.id
	template.ItemType = item.typeName
	if item.typeName == StreamItemToolCall && template.Type != StreamOutputItemStarted {
		if template.CallID == "" {
			template.CallID = item.callID
		}
		if template.ToolName == "" {
			template.ToolName = item.toolName
		}
	}
	return a.synthetic(template)
}

func (a *ItemStreamAccumulator) failItems(cancelled bool) {
	status := StreamItemFailed
	if cancelled {
		status = StreamItemCancelled
	}
	for _, item := range a.itemOrder {
		if !item.itemComplete {
			item.status = status
		}
	}
}

func (a *ItemStreamAccumulator) toolItems() []*streamAccumulatorItem {
	out := make([]*streamAccumulatorItem, 0)
	for _, item := range a.itemOrder {
		if item.typeName == StreamItemToolCall {
			out = append(out, item)
		}
	}
	return out
}

func (a *ItemStreamAccumulator) itemForRawCall(id string) *streamAccumulatorItem {
	for _, item := range a.itemOrder {
		if item.rawCallID == id {
			return item
		}
	}
	return nil
}

func sameToolCalls(left []ToolCall, right []ToolCall) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Name != right[index].Name ||
			!bytes.Equal(bytes.TrimSpace(left[index].Arguments), bytes.TrimSpace(right[index].Arguments)) {
			return false
		}
	}
	return true
}

func cloneToolCall(call ToolCall) ToolCall {
	call.Arguments = append(json.RawMessage(nil), call.Arguments...)
	call.Authority = append(json.RawMessage(nil), call.Authority...)
	return call
}

func validateStreamIdentity(value string, label string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		len([]rune(value)) > MaxItemStreamIdentity || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s stream identity must be bounded normalized UTF-8", label)
	}
	return nil
}

// ValidateStreamID validates one public or durable item-stream identity.
func ValidateStreamID(value string) error {
	return validateStreamIdentity(value, "item stream")
}

func validateToolName(name string) error {
	_, err := NormalizeToolCall(ToolCall{ID: "stream_identity", Name: name,
		Arguments: json.RawMessage(`{}`)})
	if err != nil {
		return fmt.Errorf("stream tool name is invalid: %w", err)
	}
	return nil
}

// providerStreamEvents fills the common envelope for adapter-specific wire
// mappings. Provider IDs are connection-local; the application accumulator
// replaces them with stable attempt-owned IDs.
type providerStreamEvents struct {
	provider        string
	model           string
	responseID      string
	granularity     StreamGranularity
	sequence        int
	started         bool
	terminal        bool
	firstGeneration time.Time
}

func newProviderStreamEvents(provider string, model string, responseID string,
	granularity StreamGranularity,
) providerStreamEvents {
	return providerStreamEvents{provider: strings.TrimSpace(provider), model: strings.TrimSpace(model),
		responseID: strings.TrimSpace(responseID), granularity: granularity}
}

func (s *providerStreamEvents) start() StreamEvent {
	s.started = true
	return s.emit(StreamEvent{Type: StreamResponseStarted, Granularity: s.granularity})
}

func (s *providerStreamEvents) emit(template StreamEvent) StreamEvent {
	if template.Type == StreamToolCallStarted || (template.Type == StreamTextDelta && template.TextDelta != "") ||
		(template.Type == StreamToolArgumentDelta && template.ArgumentDelta != "") {
		s.markGeneration()
	}
	s.sequence++
	template.Version = ItemStreamProtocolVersion
	template.Sequence = s.sequence
	template.ResponseID = s.responseID
	template.Provider = s.provider
	template.Model = s.model
	template.Provisional = true
	return template
}

func (s *providerStreamEvents) markGeneration() {
	if s.granularity == StreamGranularityDelta && s.firstGeneration.IsZero() {
		s.firstGeneration = time.Now()
	}
}

func (s *providerStreamEvents) terminalEvent(outcome Outcome, usage *Usage) StreamEvent {
	typeName := StreamResponseFailed
	if outcome == OutcomeSuccess {
		typeName = StreamResponseCompleted
	} else if outcome == OutcomeCancelled {
		typeName = StreamResponseCancelled
	}
	// Failed/cancelled item events are content-free boundaries. A received
	// accounting receipt stays on ChatChunk.Usage, independently of whether
	// any public output item can be accepted.
	if outcome != OutcomeSuccess {
		usage = nil
	}
	s.terminal = true
	event := s.emit(StreamEvent{Type: typeName, Outcome: outcome, Usage: usage})
	if outcome == OutcomeSuccess && !s.firstGeneration.IsZero() {
		millis := time.Since(s.firstGeneration).Milliseconds()
		event.DecodeMillis = &millis
	}
	return event
}

func (s *providerStreamEvents) failureChunk(err error) ChatChunk {
	outcome := NormalizeProviderError(s.provider, err).Kind
	if !s.started {
		start := s.start()
		terminal := s.terminalEvent(outcome, nil)
		return ChatChunk{Events: []StreamEvent{start, terminal}, Err: err}
	}
	if s.terminal {
		return ChatChunk{Err: err}
	}
	return ChatChunk{Events: []StreamEvent{s.terminalEvent(outcome, nil)}, Err: err}
}
