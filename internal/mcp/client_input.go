package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

const InputProtocolVersion = "mcp-input.v1"
const InputWaitTimeout = 5 * time.Minute
const InvocationTimeout = 10 * time.Minute

type InputField struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Type        string          `json:"type"`
	Required    bool            `json:"required"`
	Enum        []string        `json:"enum,omitempty"`
	EnumTitles  []string        `json:"enum_titles,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
}
type InputView struct {
	ID         string       `json:"id"`
	RunID      string       `json:"run_id"`
	ServerID   string       `json:"server_id"`
	ServerName string       `json:"server_name"`
	Method     string       `json:"method"`
	Message    string       `json:"message"`
	Mode       string       `json:"mode"`
	URL        string       `json:"url,omitempty"`
	Host       string       `json:"host,omitempty"`
	Fields     []InputField `json:"fields"`
	ExpiresAt  time.Time    `json:"expires_at"`
}
type InputResponse struct {
	Version string          `json:"version"`
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}
type inputBinding struct{ runID, serverID, serverName, operationID, method string }
type pendingInput struct {
	view    InputView
	schema  *jsonschema.Schema
	ctx     context.Context
	recheck func(context.Context) error
	answer  chan *sdk.ElicitResult
}

// InputBroker retains only live requests. Responses are delivered once, in memory,
// and never become durable command parameters or resumable authorization.
type InputBroker struct {
	mu      sync.Mutex
	pending map[string]*pendingInput
}

func NewInputBroker() *InputBroker { return &InputBroker{pending: map[string]*pendingInput{}} }

func prepareInput(params *sdk.ElicitParams) ([]InputField, json.RawMessage, error) {
	if params == nil {
		return nil, nil, errors.New("missing MCP input request")
	}
	if params.Mode == "url" || (params.Mode == "" && params.URL != "") {
		u, err := url.Parse(params.URL)
		if err != nil || len(params.URL) > 8192 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || params.RequestedSchema != nil || strings.ContainsAny(params.URL, "\r\n\t") {
			return nil, nil, errors.New("MCP external authorization requires an HTTPS URL without userinfo")
		}
		return nil, nil, nil
	}
	if (params.Mode != "" && params.Mode != "form") || params.URL != "" || params.ElicitationID != "" {
		return nil, nil, errors.New("unsupported MCP input mode")
	}
	raw, err := json.Marshal(params.RequestedSchema)
	if err != nil || len(raw) > 32*1024 {
		return nil, nil, errors.New("MCP input schema exceeds its bound")
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Type != "object" || len(schema.Properties) == 0 || len(schema.Properties) > 32 {
		return nil, nil, errors.New("MCP input requires a bounded flat form")
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(raw, &root)
	for k := range root {
		if !slices.Contains([]string{"type", "properties", "required", "additionalProperties", "$schema", "title", "description"}, k) {
			return nil, nil, errors.New("unsupported MCP form schema keyword")
		}
	}
	fields := make([]InputField, 0, len(schema.Properties))
	for name, value := range schema.Properties {
		if !validRemoteName(name) || redact.SensitiveJSONFieldName(name) || redact.String(name) != name {
			return nil, nil, errors.New("MCP forms cannot request credentials")
		}
		var property struct {
			Type        string   `json:"type"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
			EnumNames   []string `json:"enumNames"`
			OneOf       []struct {
				Const string `json:"const"`
				Title string `json:"title"`
			} `json:"oneOf"`
			Format  string          `json:"format"`
			Items   json.RawMessage `json:"items"`
			Default json.RawMessage `json:"default"`
		}
		if json.Unmarshal(value, &property) != nil || !slices.Contains([]string{"string", "boolean", "number", "integer", "array"}, property.Type) || len(property.Enum) > 64 {
			return nil, nil, errors.New("unsupported MCP form field")
		}
		var keywords map[string]json.RawMessage
		_ = json.Unmarshal(value, &keywords)
		for k := range keywords {
			if !slices.Contains([]string{"type", "title", "description", "enum", "enumNames", "minLength", "maxLength", "minimum", "maximum", "format", "default", "oneOf", "items", "minItems", "maxItems", "uniqueItems"}, k) {
				return nil, nil, errors.New("unsupported MCP form field constraint")
			}
		}
		if property.Format != "" && (property.Type != "string" || !slices.Contains([]string{"email", "uri", "date", "date-time"}, property.Format)) {
			return nil, nil, errors.New("unsupported MCP string format")
		}
		if len(property.OneOf) > 0 {
			if property.Type != "string" || len(property.Enum) > 0 || len(property.OneOf) > 64 {
				return nil, nil, errors.New("unsupported MCP single selection")
			}
			for _, option := range property.OneOf {
				property.Enum = append(property.Enum, option.Const)
				property.EnumNames = append(property.EnumNames, option.Title)
			}
		}
		if property.Type == "array" {
			var items struct {
				Type  string   `json:"type"`
				Enum  []string `json:"enum"`
				AnyOf []struct {
					Const string `json:"const"`
					Title string `json:"title"`
				} `json:"anyOf"`
			}
			var keys map[string]json.RawMessage
			if json.Unmarshal(property.Items, &items) != nil || json.Unmarshal(property.Items, &keys) != nil || (items.Type != "string" && !(items.Type == "" && len(items.AnyOf) > 0 && len(items.Enum) == 0)) {
				return nil, nil, errors.New("MCP multiple selection requires string items")
			}
			for k := range keys {
				if !slices.Contains([]string{"type", "enum", "anyOf"}, k) {
					return nil, nil, errors.New("unsupported MCP selection constraint")
				}
			}
			property.Enum = items.Enum
			for _, option := range items.AnyOf {
				property.Enum = append(property.Enum, option.Const)
				property.EnumNames = append(property.EnumNames, option.Title)
			}
			if len(property.Enum) == 0 || len(property.Enum) > 64 {
				return nil, nil, errors.New("MCP multiple selection requires bounded choices")
			}
		}
		if len(property.EnumNames) != 0 && len(property.EnumNames) != len(property.Enum) {
			return nil, nil, errors.New("invalid MCP option titles")
		}
		for _, title := range property.EnumNames {
			if !validClientText(title, 256, true) {
				return nil, nil, errors.New("invalid MCP option title")
			}
		}
		title := strings.TrimSpace(redact.String(property.Title))
		if title == "" {
			title = name
		}
		if !validClientText(title, 256, false) || !validClientText(property.Description, 2048, true) {
			return nil, nil, errors.New("MCP form field text is invalid")
		}
		for _, choice := range property.Enum {
			if !validClientText(choice, 1024, true) || redact.String(choice) != choice {
				return nil, nil, errors.New("MCP form option is invalid")
			}
		}
		fields = append(fields, InputField{Name: name, Title: title, Description: redact.String(property.Description), Type: property.Type, Required: slices.Contains(schema.Required, name), Enum: property.Enum, EnumTitles: property.EnumNames, Default: property.Default})
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			return nil, nil, errors.New("unknown required MCP form field")
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	// Compile using the same offline JSON schema boundary as remote tool inputs.
	// External schema references are rejected before rendering or validation.
	if err := validateInputSchema(raw); err != nil {
		return nil, nil, errors.New("invalid MCP input schema")
	}
	compiled, err := compileInputForm(raw)
	if err != nil {
		return nil, nil, errors.New("invalid MCP form constraints")
	}
	for _, field := range fields {
		if len(field.Default) > 0 {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(field.Default))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil || compiled.Properties[field.Name].Validate(value) != nil {
				return nil, nil, errors.New("invalid MCP form default")
			}
		}
	}
	return fields, raw, nil
}
func compileInputForm(raw json.RawMessage) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external form schema resources are forbidden")
	}
	if err := compiler.AddResource("mcp-form.json", bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return compiler.Compile("mcp-form.json")
}

func (b *InputBroker) request(ctx context.Context, binding inputBinding, params *sdk.ElicitParams, recheck func(context.Context) error) (*sdk.ElicitResult, error) {
	fields, rawSchema, err := prepareInput(params)
	if err != nil {
		return nil, err
	}
	var schema *jsonschema.Schema
	if rawSchema != nil {
		schema, err = compileInputForm(rawSchema)
		if err != nil {
			return nil, errors.New("invalid MCP form schema")
		}
	}
	if b == nil || recheck == nil || !validClientIdentity(binding.operationID) {
		return nil, errors.New("MCP input requires a live authorized operation")
	}
	if err := recheck(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, InputWaitTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	p := &pendingInput{view: InputView{ID: idgen.New("mcp-input"), RunID: binding.runID, ServerID: binding.serverID, ServerName: binding.serverName, Method: binding.method, Message: boundedClientMessage(params.Message), Fields: fields, ExpiresAt: deadline}, schema: schema, ctx: ctx, recheck: recheck, answer: make(chan *sdk.ElicitResult, 1)}
	if params.URL != "" {
		u, _ := url.Parse(params.URL)
		p.view.Mode = "url"
		p.view.URL = params.URL
		p.view.Host = u.Host
	} else {
		p.view.Mode = "form"
	}
	b.mu.Lock()
	if len(b.pending) >= 64 {
		b.mu.Unlock()
		return nil, errors.New("too many pending MCP inputs")
	}
	b.pending[p.view.ID] = p
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, p.view.ID); b.mu.Unlock() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case answer := <-p.answer:
			if err := recheck(ctx); err != nil {
				return nil, err
			}
			return answer, nil
		case <-ticker.C:
			if err := recheck(ctx); err != nil {
				return nil, err
			}
		}
	}
}
func (b *InputBroker) List(ctx context.Context, runID string) ([]InputView, error) {
	if !validClientIdentity(runID) {
		return nil, apperror.New(apperror.CodeInvalidArgument, "MCP input Run is invalid")
	}
	if b == nil {
		return []InputView{}, nil
	}
	b.mu.Lock()
	items := make([]*pendingInput, 0, len(b.pending))
	for _, p := range b.pending {
		if p.view.RunID == runID {
			items = append(items, p)
		}
	}
	b.mu.Unlock()
	result := []InputView{}
	for _, p := range items {
		if p.ctx.Err() == nil && p.recheck(ctx) == nil {
			copy := p.view
			copy.Fields = slices.Clone(copy.Fields)
			for i := range copy.Fields {
				copy.Fields[i].Enum = slices.Clone(copy.Fields[i].Enum)
				copy.Fields[i].EnumTitles = slices.Clone(copy.Fields[i].EnumTitles)
				copy.Fields[i].Default = slices.Clone(copy.Fields[i].Default)
			}
			result = append(result, copy)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
func (b *InputBroker) Respond(ctx context.Context, runID, id string, response InputResponse) error {
	if b == nil {
		return apperror.New(apperror.CodeNotFound, "MCP input is no longer pending")
	}
	b.mu.Lock()
	p := b.pending[id]
	b.mu.Unlock()
	if p == nil || p.view.RunID != runID || p.ctx.Err() != nil {
		return apperror.New(apperror.CodeNotFound, "MCP input is no longer pending")
	}
	if err := p.recheck(ctx); err != nil {
		return err
	}
	if response.Version != InputProtocolVersion || !slices.Contains([]string{"accept", "decline", "cancel"}, response.Action) {
		return apperror.New(apperror.CodeInvalidArgument, "MCP input response is invalid")
	}
	answer := &sdk.ElicitResult{Action: response.Action}
	if response.Action == "accept" && p.view.Mode == "form" {
		if len(response.Content) > 32*1024 || !json.Valid(response.Content) {
			return apperror.New(apperror.CodeInvalidArgument, "MCP input response does not match the requested form")
		}
		decoder := json.NewDecoder(bytes.NewReader(response.Content))
		decoder.UseNumber()
		if decoder.Decode(&answer.Content) != nil || answer.Content == nil {
			return apperror.New(apperror.CodeInvalidArgument, "MCP input must be a JSON object")
		}
		for _, field := range p.view.Fields {
			if _, present := answer.Content[field.Name]; !present && len(field.Default) > 0 {
				var value any
				decoder := json.NewDecoder(bytes.NewReader(field.Default))
				decoder.UseNumber()
				_ = decoder.Decode(&value)
				answer.Content[field.Name] = value
			}
		}
		if p.schema.Validate(answer.Content) != nil {
			return apperror.New(apperror.CodeInvalidArgument, "MCP input response does not match the requested form")
		}
		for k := range answer.Content {
			if !slices.ContainsFunc(p.view.Fields, func(f InputField) bool { return f.Name == k }) {
				return apperror.New(apperror.CodeInvalidArgument, "MCP input contains an unknown field")
			}
		}
	} else if len(response.Content) != 0 && string(response.Content) != "{}" {
		return apperror.New(apperror.CodeInvalidArgument, "this MCP response cannot carry content")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending[id] != p || p.ctx.Err() != nil {
		return apperror.New(apperror.CodeConflict, "MCP input was already answered or expired")
	}
	delete(b.pending, id)
	p.answer <- answer
	return nil
}
func (m *Manager) InputBroker() *InputBroker {
	if m == nil {
		return nil
	}
	return m.inputs
}
