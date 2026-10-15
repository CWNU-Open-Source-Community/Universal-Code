package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestInputHTTPReplyRechecksAuthorityAfterCredentialRefresh(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorized", true: "revoked_during_refresh"}[revoke], func(t *testing.T) {
			var replies atomic.Int32
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				replies.Add(1)
				if r.Header.Get("Authorization") != "Bearer refreshed-credential" {
					t.Error("reply did not use refreshed credential")
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer peer.Close()
			client := newSDKClient(nil)
			defer client.Close()
			revoked := false
			client.inputRecheck = func(ctx context.Context) error {
				if revoked {
					return errors.New("operator authority revoked")
				}
				return ctx.Err()
			}
			attempt := &sdkCallAttempt{}
			attempt.dispatched.Store(true)
			clear := client.activateInput(t.Context(), attempt)
			defer clear()
			id, _ := jsonrpc.MakeID("question")
			client.receiveLegacyInput(&jsonrpc.Request{ID: id, Method: "elicitation/create"})
			answer := json.RawMessage(`{"action":"accept","content":{"choice":"blue"}}`)
			client.activeInput.expected = answer
			reply := &jsonrpc.Response{ID: id, Result: answer}
			ctx, err := client.prepareLegacyReply(t.Context(), reply)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := jsonrpc.EncodeMessage(reply)
			if err != nil {
				t.Fatal(err)
			}
			origin, _ := url.Parse(peer.URL)
			transport := &mcpHTTPTransport{origin: origin, base: peer.Client().Transport,
				resolveBearer: func(context.Context) (string, error) {
					// Authority changes while the issuer is resolving a token.
					revoked = revoke
					return "refreshed-credential", nil
				}}
			send := func() error {
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, peer.URL, bytes.NewReader(raw))
				response, err := transport.RoundTrip(request)
				if response != nil {
					response.Body.Close()
				}
				return err
			}
			err = send()
			if revoke {
				if err == nil || replies.Load() != 0 {
					t.Fatalf("revoked operator answer reached peer: replies=%d err=%v", replies.Load(), err)
				}
			} else if err != nil || replies.Load() != 1 {
				t.Fatalf("authorized answer failed: replies=%d err=%v", replies.Load(), err)
			} else if send() == nil || replies.Load() != 1 {
				t.Fatal("operator answer was replayed")
			}
		})
	}
}
