package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMCPInputCLIUsesLiveHostAndDoesNotPrintAnswers(t *testing.T) {
	var output bytes.Buffer
	a := &App{out: &output, errOut: io.Discard}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runs/run-1/mcp-inputs/input-1" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-control" {
			t.Error("wrong live request")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["action"] != "accept" {
			t.Error("missing answer")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"accepted":true}}`)
	}))
	defer server.Close()
	t.Setenv(apiControlTokenEnvironment, "fixture-control")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	original := os.Stdin
	os.Stdin = read
	defer func() { os.Stdin = original }()
	_, _ = io.WriteString(write, `{"version":"mcp-input.v1","action":"accept","content":{"label":"private-cli-answer"}}`)
	_ = write.Close()
	if err = a.mcpInputCommand(t.Context(), []string{"respond", "--api", server.URL, "--run", "run-1", "--input", "input-1"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-cli-answer") || strings.Contains(output.String(), "fixture-control") {
		t.Fatal("CLI disclosed an answer or token")
	}
	if err = a.mcpInputCommand(t.Context(), []string{"inputs", "--api", "https://example.com", "--run", "run-1"}); err == nil {
		t.Fatal("CLI sent credentials to a non-loopback endpoint")
	}
}
