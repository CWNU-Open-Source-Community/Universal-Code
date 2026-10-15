package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/httpapi"
)

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func TestAPIOpenAPICLIPrintsAndWritesCanonicalContractWithoutRuntimeState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "runtime-not-created")
	t.Setenv("CYBERAGENT_HOME", home)
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := ExecuteContext(context.Background(), []string{"api", "openapi"}, &stdout, &stderr); code != 0 {
		t.Fatalf("OpenAPI stdout export failed: code=%d stderr=%s", code, stderr.String())
	}
	expected, err := httpapi.GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), expected) || stderr.Len() != 0 || !json.Valid(stdout.Bytes()) {
		t.Fatalf("OpenAPI stdout is not canonical: stderr=%s", stderr.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("OpenAPI export initialized runtime state: stat err=%v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "openapi.json")
	stdout.Reset()
	stderr.Reset()
	code := ExecuteContext(context.Background(), []string{"api", "openapi", "--output", outputPath},
		&stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "openapi_written: ") {
		t.Fatalf("OpenAPI file export failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	written, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, expected) {
		t.Fatal("OpenAPI file export differs from the Go contract")
	}
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func waitForAPIProcessOutput(t *testing.T, stdout *synchronizedBuffer,
	stderr *synchronizedBuffer, done <-chan int, ready func(string) bool,
) string {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	// Fresh Windows runners can spend more than 15 seconds opening and migrating
	// a new control-plane database before the server publishes its first bounded
	// metadata line. Keep the probe bounded, but cover the cold-start path.
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	for {
		output := stdout.String()
		if ready(output) {
			return output
		}
		select {
		case code := <-done:
			t.Fatalf("API process exited before startup metadata: code=%d stdout=%s stderr=%s",
				code, output, stderr.String())
		case <-timer.C:
			t.Fatalf("API process startup metadata timed out: stdout=%s stderr=%s",
				output, stderr.String())
		case <-ticker.C:
		}
	}
}

func TestAPIServeCLIStartsAuthenticatedLoopbackServerWithoutPersistingToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "")
	token := "cli-api-token-0123456789-abcdefghijkl"
	controlToken := "cli-control-token-0123456789-abcdefgh"
	t.Setenv(apiTokenEnvironment, token)
	t.Setenv(apiControlTokenEnvironment, controlToken)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stdout synchronizedBuffer
	var stderr synchronizedBuffer
	done := make(chan int, 1)
	go func() {
		done <- ExecuteContext(ctx, []string{
			"api", "serve", "--listen", "127.0.0.1:0",
			"--enable-workspace-import",
			"--enable-permission-control", "--enable-danger-full-access",
			"--enable-browser-cdp-control", "--enable-full-cdp-debug",
			"--enable-batch-validation-execution",
			"--enable-scheduled-job-worker",
		}, &stdout, &stderr)
	}()

	expectedMetadata := []struct{ field, value string }{
		{"api_token_source", apiTokenEnvironment},
		{"api_token_generated", "false"},
		{"api_control_enabled", "true"},
		{"workspace_import_enabled", "true"},
		{"api_control_token_source", apiControlTokenEnvironment},
		{"execution_permission_control_enabled", "true"},
		{"operator_approval_enabled", "true"},
		{"danger_full_access_enabled", "true"},
		{"browser_cdp_permission_control_enabled", "true"},
		{"full_cdp_debug_enabled", "true"},
		{"batch_delivery_host_validation_enabled", "true"},
	}
	output := waitForAPIProcessOutput(t, &stdout, &stderr, done, func(output string) bool {
		if outputField(output, "api_url") == "" {
			return false
		}
		// Startup metadata spans several writes. Wait for every required field;
		// incorrect values must still reach the assertions below immediately.
		for _, expected := range expectedMetadata {
			if outputField(output, expected.field) == "" {
				return false
			}
		}
		return true
	})
	baseURL := outputField(output, "api_url")
	if baseURL == "" {
		t.Fatalf("API did not report its URL: stdout=%s stderr=%s", output, stderr.String())
	}
	if strings.Contains(output, token) || strings.Contains(output, controlToken) {
		t.Fatal("API startup output exposed an environment token")
	}
	for _, expected := range expectedMetadata {
		if outputField(output, expected.field) != expected.value {
			t.Fatalf("API startup metadata %s has an unexpected value", expected.field)
		}
	}

	request, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !json.Valid(body) ||
		!bytes.Contains(body, []byte(`"version":"api.v1"`)) {
		t.Fatalf("unexpected CLI API response: status=%d body=%s err=%v", response.StatusCode, body, readErr)
	}
	capabilityRequest, err := http.NewRequest(http.MethodGet,
		baseURL+"/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	capabilityRequest.Header.Set("Authorization", "Bearer "+token)
	capabilityResponse, err := (&http.Client{Timeout: 2 * time.Second}).
		Do(capabilityRequest)
	if err != nil {
		t.Fatal(err)
	}
	capabilityBody, readErr := io.ReadAll(capabilityResponse.Body)
	_ = capabilityResponse.Body.Close()
	if readErr != nil || capabilityResponse.StatusCode != http.StatusOK ||
		!bytes.Contains(capabilityBody, []byte(`"workspace_import_enabled":true`)) ||
		!bytes.Contains(capabilityBody,
			[]byte(`"execution_permission_control_enabled":true`)) ||
		!bytes.Contains(capabilityBody,
			[]byte(`"browser_cdp_permission_control_enabled":true`)) ||
		!bytes.Contains(capabilityBody, []byte(`"full_cdp_debug_enabled":true`)) ||
		!bytes.Contains(capabilityBody, []byte(`"scheduled_job_worker_enabled":true`)) ||
		!bytes.Contains(capabilityBody, []byte(`"selection_scope":"all_jobs"`)) ||
		!bytes.Contains(capabilityBody,
			[]byte(`"batch_delivery_host_validation_enabled":true`)) {
		t.Fatalf("execution permission capability is not wired by api serve: status=%d body=%s err=%v",
			capabilityResponse.StatusCode, capabilityBody, readErr)
	}

	selected := t.TempDir()
	importBody, err := json.Marshal(httpapi.WorkspaceImportRequestView{
		Version: httpapi.WorkspaceImportProtocolVersion, DirectoryPath: selected, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	importRequest, err := http.NewRequest(http.MethodPost, baseURL+"/workspaces/import", bytes.NewReader(importBody))
	if err != nil {
		t.Fatal(err)
	}
	importRequest.Header.Set("Authorization", "Bearer "+controlToken)
	importRequest.Header.Set("Content-Type", "application/json")
	importResponse, err := (&http.Client{Timeout: 2 * time.Second}).Do(importRequest)
	if err != nil {
		t.Fatal(err)
	}
	importResult, err := io.ReadAll(importResponse.Body)
	_ = importResponse.Body.Close()
	if err != nil || importResponse.StatusCode != http.StatusOK ||
		!bytes.Contains(importResult, []byte(`"directory_content_modified":false`)) {
		t.Fatalf("CLI import is not wired: status=%d body=%s err=%v", importResponse.StatusCode, importResult, err)
	}
	entries, err := os.ReadDir(selected)
	if err != nil || len(entries) != 0 {
		t.Fatalf("CLI import wrote selected directory: %#v %v", entries, err)
	}

	messageRequest, err := http.NewRequest(http.MethodPost,
		baseURL+"/sessions/session-test/messages",
		strings.NewReader(`{"version":"session_message_submission.v1","content":"queued"}`))
	if err != nil {
		t.Fatal(err)
	}
	messageRequest.Header.Set("Authorization", "Bearer "+controlToken)
	messageRequest.Header.Set("Content-Type", "application/json")
	messageResponse, err := (&http.Client{Timeout: 2 * time.Second}).Do(messageRequest)
	if err != nil {
		t.Fatal(err)
	}
	messageBody, readErr := io.ReadAll(messageResponse.Body)
	_ = messageResponse.Body.Close()
	if readErr != nil || messageResponse.StatusCode != http.StatusBadRequest ||
		!bytes.Contains(messageBody, []byte(`"code":"INVALID_ARGUMENT"`)) ||
		!bytes.Contains(messageBody, []byte("Idempotency-Key")) {
		t.Fatalf("Session-message capability is not wired by api serve: status=%d body=%s err=%v",
			messageResponse.StatusCode, messageBody, readErr)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("API command exited with code %d: %s", code, stderr.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("API command did not stop after context cancellation")
	}
	if stderr.String() != "" {
		t.Fatalf("API command wrote unexpected stderr: %s", stderr.String())
	}
	assertDirectoryOmitsValue(t, home, token)
	assertDirectoryOmitsValue(t, home, controlToken)
}

func TestAPIServeCLIRejectsInvalidExecutionPermissionStartupGates(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	t.Setenv(apiTokenEnvironment, "")
	t.Setenv(apiControlTokenEnvironment, "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := ExecuteContext(context.Background(), []string{
		"api", "serve", "--enable-danger-full-access",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(),
		"danger full access requires permission control") {
		t.Fatalf("invalid hierarchy stdout=%q stderr=%q code=%d",
			stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	code = ExecuteContext(context.Background(), []string{
		"api", "serve", "--enable-permission-control",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(),
		"require CYBERAGENT_API_CONTROL_TOKEN") {
		t.Fatalf("missing control token stdout=%q stderr=%q code=%d",
			stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	code = ExecuteContext(context.Background(), []string{
		"api", "serve", "--enable-workspace-import",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(),
		"--enable-workspace-import requires CYBERAGENT_API_CONTROL_TOKEN") {
		t.Fatalf("workspace import accepted missing control token: stdout=%q stderr=%q code=%d",
			stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	code = ExecuteContext(context.Background(), []string{
		"api", "serve", "--enable-full-cdp-debug",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(),
		"full CDP debug requires browser CDP control") {
		t.Fatalf("invalid browser CDP hierarchy stdout=%q stderr=%q code=%d",
			stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	code = ExecuteContext(context.Background(), []string{
		"api", "serve", "--enable-host-command-proposals",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(),
		"flag provided but not defined: -enable-host-command-proposals") {
		t.Fatalf("retired host command flag stdout=%q stderr=%q code=%d",
			stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	code = ExecuteContext(context.Background(), []string{
		"api", "serve", "--enable-batch-validation-execution",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(),
		"requires permission control and danger-full-access") {
		t.Fatalf("invalid batch validation hierarchy stdout=%q stderr=%q code=%d",
			stdout.String(), stderr.String(), code)
	}
	stdout.Reset()
	stderr.Reset()
	t.Setenv(apiControlTokenEnvironment, "batch-validation-control-token-0123456789")
	code = ExecuteContext(context.Background(), []string{
		"api", "serve", "--enable-permission-control",
		"--enable-batch-validation-execution",
	}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(),
		"requires permission control and danger-full-access") {
		t.Fatalf("batch validation without danger full access stdout=%q stderr=%q code=%d",
			stdout.String(), stderr.String(), code)
	}
}

func TestAPIServeCLIGeneratesUsableProcessToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	t.Setenv(apiTokenEnvironment, "")
	t.Setenv(apiControlTokenEnvironment, "")
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stdout synchronizedBuffer
	var stderr synchronizedBuffer
	done := make(chan int, 1)
	go func() {
		done <- ExecuteContext(ctx, []string{"api", "serve", "--listen", "127.0.0.1:0"}, &stdout, &stderr)
	}()

	output := waitForAPIProcessOutput(t, &stdout, &stderr, done, func(output string) bool {
		return outputField(output, "api_url") != "" && len(outputField(output, "api_token")) >= 32 &&
			strings.Contains(output, "api_token_generated: true") &&
			strings.Contains(output, "api_control_enabled: false")
	})
	baseURL := outputField(output, "api_url")
	token := outputField(output, "api_token")
	if baseURL == "" || len(token) < 32 {
		t.Fatalf("generated API credentials are incomplete: %s", output)
	}
	request, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("generated token was unusable: status=%d err=%v", response.StatusCode, readErr)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 || stderr.String() != "" {
			t.Fatalf("generated-token API exit failed: code=%d stderr=%s", code, stderr.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("generated-token API did not stop")
	}
	assertDirectoryOmitsValue(t, home, token)
}

func TestAPIServeCLIHostsImmutableWebUIWithoutWeakeningAPI(t *testing.T) {
	home := t.TempDir()
	uiDirectory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(uiDirectory, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	indexBody := []byte(`<!doctype html><script type="module" src="/assets/index-AbCd1234.js"></script>`)
	assetBody := []byte(`document.body.dataset.ready = "yes";`)
	if err := os.WriteFile(filepath.Join(uiDirectory, "index.html"), indexBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiDirectory, "assets", "index-AbCd1234.js"), assetBody, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CYBERAGENT_HOME", home)
	t.Setenv("MIMO_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "")
	token := "cli-web-token-0123456789-abcdefghijkl"
	t.Setenv(apiTokenEnvironment, token)
	t.Setenv(apiControlTokenEnvironment, "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var stdout synchronizedBuffer
	var stderr synchronizedBuffer
	done := make(chan int, 1)
	go func() {
		done <- ExecuteContext(ctx, []string{"api", "serve", "--listen", "127.0.0.1:0",
			"--ui-dir", uiDirectory}, &stdout, &stderr)
	}()

	output := waitForAPIProcessOutput(t, &stdout, &stderr, done, func(output string) bool {
		return outputField(output, "api_url") != "" && outputField(output, "ui_url") != "" &&
			len(outputField(output, "ui_digest")) == 64 && strings.Contains(output, "ui_assets: 1")
	})
	apiURL := outputField(output, "api_url")
	uiURL := outputField(output, "ui_url")
	if apiURL == "" || uiURL == "" || strings.Contains(output, token) {
		t.Fatalf("Web UI startup metadata is incomplete or unsafe: %s", output)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	uiResponse, err := client.Get(uiURL)
	if err != nil {
		t.Fatal(err)
	}
	servedIndex, readErr := io.ReadAll(uiResponse.Body)
	_ = uiResponse.Body.Close()
	if readErr != nil || uiResponse.StatusCode != http.StatusOK || !bytes.Equal(servedIndex, indexBody) ||
		strings.Contains(uiResponse.Header.Get("Content-Security-Policy"), "unsafe-inline") ||
		uiResponse.Header.Get("X-CyberAgent-UI-Version") != "web-ui.v1" {
		t.Fatalf("unexpected hosted UI response: status=%d headers=%#v body=%q err=%v",
			uiResponse.StatusCode, uiResponse.Header, servedIndex, readErr)
	}
	assetResponse, err := client.Get(strings.TrimSuffix(uiURL, "/") + "/assets/index-AbCd1234.js")
	if err != nil {
		t.Fatal(err)
	}
	servedAsset, readErr := io.ReadAll(assetResponse.Body)
	_ = assetResponse.Body.Close()
	if readErr != nil || assetResponse.StatusCode != http.StatusOK || !bytes.Equal(servedAsset, assetBody) ||
		assetResponse.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("unexpected hosted asset response: status=%d headers=%#v body=%q err=%v",
			assetResponse.StatusCode, assetResponse.Header, servedAsset, readErr)
	}

	unauthorized, err := client.Get(apiURL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, unauthorized.Body)
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous API request returned %d", unauthorized.StatusCode)
	}
	authorizedRequest, err := http.NewRequest(http.MethodGet, apiURL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	authorizedRequest.Header.Set("Authorization", "Bearer "+token)
	authorized, err := client.Do(authorizedRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, authorized.Body)
	_ = authorized.Body.Close()
	if authorized.StatusCode != http.StatusOK {
		t.Fatalf("authorized API request returned %d", authorized.StatusCode)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 || stderr.String() != "" {
			t.Fatalf("Web UI API exit failed: code=%d stderr=%s", code, stderr.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Web UI API command did not stop")
	}
	assertDirectoryOmitsValue(t, home, token)
}

func TestAPIServeCLIRejectsUnsafeConfiguration(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	missingUI := filepath.Join(t.TempDir(), "missing-ui")
	tests := []struct {
		name         string
		token        string
		controlToken string
		args         []string
		code         int
		text         string
	}{
		{name: "short token", token: "short", args: []string{"api", "serve", "--listen", "127.0.0.1:0"},
			code: 2, text: "access token"},
		{name: "non-normalized token", token: strings.Repeat("x", 32) + " ",
			args: []string{"api", "serve", "--listen", "127.0.0.1:0"}, code: 2, text: "access token"},
		{name: "short control token", token: strings.Repeat("x", 32), controlToken: "short",
			args: []string{"api", "serve", "--listen", "127.0.0.1:0"}, code: 2, text: "control token"},
		{name: "shared read and control token", token: strings.Repeat("x", 32), controlToken: strings.Repeat("x", 32),
			args: []string{"api", "serve", "--listen", "127.0.0.1:0"}, code: 2, text: "must be distinct"},
		{name: "public bind", token: strings.Repeat("x", 32),
			args: []string{"api", "serve", "--listen", "0.0.0.0:0"}, code: 5, text: "loopback"},
		{name: "missing Web UI", token: strings.Repeat("x", 32),
			args: []string{"api", "serve", "--ui-dir", missingUI}, code: 2, text: "invalid Web UI directory"},
		{name: "unknown subcommand", token: strings.Repeat("x", 32),
			args: []string{"api", "publish"}, code: 2, text: "unknown API subcommand"},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			t.Setenv(apiTokenEnvironment, current.token)
			t.Setenv(apiControlTokenEnvironment, current.controlToken)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := ExecuteContext(context.Background(), current.args, &stdout, &stderr)
			if code != current.code || !strings.Contains(stderr.String(), current.text) || stdout.Len() != 0 {
				t.Fatalf("unexpected CLI result: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		})
	}
}

func outputField(output string, key string) string {
	prefix := key + ": "
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func assertDirectoryOmitsValue(t *testing.T, root string, forbidden string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte(forbidden)) {
			t.Errorf("API token was persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
