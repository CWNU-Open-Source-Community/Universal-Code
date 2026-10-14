package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosida95/uritemplate/v3"
)

func (r RemoteResourceTemplate) Validate() error {
	if !validClientText(r.URITemplate, 4096, false) || !validClientText(r.Name, 128, false) || !validClientText(r.Description, MaxClientDescriptionBytes, true) || !validClientText(r.MIMEType, 256, true) {
		return errors.New("invalid MCP resource template")
	}
	_, err := uritemplate.New(r.URITemplate)
	return err
}

func (c *Client) listResourceTemplates(ctx context.Context, session *sdk.ClientSession) ([]RemoteResourceTemplate, error) {
	var items []RemoteResourceTemplate
	cursor := ""
	for page := 0; page < 16; page++ {
		result, err := session.ListResourceTemplates(ctx, &sdk.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, c.sdkError(err)
		}
		for _, item := range result.ResourceTemplates {
			if item == nil {
				return nil, errors.New("null MCP resource template")
			}
			candidate := RemoteResourceTemplate{URITemplate: c.sanitizeText(item.URITemplate), Name: c.sanitizeText(item.Name), Description: c.sanitizeText(item.Description), MIMEType: c.sanitizeText(item.MIMEType)}
			if err := candidate.Validate(); err != nil {
				return nil, err
			}
			items = append(items, candidate)
			if len(items) > MaxClientResources {
				return nil, errors.New("MCP resource template catalog exceeds its limit")
			}
		}
		cursor = strings.TrimSpace(result.NextCursor)
		if cursor == "" {
			slices.SortFunc(items, func(a, b RemoteResourceTemplate) int { return strings.Compare(a.URITemplate, b.URITemplate) })
			return items, nil
		}
		if !validClientText(cursor, 4096, false) {
			return nil, errors.New("invalid MCP resource template cursor")
		}
	}
	return nil, errors.New("MCP resource template pagination exceeded its page limit")
}

func decodeContentArguments(raw json.RawMessage, value any) error {
	if len(bytes.TrimSpace(raw)) == 0 || len(raw) > MaxClientArgumentsBytes || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("invalid MCP content arguments")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errors.New("invalid MCP content arguments")
	}
	for _, key := range []string{"arguments", "variables"} {
		if field, ok := fields[key]; ok {
			var values map[string]json.RawMessage
			if json.Unmarshal(field, &values) != nil || values == nil {
				return errors.New("MCP content arguments must be string maps")
			}
			for _, v := range values {
				v = bytes.TrimSpace(v)
				if len(v) == 0 || v[0] != '"' {
					return errors.New("MCP content argument values must be strings")
				}
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid MCP content arguments")
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing MCP content arguments")
	}
	return nil
}

// ValidateInvocation resolves only entries in the operator-reviewed snapshot.
// Returned parameters are the exact wire input, never a local file/HTTP fetch.
func ValidateInvocation(snapshot CapabilitySnapshot, method, name string, arguments json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "":
		tool, found := findRemoteTool(snapshot.Tools, name)
		if !found {
			return nil, errors.New("MCP tool is absent from the approved snapshot")
		}
		return arguments, validateArgumentsAgainstSchema(tool.InputSchema, arguments)
	case "resources/read":
		if name != "resources.read" {
			return nil, errors.New("invalid resource operation label")
		}
		var p struct {
			URI       string            `json:"uri"`
			Template  string            `json:"uri_template"`
			Variables map[string]string `json:"variables"`
		}
		if err := decodeContentArguments(arguments, &p); err != nil {
			return nil, err
		}
		if p.Template != "" {
			if p.URI != "" {
				return nil, errors.New("resource URI and template are mutually exclusive")
			}
			found := slices.ContainsFunc(snapshot.ResourceTemplates, func(r RemoteResourceTemplate) bool { return r.URITemplate == p.Template })
			if !found {
				return nil, errors.New("resource template is absent from the approved snapshot")
			}
			template, err := uritemplate.New(p.Template)
			if err != nil {
				return nil, err
			}
			vars := uritemplate.Values{}
			for k, v := range p.Variables {
				if !slices.Contains(template.Varnames(), k) {
					return nil, errors.New("unknown resource template variable")
				}
				vars[k] = uritemplate.String(v)
			}
			p.URI, err = template.Expand(vars)
			if err != nil {
				return nil, err
			}
		} else if len(p.Variables) != 0 || !slices.ContainsFunc(snapshot.Resources, func(r RemoteResource) bool { return r.URI == p.URI }) {
			return nil, errors.New("resource URI is absent from the approved snapshot")
		}
		if !validClientText(p.URI, 4096, false) {
			return nil, errors.New("invalid resource URI")
		}
		return json.Marshal(struct {
			URI string `json:"uri"`
		}{p.URI})
	case "prompts/get":
		if name != "prompts.get" {
			return nil, errors.New("invalid prompt operation label")
		}
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments,omitempty"`
		}
		if err := decodeContentArguments(arguments, &p); err != nil {
			return nil, err
		}
		for _, prompt := range snapshot.Prompts {
			if prompt.Name != p.Name {
				continue
			}
			known := map[string]bool{}
			for _, a := range prompt.Arguments {
				known[a.Name] = true
				if _, ok := p.Arguments[a.Name]; a.Required && !ok {
					return nil, errors.New("required prompt argument is missing")
				}
			}
			for k := range p.Arguments {
				if !known[k] {
					return nil, errors.New("unknown prompt argument")
				}
			}
			return json.Marshal(p)
		}
		return nil, errors.New("prompt is absent from the approved snapshot")
	default:
		return nil, errors.New("unsupported MCP operation")
	}
}

// ContentChoices describes host operations, kept separate from remote tools.
// The same exact schemas drive model visibility and the approval preflight.
type ContentChoice struct {
	Method, Name string
	InputSchema  json.RawMessage
}

func (s ScopedServerCapability) ContentChoices() []ContentChoice {
	var choices []ContentChoice
	add := func(method, name string, properties map[string]any, required []string) {
		raw, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required})
		choices = append(choices, ContentChoice{method, name, raw})
	}
	for _, r := range s.Resources {
		add("resources/read", "resources.read", map[string]any{"uri": map[string]any{"const": r.URI, "description": r.Description}}, []string{"uri"})
	}
	for _, r := range s.ResourceTemplates {
		template, err := uritemplate.New(r.URITemplate)
		if err != nil {
			continue
		}
		vars := map[string]any{}
		for _, name := range template.Varnames() {
			vars[name] = map[string]any{"type": "string"}
		}
		add("resources/read", "resources.read", map[string]any{"uri_template": map[string]any{"const": r.URITemplate, "description": r.Description}, "variables": map[string]any{"type": "object", "additionalProperties": false, "properties": vars}}, []string{"uri_template", "variables"})
	}
	for _, p := range s.Prompts {
		args := map[string]any{}
		required := []string{}
		for _, a := range p.Arguments {
			args[a.Name] = map[string]any{"type": "string", "description": a.Description}
			if a.Required {
				required = append(required, a.Name)
			}
		}
		add("prompts/get", "prompts.get", map[string]any{"name": map[string]any{"const": p.Name, "description": p.Description}, "arguments": map[string]any{"type": "object", "additionalProperties": false, "properties": args, "required": required}}, []string{"name", "arguments"})
	}
	return choices
}
