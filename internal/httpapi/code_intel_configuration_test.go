package httpapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/codeintel"
)

func TestCodeIntelConfigurationLSPHelperProcess(t *testing.T) {
	mode := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.outputdir=onboard-lsp-") {
			mode = strings.TrimPrefix(arg, "-test.outputdir=onboard-lsp-")
		}
	}
	if mode == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				os.Exit(0)
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length:") {
				length, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
			}
		}
		if length < 2 || length > codeintel.MaxMessageBytes {
			os.Exit(2)
		}
		raw := make([]byte, length)
		if _, err := io.ReadFull(reader, raw); err != nil {
			os.Exit(0)
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(raw, &request) != nil {
			os.Exit(2)
		}
		if request.Method == "exit" {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]bool{"documentSymbolProvider": true}, "serverInfo": map[string]string{"name": "Onboarding LSP fixture", "version": "fixture"}}
		case "textDocument/documentSymbol":
			result = []any{map[string]any{"name": "FixtureMain", "kind": 12, "range": map[string]any{"start": map[string]int{"line": 1, "character": 0}, "end": map[string]int{"line": 1, "character": 21}}, "selectionRange": map[string]any{"start": map[string]int{"line": 1, "character": 5}, "end": map[string]int{"line": 1, "character": 16}}}}
		case "shutdown":
			result = nil
		default:
			result = []any{}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
		if mode == "query-error" && request.Method == "textDocument/documentSymbol" {
			delete(response, "result")
			response["error"] = map[string]any{"code": -32603, "message": "fixture semantic read failed"}
		}
		encoded, _ := json.Marshal(response)
		_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(encoded))
		_, _ = os.Stdout.Write(encoded)
	}
}

func newCodeIntelConfigurationFixture(t *testing.T, mode string) (*apiFixture, *API, *application.CodeIntelControlService, CodeIntelConfigurationRequestView, string) {
	t.Helper()
	f, extensionController, _, _ := newExtensionOnboardingFixture(t)
	if err := os.WriteFile(filepath.Join(f.workspace.RootPath, "main.go"), []byte("package main\nfunc FixtureMain() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	managedPath := filepath.Join(t.TempDir(), "code-intel.json")
	service, err := application.OpenCodeIntelControlService(f.store, nil, managedPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Manager().Close(ctx)
	})
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, ExtensionControlEnabled: true, ExtensionController: extensionController, CodeIntelSource: service.Manager(), CodeIntelController: service})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	view := CodeIntelConfigurationRequestView{Version: CodeIntelConfigurationProtocol, ServerID: "onboarding-lsp", Name: "Onboarding LSP fixture", WorkspaceID: f.workspace.ID,
		Languages: []codeintel.Language{{ID: "go", Extensions: []string{".go"}}}, Executable: filepath.Clean(executable), ExecutableSHA256: hex.EncodeToString(hash[:]),
		Arguments: []string{"-test.run=^TestCodeIntelConfigurationLSPHelperProcess$", "-test.outputdir=onboard-lsp-" + mode}, RequestTimeoutMillis: 2000}
	return f, api, service, view, managedPath
}

func TestCodeIntelConfigurationFirstSetupReviewRealQueryAndRestartReadback(t *testing.T) {
	f, api, service, input, path := newCodeIntelConfigurationFixture(t, "normal")
	request := func(target string, value any) []byte {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		response := performSessionMessageRequest(t, api, http.MethodPost, target, testControlToken, "", "application/json", strings.NewReader(string(raw)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s: %d %s", target, response.Code, response.Body.String())
		}
		return append([]byte(nil), response.Body.Bytes()...)
	}
	stageRaw := request(CodeIntelConfigurationsPath, input)
	var staged CodeIntelConfigurationView
	if err := json.Unmarshal(envelopeData(t, stageRaw), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.ReviewState != "pending_review" || len(service.Manager().Inventory()) != 0 {
		t.Fatal("staging granted process configuration")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unreviewed descriptor was persisted")
	}
	review := CodeIntelConfigurationReviewRequestView{Version: CodeIntelConfigurationProtocol, WorkspaceID: f.workspace.ID, ExpectedDescriptorFingerprint: staged.DescriptorFingerprint}
	reviewRaw := request(CodeIntelConfigurationsPath+"/"+input.ServerID+"/review", review)
	var reviewed CodeIntelConfigurationView
	if err := json.Unmarshal(envelopeData(t, reviewRaw), &reviewed); err != nil {
		t.Fatal(err)
	}
	if reviewed.ReviewState != "reviewed" || reviewed.ReviewedBy != "http_extension_operator" || service.Manager().Inventory()[0].Health != codeintel.HealthConfigured || service.Manager().Inventory()[0].Generation != "" {
		t.Fatal("review incorrectly reported process/query readiness")
	}
	probe := CodeIntelConfigurationTestRequestView{Version: CodeIntelConfigurationProtocol, WorkspaceID: f.workspace.ID, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, Tool: codeintel.ToolDocumentSymbols, Path: "main.go"}
	probeRaw := request(CodeIntelConfigurationsPath+"/"+input.ServerID+"/test", probe)
	var tested CodeIntelConfigurationTestView
	if err := json.Unmarshal(envelopeData(t, probeRaw), &tested); err != nil {
		t.Fatal(err)
	}
	if tested.Server.Health != "healthy" || tested.Result.State != "current" || len(tested.Result.Items) != 1 || tested.Result.Items[0].Name != "FixtureMain" || tested.Result.QueryFingerprint == "" || tested.Result.DocumentSHA256 == "" || tested.Result.ServerGeneration != tested.Server.Generation {
		t.Fatalf("probe omitted actual semantic result: %#v", tested)
	}
	inventoryResponse := performSessionMessageRequest(t, api, http.MethodGet, CodeIntelInventoryPath+"?workspace_id="+f.workspace.ID, testAccessToken, "", "", nil)
	var inventory CodeIntelInventoryView
	decodeDataStatus(t, inventoryResponse, http.StatusOK, &inventory)
	if len(inventory.Configurations) != 1 || inventory.Configurations[0].ReviewState != "reviewed" || inventory.Servers[0].Health != "healthy" {
		t.Fatalf("inventory readback=%#v", inventory)
	}
	for _, body := range [][]byte{stageRaw, reviewRaw, probeRaw, inventoryResponse.Body.Bytes()} {
		text := string(body)
		for _, forbidden := range []string{input.Executable, f.workspace.RootPath, `"arguments"`, `"initialization_options"`, `"document_uri"`, `"workspace_root"`} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("public LSP metadata leaked %q", forbidden)
			}
		}
	}
	reloaded, err := application.OpenCodeIntelControlService(f.store, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Manager().Close(context.Background()) })
	if got := reloaded.Manager().Inventory(); len(got) != 1 || got[0].DescriptorFingerprint != reviewed.DescriptorFingerprint || got[0].Health != codeintel.HealthConfigured || got[0].Generation != "" {
		t.Fatalf("restart recovered invented readiness: %#v", got)
	}
	if directory := os.Getenv("ISSUE276_LSP_FIXTURE_OUTPUT"); directory != "" {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for filename, body := range map[string][]byte{"stage.json": stageRaw, "review.json": reviewRaw, "test.json": probeRaw, "inventory.json": inventoryResponse.Body.Bytes()} {
			if err := os.WriteFile(filepath.Join(directory, filename), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func envelopeData(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestCodeIntelConfigurationRejectsUnauthorizedInvalidUnreviewedAndHashDrift(t *testing.T) {
	f, api, service, input, _ := newCodeIntelConfigurationFixture(t, "normal")
	raw, _ := json.Marshal(input)
	for _, token := range []string{"", testAccessToken} {
		response := performSessionMessageRequest(t, api, http.MethodPost, CodeIntelConfigurationsPath, token, "", "application/json", strings.NewReader(string(raw)))
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden {
			t.Fatalf("unauthorized setup=%d", response.Code)
		}
	}
	for _, body := range []string{`{"version":"code-intel-configuration.v1","reviewed_by":"renderer"}`, `{"version":"code-intel-configuration.v1","version":"code-intel-configuration.v1"}`, `{"version":"unexpected"}`} {
		response := performSessionMessageRequest(t, api, http.MethodPost, CodeIntelConfigurationsPath, testControlToken, "", "application/json", strings.NewReader(body))
		assertAPIError(t, response, http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	staged, err := service.Stage(context.Background(), application.CodeIntelConfigurationRequest{ServerID: input.ServerID, Name: input.Name, WorkspaceID: input.WorkspaceID, Languages: input.Languages,
		Executable: input.Executable, Arguments: input.Arguments, ExecutableSHA256: input.ExecutableSHA256, RequestTimeoutMillis: input.RequestTimeoutMillis})
	if err != nil {
		t.Fatal(err)
	}
	probe := CodeIntelConfigurationTestRequestView{Version: CodeIntelConfigurationProtocol, WorkspaceID: f.workspace.ID, ExpectedDescriptorFingerprint: staged.DescriptorFingerprint, Tool: codeintel.ToolDocumentSymbols, Path: "main.go"}
	raw, _ = json.Marshal(probe)
	response := performSessionMessageRequest(t, api, http.MethodPost, CodeIntelConfigurationsPath+"/"+input.ServerID+"/test", testControlToken, "", "application/json", strings.NewReader(string(raw)))
	assertAPIError(t, response, http.StatusPreconditionFailed, "FAILED_PRECONDITION")
	input.ExecutableSHA256 = strings.Repeat("0", 64)
	raw, _ = json.Marshal(input)
	response = performSessionMessageRequest(t, api, http.MethodPost, CodeIntelConfigurationsPath, testControlToken, "", "application/json", strings.NewReader(string(raw)))
	var badHash CodeIntelConfigurationView
	decodeDataStatus(t, response, http.StatusAccepted, &badHash)
	review := CodeIntelConfigurationReviewRequestView{Version: CodeIntelConfigurationProtocol, WorkspaceID: f.workspace.ID, ExpectedDescriptorFingerprint: badHash.DescriptorFingerprint}
	raw, _ = json.Marshal(review)
	response = performSessionMessageRequest(t, api, http.MethodPost, CodeIntelConfigurationsPath+"/"+input.ServerID+"/review", testControlToken, "", "application/json", strings.NewReader(string(raw)))
	assertAPIError(t, response, http.StatusPreconditionFailed, "FAILED_PRECONDITION")
	if len(service.Manager().Inventory()) != 0 {
		t.Fatal("denied hash review activated configuration")
	}
}

func TestCodeIntelConfigurationProbeInitializationDoesNotClaimQuerySuccess(t *testing.T) {
	_, api, service, input, _ := newCodeIntelConfigurationFixture(t, "query-error")
	staged, err := service.Stage(context.Background(), application.CodeIntelConfigurationRequest{ServerID: input.ServerID, Name: input.Name, WorkspaceID: input.WorkspaceID, Languages: input.Languages,
		Executable: input.Executable, Arguments: input.Arguments, ExecutableSHA256: input.ExecutableSHA256, RequestTimeoutMillis: input.RequestTimeoutMillis})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := service.Review(context.Background(), input.ServerID, application.CodeIntelConfigurationReviewRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: staged.DescriptorFingerprint, ReviewedBy: "fixture_operator"})
	if err != nil {
		t.Fatal(err)
	}
	probe := CodeIntelConfigurationTestRequestView{Version: CodeIntelConfigurationProtocol, WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, Tool: codeintel.ToolDocumentSymbols, Path: "main.go"}
	raw, _ := json.Marshal(probe)
	response := performSessionMessageRequest(t, api, http.MethodPost, CodeIntelConfigurationsPath+"/"+input.ServerID+"/test", testControlToken, "", "application/json", strings.NewReader(string(raw)))
	assertAPIError(t, response, http.StatusServiceUnavailable, "UNAVAILABLE")
	if got := service.Manager().Inventory(); len(got) != 1 || got[0].Health != codeintel.HealthHealthy {
		t.Fatalf("fixture failed initialization rather than query: %#v", got)
	}
	probe.Path = "missing.go"
	raw, _ = json.Marshal(probe)
	response = performSessionMessageRequest(t, api, http.MethodPost, CodeIntelConfigurationsPath+"/"+input.ServerID+"/test", testControlToken, "", "application/json", strings.NewReader(string(raw)))
	if response.Code == http.StatusAccepted {
		t.Fatal("missing document falsely reported query success")
	}
}

func TestCodeIntelConfigurationFailedPublicationPreservesLiveReviewAndRejectsInvalidProbeBeforeStart(t *testing.T) {
	_, _, service, input, path := newCodeIntelConfigurationFixture(t, "normal")
	stage := func(name string) application.CodeIntelConfiguration {
		value, err := service.Stage(context.Background(), application.CodeIntelConfigurationRequest{ServerID: input.ServerID, Name: name, WorkspaceID: input.WorkspaceID, Languages: input.Languages,
			Executable: input.Executable, Arguments: input.Arguments, ExecutableSHA256: input.ExecutableSHA256, RequestTimeoutMillis: input.RequestTimeoutMillis})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := stage(input.Name)
	reviewed, err := service.Review(context.Background(), input.ServerID, application.CodeIntelConfigurationReviewRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: first.DescriptorFingerprint, ReviewedBy: "fixture_operator"})
	if err != nil {
		t.Fatal(err)
	}
	probe := application.CodeIntelConfigurationTestRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, Tool: codeintel.ToolDocumentSymbols, Path: "missing.go"}
	if _, err = service.Test(context.Background(), input.ServerID, probe); err == nil {
		t.Fatal("missing document probe was accepted")
	}
	if service.Manager().Inventory()[0].Generation != "" {
		t.Fatal("invalid probe input spawned a server")
	}
	probe.Path = "main.go"
	succeeded, err := service.Test(context.Background(), input.ServerID, probe)
	if err != nil {
		t.Fatal(err)
	}
	changed := stage("Changed fixture")
	if err = os.WriteFile(path, []byte("external configuration change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Review(context.Background(), input.ServerID, application.CodeIntelConfigurationReviewRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: changed.DescriptorFingerprint, ReviewedBy: "fixture_operator"}); err == nil {
		t.Fatal("changed managed source was overwritten")
	}
	if got := service.Manager().Inventory()[0]; got.Generation != succeeded.Server.Generation || got.DescriptorFingerprint != reviewed.DescriptorFingerprint || got.Health != codeintel.HealthHealthy {
		t.Fatalf("failed publication replaced/stopped reviewed runtime: %#v", got)
	}
	if _, err = service.Test(context.Background(), input.ServerID, probe); err != nil {
		t.Fatalf("old live configuration no longer works after failed publication: %v", err)
	}
}

func TestCodeIntelConfigurationProbeRejectsExecutableHashDriftAfterReview(t *testing.T) {
	_, _, service, input, _ := newCodeIntelConfigurationFixture(t, "normal")
	raw, err := os.ReadFile(input.Executable)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "reviewed-lsp.exe")
	if err = os.WriteFile(copyPath, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	staged, err := service.Stage(context.Background(), application.CodeIntelConfigurationRequest{ServerID: input.ServerID, Name: input.Name, WorkspaceID: input.WorkspaceID, Languages: input.Languages,
		Executable: copyPath, Arguments: input.Arguments, ExecutableSHA256: input.ExecutableSHA256, RequestTimeoutMillis: input.RequestTimeoutMillis})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := service.Review(context.Background(), input.ServerID, application.CodeIntelConfigurationReviewRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: staged.DescriptorFingerprint, ReviewedBy: "fixture_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(copyPath, append(raw, []byte("changed after review")...), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Test(context.Background(), input.ServerID, application.CodeIntelConfigurationTestRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: reviewed.DescriptorFingerprint, Tool: codeintel.ToolDocumentSymbols, Path: "main.go"}); err == nil {
		t.Fatal("post-review executable hash drift was ignored")
	}
	if service.Manager().Inventory()[0].Generation != "" {
		t.Fatal("hash-drifted executable was started")
	}
}

func TestCodeIntelConfigurationEveryMutationRequiresControlBearerAndEnabledGate(t *testing.T) {
	f, api, service, _, _ := newCodeIntelConfigurationFixture(t, "normal")
	disabled, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, CodeIntelSource: service.Manager(), CodeIntelController: service})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{CodeIntelConfigurationsPath, CodeIntelConfigurationsPath + "/onboarding-lsp/review", CodeIntelConfigurationsPath + "/onboarding-lsp/test"} {
		response := performSessionMessageRequest(t, api, http.MethodPost, path, testAccessToken, "", "application/json", strings.NewReader(`{"version":"code-intel-configuration.v1"}`))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("read bearer allowed %s: %d", path, response.Code)
		}
		response = performSessionMessageRequest(t, disabled, http.MethodPost, path, testControlToken, "", "application/json", strings.NewReader(`{"version":"code-intel-configuration.v1"}`))
		if response.Code != http.StatusNotFound {
			t.Fatalf("disabled gate allowed %s: %d", path, response.Code)
		}
	}
}

func TestCodeIntelConfigurationConcurrentOwnersDoNotLoseReviewedUpdates(t *testing.T) {
	f, _, first, input, path := newCodeIntelConfigurationFixture(t, "normal")
	second, err := application.OpenCodeIntelControlService(f.store, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Manager().Close(context.Background()) })
	stage := func(service *application.CodeIntelControlService, id string) application.CodeIntelConfiguration {
		configuration, err := service.Stage(context.Background(), application.CodeIntelConfigurationRequest{ServerID: id, Name: input.Name, WorkspaceID: input.WorkspaceID, Languages: input.Languages,
			Executable: input.Executable, Arguments: input.Arguments, ExecutableSHA256: input.ExecutableSHA256, RequestTimeoutMillis: input.RequestTimeoutMillis})
		if err != nil {
			t.Fatal(err)
		}
		return configuration
	}
	firstDraft, secondDraft := stage(first, "first-lsp"), stage(second, "second-lsp")
	start := make(chan struct{})
	type outcome struct {
		configuration application.CodeIntelConfiguration
		err           error
	}
	done := make(chan outcome, 2)
	for _, candidate := range []struct {
		service *application.CodeIntelControlService
		draft   application.CodeIntelConfiguration
	}{{first, firstDraft}, {second, secondDraft}} {
		go func(service *application.CodeIntelControlService, draft application.CodeIntelConfiguration) {
			<-start
			configuration, err := service.Review(context.Background(), draft.ServerID, application.CodeIntelConfigurationReviewRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: draft.DescriptorFingerprint, ReviewedBy: "fixture_operator"})
			done <- outcome{configuration, err}
		}(candidate.service, candidate.draft)
	}
	close(start)
	var winner application.CodeIntelConfiguration
	successes := 0
	for range 2 {
		current := <-done
		if current.err == nil {
			successes++
			winner = current.configuration
		} else if apperror.CodeOf(current.err) != apperror.CodeConflict {
			t.Fatalf("concurrent review error=%v", current.err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent publications=%d, expected one", successes)
	}
	saved, _, err := codeintel.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Servers) != 1 || saved.Servers[0].ID != winner.ServerID || saved.Servers[0].Fingerprint() != winner.DescriptorFingerprint {
		t.Fatalf("saved source lost the successful review: %#v", saved)
	}
	recovered, err := application.OpenCodeIntelControlService(f.store, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Manager().Close(context.Background()) })
	draft := stage(recovered, "recovered-lsp")
	if _, err = recovered.Review(context.Background(), draft.ServerID, application.CodeIntelConfigurationReviewRequest{WorkspaceID: input.WorkspaceID, ExpectedDescriptorFingerprint: draft.DescriptorFingerprint, ReviewedBy: "fixture_operator"}); err != nil {
		t.Fatalf("unheld persisted sidecar blocked the next review: %v", err)
	}
	saved, _, err = codeintel.LoadConfig(path)
	if err != nil || len(saved.Servers) != 2 {
		t.Fatalf("recovered publication=%#v err=%v", saved, err)
	}
}

type codeIntelCatalogController struct{ workspaceID string }

// The route catalog uses valid, inert metadata; the real subprocess fixture
// above independently proves staging, review, initialize and query boundaries.
func newCodeIntelOpenAPITestController(t *testing.T, f *apiFixture) CodeIntelController {
	t.Helper()
	return codeIntelCatalogController{workspaceID: f.workspace.ID}
}

func (s codeIntelCatalogController) configuration(id string) application.CodeIntelConfiguration {
	return application.CodeIntelConfiguration{ServerID: id, ServerName: "Catalog LSP fixture", WorkspaceID: s.workspaceID,
		Languages: []codeintel.Language{{ID: "go", Extensions: []string{".go"}}}, ExecutableSHA256: strings.Repeat("a", 64), DescriptorFingerprint: strings.Repeat("a", 64),
		ReviewState: "reviewed", Source: codeintel.Source{Kind: "operator_config", Label: "code-intel.json", SHA256: strings.Repeat("a", 64)},
		ReviewedBy: "fixture_operator", ReviewedAt: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)}
}

func (s codeIntelCatalogController) Configurations(context.Context, string) ([]application.CodeIntelConfiguration, error) {
	return []application.CodeIntelConfiguration{s.configuration("onboarding-lsp")}, nil
}
func (s codeIntelCatalogController) Stage(_ context.Context, r application.CodeIntelConfigurationRequest) (application.CodeIntelConfiguration, error) {
	result := s.configuration(r.ServerID)
	result.ReviewState = "pending_review"
	result.ReviewedBy = ""
	result.ReviewedAt = time.Time{}
	return result, nil
}
func (s codeIntelCatalogController) Review(_ context.Context, id string, _ application.CodeIntelConfigurationReviewRequest) (application.CodeIntelConfiguration, error) {
	return s.configuration(id), nil
}
func (s codeIntelCatalogController) Test(_ context.Context, id string, r application.CodeIntelConfigurationTestRequest) (application.CodeIntelConfigurationTest, error) {
	configuration := s.configuration(id)
	capabilities := codeintel.Capabilities{DocumentSymbols: true}
	now := configuration.ReviewedAt
	server := codeintel.CapabilitySnapshot{ProtocolVersion: codeintel.ProtocolVersion, ServerID: id, ServerName: configuration.ServerName, WorkspaceID: s.workspaceID,
		Languages: []string{"go"}, Source: configuration.Source, DescriptorFingerprint: configuration.DescriptorFingerprint, CapabilityFingerprint: strings.Repeat("b", 64),
		Generation: strings.Repeat("c", 64), Health: codeintel.HealthHealthy, Capabilities: capabilities, ModelVisibleTools: capabilities.ToolNames(), QualifiedAt: &now, ProcessOwned: true, ReadOnly: true}
	result := codeintel.Result{ProtocolVersion: codeintel.ProtocolVersion, Tool: r.Tool, State: codeintel.EvidenceCurrent, EvidenceLevel: "semantic_language_server",
		Provenance: codeintel.Provenance{ProtocolVersion: codeintel.ProtocolVersion, WorkspaceID: s.workspaceID, RootFingerprint: strings.Repeat("d", 64), DirtyDigest: strings.Repeat("e", 64),
			DocumentURI: "file:///fixture/main.go", DocumentPath: "main.go", DocumentSHA256: strings.Repeat("f", 64), DocumentVersion: 1, ServerID: id,
			ServerGeneration: server.Generation, CapabilityFingerprint: server.CapabilityFingerprint, QueryFingerprint: strings.Repeat("1", 64)},
		Items: []codeintel.EvidenceItem{}, Page: codeintel.Page{Limit: 20, Returned: 0, Total: 0}, Warnings: []string{}}
	return application.CodeIntelConfigurationTest{Configuration: configuration, Server: server, Result: result}, nil
}
