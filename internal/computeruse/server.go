package computeruse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	Arg             = "--internal-computer-use-mcp-v1"
	ServerName      = "universal-code-computer-use"
	ServerVersion   = "1"
	ProtocolVersion = "2026-07-28"
	// The host accepts 4 MiB JSON-RPC messages. Reserve the maximum inbound
	// request size for its echoed ID, plus SDK response metadata and envelope.
	maxResultBytes = 4*1024*1024 - 64*1024 - 1024
)

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Catalog is fixed and shared with the host's built-in MCP descriptor.
func Catalog() []ToolDefinition {
	names := []string{"list_windows", "observe", "activate", "click", "type", "key", "scroll"}
	descriptions := []string{
		"List controllable visible Windows windows with live window_id, app_id, native HWND and process identity.",
		"Capture the foreground target window from the visible desktop using GDI; returns exact image dimensions and observation_id. Occluding windows are visible in the image.",
		"Activate the identified window and return a fresh observation. Requires host approval for this application.",
		"Click window-relative image pixel x,y using the latest unchanged observation; button is left (default), right or middle. Returns a fresh observation.",
		"Type Unicode text (up to 8192 UTF-8 bytes) into the foreground window using the latest unchanged observation. LF and CRLF line endings each become one typed carriage return. Returns a fresh observation.",
		"Send a key chord, e.g. CTRL+A, ENTER, ALT+F4. Supported modifiers CTRL, SHIFT, ALT, WIN; letters, digits, F1-F12 and navigation keys. Returns a fresh observation.",
		"Scroll the foreground window in wheel detents, dx positive right and dy positive up, each -10 to 10. Uses the latest unchanged observation and returns a fresh observation.",
	}
	out := make([]ToolDefinition, 0, len(names))
	for i, name := range names {
		props := map[string]any{}
		required := []string{}
		add := func(k, t string) { props[k] = map[string]any{"type": t}; required = append(required, k) }
		if i > 0 {
			add("window_id", "string")
			add("app_id", "string")
		}
		if i > 2 {
			add("observation_id", "string")
		}
		switch name {
		case "click":
			add("x", "integer")
			add("y", "integer")
			props["button"] = map[string]any{"type": "string", "enum": []string{"left", "right", "middle"}}
		case "type":
			add("text", "string")
		case "key":
			add("key", "string")
		case "scroll":
			add("dx", "integer")
			add("dy", "integer")
		}
		out = append(out, ToolDefinition{"computer_" + name, descriptions[i], map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}})
	}
	return out
}

// Execute dispatches only the exact reserved helper invocation. It never starts
// the normal application when a malformed reserved invocation is supplied.
func Execute(args []string, input io.Reader, output io.Writer) (handled bool, code int) {
	reserved := false
	for _, arg := range args {
		if arg == Arg || strings.HasPrefix(arg, Arg+"=") {
			reserved = true
		}
	}
	if !reserved {
		return false, 0
	}
	if len(args) != 1 || args[0] != Arg || input == nil || output == nil {
		return true, 2
	}
	if err := Serve(context.Background(), input, output); err != nil {
		return true, 1
	}
	return true, 0
}

// Serve runs the SDK stdio server. Context cancellation cancels active actions.
// For a cancellable externally supplied reader, its Close must unblock Read.
func Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if input == nil || output == nil {
		return fmt.Errorf("nil MCP stream")
	}
	return newServer(newExecutor(newBackend())).Run(ctx, &mcp.IOTransport{Reader: readCloser(input), Writer: writeCloser{output}, MaxLineLength: 64 * 1024})
}

type writeCloser struct{ io.Writer }

func (writeCloser) Close() error { return nil }
func readCloser(r io.Reader) io.ReadCloser {
	if c, ok := r.(io.ReadCloser); ok {
		return c
	}
	return io.NopCloser(r)
}
func newServer(e *executor) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: ServerVersion}, nil)
	for _, def := range Catalog() {
		s.AddTool(&mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: def.InputSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a, err := decodeArguments(def, req.Params.Arguments)
			if err != nil {
				return toolError(fmt.Errorf("invalid tool arguments")), nil
			}
			value, png, err := e.run(ctx, def.Name, a)
			if err != nil {
				return toolError(err), nil
			}
			return toolResult(value, png)
		})
	}
	return s
}

func toolResult(value any, png []byte) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	r := &mcp.CallToolResult{StructuredContent: value, Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
	if len(png) > 0 {
		r.Content = append(r.Content, &mcp.ImageContent{Data: png, MIMEType: "image/png"})
	}
	_, r.IsError = value.(ObservationUnavailable)
	return r, nil
}

func resultFits(value any, png []byte) (bool, error) {
	r, err := toolResult(value, png)
	if err != nil {
		return false, err
	}
	// Marshal the actual SDK result, including base64 image encoding, both
	// structured/text metadata copies, JSON escaping and content discriminators.
	encoded, err := json.Marshal(r)
	return len(encoded) <= maxResultBytes, err
}

func decodeArguments(def ToolDefinition, raw []byte) (Arguments, error) {
	var a Arguments
	dec := json.NewDecoder(bytes.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return a, fmt.Errorf("expected argument object")
	}
	props := def.InputSchema["properties"].(map[string]any)
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return a, err
		}
		k, ok := key.(string)
		p, exists := props[k]
		if !ok || !exists || seen[k] {
			return a, fmt.Errorf("unknown or duplicate argument")
		}
		seen[k] = true
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return a, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return a, fmt.Errorf("null argument")
		}
		if p.(map[string]any)["type"] == "integer" {
			var n int
			if err = json.Unmarshal(value, &n); err != nil {
				return a, err
			}
		} else {
			var s string
			if err = json.Unmarshal(value, &s); err != nil {
				return a, err
			}
		}
	}
	if _, err = dec.Token(); err != nil {
		return a, err
	}
	if _, err = dec.Token(); err != io.EOF {
		return a, fmt.Errorf("trailing argument data")
	}
	for _, k := range def.InputSchema["required"].([]string) {
		if !seen[k] {
			return a, fmt.Errorf("missing argument")
		}
	}
	err = json.Unmarshal(raw, &a)
	return a, err
}
func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}
