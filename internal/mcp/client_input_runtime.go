package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/toolcontract"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Initial calls preserve the historical fingerprint; continuations bind every
// state/answer byte as well as the original arguments before a new dispatch.
func fingerprintToolParams(key []byte, launch toolcontract.ResolvedLaunch, raw json.RawMessage) (string, error) {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil {
		return "", errors.New("invalid MCP tool parameters")
	}
	if len(p["requestState"]) > 0 || len(p["inputResponses"]) > 0 {
		return FingerprintContentInput(key, launch, "tools/call", raw)
	}
	var name string
	if json.Unmarshal(p["name"], &name) != nil {
		return "", errors.New("invalid MCP tool name")
	}
	return FingerprintCallInput(key, launch, name, p["arguments"])
}

type inputClockKey struct{}
type inputClock struct {
	mu       sync.Mutex
	timer    *time.Timer
	duration time.Duration
	waiting  bool
	closed   bool
}

func timedInputCall(ctx context.Context, duration time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	clock := &inputClock{duration: duration}
	clock.timer = time.AfterFunc(duration, cancel)
	return context.WithValue(ctx, inputClockKey{}, clock), func() { clock.mu.Lock(); clock.closed = true; clock.timer.Stop(); clock.mu.Unlock(); cancel() }
}
func (c *inputClock) pause() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.waiting || !c.timer.Stop() {
		return false
	}
	c.waiting = true
	return true
}
func (c *inputClock) resume() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.waiting = false
		c.timer.Reset(c.duration)
	}
}

type activeClientInput struct {
	requestID *jsonrpc.ID
	expected  json.RawMessage
	recheck   func(context.Context) error
	ctx       context.Context
	attempt   *sdkCallAttempt
	busy      bool
	handler   func(context.Context, *sdk.ElicitParams) (*sdk.ElicitResult, error)
}

func (c *Client) activateInput(ctx context.Context, attempt *sdkCallAttempt) func() {
	c.inputMu.Lock()
	active := &activeClientInput{ctx: ctx, attempt: attempt, handler: c.inputHandler, recheck: c.inputRecheck}
	c.activeInput = active
	c.inputMu.Unlock()
	return func() {
		c.inputMu.Lock()
		if c.activeInput == active {
			c.activeInput = nil
		}
		c.inputMu.Unlock()
	}
}

// Reject reflected connection credentials before constructing any operator DTO.
// Changing schema values would silently change the requested answer semantics.
func (c *Client) rejectCredentialInput(params *sdk.ElicitParams) error {
	raw, err := json.Marshal(params)
	if err != nil || len(raw) > MaxMessageBytes {
		return errors.New("invalid MCP input request")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return errors.New("invalid MCP input request")
	}
	c.secretsMu.RLock()
	defer c.secretsMu.RUnlock()
	contains := func(text string) bool {
		for _, secret := range c.secrets {
			if secret != "" && strings.Contains(text, secret) {
				return true
			}
		}
		return false
	}
	var reflected func(any) bool
	reflected = func(value any) bool {
		switch v := value.(type) {
		case string:
			return contains(v)
		case []any:
			for _, item := range v {
				if reflected(item) {
					return true
				}
			}
		case map[string]any:
			for key, item := range v {
				if contains(key) || reflected(item) {
					return true
				}
			}
		}
		return false
	}
	if reflected(value) {
		return errors.New("MCP input reflects a connection credential")
	}
	// URL escaping must not disguise a bearer in a query or path. Unrelated
	// one-time authorization state remains valid and is never rewritten.
	if params != nil && params.URL != "" {
		decoded, err := url.QueryUnescape(params.URL)
		if err == nil && contains(decoded) {
			return errors.New("MCP input reflects a connection credential")
		}
	}
	return nil
}

func (c *Client) handleLegacyInput(ctx context.Context, request *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	c.inputMu.Lock()
	active := c.activeInput
	if active == nil || active.requestID == nil || active.handler == nil || active.busy || !active.attempt.dispatched.Load() || active.ctx.Err() != nil {
		c.inputMu.Unlock()
		return nil, errors.New("MCP input requires an active authorized invocation")
	}
	active.busy = true
	c.inputMu.Unlock()
	defer func() {
		c.inputMu.Lock()
		if len(active.expected) == 0 {
			active.busy = false
		}
		c.inputMu.Unlock()
	}()
	if err := c.rejectCredentialInput(request.Params); err != nil {
		return nil, err
	}
	if _, _, err := prepareInput(request.Params); err != nil {
		return nil, err
	}
	clock, _ := active.ctx.Value(inputClockKey{}).(*inputClock)
	if !clock.pause() {
		return nil, errors.New("MCP invocation no longer accepts input")
	}
	defer clock.resume()
	inputCtx, cancel := context.WithCancel(active.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	answer, err := active.handler(inputCtx, request.Params)
	if err == nil {
		c.rememberInput(request.Params, answer)
		c.inputMu.Lock()
		active.expected, _ = json.Marshal(answer)
		c.inputMu.Unlock()
	}
	return answer, err
}
func (c *Client) rememberInput(params *sdk.ElicitParams, answer *sdk.ElicitResult) {
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	if params.URL != "" {
		c.inputSecrets = append(c.inputSecrets, params.URL)
	}
	var add func(any)
	add = func(v any) {
		switch v := v.(type) {
		case string:
			if v != "" {
				c.inputSecrets = append(c.inputSecrets, v)
			}
		case []any:
			for _, x := range v {
				add(x)
			}
		case map[string]any:
			for _, x := range v {
				add(x)
			}
		}
	}
	if answer != nil {
		add(answer.Content)
	}
}

func (m *Manager) continueInput(ctx context.Context, client *Client, binding inputBinding, raw json.RawMessage, original json.RawMessage, recheck func(context.Context) error) (json.RawMessage, error) {
	// Decode the bounded raw envelope directly. Unsupported reverse methods are
	// rejected, without asking the SDK to handle sampling or roots implicitly.
	var result struct {
		ResultType    string `json:"resultType"`
		RequestState  string `json:"requestState"`
		InputRequests map[string]struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		} `json:"inputRequests"`
	}
	if len(raw) > MaxMessageBytes || json.Unmarshal(raw, &result) != nil || result.ResultType != "input_required" || len(result.InputRequests) == 0 || len(result.InputRequests) > 8 || len(result.RequestState) > 32768 {
		return nil, errors.New("unsupported MCP input-required result")
	}
	client.inputMu.Lock()
	client.inputSecrets = append(client.inputSecrets, result.RequestState)
	client.inputMu.Unlock()
	ids := make([]string, 0, len(result.InputRequests))
	for id, request := range result.InputRequests {
		if !validClientIdentity(id) || request.Method != "elicitation/create" {
			return nil, errors.New("unsupported MCP reverse request")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	responses := sdk.InputResponseMap{}
	for _, id := range ids {
		var params sdk.ElicitParams
		var exact struct {
			Schema json.RawMessage `json:"requestedSchema"`
		}
		if json.Unmarshal(result.InputRequests[id].Params, &exact) != nil {
			return nil, errors.New("invalid MCP input request")
		}
		if json.Unmarshal(result.InputRequests[id].Params, &params) != nil {
			return nil, errors.New("invalid MCP input request")
		}
		if len(exact.Schema) > 0 && string(exact.Schema) != "null" {
			params.RequestedSchema = exact.Schema
		}
		if err := client.rejectCredentialInput(&params); err != nil {
			return nil, err
		}
		answer, err := m.inputs.request(ctx, binding, &params, recheck)
		if err != nil {
			return nil, err
		}
		client.rememberInput(&params, answer)
		responses[id] = answer
	}
	if err := recheck(ctx); err != nil {
		return nil, err
	}
	var next map[string]json.RawMessage
	if json.Unmarshal(original, &next) != nil {
		return nil, errors.New("invalid original MCP parameters")
	}
	if result.RequestState != "" {
		next["requestState"], _ = json.Marshal(result.RequestState)
	}
	next["inputResponses"], _ = json.Marshal(responses)
	encoded, err := json.Marshal(next)
	if err != nil || len(encoded) > MaxClientArgumentsBytes {
		return nil, errors.New("MCP continuation exceeds its input limit")
	}
	return encoded, nil
}

func decodeToolParams(raw json.RawMessage) (*sdk.CallToolParams, error) {
	var p sdk.CallToolParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	p.Arguments = fields["arguments"]
	responses, err := decodeInputResponses(raw)
	p.InputResponses = responses
	return &p, err
}
func decodeInputResponses(raw json.RawMessage) (sdk.InputResponseMap, error) {
	var p struct {
		Responses map[string]json.RawMessage `json:"inputResponses"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Responses == nil {
		return nil, nil
	}
	responses := sdk.InputResponseMap{}
	for id, body := range p.Responses {
		var answer sdk.ElicitResult
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&answer); err != nil {
			return nil, err
		}
		responses[id] = &answer
	}
	return responses, nil
}

type legacyReplyKey struct{}
type legacyReplyPermit struct {
	client *Client
	active *activeClientInput
	id     jsonrpc.ID
	used   bool
}

func (c *Client) receiveLegacyInput(request *jsonrpc.Request) {
	if request.Method != "elicitation/create" || !request.IsCall() {
		return
	}
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	active := c.activeInput
	if active != nil && active.requestID == nil && c.legacyInputCount < 32 && active.attempt.dispatched.Load() && active.ctx.Err() == nil {
		id := request.ID
		active.requestID = &id
		c.legacyInputCount++
	}
}
func (c *Client) prepareLegacyReply(ctx context.Context, response *jsonrpc.Response) (context.Context, error) {
	if response.Error != nil || canonicalInputJSON(response.Result) == "{}" {
		return ctx, nil
	}
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	active := c.activeInput
	if active == nil || active.requestID == nil || *active.requestID != response.ID || response.Error != nil || len(active.expected) == 0 {
		return ctx, errors.New("MCP reply has no authorized operator response")
	}
	return context.WithValue(ctx, legacyReplyKey{}, &legacyReplyPermit{client: c, active: active, id: response.ID}), nil
}

// HTTP preflights before credential resolution, then consumes the permit only
// after rechecking authority at dispatch. Stdio consumes at its locked write.
func authorizeLegacyReply(ctx context.Context, response *jsonrpc.Response, consume bool) error {
	permit, _ := ctx.Value(legacyReplyKey{}).(*legacyReplyPermit)
	if permit == nil {
		return errors.New("MCP reverse response has no live authority")
	}
	c, active := permit.client, permit.active
	// Never hold the broker/client mutex during application authority reads.
	if active.recheck != nil {
		if err := active.recheck(active.ctx); err != nil {
			return err
		}
	}
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	if permit.used || c.activeInput != active || active.ctx.Err() != nil || ctx.Err() != nil || response.ID != permit.id || active.requestID == nil || *active.requestID != response.ID || response.Error != nil || canonicalInputJSON(response.Result) != canonicalInputJSON(active.expected) {
		return errors.New("MCP reverse response changed or expired")
	}
	if consume {
		permit.used = true
		active.requestID = nil
		active.expected = nil
		active.busy = false
	}
	return nil
}
func canonicalInputJSON(raw json.RawMessage) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return "invalid"
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
