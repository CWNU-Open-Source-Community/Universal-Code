package httpapi

import (
	"context"
	"cyberagent-workbench/internal/mcp"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type mcpInputRoutingStub struct {
	extensionControllerStub
	runID string
}

func (s *mcpInputRoutingStub) ListMCPInputs(context.Context, string) ([]mcp.InputView, error) {
	return []mcp.InputView{}, nil
}

func (s *mcpInputRoutingStub) RespondMCPInput(_ context.Context, runID, inputID string, response mcp.InputResponse) error {
	if runID != s.runID || inputID != "mcp-input-openapi" || response.Version != mcp.InputProtocolVersion || response.Action != "decline" {
		return errors.New("MCP input route lost its bound response")
	}
	return nil
}

func TestMCPInputHTTPAuthorityAndLiveOnly(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	path := "/api/v1/runs/" + f.run.ID + "/mcp-inputs"
	var list MCPInputListView
	response := f.get(t, path)
	decodeData(t, response, &list)
	if list.Version != mcp.InputProtocolVersion || len(list.Items) != 0 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid live input list")
	}
	for _, tc := range []struct {
		method, path, token, body string
		want                      int
	}{
		{"GET", path, "", "", 401}, {"GET", path + "?state=secret", testAccessToken, "", 400},
		{"POST", path + "/input-1", testAccessToken, `{"version":"mcp-input.v1","action":"accept"}`, 401},
		{"POST", path + "/input-1", testControlToken, `{"version":"mcp-input.v1","action":"accept","extra":"secret"}`, 400},
		{"POST", path + "/input-1", testControlToken, `{"version":"mcp-input.v1","action":"accept"}`, 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Host = "127.0.0.1"
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Content-Type", "application/json")
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		f.api.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d want %d %s", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("input body leaked")
		}
	}
}
