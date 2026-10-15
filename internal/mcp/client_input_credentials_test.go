package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInputRejectsReflectedConnectionCredential(t *testing.T) {
	const secret = "opaque-connection-value/+"
	for _, legacy := range []bool{false, true} {
		for _, field := range []string{"message", "title", "description", "default", "option-title", "option-value", "url", "escaped-url", "safe-url"} {
			name := "modern/" + field
			if legacy {
				name = "legacy/" + field
			}
			t.Run(name, func(t *testing.T) {
				b := NewInputBroker()
				c := newSDKClient(nil)
				defer c.Close()
				c.secrets = []string{secret}
				property := map[string]any{"type": "string", "title": "Label"}
				params := &sdk.ElicitParams{Message: "Choose a label", RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"label": property}}}
				switch field {
				case "message":
					params.Message = "Please confirm " + secret
				case "title", "description", "default":
					property[field] = secret
				case "option-title":
					property["oneOf"] = []any{map[string]any{"const": "blue", "title": secret}}
				case "option-value":
					property["enum"] = []string{secret}
				default:
					params = &sdk.ElicitParams{Mode: "url", Message: "Authorize", URL: "https://auth.example/authorize?state=one-time-flow-state"}
					if field == "url" {
						params.URL += "&value=" + secret
					}
					if field == "escaped-url" {
						params.URL += "&value=" + url.QueryEscape(secret)
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				binding := inputBinding{runID: "run-1", operationID: "op-1"}
				check := func(ctx context.Context) error { return ctx.Err() }
				done := make(chan error, 1)
				if legacy {
					c.inputHandler = func(ctx context.Context, p *sdk.ElicitParams) (*sdk.ElicitResult, error) {
						return b.request(ctx, binding, p, check)
					}
					attempt := &sdkCallAttempt{}
					attempt.dispatched.Store(true)
					clear := c.activateInput(ctx, attempt)
					defer clear()
					id, _ := jsonrpc.MakeID("q")
					c.receiveLegacyInput(&jsonrpc.Request{ID: id, Method: "elicitation/create"})
					go func() { _, err := c.handleLegacyInput(ctx, &sdk.ElicitRequest{Params: params}); done <- err }()
				} else {
					raw, _ := json.Marshal(map[string]any{"resultType": "input_required", "inputRequests": map[string]any{"q": map[string]any{"method": "elicitation/create", "params": params}}})
					m := &Manager{inputs: b}
					go func() {
						_, err := m.continueInput(ctx, c, binding, raw, json.RawMessage(`{"name":"lookup","arguments":{}}`), check)
						done <- err
					}()
				}
				timer := time.NewTimer(2 * time.Second)
				defer timer.Stop()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case err := <-done:
						if field == "safe-url" {
							t.Fatalf("legitimate URL rejected: %v", err)
						}
						if err == nil || errors.Is(err, context.Canceled) {
							t.Fatalf("credential request was not explicitly rejected: %v", err)
						}
						if strings.Contains(err.Error(), secret) {
							t.Fatal("error reflected connection credential")
						}
						items, _ := b.List(t.Context(), "run-1")
						if len(items) != 0 {
							t.Fatal("rejected request retained")
						}
						return
					case <-ticker.C:
						items, _ := b.List(t.Context(), "run-1")
						if len(items) == 0 {
							continue
						}
						if field != "safe-url" {
							t.Fatal("credential-bearing request reached read-token input projection")
						}
						if items[0].URL != params.URL {
							t.Fatal("legitimate one-time URL changed")
						}
						if err := b.Respond(t.Context(), "run-1", items[0].ID, InputResponse{Version: InputProtocolVersion, Action: "accept"}); err != nil {
							t.Fatal(err)
						}
						if err := <-done; err != nil {
							t.Fatal(err)
						}
						return
					case <-timer.C:
						t.Fatal("input neither rejected nor published")
					}
				}
			})
		}
	}
}
