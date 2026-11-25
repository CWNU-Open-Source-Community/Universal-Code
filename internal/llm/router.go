package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/redact"
)

type ModelRef struct {
	Provider string
	Model    string
}

type Router struct {
	mu             sync.RWMutex
	providers      map[string]Provider
	routes         map[string]ModelRef
	contextWindows map[string]ContextWindow
	qualifications map[string]HarnessQualification
	defaultRef     ModelRef
	generation     *routerGeneration
}

func NewRouter(defaultRef ModelRef) *Router {
	return &Router{
		providers:      map[string]Provider{},
		routes:         map[string]ModelRef{},
		contextWindows: map[string]ContextWindow{},
		qualifications: map[string]HarnessQualification{},
		defaultRef:     defaultRef,
		generation:     &routerGeneration{},
	}
}

func NewDefaultRouter() *Router {
	r := NewRouter(ModelRef{Provider: "mock", Model: "mock-cyber-agent"})
	r.RegisterProvider(NewMockProvider())
	r.SetRoute("ctf", ModelRef{Provider: "mock", Model: "mock-cyber-agent"})
	r.SetRoute("script", ModelRef{Provider: "mock", Model: "mock-code"})
	r.SetRoute("learn", ModelRef{Provider: "mock", Model: "mock-fast"})
	r.SetRoute("code", ModelRef{Provider: "mock", Model: "mock-code"})
	r.SetRoute("review", ModelRef{Provider: "mock", Model: "mock-cyber-agent"})
	return r
}

func (r *Router) RegisterProvider(provider Provider) {
	if r == nil || provider == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.Name()] = provider
	r.generation = &routerGeneration{}
}

// ReplaceConfiguration atomically installs a complete Router generation.
// Calls that already captured a Provider continue on that immutable Provider
// value; calls entering after the swap observe only the new generation.
func (r *Router) ReplaceConfiguration(next *Router) error {
	if r == nil || next == nil {
		return fmt.Errorf("router generations are required")
	}
	if r == next {
		return nil
	}
	next.mu.RLock()
	providers := make(map[string]Provider, len(next.providers))
	for name, provider := range next.providers {
		providers[name] = provider
	}
	routes := make(map[string]ModelRef, len(next.routes))
	for name, ref := range next.routes {
		routes[name] = ref
	}
	contextWindows := make(map[string]ContextWindow, len(next.contextWindows))
	for key, window := range next.contextWindows {
		contextWindows[key] = window
	}
	qualifications := make(map[string]HarnessQualification, len(next.qualifications))
	for key, qualification := range next.qualifications {
		qualifications[key] = qualification
	}
	defaultRef := next.defaultRef
	next.mu.RUnlock()

	r.mu.Lock()
	r.providers = providers
	r.routes = routes
	r.contextWindows = contextWindows
	r.qualifications = qualifications
	r.defaultRef = defaultRef
	r.generation = &routerGeneration{}
	r.mu.Unlock()
	return nil
}

func (r *Router) SetContextWindow(ref ModelRef, window ContextWindow) error {
	if r == nil {
		return fmt.Errorf("router is required")
	}
	key, ok := contextWindowKey(ref)
	if !ok {
		return fmt.Errorf("model context-window ref is invalid")
	}
	if err := window.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.contextWindows == nil {
		r.contextWindows = make(map[string]ContextWindow)
	}
	r.contextWindows[key] = window
	r.generation = &routerGeneration{}
	return nil
}

func (r *Router) ContextWindow(ref ModelRef) ContextWindow {
	if r == nil {
		return DefaultContextWindow()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.contextWindowLocked(ref, r.providers[ref.Provider])
}

func (r *Router) ProviderNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Router) Routes() map[string]ModelRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]ModelRef, len(r.routes))
	for k, v := range r.routes {
		out[k] = v
	}
	return out
}

func (r *Router) SetRoute(name string, ref ModelRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[name] = ref
}

func (r *Router) Resolve(route string) ModelRef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if ref, ok := r.routes[route]; ok {
		return ref
	}
	return r.defaultRef
}

func (r *Router) SupportsJSONMode(ref ModelRef) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[strings.TrimSpace(ref.Provider)]
	return ok && provider.SupportsJSONMode(strings.TrimSpace(ref.Model))
}

func (r *Router) HarnessProfile(ref ModelRef) (ModelHarness, error) {
	if r == nil {
		return ModelHarness{}, errors.New("router is required")
	}
	ref.Provider = strings.TrimSpace(ref.Provider)
	ref.Model = strings.TrimSpace(ref.Model)
	if ref.Provider == "" || ref.Model == "" {
		return ModelHarness{}, errors.New("model Harness ref is invalid")
	}
	r.mu.RLock()
	provider, ok := r.providers[ref.Provider]
	qualification := r.qualifications[ref.Provider+"\x00"+ref.Model]
	r.mu.RUnlock()
	if !ok {
		return ModelHarness{}, fmt.Errorf("provider %q is not registered", ref.Provider)
	}
	profile := providerContractHarness(provider, ref.Model)
	if describer, described := provider.(ModelHarnessDescriber); described {
		profile = describer.DescribeModelHarness(ref.Model)
	}
	if err := profile.Validate(); err != nil {
		return ModelHarness{}, fmt.Errorf("provider %q returned an invalid model Harness: %w",
			ref.Provider, err)
	}
	return applyHarnessQualification(profile, qualification, time.Now().UTC()), nil
}

func (r *Router) SetHarnessQualification(ref ModelRef,
	qualification HarnessQualification,
) error {
	if r == nil {
		return errors.New("router is required")
	}
	ref.Provider = strings.TrimSpace(ref.Provider)
	ref.Model = strings.TrimSpace(ref.Model)
	profile, err := r.HarnessProfile(ref)
	if err != nil {
		return err
	}
	if err := qualification.Validate(profile.BindingDigest, time.Now().UTC()); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.qualifications == nil {
		r.qualifications = make(map[string]HarnessQualification)
	}
	r.qualifications[ref.Provider+"\x00"+ref.Model] = qualification
	return nil
}

// ClearHarnessQualification removes a previously verified qualification for
// one exact Provider/model pair. It is used when credential authority changes:
// a qualification obtained with the previous credential must not keep a model
// root eligible while the Registry reload is still in progress or has failed.
func (r *Router) ClearHarnessQualification(ref ModelRef) {
	if r == nil {
		return
	}
	ref.Provider = strings.TrimSpace(ref.Provider)
	ref.Model = strings.TrimSpace(ref.Model)
	if ref.Provider == "" || ref.Model == "" {
		return
	}
	r.mu.Lock()
	delete(r.qualifications, ref.Provider+"\x00"+ref.Model)
	r.generation = &routerGeneration{}
	r.mu.Unlock()
}

func (r *Router) PrepareHarnessRequest(ref ModelRef, workload HarnessWorkload,
	request ChatRequest,
) (ChatRequest, ModelHarness, error) {
	request, err := r.PrepareModelRequest(ref, request)
	if err != nil {
		return ChatRequest{}, ModelHarness{}, err
	}
	profile := request.preparedModel.profile
	prepared, err := prepareHarnessRequest(profile, workload, request)
	if err != nil {
		return ChatRequest{}, profile, err
	}
	prepared.preparedModel.workload = workload
	return prepared, profile, nil
}

func (r *Router) Chat(ctx context.Context, route string, req ChatRequest) (*ChatResponse, error) {
	r.mu.RLock()
	ref, routed := r.routes[route]
	if !routed {
		ref = r.defaultRef
	}
	provider, registered, policyErr := r.providerForRequestLocked(ref, req)
	r.mu.RUnlock()
	if policyErr != nil {
		return nil, policyErr
	}
	return chatWithProvider(ctx, ref, provider, registered, req)
}

func (r *Router) ChatModelRef(ctx context.Context, ref ModelRef, req ChatRequest) (*ChatResponse, error) {
	r.mu.RLock()
	provider, ok, policyErr := r.providerForRequestLocked(ref, req)
	r.mu.RUnlock()
	if policyErr != nil {
		return nil, policyErr
	}
	return chatWithProvider(ctx, ref, provider, ok, req)
}

func chatWithProvider(ctx context.Context, ref ModelRef, provider Provider, registered bool,
	req ChatRequest,
) (*ChatResponse, error) {
	if !registered {
		return nil, NewProviderError(OutcomePermanent, ref.Provider, fmt.Sprintf("provider %q is not registered", ref.Provider), nil)
	}
	if req.Model == "" {
		req.Model = ref.Model
	}
	if err := validateRequestImages(provider, req.Model, req.Messages); err != nil {
		return nil, NewProviderError(OutcomePermanent, ref.Provider, "image input is invalid or unsupported", err)
	}
	req, err := redactRequest(req)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, ref.Provider, "invalid model request", err)
	}
	response, err := provider.Chat(ctx, req)
	if err != nil {
		return response, NormalizeProviderError(ref.Provider, err)
	}
	if response != nil {
		if terminalErr := CompletionError(ref.Provider, response.FinishReason); terminalErr != nil {
			return response, terminalErr
		}
	}
	return response, nil
}

func (r *Router) StreamChat(ctx context.Context, route string, req ChatRequest) (<-chan ChatChunk, error) {
	r.mu.RLock()
	ref, routed := r.routes[route]
	if !routed {
		ref = r.defaultRef
	}
	provider, registered, policyErr := r.providerForRequestLocked(ref, req)
	r.mu.RUnlock()
	if policyErr != nil {
		return nil, policyErr
	}
	return streamWithProvider(ctx, ref, provider, registered, req)
}

func (r *Router) StreamChatModelRef(ctx context.Context, ref ModelRef, req ChatRequest) (<-chan ChatChunk, error) {
	r.mu.RLock()
	provider, ok, policyErr := r.providerForRequestLocked(ref, req)
	r.mu.RUnlock()
	if policyErr != nil {
		return nil, policyErr
	}
	return streamWithProvider(ctx, ref, provider, ok, req)
}

func streamWithProvider(ctx context.Context, ref ModelRef, provider Provider, registered bool,
	req ChatRequest,
) (<-chan ChatChunk, error) {
	if !registered {
		return nil, NewProviderError(OutcomePermanent, ref.Provider, fmt.Sprintf("provider %q is not registered", ref.Provider), nil)
	}
	if req.Model == "" {
		req.Model = ref.Model
	}
	if err := validateRequestImages(provider, req.Model, req.Messages); err != nil {
		return nil, NewProviderError(OutcomePermanent, ref.Provider, "image input is invalid or unsupported", err)
	}
	var err error
	req, err = redactRequest(req)
	if err != nil {
		return nil, NewProviderError(OutcomePermanent, ref.Provider, "invalid model request", err)
	}
	chunks, err := provider.StreamChat(ctx, req)
	if err != nil {
		return nil, NormalizeProviderError(ref.Provider, err)
	}
	if chunks == nil {
		return nil, NewProviderError(OutcomeInvalidResponse, ref.Provider, "returned a nil stream", nil)
	}
	return chunks, nil
}

func (r *Router) ListModels(ctx context.Context) ([]ModelInfo, error) {
	r.mu.RLock()
	providers := make(map[string]Provider, len(r.providers))
	for name, provider := range r.providers {
		providers[name] = provider
	}
	r.mu.RUnlock()
	var all []ModelInfo
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		models, err := providers[name].ListModels(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, models...)
	}
	return all, nil
}

func ParseModelRef(value string) (ModelRef, error) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ModelRef{}, fmt.Errorf("model ref must use provider/model format")
	}
	return ModelRef{Provider: parts[0], Model: parts[1]}, nil
}

func redactRequest(req ChatRequest) (ChatRequest, error) {
	req.Messages = append([]Message(nil), req.Messages...)
	for i := range req.Messages {
		req.Messages[i].Replay = req.Messages[i].Replay.Clone()
		// Image bytes are inert operator evidence, not text for the redactor.
		// Copy before handing them to a Provider; omit them from JSON logs.
		req.Messages[i].Images = append([]ImagePart(nil), req.Messages[i].Images...)
		for j := range req.Messages[i].Images {
			req.Messages[i].Images[j].Data = append([]byte(nil), req.Messages[i].Images[j].Data...)
		}
		if req.Metadata["purpose"] != "context_compaction" || !req.Messages[i].preservesContextCompactionData() {
			req.Messages[i].Content = redact.String(req.Messages[i].Content)
		}
		calls, err := NormalizeToolCalls(req.Messages[i].ToolCalls)
		if err != nil {
			return ChatRequest{}, err
		}
		for index := range calls {
			calls[index].Arguments, err = redactModelJSON(calls[index].Arguments)
			if err != nil {
				return ChatRequest{}, err
			}
		}
		req.Messages[i].ToolCalls = calls
		results := make([]ToolResult, len(req.Messages[i].ToolResults))
		for index, result := range req.Messages[i].ToolResults {
			normalized, err := NormalizeToolResult(result)
			if err != nil {
				return ChatRequest{}, err
			}
			if !normalized.preservesStoredHistory() {
				normalized.Content = redact.String(normalized.Content)
			}
			results[index] = normalized
		}
		req.Messages[i].ToolResults = results
	}
	req.Tools = append([]ToolSpec(nil), req.Tools...)
	for index := range req.Tools {
		req.Tools[index].Name = strings.TrimSpace(req.Tools[index].Name)
		req.Tools[index].Description = redact.String(strings.TrimSpace(req.Tools[index].Description))
		req.Tools[index].Parameters = append(json.RawMessage(nil), bytes.TrimSpace(req.Tools[index].Parameters)...)
		if req.Tools[index].Name == "" || len(req.Tools[index].Parameters) == 0 || !json.Valid(req.Tools[index].Parameters) {
			return ChatRequest{}, fmt.Errorf("model tool specification at index %d is invalid", index)
		}
	}
	if len(req.Metadata) > 0 {
		metadata := make(map[string]string, len(req.Metadata))
		for key, value := range req.Metadata {
			metadata[key] = redact.String(value)
		}
		req.Metadata = metadata
	}
	return req, nil
}

func redactModelJSON(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureModelJSONEOF(decoder); err != nil {
		return nil, err
	}
	safe, err := redact.ModelJSONValue(value, false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(safe)
}

func ensureModelJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return fmt.Errorf("model tool JSON contains trailing data")
}
