//go:build workbenchjourney

// workbenchjourney runs the production API and renderer against a disposable
// project and an offline model provider. It is only built by the browser journeys.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"cyberagent-workbench/internal/app"
)

const (
	model     = "journey-model"
	childGoal = "Read notes.txt and report its exact proof after a second review turn."
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	uiDir := flag.String("ui-dir", "", "production web bundle directory")
	flag.Parse()
	if *uiDir == "" {
		return errors.New("--ui-dir is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Playwright closes stdin on teardown, including on Windows where SIGTERM
	// would otherwise terminate the process before SQLite and temp files close.
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	temp, err := os.MkdirTemp("", "universal-code-journey-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(temp); err != nil {
			fmt.Fprintln(os.Stderr, "remove journey directory:", err)
		}
	}()
	workspace := filepath.Join(temp, "journey-project")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	for name, content := range map[string]string{
		"README.md": "# Journey project\n\nJOURNEY_FILE_PROOF_2026\n",
		"notes.txt": "JOURNEY_CHILD_PROOF_2026\nA child must read this file before reporting it.\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0600); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"init", "--initial-branch=main"}, {"add", "README.md", "notes.txt"},
		{"-c", "user.name=Journey", "-c", "user.email=journey@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Initial journey project"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workspace
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("prepare journey project: %w: %s", err, output)
		}
	}
	provider := httptest.NewServer(http.HandlerFunc(serveModel))
	defer provider.Close()
	readToken, controlToken := randomID(), randomID()
	// Explicit empty credentials prevent inheriting a developer's paid providers.
	for name, value := range map[string]string{
		"CYBERAGENT_HOME":      filepath.Join(temp, "home"),
		"CYBERAGENT_API_TOKEN": readToken, "CYBERAGENT_API_CONTROL_TOKEN": controlToken,
		"MIMO_API_KEY": "", "DEEPSEEK_API_KEY": "", "CYBERAGENT_OPENAI_API_KEY": "",
		"CYBERAGENT_OLLAMA_BASE_URL": "", "CYBERAGENT_OLLAMA_MODEL": "",
		"CYBERAGENT_ANTHROPIC_API_KEY":  "offline-journey-provider",
		"CYBERAGENT_ANTHROPIC_BASE_URL": provider.URL, "CYBERAGENT_ANTHROPIC_MODEL": model,
		"CYBERAGENT_ANTHROPIC_TIMEOUT_SECONDS": "10",
		"CYBERAGENT_CODE_INTEL_CONFIG":         "", "CYBERAGENT_WEB_SEARCH_ENDPOINT": "",
	} {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	var qualification bytes.Buffer
	if app.ExecuteContext(ctx, []string{"provider", "qualify", "anthropic/" + model}, &qualification, os.Stderr) != 0 ||
		!strings.Contains(qualification.String(), "root_eligible: true") {
		return fmt.Errorf("journey provider qualification failed: %s", qualification.String())
	}
	for _, route := range []string{"code", "learn", "review"} {
		if app.ExecuteContext(ctx, []string{"model", "set", route, "anthropic/" + model}, io.Discard, os.Stderr) != 0 {
			return fmt.Errorf("set journey %s route", route)
		}
	}
	metadata, _ := json.Marshal(map[string]string{
		"workspace": workspace, "read_token": readToken, "control_token": controlToken,
		"temp_root": temp,
	})
	fmt.Printf("journey_metadata: %s\n", metadata)
	if code := app.ExecuteContext(ctx, []string{"api", "serve", "--listen", "127.0.0.1:0", "--ui-dir", *uiDir,
		"--enable-workspace-import", "--enable-file-edit-proposals", "--enable-wake-worker"}, os.Stdout, os.Stderr); code != 0 {
		return fmt.Errorf("journey API exited %d", code)
	}
	return nil
}

func randomID() string {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}

type modelRequest struct {
	System   json.RawMessage `json:"system"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Stream bool `json:"stream"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   any             `json:"input,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

func serveModel(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": model, "display_name": "Journey model"}}, "has_more": false})
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		http.NotFound(w, r)
		return
	}
	var request modelRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	block, err := nextResponse(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, "journey provider:", err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	stopReason := "end_turn"
	if block.Type == "tool_use" {
		stopReason = "tool_use"
	}
	message := map[string]any{"id": "msg_" + randomID(), "type": "message", "role": "assistant", "model": model,
		"content": []contentBlock{block}, "stop_reason": stopReason, "usage": map[string]int{"input_tokens": 16, "output_tokens": 16}}
	if !request.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(value any) {
		data, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	}
	message["content"] = []any{}
	message["stop_reason"] = nil
	emit(map[string]any{"type": "message_start", "message": message})
	var start any = map[string]string{"type": "text", "text": ""}
	delta := map[string]string{"type": "text_delta", "text": block.Text}
	if block.Type == "tool_use" {
		encoded, _ := json.Marshal(block.Input)
		start = map[string]any{"type": "tool_use", "id": block.ID, "name": block.Name, "input": map[string]any{}}
		delta = map[string]string{"type": "input_json_delta", "partial_json": string(encoded)}
	}
	emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": start})
	emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
	emit(map[string]any{"type": "content_block_stop", "index": 0})
	emit(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stopReason}, "usage": map[string]int{"output_tokens": 16}})
	emit(map[string]string{"type": "message_stop"})
}

var noncePattern = regexp.MustCompile(`nonce ([0-9a-f]{32})`)

func nextResponse(request modelRequest) (contentBlock, error) {
	all, _ := json.Marshal(request.Messages)
	history := string(all)
	var results []contentBlock
	for _, message := range request.Messages {
		var blocks []contentBlock
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "tool_result" {
				results = append(results, block)
			}
		}
	}
	tool := func(name string, input any) (contentBlock, error) {
		for _, offered := range request.Tools {
			if offered.Name == name {
				return contentBlock{Type: "tool_use", ID: "tool_" + randomID(), Name: name, Input: input}, nil
			}
		}
		return contentBlock{}, fmt.Errorf("required journey tool %s was not advertised", name)
	}
	text := func(value any) (contentBlock, error) {
		data, _ := json.Marshal(value)
		return contentBlock{Type: "text", Text: string(data)}, nil
	}
	rootReply := func(message string) (contentBlock, error) {
		return text(map[string]string{"version": "root_lifecycle.v1", "action": "finish", "message": message, "summary": message})
	}
	if nonce := noncePattern.FindStringSubmatch(history); len(nonce) == 2 {
		if len(results) == 0 {
			return tool("prayu_harness_echo", map[string]string{"nonce": nonce[1]})
		}
		return text(map[string]string{"version": "model_harness_probe.v1", "status": "ok", "nonce": nonce[1]})
	}
	read := func(path string) (contentBlock, error) {
		return tool("workspace_read", map[string]any{"version": "agent-code-tools.v1", "path": path, "start_line": 1, "end_line": 4})
	}
	observed := func(proof string) bool {
		for _, result := range results {
			if !result.IsError && strings.Contains(string(result.Content), proof) {
				return true
			}
		}
		return false
	}
	if strings.Contains(string(request.System), "specialist_lifecycle.v1") {
		if !strings.Contains(history, childGoal) {
			return contentBlock{}, errors.New("the admitted child did not receive its assigned goal")
		}
		if strings.Contains(history, "Journey child read complete") {
			return text(map[string]any{"version": "specialist_lifecycle.v1", "action": "finish", "message": "Journey child finished",
				"report": map[string]any{"version": "agent_completion.v1", "outcome": "succeeded", "summary": "JOURNEY_CHILD_PROOF_2026", "work_item_ids": []string{}, "note_ids": []string{}}})
		}
		if observed("JOURNEY_CHILD_PROOF_2026") {
			return text(map[string]string{"version": "specialist_lifecycle.v1", "action": "continue", "message": "Journey child read complete: JOURNEY_CHILD_PROOF_2026"})
		}
		return read("notes.txt")
	}
	if strings.Contains(history, "[Authorized child completion notification]") && strings.Contains(history, "JOURNEY_CHILD_PROOF_2026") {
		return rootReply("Child completed; main conversation summary: JOURNEY_CHILD_PROOF_2026")
	}
	if strings.Contains(history, "JOURNEY_CHILD:") {
		if len(results) > 0 {
			return rootReply("Journey child proposed. Approve the child task and run it continuously.")
		}
		return tool("child_task_propose", map[string]any{"version": "child_task_proposal.v1", "tasks": []map[string]any{{
			"title": "Journey child", "goal": childGoal, "skills": []string{"model.chat"},
			"surface_hint": "core", "turn_limit": 4, "token_limit": 4096, "timeout_millis": 60000,
		}}})
	}
	if strings.Contains(history, "JOURNEY_READ:") {
		if observed("JOURNEY_FILE_PROOF_2026") {
			return rootReply("Read README.md: JOURNEY_FILE_PROOF_2026")
		}
		return read("README.md")
	}
	return contentBlock{}, errors.New("unexpected model request outside the three workbench journeys")
}
