package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/pricing"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/standardcodedelivery"
	"cyberagent-workbench/internal/testfixtures/legacyskill"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/uievidence"
	"cyberagent-workbench/internal/verification"
	"cyberagent-workbench/internal/workspace"
)

func TestOpenAPIDocumentIsDeterministicCapabilitySeparatedAndSecretFree(t *testing.T) {
	first, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) == 0 || first[len(first)-1] != '\n' || !json.Valid(first) {
		t.Fatal("OpenAPI generation is not deterministic canonical JSON")
	}
	for _, forbidden := range []string{`"lease_id"`, `"pending_input"`, `"fencing_token"`, `"api_key"`} {
		if bytes.Contains(first, []byte(forbidden)) {
			t.Fatalf("OpenAPI document exposed forbidden internal property %s", forbidden)
		}
	}

	var document openAPIDocument
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	if document.OpenAPI != openAPISpecVersion || document.JSONSchemaDialect != openAPIJSONSchemaDialect ||
		document.Info.Version != Version || document.Info.License.Identifier != "Apache-2.0" ||
		document.ReadOnly || len(document.Security) != 1 {
		t.Fatalf("OpenAPI metadata is incomplete: %#v", document)
	}
	expectedPaths := sortedOpenAPIPaths()
	specsByOperation := make(map[string]openAPIOperationSpec)
	expectedOperationCounts := make(map[string]int)
	for _, spec := range openAPIOperationSpecs() {
		specsByOperation[spec.OperationID] = spec
		expectedOperationCounts[spec.Path]++
	}
	actualPaths := make([]string, 0, len(document.Paths))
	operationIDs := make(map[string]struct{}, len(document.Paths))
	for path, item := range document.Paths {
		actualPaths = append(actualPaths, path)
		type operationEntry struct {
			method    string
			operation *openAPIOperation
		}
		operations := make([]operationEntry, 0, 4)
		if item.Get != nil {
			operations = append(operations, operationEntry{method: http.MethodGet, operation: item.Get})
		}
		if item.Post != nil {
			operations = append(operations, operationEntry{method: http.MethodPost, operation: item.Post})
		}
		if item.Put != nil {
			operations = append(operations, operationEntry{method: http.MethodPut, operation: item.Put})
		}
		if item.Patch != nil {
			operations = append(operations, operationEntry{method: http.MethodPatch, operation: item.Patch})
		}
		if item.Delete != nil {
			operations = append(operations, operationEntry{method: http.MethodDelete, operation: item.Delete})
		}
		expectedOperations := expectedOperationCounts[path]
		if len(operations) != expectedOperations {
			t.Fatalf("path %s exposes %d operations, want %d: %#v",
				path, len(operations), expectedOperations, item)
		}
		for _, entry := range operations {
			operation := entry.operation
			spec, found := specsByOperation[operation.OperationID]
			if !found || spec.Path != path {
				t.Fatalf("path %s exposes undocumented operation %#v", path, operation)
			}
			expectedMethod := spec.Method
			if expectedMethod == "" {
				expectedMethod = http.MethodGet
			}
			status, statusErr := openAPISuccessStatus(spec)
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if expectedMethod != entry.method || operation.Responses[status] == nil ||
				(operation.RequestBody != nil) != (spec.RequestType != nil) {
				t.Fatalf("path %s operation does not match its Go spec: %#v", path, operation)
			}
			if spec.Control {
				if operation.ReadOnly || len(operation.Security) != 1 ||
					operation.Security[0]["ControlBearerAuth"] == nil || (operation.RequestBody == nil && !spec.EmptyBody) {
					t.Fatalf("path %s has an incomplete control operation: %#v", path, operation)
				}
			} else if !operation.ReadOnly || len(operation.Security) != 0 {
				t.Fatalf("path %s has an incomplete read operation: %#v", path, operation)
			}
			if _, duplicate := operationIDs[operation.OperationID]; duplicate {
				t.Fatalf("duplicate OpenAPI operation id %q", operation.OperationID)
			}
			operationIDs[operation.OperationID] = struct{}{}
		}
	}
	sort.Strings(actualPaths)
	if !reflect.DeepEqual(actualPaths, expectedPaths) {
		t.Fatalf("OpenAPI path catalog drifted:\n got %v\nwant %v", actualPaths, expectedPaths)
	}

	var raw struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(first, &raw); err != nil {
		t.Fatal(err)
	}
	allowedMethods := make(map[string]map[string]bool)
	for _, spec := range openAPIOperationSpecs() {
		method := strings.ToLower(spec.Method)
		if method == "" {
			method = "get"
		}
		if allowedMethods[spec.Path] == nil {
			allowedMethods[spec.Path] = make(map[string]bool)
		}
		allowedMethods[spec.Path][method] = true
	}
	for path, item := range raw.Paths {
		for method := range item {
			if !allowedMethods[path][method] {
				t.Fatalf("OpenAPI path %s exposed unexpected operation %q", path, method)
			}
		}
	}
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "RunExecutionLeaseView", "lease_id")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "SupervisorCheckpointView", "pending_input")
	for _, field := range []string{"content", "content_sha256", "requested_by", "session_id", "session_message_id"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"OperatorSteeringMessageView", field)
	}
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "ArtifactView", "content")
	for _, field := range []string{"payload_json", "authority_json", "result_json",
		"environment", "environment_values", "stdin", "credentials", "raw_output",
		"raw_payload_json", "pid", "process_group", "host_path", "executable_path"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"ThreadActivityToolDetailView", field)
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"ThreadActivityCommandDetailView", field)
	}
	assertOpenAPISchemaOptional(t, document.Components.Schemas,
		"ThreadTranscriptItemView", "activity_detail_ref")
	for _, field := range []string{"pid", "process_id", "executable", "executable_path",
		"profile", "profile_path", "devtools_endpoint", "websocket_url", "token",
		"execution_fence", "authorization_fingerprint"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"FullCDPSessionView", field)
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"FullCDPSessionOpenRequestView", field)
	}
	assertOpenAPISchemaOptional(t, document.Components.Schemas,
		"FullCDPSessionView", "browser")
	for _, operation := range []*openAPIOperation{
		document.Paths[FullCDPSessionControlPathTemplate].Post,
		document.Paths[FullCDPSessionCloseControlPathTemplate].Post,
	} {
		found := false
		for _, parameter := range operation.Parameters {
			if parameter.Name != "Idempotency-Key" {
				continue
			}
			found = true
			if parameter.Schema["minLength"] != float64(domain.MinAgentOperationKeyBytes) ||
				parameter.Schema["maxLength"] != float64(domain.MaxAgentOperationKeyBytes) ||
				parameter.Schema["pattern"] != `^\S+$` {
				t.Fatalf("Full CDP idempotency key schema drifted: %#v", parameter.Schema)
			}
		}
		if !found {
			t.Fatal("Full CDP control operation omitted Idempotency-Key")
		}
	}
	assertOpenAPISchemaOmits(t, document.Components.Schemas,
		"ProviderCredentialStatusView", "secret")
	assertOpenAPIPropertyFlag(t, document.Components.Schemas,
		"ProviderCredentialRequestView", "secret", "writeOnly", true)
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "MCPCredentialStatusView", "secret")
	assertOpenAPIPropertyFlag(t, document.Components.Schemas, "MCPCredentialRequestView", "secret", "writeOnly", true)
	assertOpenAPIPropertyFlag(t, document.Components.Schemas, "MCPCredentialRequestView", "secret", "maxLength", float64(credential.MaxSecretBytes))
	assertOpenAPIPropertyFlag(t, document.Components.Schemas,
		"ProviderCredentialListView", "items", "minItems", float64(4))
	assertOpenAPIPropertyFlag(t, document.Components.Schemas,
		"ProviderCredentialListView", "items", "maxItems",
		float64(4+modelregistry.MaxCustomProviderDefinitions))
	assertOpenAPIPropertyFlag(t, document.Components.Schemas,
		"ProviderCredentialStatusView", "provider", "maxLength", float64(64))
	credentialPath, credentialPathFound := document.Paths[ProviderCredentialPathTemplate]
	if !credentialPathFound || credentialPath.Post == nil {
		t.Fatal("Provider credential control path is missing")
	}
	providerParameterFound := false
	for _, parameter := range credentialPath.Post.Parameters {
		if parameter.Name != "provider" {
			continue
		}
		providerParameterFound = true
		if _, closed := parameter.Schema["enum"]; closed {
			t.Fatal("Provider credential path must allow persistent custom Provider identities")
		}
	}
	if !providerParameterFound {
		t.Fatal("Provider credential path parameter is missing")
	}
	assertOpenAPIEnum(t, document.Components.Schemas, "ProviderAvailabilityView", "kind",
		[]string{"local", "anthropic_compatible", "openai_compatible", "ollama"})
	assertOpenAPIEnum(t, document.Components.Schemas, "ModelHarnessAvailabilityView",
		"transport_protocol", []string{"mock", "anthropic_messages",
			"openai_chat_completions", "openai_responses", "ollama_chat", "provider_contract"})
	assertOpenAPIEnum(t, document.Components.Schemas, "ProviderDefinition", "transport",
		[]string{"openai_chat_completions", "openai_responses", "anthropic_messages"})
	failureReasons := []string{"none", "not_configured", "authentication", "network",
		"rate_limit", "capacity", "model_not_found", "protocol_incompatible", "context_limit", "output_limit", "paused", "refusal"}
	assertOpenAPIEnum(t, document.Components.Schemas, "ProviderDiagnosticView",
		"failure_reason", failureReasons)
	assertOpenAPIEnum(t, document.Components.Schemas, "ModelHarnessQualificationView",
		"failure_reason", failureReasons)
	qualificationStatuses := []string{"not_configured", "available", "protocol_mismatch",
		"auth_failed", "network_failed", "rate_limit", "capacity", "model_unsupported", "response_incomplete"}
	assertOpenAPIEnum(t, document.Components.Schemas, "ProviderDiagnosticView",
		"qualification_status", qualificationStatuses)
	assertOpenAPIEnum(t, document.Components.Schemas, "ModelHarnessQualificationView",
		"qualification_status", qualificationStatuses)
	assertOpenAPIEnum(t, document.Components.Schemas, "ModelHarnessAvailabilityView",
		"latest_qualification_status", append([]string{""}, qualificationStatuses...))
	for _, field := range []string{"path", "content", "command", "hook"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"SkillPackageInstallRequestView", field)
	}
	for _, field := range []string{"command", "content", "path", "request_fingerprint",
		"decision_reason", "requested_by", "reviewed_by", "grant_id"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"ApprovalQueueItemView", field)
	}
	for _, field := range []string{"executable", "argv", "shell", "environment",
		"raw_output"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"ControlledCommandProposalView", field)
	}
	for _, field := range []string{"command", "shell", "environment_values", "raw_output"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"HostCommandProposalView", field)
	}
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "AgentNodeView", "status_reason")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "DelegationReviewView", "reason")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "DelegationApplicationView", "policy_fingerprint")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "FanoutExecutionShardView", "report_json")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "FanoutExecutionShardView", "error_reason")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "FindingArtifactEvidenceView", "note")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "FindingArtifactEvidenceView", "attached_by")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "RunExecutionProfileView", "requested_by")
	assertOpenAPISchemaOmits(t, document.Components.Schemas, "RunExecutionProfileView", "reason")
	for _, field := range []string{"selection_id", "mission_id", "mode_snapshot_id", "requested_by",
		"operation_id", "fingerprint", "digest", "content", "path"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"ExternalSkillProjectionView", field)
	}
	for _, field := range []string{"selection_id", "installation_id", "fingerprint", "sha256",
		"object_key", "content", "path", "archive_bytes", "content_bytes"} {
		assertOpenAPISchemaOmits(t, document.Components.Schemas,
			"ExternalSkillProjectionItemView", field)
	}
	assertOpenAPISchemaOptional(t, document.Components.Schemas, "AgentGraphView", "root_agent_id")
}

func TestOpenAPIGoldenDocumentMatchesGoDTOs(t *testing.T) {
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "docs", "openapi.json")
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed OpenAPI document: %v", err)
	}
	if !bytes.Equal(committed, generated) {
		t.Fatalf("%s is stale; regenerate it with `cyberagent api openapi --output docs/openapi.json`", path)
	}
}

type openAPILiveThreadModelRouteController struct {
	delegate *application.ThreadModelRouteService
}

func (c openAPILiveThreadModelRouteController) Catalog(context.Context) (
	application.ModelRouteCatalog, error,
) {
	// The product catalog intentionally hides the deterministic mock Provider.
	// This live-route fixture still needs one non-networked, Harness-ready route
	// so POST /threads exercises the successful authenticated handler rather
	// than the product's no-configured-model precondition.
	return application.ModelRouteCatalog{
		ProtocolVersion: application.ModelRouteCatalogProtocolVersion,
		Generation:      1,
		Routes: []application.ModelRouteCatalogItem{{
			ProviderID: "mock", ProviderName: "OpenAPI fixture",
			Model: "mock-code", Enabled: true, HarnessReady: true, Selectable: true,
			DefaultForRoutes: []string{"code", "review"},
		}},
	}, nil
}

func (c openAPILiveThreadModelRouteController) Get(ctx context.Context,
	threadID string,
) (application.ThreadModelRouteView, error) {
	return c.delegate.Get(ctx, threadID)
}

func (c openAPILiveThreadModelRouteController) Change(ctx context.Context,
	request application.ChangeThreadModelRouteRequest,
) (application.ThreadModelRouteView, error) {
	return c.delegate.Change(ctx, request)
}

// These route-catalog controllers keep writes and network calls out of this
// contract test. Dedicated Thread Git/PR/Plan tests exercise the real services;
// here the authenticated handlers must decode and forward the exact fixture.
type openAPIThreadPlanController struct {
	*threadTurnControllerStub
	threadID, runID string
}

func (c openAPIThreadPlanController) PromoteCurrentSteering(_ context.Context, r domain.PromoteOperatorSteeringRequest) (domain.PromoteOperatorSteeringResult, error) {
	if r.ExpectedExecutionID != "execution-openapi-promotion" || r.ExpectedAttemptID != "attempt-openapi-promotion" || r.ExpectedRevision != 0 || r.ExpectedContentSHA256 != strings.Repeat("a", 64) || r.RequestedBy != "http_session_operator" {
		return domain.PromoteOperatorSteeringResult{}, fmt.Errorf("promotion route did not forward captured intent")
	}
	return domain.PromoteOperatorSteeringResult{Receipt: domain.OperatorSteeringPromotionReceipt{
		ID: "promotion-openapi", MessageID: r.MessageID, ReplacementMessageID: "steer-openapi-replacement", RunID: c.runID, SessionID: r.SessionID,
		ExpectedRevision: r.ExpectedRevision, ContentSHA256: r.ExpectedContentSHA256, TargetAttemptID: r.ExpectedAttemptID, ExecutionID: r.ExpectedExecutionID,
		CancellationID: "cancel-openapi-promotion", RequestedBy: r.RequestedBy, CreatedAt: time.Now().UTC(),
		OperationKeyDigest: strings.Repeat("b", 64), RequestFingerprint: strings.Repeat("c", 64)}}, nil
}

func (c openAPIThreadPlanController) InspectPlan(_ context.Context, r application.ThreadPlanControlRequest) (application.ThreadPlanControlResult, error) {
	if r.Version != application.PlanDeliveryControlProtocolVersion || r.ThreadID != c.threadID || r.RunID != c.runID || r.Action != "enter_plan" || r.OperationKey == "" || r.RequestedBy != "http_thread_operator" {
		return application.ThreadPlanControlResult{}, fmt.Errorf("Plan route did not forward the original request identity")
	}
	return application.ThreadPlanControlResult{ThreadID: c.threadID, RunID: c.runID, Action: r.Action, State: "not_received"}, nil
}

func (c openAPIThreadPlanController) ControlPlan(ctx context.Context, r application.ThreadPlanControlRequest) (application.ThreadPlanControlResult, error) {
	result, err := c.InspectPlan(ctx, r)
	result.State = "completed"
	return result, err
}

type openAPIThreadGitController struct{ binding application.ThreadGitContext }

func (c openAPIThreadGitController) State(_ context.Context, threadID string) (application.ThreadGitState, error) {
	if threadID != c.binding.ThreadID {
		return application.ThreadGitState{}, fmt.Errorf("Git route changed Thread identity")
	}
	return application.ThreadGitState{Version: application.ThreadGitProtocolVersion, ThreadGitContext: c.binding,
		Branches: []string{"main", c.binding.Branch}, Changes: []repository.Change{}, CanExecute: true}, nil
}

func (c openAPIThreadGitController) Preview(ctx context.Context, threadID string, r application.ThreadGitPreviewRequest) (application.ThreadGitPreview, error) {
	if _, err := c.State(ctx, threadID); err != nil {
		return application.ThreadGitPreview{}, err
	}
	if r.Version != application.ThreadGitProtocolVersion || r.RunID != c.binding.RunID || !reflect.DeepEqual(r.Spec, application.ThreadGitSpec{Operation: "stage", Paths: []string{"README.md"}}) {
		return application.ThreadGitPreview{}, fmt.Errorf("Git preview route changed the selected operation")
	}
	return application.ThreadGitPreview{Version: r.Version, ThreadGitContext: c.binding, Spec: r.Spec,
		PreviewFingerprint: strings.Repeat("b", 64), CanExecute: true}, nil
}

func (c openAPIThreadGitController) Execute(ctx context.Context, threadID string, r application.ThreadGitExecuteRequest) (application.ThreadGitResult, error) {
	if _, err := c.Preview(ctx, threadID, application.ThreadGitPreviewRequest{Version: r.Version, RunID: r.RunID, Spec: r.Spec}); err != nil {
		return application.ThreadGitResult{}, err
	}
	if r.OperationKey != "openapi-thread-git-operation-0001" || r.ExpectedPreviewFingerprint != strings.Repeat("b", 64) || r.RequestedBy != "openapi_test" {
		return application.ThreadGitResult{}, fmt.Errorf("Git execute route changed the reviewed request identity")
	}
	return application.ThreadGitResult{Version: r.Version, ThreadID: threadID, RunID: r.RunID, Spec: &r.Spec,
		OperationID: "git-operation-openapi", State: "completed", ReceiptSaved: true}, nil
}

func (c openAPIThreadGitController) Observe(ctx context.Context, threadID, key string) (application.ThreadGitResult, error) {
	if _, err := c.State(ctx, threadID); err != nil {
		return application.ThreadGitResult{}, err
	}
	if key != "openapi-thread-git-operation-0001" {
		return application.ThreadGitResult{}, fmt.Errorf("Git observation route did not forward the original key")
	}
	return application.ThreadGitResult{Version: application.ThreadGitProtocolVersion, ThreadID: threadID,
		RunID: c.binding.RunID, State: "not_received", Observed: true}, nil
}

type openAPIThreadPullRequestController struct{ binding application.ThreadGitContext }

func (c openAPIThreadPullRequestController) Discover(_ context.Context, threadID, connection, base string) (application.ThreadPullRequestDiscovery, error) {
	if threadID != c.binding.ThreadID || connection != "github-connection-openapi" || base != "main" {
		return application.ThreadPullRequestDiscovery{}, fmt.Errorf("PR discovery route changed the selected repository context")
	}
	return application.ThreadPullRequestDiscovery{Version: application.ThreadPullRequestVersion, Context: c.binding,
		ConnectionID: connection, Repository: githubreview.RepositoryIdentity{Host: "github.com", Owner: "example", Name: "fixture", FullName: "example/fixture"},
		BaseBranch: base, BaseSHA: strings.Repeat("c", 40), RemoteHeadSHA: c.binding.HeadSHA, HeadPublished: true,
		PullRequests: []githubreview.PullRequest{}, Diagnostics: []githubreview.Diagnostic{}, CheckedAt: time.Now().UTC()}, nil
}

func (c openAPIThreadPullRequestController) Preview(ctx context.Context, threadID string, r application.ThreadPullRequestPreviewRequest) (application.ThreadPullRequestPreviewResult, error) {
	if _, err := c.Discover(ctx, threadID, r.ConnectionID, r.BaseBranch); err != nil {
		return application.ThreadPullRequestPreviewResult{}, err
	}
	if r.Version != application.ThreadPullRequestVersion || r.ExpectedRunID != c.binding.RunID || r.ExpectedHeadSHA != c.binding.HeadSHA || r.OperationKey != "openapi-thread-pr-operation-0001" || r.Title != "OpenAPI draft" || r.Body != "Reviewed fixture changes." {
		return application.ThreadPullRequestPreviewResult{}, fmt.Errorf("PR preview route changed the reviewed intent")
	}
	return application.ThreadPullRequestPreviewResult{Version: r.Version, ExistingPullRequests: []githubreview.PullRequest{},
		Preview: &application.ThreadPullRequestPreview{Version: r.Version, OperationID: "git-remote-openapi-pr", ThreadID: threadID,
			RunID: c.binding.RunID, SessionID: c.binding.SessionID, WorkspaceID: c.binding.WorkspaceID,
			SourceWorkspaceID: c.binding.SourceWorkspaceID, ConnectionID: r.ConnectionID, DraftOnly: true,
			BindingFingerprint: c.binding.StatusFingerprint, ApprovalFingerprint: strings.Repeat("d", 64), CreatedAt: time.Now().UTC()}}, nil
}

func (c openAPIThreadPullRequestController) Observe(_ context.Context, threadID, key string) (application.ThreadPullRequestResult, error) {
	if threadID != c.binding.ThreadID || key != "openapi-thread-pr-operation-0001" {
		return application.ThreadPullRequestResult{}, fmt.Errorf("PR observation route did not forward the original key")
	}
	return application.ThreadPullRequestResult{Version: application.ThreadPullRequestVersion, ThreadID: threadID,
		RunID: c.binding.RunID, State: "not_received", CheckedAt: time.Now().UTC()}, nil
}

func (c openAPIThreadPullRequestController) Create(_ context.Context, threadID string, r application.ThreadPullRequestCreateRequest) (application.ThreadPullRequestResult, error) {
	if threadID != c.binding.ThreadID || r.Version != application.ThreadPullRequestVersion || r.OperationID != "git-remote-openapi-pr" || r.ApprovalID != "approval-openapi-pr" {
		return application.ThreadPullRequestResult{}, fmt.Errorf("PR create route changed the reviewed operation and approval")
	}
	return application.ThreadPullRequestResult{Version: r.Version, ThreadID: threadID, RunID: c.binding.RunID,
		OperationID: r.OperationID, State: "created", ReceiptSaved: true, CheckedAt: time.Now().UTC()}, nil
}

func (c openAPIThreadPullRequestController) Refresh(_ context.Context, threadID string, r application.ThreadPullRequestRefreshRequest) (application.ThreadPullRequestRefreshResult, error) {
	if threadID != c.binding.ThreadID || r.Version != application.ThreadPullRequestVersion || r.ConnectionID != "github-connection-openapi" || r.PullRequest != 7 {
		return application.ThreadPullRequestRefreshResult{}, fmt.Errorf("PR refresh route changed the selected PR")
	}
	return application.ThreadPullRequestRefreshResult{Version: r.Version, ThreadID: threadID, RunID: c.binding.RunID,
		LocalHeadSHA: c.binding.HeadSHA, Omissions: []string{}}, nil
}

func (c openAPIThreadPullRequestController) ImportCredential(ctx context.Context, threadID string, r application.ThreadPullRequestCredentialRequest) (application.GitHubReviewCredentialView, error) {
	if threadID != c.binding.ThreadID || r.Version != application.ThreadPullRequestVersion || r.ConnectionID != "github-connection-openapi" || r.Token != "openapi-fixture-token-not-a-real-credential" {
		return application.GitHubReviewCredentialView{}, fmt.Errorf("PR credential route changed the selected connection")
	}
	return (&githubReviewControllerStub{}).CredentialStatus(ctx, r.ConnectionID)
}

func TestOpenAPIRoutesMatchAuthenticatedLiveHandlers(t *testing.T) {
	fixture := newAPIFixture(t)
	workflowController, workflowRun, workflowDefinition, workflowExecution := prepareOpenAPIWorkflowTarget(t, fixture)
	fixture.api.skillCatalogController = application.NewSkillCatalogService(fixture.store, nil)
	fixture.api.skillCatalogControlEnabled = true
	fixture.api.chatGPTAuthEnabled = true
	fixture.api.chatGPTAuthController = &chatGPTAuthControllerStub{}
	fixture.api.projectActionsController = &projectActionsControllerStub{}
	preparationController := &preparationControllerStub{}
	fixture.api.projectPreparationController = preparationController
	environmentFixture := newSandboxEnvironmentAPIFixture(t)
	fixture.api.sandboxEnvironmentController = environmentFixture.api.sandboxEnvironmentController
	fixture.api.sandboxEnvironmentControlEnabled = true
	fixture.api.eventStream = testEventStreamConfig(1, 100*time.Millisecond)
	fullCDPStartedAt := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	fullCDPExpiresAt := fullCDPStartedAt.Add(15 * time.Minute)
	fullCDPReady := application.FullCDPSessionView{
		Version: application.FullCDPSessionProtocolVersion, RunID: fixture.run.ID,
		SessionID: "full_cdp_session-openapi-live-0001",
		State:     application.FullCDPSessionReady,
		Browser: application.FullCDPBrowserSelection{
			Product: "chrome", Channel: "stable",
		},
		TargetOrigin: "http://127.0.0.1:18080", RuntimeAvailable: true,
		StartedAt: &fullCDPStartedAt, ExpiresAt: &fullCDPExpiresAt,
	}
	fullCDPClosedAt := fullCDPStartedAt.Add(time.Minute)
	fullCDPClosed := fullCDPReady
	fullCDPClosed.State = application.FullCDPSessionClosed
	fullCDPClosed.CloseReason = application.FullCDPCloseOperator
	fullCDPClosed.CompletedAt = &fullCDPClosedAt
	fullCDPClosed.CDPClosed = true
	fullCDPClosed.ProcessTreeQuiescent = true
	fullCDPClosed.ProfileReleased = true
	fullCDPClosed.ProfileCleaned = true
	var imageBuffer bytes.Buffer
	if err := png.Encode(&imageBuffer, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	imageBytes := imageBuffer.Bytes()
	imageReceipt, err := fixture.store.SaveWorkspaceImage(t.Context(), fixture.workspace.ID,
		"openapi-uploaded-image-seed", "image/png", "image.png", imageBytes)
	if err != nil {
		t.Fatal(err)
	}
	fileBytes := []byte("上传附件原始文本")
	fileReceipt, err := fixture.store.SaveWorkspaceFileAttachment(t.Context(), fixture.workspace.ID,
		"openapi-uploaded-file-seed", "text/plain", "notes.txt", fileBytes)
	if err != nil {
		t.Fatal(err)
	}
	previewView := application.FullCDPPreviewView{Version: application.FullCDPPreviewProtocolVersion,
		RunID: fixture.run.ID, SessionID: fullCDPReady.SessionID, CanonicalURL: fullCDPReady.TargetOrigin + "/", CapturedAt: time.Now().UTC(),
		Page:  application.FullCDPPreviewPage{SnapshotID: strings.Repeat("a", 64), UntrustedEvidence: true},
		Image: application.FullCDPPreviewImage{MediaType: "image/png", Bytes: len(imageBytes), SHA256: imageReceipt.SHA256, Width: 2, Height: 3}}
	fixture.api.fullCDPSessionController = &fullCDPPreviewStub{
		fullCDPSessionControllerStub: fullCDPSessionControllerStub{
			view: fullCDPReady, openResult: application.FullCDPSessionResult{Session: fullCDPReady},
			closeResult: application.FullCDPSessionResult{Session: fullCDPClosed},
		},
		capture: application.FullCDPPreviewCapture{View: previewView, PNG: imageBytes},
	}
	fixture.api.fullCDPSessionControlEnabled = true
	fixture.api.agentBrowserController = &agentBrowserHTTPStub{png: imageBytes, digest: imageReceipt.SHA256,
		view: application.AgentBrowserView{Version: "agent_browser_status.v1", RunID: fixture.run.ID,
			SessionID: "agent-browser-openapi", Generation: 1, State: "ready", Headless: true,
			UpdatedAt: time.Now().UTC(), ArtifactLocator: "openapi-browser.png", ScreenshotSHA256: imageReceipt.SHA256,
			ScreenshotBytes: len(imageBytes), Capabilities: application.AgentBrowserCapabilities{Available: true}}}
	fixture.api.gitAdvancedController = &gitAdvancedControllerStub{}
	fixture.api.gitAdvancedControlEnabled = true
	fixture.api.githubReviewController = &githubReviewControllerStub{}
	fixture.api.githubReviewControlEnabled = true
	fixture.api.standardCodeDeliveryController = &standardCodeDeliveryControllerStub{
		binding: standardcodedelivery.Binding{
			MissionID:         fixture.run.MissionID,
			SessionID:         fixture.run.SessionID,
			SourceWorkspaceID: fixture.workspace.ID,
		},
	}
	childRun, child, childAttempt, childModel :=
		prepareOpenAPISpecialistCancellationTarget(t, fixture)
	childExecution := &specialistExecutionCapture{}
	fixture.api.specialistExecutionController = childExecution
	_, profileRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI execution profile target", Profile: "code",
			Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunExecutionPermissionService(fixture.store,
		domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
			RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
		}).Change(t.Context(), application.ChangeRunExecutionPermissionRequest{
		RunID: profileRun.ID, Mode: string(domain.RunExecutionPermissionFull),
		OperationKey: "openapi-browser-cdp-debug-permission-0001",
		RequestedBy:  "openapi_test", Reason: "prepare full CDP debug contract",
		ConfirmFull: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Selecting Full records its Full CDP preference without browser authority;
	// the OpenAPI browser route below therefore has a real restricted transition target.
	_, interactionRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI execution interaction target", Profile: "code",
			Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunExecutionProfileService(fixture.store).Change(t.Context(),
		application.ChangeRunExecutionProfileRequest{
			RunID: interactionRun.ID, Profile: string(domain.RunExecutionProfileLocal),
			OperationKey: "openapi-interaction-profile-0001",
			RequestedBy:  "openapi_test", Reason: "prepare controlled interaction",
		}); err != nil {
		t.Fatal(err)
	}
	_, lifecycleRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI lifecycle target", Profile: "code",
			Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	_, executionCreated, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI execution target", Profile: "code",
			ModelRoute: "mock/mock-code", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	executionRun, err := application.NewRunService(fixture.store).Start(t.Context(),
		executionCreated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnqueueOperatorSteering(t.Context(),
		domain.EnqueueOperatorSteeringRequest{
			RunID: executionRun.ID, SessionID: executionRun.SessionID,
			Content:      "OpenAPI execution input",
			OperationKey: "openapi-execution-queue-0001", RequestedBy: "openapi_test",
		}); err != nil {
		t.Fatal(err)
	}
	_, planModeRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI Plan entry target", Profile: "code",
			Phase: "deliver", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	planRun, planProposal := prepareOpenAPIPlanControlTarget(t, fixture)
	checker := policy.NewDefaultChecker()
	gateway := toolgateway.New(fixture.store, checker)
	pendingApproval, err := gateway.Invoke(t.Context(), toolgateway.ToolCall{
		Name: toolgateway.ShellTool, Arguments: map[string]string{"command": "echo OpenAPI approval"},
		RunID: fixture.run.ID, SessionID: fixture.run.SessionID,
		WorkspaceID: fixture.workspace.ID, RequestedBy: "openapi_test",
	})
	if err != nil || pendingApproval.Proposal == nil {
		t.Fatalf("prepare OpenAPI approval=%#v err=%v", pendingApproval, err)
	}
	approvalRecord, err := fixture.store.GetApprovalByProposal(t.Context(), pendingApproval.Proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	fileEditRecord, err := fileedit.NewManager(fixture.store).Propose(t.Context(), fileedit.Proposal{
		SessionID: fixture.run.SessionID, WorkspaceID: fixture.workspace.ID,
		WorkspaceRoot: fixture.workspace.RootPath, Path: "openapi-review.txt",
		ProposedText: "bounded OpenAPI review\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	revertSource, err := fileedit.NewManager(fixture.store).Propose(t.Context(), fileedit.Proposal{
		SessionID: fixture.run.SessionID, WorkspaceID: fixture.workspace.ID,
		WorkspaceRoot: fixture.workspace.RootPath, Path: "openapi-revert-source.txt",
		ProposedText: "bounded applied source\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewFileEditReviewService(fixture.store).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: fixture.run.ID,
		EditID: revertSource.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	// Seed an applied tool edit under the fixture's existing execution owner.
	if _, err := fileedit.NewManager(fixture.store).Approve(t.Context(), revertSource.ID,
		fixture.workspace.RootPath); err != nil {
		t.Fatal(err)
	}
	_, wakeCreated, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI wake target", Profile: "code",
			Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	wakeRun, err := application.NewRunService(fixture.store).Start(t.Context(), wakeCreated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnqueueOperatorSteering(t.Context(),
		domain.EnqueueOperatorSteeringRequest{RunID: wakeRun.ID, SessionID: wakeRun.SessionID,
			Content: "OpenAPI wake input", OperationKey: "openapi-wake-queue-0001",
			RequestedBy: "openapi_test"}); err != nil {
		t.Fatal(err)
	}
	_, wakeExecutionCreated, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI wake execution target", Profile: "code",
			ModelRoute: "mock/mock-code", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	wakeExecutionRun, err := application.NewRunService(fixture.store).Start(t.Context(),
		wakeExecutionCreated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnqueueOperatorSteering(t.Context(),
		domain.EnqueueOperatorSteeringRequest{RunID: wakeExecutionRun.ID,
			SessionID: wakeExecutionRun.SessionID, Content: "OpenAPI foreground wake input",
			OperationKey: "openapi-wake-execution-queue-0001",
			RequestedBy:  "openapi_test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunWakeControlService(fixture.store).Schedule(t.Context(),
		application.ScheduleRunWakeRequest{Version: domain.RunWakeControlProtocolVersion,
			RunID: wakeExecutionRun.ID, OperationKey: "openapi-wake-execution-schedule-0001",
			RequestedBy: "openapi_test", MaxAttempts: 2, BaseBackoffSeconds: 5,
			MaxBackoffSeconds: 30, MaxElapsedSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	skillArchive := buildOpenAPISkillPackage(t)
	objects, err := skills.NewLocalPackageObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	codeSkill, found := builtins.Get("code")
	if !found {
		t.Fatal("OpenAPI fixture requires the built-in code Skill")
	}
	projectSkillRef := "code@" + codeSkill.Version
	_, projectSkillRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI project Skill selection target", Profile: "code",
			WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2},
			ProjectConfig: &projectconfig.Effective{Protocol: projectconfig.ProtocolVersion,
				SkillSuggestions: []string{projectSkillRef}}})
	if err != nil {
		t.Fatal(err)
	}
	// The installed-Skill selection route is exercised on a Run with no pinned
	// project suggestions, so only the operator-named admission can succeed here.
	_, installedSkillRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI installed Skill selection target", Profile: "code",
			WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	_, scheduledCreated, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI scheduled observation target", Profile: "code",
			Surface: "code", Phase: "plan", WorkspaceID: fixture.workspace.ID,
			Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	scheduledJobs := application.NewScheduledJobService(fixture.store)
	scheduledAnchor := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	scheduledSeed, err := scheduledJobs.Create(t.Context(), application.CreateScheduledJobRequest{
		Version: domain.ScheduledJobProtocolVersion, RunID: scheduledCreated.ID,
		TargetRunID: scheduledCreated.ID,
		Schedule: domain.ScheduledJobSchedule{Kind: domain.ScheduledJobOnce, Timezone: "UTC",
			AnchorAt: scheduledAnchor, MisfirePolicy: domain.ScheduledJobMisfireRunOnce},
		DeadlineAt: scheduledAnchor.Add(time.Hour), StopOnTargetTerminal: true,
		MaxRounds: 1, MaxModelCalls: 0, MaxElapsedSeconds: 3600,
		Retry: domain.ScheduledJobRetryPolicy{MaxAttempts: 2,
			InitialBackoffSeconds: 1, MaxBackoffSeconds: 10},
		Notification: domain.ScheduledJobNotifyAll, ExecutionMode: domain.ScheduledJobReadOnly,
		OperationKey: "openapi-scheduled-seed-0001", RequestedBy: "openapi_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.runLifecycleEnabled = true
	fixture.api.runExecutionEnabled = true
	fixture.api.planDeliveryControlEnabled = true
	fixture.api.approvalControlEnabled = true
	fixture.api.modelControlEnabled = true
	fixture.api.providerCredentialEnabled = true
	fixture.api.fileEditReviewEnabled = true
	fixture.api.fileEditProposalEnabled = true
	fixture.api.runWakeControlEnabled = true
	fixture.api.fileEditApplyEnabled = true
	fixture.api.runWakeExecutionEnabled = true
	fixture.api.scheduledJobControlEnabled = true
	fixture.api.skillInstallationEnabled = true
	fixture.api.evidenceAttachmentEnabled = true
	fixture.api.verificationEvidenceEnabled = true
	fixture.api.embeddedAnalyzerExecutionEnabled = true
	fixture.api.extensionControlEnabled = true
	fixture.api.extensionController = &mcpInputRoutingStub{runID: fixture.run.ID}
	fixture.api.codeIntelController = newCodeIntelOpenAPITestController(t, fixture)
	onboardingFixture, _, _, _ := newExtensionOnboardingFixture(t)
	credentialFixture, credentialBinding, credentialStatus, _ := newMCPCredentialFixture(t)
	fixture.api.dockerSandboxControlEnabled = true
	fixture.api.dockerSandboxController = &dockerSandboxControllerStub{}
	fixture.api.dockerEnvironmentController = &dockerEnvironmentStub{}
	fixture.api.runLifecycleController = application.NewRunLifecycleControlService(fixture.store)
	executionController := application.NewRunExecutionHandoffService(
		fixture.store, llm.NewDefaultRouter(), policy.NewDefaultChecker())
	fixture.api.runExecutionController = executionController
	now := time.Now().UTC()
	fixture.api.publicModelStreamSource = staticPublicModelStreamSource{found: true,
		snapshot: application.PublicModelStreamSnapshot{
			Version: application.PublicModelStreamVersion,
			Call: application.ActiveCallInfo{
				RunID: fixture.run.ID, SessionID: fixture.run.SessionID,
				AttemptID: "openapi-public-stream-attempt", ModelAttempt: 1,
				TransportAttempt: 1, MaxAttempts: 1, Provider: "mock", Model: "mock-code",
				StartedAt: now,
			},
			Revision: 1, ContentKind: application.PublicModelStreamRootMessage,
			Text: "OpenAPI public model stream", Provisional: true,
			UpdatedAt: now,
		}}
	fixture.api.planDeliveryController = application.NewPlanDeliveryControlService(fixture.store)
	fixture.api.approvalController = application.NewApprovalControlService(fixture.store,
		gateway, checker)
	fixture.api.controlledCommandProposalController =
		&controlledCommandProposalControllerStub{
			view: application.ControlledCommandProposalView{
				Proposal: runner.ControlledCommandProposal{
					ID:                  "controlled-command-proposal-openapi",
					ProtocolVersion:     runner.ControlledCommandProposalProtocolVersion,
					PolicyVersion:       runner.ControlledCommandProposalPolicyVersion,
					RunID:               fixture.run.ID,
					MissionID:           fixture.run.MissionID,
					SessionID:           fixture.run.SessionID,
					WorkspaceID:         fixture.workspace.ID,
					Kind:                runner.ControlledCommandGitStatus,
					TimeoutMilliseconds: 5000,
					Purpose:             "inspect OpenAPI Git state",
					PermissionMode:      domain.RunExecutionPermissionConservative,
					PermissionRevision:  1,
					Fingerprint:         strings.Repeat("a", 64),
					CreatedAt:           time.Now().UTC(),
				},
			},
		}
	hostView := testHostCommandProposalView(t, fixture.run.ID, fixture.run.MissionID,
		fixture.run.SessionID, fixture.workspace.ID)
	hostView.Proposal.ID = "controlled-command-proposal-openapi"
	hostView.Review = &runner.HostCommandReview{Decision: runner.HostCommandReviewDeny}
	fixture.api.hostCommandProposalController = &hostCommandProposalControllerStub{view: hostView}
	fixture.api.modelControlController = application.NewModelControlService(
		fixture.api.modelRegistry, fixture.store)
	fixture.api.threadModelRouteController = openAPILiveThreadModelRouteController{
		delegate: application.NewThreadModelRouteService(
			fixture.store, fixture.api.modelRegistry),
	}
	fixture.api.providerSearchReadinessController = &searchDiagnosticsFake{
		providerSearchReadinessControllerFake: providerSearchReadinessControllerFake{
			value: application.ProviderSearchReadiness{
				ProtocolVersion: application.ProviderSearchReadinessProtocolVersion,
				RunID:           fixture.run.ID, ModelRoute: fixture.run.Config.ModelRoute,
				Provider: "mock", Model: "mock-code", SearchPolicy: "disabled",
				State:       application.ProviderSearchStateNetworkDisabled,
				Reason:      application.ProviderSearchReasonRunNetworkDisabled,
				Remediation: application.ProviderSearchRemediationEnableNetwork,
				NetworkMode: "disabled",
			},
		},
	}
	providerDefinitionController, err := application.NewProviderDefinitionService(
		fixture.store, fixture.api.modelRegistry)
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.providerDefinitionEnabled = true
	fixture.api.providerDefinitionController = providerDefinitionController
	fixture.api.priceSnapshotController = fixture.store
	fixture.api.fanoutExecutionController = application.NewReadOnlyFanoutExecutionService(
		fixture.store, llm.NewDefaultRouter(), policy.NewDefaultChecker())
	fixture.api.childTaskControlController = application.NewChildTaskControlService(fixture.store)
	fixture.api.batchDeliveryControlEnabled = true
	fixture.api.batchDeliveryController = application.NewBatchDeliveryWorkbenchService(application.NewBatchDeliveryService(fixture.store))
	credentialStore := credential.NewMemoryStore()
	fixture.api.providerCredentialController = application.NewProviderCredentialService(
		credentialStore).WithRegistryReload(fixture.api.modelRegistry, fixture.store)
	fixture.api.fileEditReviewController = application.NewFileEditReviewService(fixture.store)
	fileEditProposalController := application.NewFileEditProposalService(fixture.store,
		checker)
	fixture.api.fileEditProposalController = fileEditProposalController
	fixture.api.runWakeController = application.NewRunWakeControlService(fixture.store)
	fixture.api.fileEditApplyController = application.NewFileEditApplyService(fixture.store, checker)
	fixture.api.runWakeExecutionController = application.NewForegroundRunWakeConsumer(
		fixture.store, executionController)
	fixture.api.scheduledJobController = scheduledJobs
	fixture.api.embeddedAnalyzerExecutionController =
		application.NewEmbeddedAnalyzerExecutionService(fixture.store)
	fixture.api.workspaceCheckpointControlEnabled = true
	fixture.api.workspaceCheckpointController = &workspaceCheckpointControllerStub{}
	uiAttemptID := "ui-attempt-openapi-0001"
	uiArtifact, err := uievidence.SealArtifact(uievidence.ArtifactMetadata{
		ID: fixture.artifactID, AttemptID: uiAttemptID, RunID: fixture.run.ID,
		StepID: "navigate", Kind: uievidence.ArtifactDOM, MIME: "application/octet-stream",
		Viewport:     uievidence.Viewport{Width: 1440, Height: 900, DPR: 1},
		SourceCommit: "non-git", RetentionPolicy: uievidence.ArtifactRetentionRunHistory,
		Untrusted: true, CreatedAt: time.Now().UTC(),
	}, []byte("evidence"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.uiEvidenceControlEnabled = true
	fixture.api.uiEvidenceController = &uiEvidenceControllerStub{
		attempt: uievidence.Attempt{ProtocolVersion: uievidence.AttemptProtocolVersion,
			Manifest: uievidence.Manifest{AttemptID: uiAttemptID, RunID: fixture.run.ID},
			Status:   uievidence.StatusNotRun, FailureStage: uievidence.FailureNone},
		bundle: application.UIEvidenceBundle{Steps: []uievidence.StepReceipt{},
			Artifacts: []uievidence.ArtifactMetadata{}},
		artifact: uiArtifact,
	}
	uiStub := fixture.api.uiEvidenceController.(*uiEvidenceControllerStub)
	uiStub.bundle.Attempt = uiStub.attempt
	fixture.api.skillInstallationController = application.NewSkillPackageRegistryService(
		fixture.store, objects, builtins)
	removalSkillRef := seedOpenAPILegacySkillForRemoval(t, fixture)
	steering, err := fixture.store.EnqueueOperatorSteering(t.Context(),
		domain.EnqueueOperatorSteeringRequest{
			RunID: fixture.run.ID, SessionID: fixture.run.SessionID,
			Content:      "OpenAPI cancellation target",
			OperationKey: "openapi-cancellation-target-0001",
			RequestedBy:  "openapi_test",
		})
	if err != nil {
		t.Fatal(err)
	}
	evidenceContent := "OpenAPI evidence\n"
	revisionSteering, err := fixture.store.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{
		RunID: fixture.run.ID, SessionID: fixture.run.SessionID, Content: "OpenAPI revision target",
		OperationKey: "openapi-revision-target-0001", RequestedBy: "openapi_test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.workspace.RootPath, "README.md"),
		[]byte(evidenceContent), 0o644); err != nil {
		t.Fatal(err)
	}
	evidenceDigest := sha256.Sum256([]byte(evidenceContent))
	proposalSource, err := fileEditProposalController.IssueSource(t.Context(),
		fixture.run.ID, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	recoverySource, err := fileEditProposalController.IssueSource(t.Context(),
		fixture.run.ID, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	recoveryProposal, err := fileEditProposalController.Propose(t.Context(),
		application.CreateFileEditProposalRequest{
			Version: application.FileEditProposalProtocolVersion, RunID: fixture.run.ID,
			SourceHandle: recoverySource.Handle, ProposedText: "OpenAPI recovery proposal\n",
		})
	if err != nil {
		t.Fatal(err)
	}
	verificationPlan, err := application.NewVerificationPlanService(fixture.store).Record(
		t.Context(), application.RecordVerificationPlanRequest{
			Version: verification.PlanProtocolVersion, RunID: fixture.run.ID,
			Title: "OpenAPI association plan", Summary: "Live route metadata",
			Items: []application.VerificationPlanItemRequest{{Title: "Live association",
				ExpectedObservation: "Observe an explicit operator result"}},
			OperationKey: "openapi-association-plan-operation-0001", AuthoredBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	verificationEvidence, err := application.NewVerificationEvidenceService(fixture.store).Record(
		t.Context(), application.RecordVerificationEvidenceRequest{
			Version: verification.EvidenceProtocolVersion, RunID: fixture.run.ID,
			Outcome: string(verification.OutcomePass), Title: "OpenAPI association evidence",
			Summary:      "Explicit live route observation",
			OperationKey: "openapi-association-evidence-operation-0001", RecordedBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	openAPISnapshot, err := application.NewVerificationSnapshotExportService(fixture.store).Build(
		t.Context(), fixture.run.ID, verificationPlan.Plan.ID, 1,
		application.VerificationSnapshotExportFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	openAPIReceipt, err := application.NewVerificationSnapshotReceiptService(fixture.store).Record(
		t.Context(), application.RecordVerificationSnapshotReceiptRequest{
			Version: verification.SnapshotReceiptProtocolVersion, RunID: fixture.run.ID,
			PlanID: verificationPlan.Plan.ID, PlanItemOrdinal: 1,
			Format:                         openAPISnapshot.Format,
			SnapshotHighWaterEventSequence: openAPISnapshot.SnapshotHighWaterEventSequence,
			ContentSHA256:                  openAPISnapshot.ContentSHA256, ConfirmMetadataSnapshot: true,
			OperationKey: "openapi-snapshot-review-receipt-0001", RecordedBy: "operator",
		})
	if err != nil {
		t.Fatal(err)
	}
	confirmCandidateNote := createHTTPMemoryCandidateSource(t, fixture)
	rejectCandidateNote := createHTTPMemoryCandidateSource(t, fixture)
	openAPICandidates, err := application.NewContextMemoryCandidateService(fixture.store).List(t.Context(),
		contextmgr.MemoryCandidateFilter{WorkspaceID: fixture.workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	var openAPIConfirmCandidate, openAPIRejectCandidate contextmgr.MemoryCandidate
	for _, candidate := range openAPICandidates {
		if candidate.Source.NoteID == confirmCandidateNote.ID {
			openAPIConfirmCandidate = candidate
		} else if candidate.Source.NoteID == rejectCandidateNote.ID {
			openAPIRejectCandidate = candidate
		}
	}
	if openAPIConfirmCandidate.ID == "" || openAPIRejectCandidate.ID == "" {
		t.Fatal("OpenAPI memory candidate fixture was not derived")
	}
	openAPIMemory, err := application.NewContextMemoryService(fixture.store).Create(t.Context(),
		contextmgr.CreateMemoryRequest{Scope: contextmgr.MemoryScopeUser,
			ScopeID: contextmgr.LocalUserMemoryScope, Title: "OpenAPI memory fixture",
			Content: "Preserve the public contract.", RequestedBy: "openapi_test"})
	if err != nil {
		t.Fatal(err)
	}
	openAPICheckpoint, err := application.NewContextContinuityService(fixture.store).Checkpoint(
		t.Context(), application.CreateContinuityCheckpointRequest{RunID: fixture.run.ID,
			Title: "OpenAPI checkpoint fixture", RequestedBy: "openapi_test"})
	if err != nil {
		t.Fatal(err)
	}
	_, openAPIThreadRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI Thread fixture", Profile: "review",
			WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.threadContextCompactionController = application.NewThreadContextCompactionService(fixture.store, llm.NewDefaultRouter())
	_, contextRecoveryRun, err := application.NewRunService(fixture.store).Create(t.Context(), application.CreateRunRequest{Goal: "OpenAPI pending compaction", Profile: "review", WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	contextRecoveryOperation, _, err := fixture.store.BeginThreadContextCompaction(t.Context(), domain.ThreadContextCompactionRequest{Version: domain.ThreadContextCompactionVersion, ThreadID: domain.InitialThreadID(contextRecoveryRun.ID), ExpectedRunID: contextRecoveryRun.ID, OperationKey: "openapi-context-recovery", RequestedBy: "openapi_test"})
	if err != nil {
		t.Fatal(err)
	}
	openAPIThread, err := fixture.store.GetThreadByRun(t.Context(), openAPIThreadRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, recoveryCreated, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI Thread recovery fixture", Profile: "review",
			WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	recoveryRun, err := application.NewRunService(fixture.store).Start(t.Context(),
		recoveryCreated.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveryThread, err := fixture.store.GetThreadByRun(t.Context(), recoveryRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.EnqueueOperatorSteering(t.Context(),
		domain.EnqueueOperatorSteeringRequest{RunID: recoveryRun.ID,
			SessionID: recoveryRun.SessionID, Content: "OpenAPI recovery message",
			OperationKey: "openapi-recovery-message-0001", RequestedBy: "openapi_test"}); err != nil {
		t.Fatal(err)
	}
	recoveryHandoffKey := "openapi-recovery-handoff-0001"
	recoveryOperation := domain.RunExecutionHandoffOperation{
		ID: idgen.New("run-handoff"), ProtocolVersion: domain.RunExecutionHandoffProtocolVersion,
		KeyDigest: runmutation.RunExecutionHandoffOperationDigest(
			recoveryRun.ID, recoveryHandoffKey),
		RequestFingerprint: runmutation.RunExecutionHandoffRequestFingerprint(
			recoveryRun.ID, "openapi_test", 1),
		RunID: recoveryRun.ID, SessionID: recoveryRun.SessionID,
		RequestedBy: "openapi_test", MaxSteps: 1, CreatedAt: time.Now().UTC(),
	}
	if _, _, err := fixture.store.PrepareRunExecutionHandoff(t.Context(),
		recoveryOperation); err != nil {
		t.Fatal(err)
	}
	recoveryLease, err := fixture.store.AcquireRunExecutionLease(t.Context(),
		domain.AcquireRunExecutionLeaseRequest{RunID: recoveryRun.ID,
			OwnerID: "openapi-recovery-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.CompleteRunExecutionHandoff(t.Context(),
		recoveryOperation.ID, recoveryLease.Lease, domain.RunExecutionHandoffFailed,
		"failed_precondition", "failed_precondition", 0, false, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.ReleaseRunExecutionLease(t.Context(),
		recoveryLease.Lease); err != nil {
		t.Fatal(err)
	}
	_, networkAuthorityRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI network authority fixture", Profile: "review",
			WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	_, webAccessRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI web access fixture", Profile: "review",
			WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.executionPermissionCapabilities.RuntimeAuthority =
		domain.NewExecutionPermissionRuntimeAuthority()
	openAPIThreadSession, err := fixture.store.GetSession(t.Context(), openAPIThreadRun.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.threadTurnController = &openAPIThreadPlanController{
		threadID: openAPIThread.ID, runID: openAPIThreadRun.ID,
		threadTurnControllerStub: &threadTurnControllerStub{
			result: application.ExecuteThreadTurnResult{
				Submission: application.SubmitThreadMessageResult{Thread: openAPIThread,
					Run: openAPIThreadRun, Session: openAPIThreadSession,
					Message: domain.OperatorSteeringMessage{ID: "steer-openapi-thread-turn",
						RunID: openAPIThreadRun.ID, SessionID: openAPIThreadRun.SessionID,
						Sequence: 1, Status: domain.OperatorSteeringCommitted,
						CreatedAt: time.Now().UTC()}},
				ExecutionStarted: true, ModelCalled: true,
			},
		},
	}
	fixture.api.threadReview = application.NewThreadReviewService(fixture.store).
		WithCodeHandoff(application.NewCodeHandoffService(fixture.store))
	threadGitBinding := application.ThreadGitContext{ThreadID: openAPIThread.ID,
		RunID: openAPIThreadRun.ID, SessionID: openAPIThreadSession.ID,
		WorkspaceID: fixture.workspace.ID, SourceWorkspaceID: fixture.workspace.ID,
		RootPath: fixture.workspace.RootPath, HeadSHA: strings.Repeat("a", 40),
		Branch: "feature/openapi", StatusFingerprint: strings.Repeat("a", 64),
		RemoteURLs: []application.ThreadGitRemote{{Name: "origin", URL: "https://github.com/example/fixture.git"}}}
	fixture.api.threadGitController = openAPIThreadGitController{binding: threadGitBinding}
	fixture.api.threadPullRequestController = openAPIThreadPullRequestController{binding: threadGitBinding}
	prFollowups := &prFollowupControllerStub{expectedThreadID: openAPIThread.ID}
	fixture.api.prFollowupController = prFollowups
	projectHooks := &projectHooksControllerStub{}
	fixture.api.projectHooksController = projectHooks
	legacyArchiveSession := session.New(fixture.workspace.ID,
		"OpenAPI legacy archive fixture", "review")
	if err := fixture.store.SaveSession(t.Context(), legacyArchiveSession); err != nil {
		t.Fatal(err)
	}
	_, deleteThreadRun, err := application.NewRunService(fixture.store).Create(t.Context(),
		application.CreateRunRequest{Goal: "OpenAPI delete Thread fixture", Profile: "review",
			WorkspaceID: fixture.workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(fixture.store).Cancel(t.Context(),
		deleteThreadRun.ID); err != nil {
		t.Fatal(err)
	}
	deleteThread, err := fixture.store.GetThreadByRun(t.Context(), deleteThreadRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	standardCodeController := &standardCodePresetControllerStub{}
	fixture.api.workspaceImportEnabled = true
	fixture.api.workspaceImporter = workspace.NewManager("", fixture.store)
	standardCodeAPI, err := New(fixture.store, Config{
		AccessToken: testAccessToken, ControlToken: testControlToken,
		RunControlEnabled: true, ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			WorkspaceSandboxEnabled: true,
		},
		StandardCodePresetEnabled:    true,
		StandardCodePresetController: standardCodeController,
		AppVersion:                   "openapi-standard-code-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	replacements := map[string]string{
		"{thread_id}":         openAPIThread.ID,
		"{operation_key}":     "openapi-thread-git-operation-0001",
		"{operation_id}":      "prep-saved",
		"{activity_ref}":      "activity-openapi-missing-0001",
		"{run_id}":            fixture.run.ID,
		"{workspace_id}":      fixture.workspace.ID,
		"{image_id}":          imageReceipt.ID,
		"{attachment_id}":     fileReceipt.ID,
		"{agent_id}":          child.ID,
		"{session_id}":        fixture.run.SessionID,
		"{message_id}":        steering.Message.ID,
		"{work_item_id}":      fixture.workItems[0].ID,
		"{note_id}":           fixture.notes[0].ID,
		"{artifact_id}":       fixture.artifactID,
		"{report_id}":         "report-openapi-missing-0001",
		"{execution_id}":      "fanout-execution-openapi-missing-0001",
		"{definition_id}":     workflowDefinition.ID,
		"{approval_id}":       approvalRecord.ID,
		"{proposal_id}":       "controlled-command-proposal-openapi",
		"{batch_delivery_id}": "batch-openapi-missing-0001",
		"{connection_id}":     "github-connection-openapi",
		"{followup_id}":       "followup-one",
		"{attempt_id}":        uiAttemptID,
		"{edit_id}":           fileEditRecord.ID,
		"{source_edit_id}":    revertSource.ID,
		"{job_id}":            scheduledSeed.Job.ID,
		"{slot}":              "test",
		"{action}":            string(domain.ScheduledJobPause),
		"{object_id}":         strings.Repeat("a", 40),
		"{plan_id}":           verificationPlan.Plan.ID,
		"{ordinal}":           "1",
		"{route}":             "code",
		"{provider}":          "mimo",
		"{server_id}":         "mcp-openapi",
		"{input_id}":          "mcp-input-openapi",
		"{installation_id}":   "plugin-openapi",
		"{memory_id}":         openAPIMemory.ID,
		"{candidate_id}":      openAPIConfirmCandidate.ID,
		"{node_id}":           openAPICheckpoint.ID,
	}
	_, branchSourceRun, err := application.NewRunService(fixture.store).Create(t.Context(), application.CreateRunRequest{
		Goal: "OpenAPI branch source", Profile: "review", WorkspaceID: fixture.workspace.ID, Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SaveSessionMessage(t.Context(), session.NewMessage(branchSourceRun.SessionID, "user", "Retain the selected conversation")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SaveSessionMessage(t.Context(), session.NewMessage(branchSourceRun.SessionID, "assistant", "Selected OpenAPI reply")); err != nil {
		t.Fatal(err)
	}
	branchEvents, err := fixture.store.ListRunEvents(t.Context(), branchSourceRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	branchSourceEventID := branchEvents[len(branchEvents)-1].EventID
	for _, spec := range openAPIOperationSpecs() {
		requestPath := spec.Path
		for placeholder, value := range replacements {
			requestPath = strings.ReplaceAll(requestPath, placeholder, value)
		}
		if strings.HasPrefix(spec.Path, "/api/v1/runs/{run_id}/workflows") {
			requestPath = strings.ReplaceAll(requestPath, fixture.run.ID, workflowRun.ID)
			requestPath = strings.ReplaceAll(requestPath, replacements["{execution_id}"], workflowExecution.ID)
		}
		if spec.Path == SessionSteeringRevisionPathTemplate || spec.Path == SessionSteeringRevisionObservationPathTemplate {
			requestPath = strings.ReplaceAll(requestPath, steering.Message.ID, revisionSteering.Message.ID)
		}
		if spec.Path == SpecialistModelCancellationPathTemplate || spec.Path == SpecialistExecutionPathTemplate || spec.Path == SpecialistContinuationPathTemplate || spec.Path == SpecialistReadRevokePathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", childRun.ID)
			requestPath = strings.ReplaceAll(requestPath, "{agent_id}", child.ID)
		} else if spec.Path == ProjectSkillSelectionPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", projectSkillRun.ID)
		} else if spec.Path == InstalledSkillSelectionPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", installedSkillRun.ID)
		} else if spec.Path == RunExecutionProfileControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", profileRun.ID)
		} else if spec.Path == RunExecutionPermissionControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", profileRun.ID)
		} else if spec.Path == ThreadExecutionPermissionControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{thread_id}", openAPIThread.ID)
		} else if spec.Path == ThreadRunRecoveryControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{thread_id}", recoveryThread.ID)
		} else if spec.Path == ThreadContextCompactionRecoveryPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{thread_id}", domain.InitialThreadID(contextRecoveryRun.ID))
			requestPath = strings.ReplaceAll(requestPath, "{operation_id}", contextRecoveryOperation.OperationID)
		} else if spec.Path == SessionArchiveControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{session_id}", legacyArchiveSession.ID)
		} else if spec.Path == RunBrowserCDPPermissionControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", profileRun.ID)
		} else if spec.Path == RunNetworkAuthorityControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", networkAuthorityRun.ID)
		} else if spec.Path == RunWebAccessControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", webAccessRun.ID)
		} else if spec.Path == ProviderDefinitionPathTemplate ||
			spec.Path == ProviderDefinitionDeletePathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{provider}", "openapi-custom")
		} else if spec.Path == RunExecutionInteractionControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", interactionRun.ID)
		} else if spec.Path == RunLifecycleControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", lifecycleRun.ID)
		} else if spec.Path == PlanModeControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", planModeRun.ID)
		} else if spec.Path == PlanDirectionControlPathTemplate ||
			spec.Path == PlanDeliveryControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", planRun.ID)
		} else if spec.Path == PlanDeliveryWorkItemStartPathTemplate || spec.Path == PlanDeliveryWorkItemCheckpointPathTemplate || spec.Path == PlanDeliveryWorkItemCompletePathTemplate {
			item := selectedHTTPPlanWorkItem(t, fixture.store, planRun.ID)
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", planRun.ID)
			requestPath = strings.ReplaceAll(requestPath, "{work_item_id}", item.ID)
		} else if spec.Path == RunExecutionControlPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", executionRun.ID)
		} else if spec.Path == RunWakeIntentPathTemplate ||
			spec.Path == RunWakeCancellationPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", wakeRun.ID)
		} else if spec.Path == RunWakeExecutionPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", wakeExecutionRun.ID)
		} else if spec.Path == RunScheduledJobsPathTemplate ||
			spec.Path == ScheduledJobActionPathTemplate || spec.Path == ScheduledJobObservationPathTemplate {
			requestPath = strings.ReplaceAll(requestPath, fixture.run.ID, scheduledCreated.ID)
		} else if spec.Path == FileEditProposalRecoveryPathTemplate {
			requestPath = strings.ReplaceAll(spec.Path, "{run_id}", fixture.run.ID)
			requestPath = strings.ReplaceAll(requestPath, "{edit_id}", recoveryProposal.Edit.ID)
		} else if spec.OperationID == "branchThread" || spec.OperationID == "getThreadChanges" || spec.OperationID == "undoThreadChanges" {
			requestPath = strings.ReplaceAll(spec.Path, "{thread_id}", domain.InitialThreadID(branchSourceRun.ID))
		} else if spec.OperationID == "getSavedThreadMessage" {
			requestPath = strings.ReplaceAll(spec.Path, "{thread_id}", domain.InitialThreadID(branchSourceRun.ID))
			requestPath = strings.ReplaceAll(requestPath, "{message_id}", branchSourceEventID)
		} else if spec.OperationID == "deleteThread" {
			requestPath = strings.ReplaceAll(spec.Path, "{thread_id}", deleteThread.ID)
		} else if spec.OperationID == "rejectContextMemoryCandidate" {
			requestPath = strings.ReplaceAll(spec.Path, "{candidate_id}", openAPIRejectCandidate.ID)
		}
		if spec.OperationID == "getThreadChanges" {
			requestPath += "?source_event_id=" + branchSourceEventID
		} else if spec.OperationID == "listContextMemoryCandidates" || spec.OperationID == "getContextMemoryCandidate" {
			requestPath += "?workspace_id=" + fixture.workspace.ID
		} else if spec.OperationID == "searchWorkspace" {
			requestPath += "?query=README"
		} else if spec.OperationID == "searchSavedThreadMessages" {
			requestPath += "?q=Selected"
		} else if spec.Path == TaskSkillCandidatesPath {
			requestPath += "?workspace_id=" + fixture.workspace.ID + "&surface=code&phase=deliver&profile=review"
		} else if spec.OperationID == "getWorkspaceRepositoryFileHistory" {
			requestPath += "?path=README.md"
		} else if spec.OperationID == "getWorkspaceRepositoryCommitFilePreview" {
			requestPath += "?path=README.md"
		} else if spec.OperationID == "compareWorkspaceRepositoryCommits" {
			requestPath += "?base_object_id=" + strings.Repeat("a", 40) +
				"&head_object_id=" + strings.Repeat("b", 40)
		} else if spec.OperationID == "issueFileEditProposalSource" {
			requestPath += "?path=README.md"
		} else if spec.OperationID == "exportCodeHandoff" {
			requestPath += "?format=markdown"
		} else if spec.OperationID == "exportRunVerificationPlanItemSnapshot" {
			requestPath += "?format=json"
		} else if spec.OperationID == "getDockerSandboxStatus" {
			requestPath += "?admission_id=docker-sandbox-admission-openapi"
		} else if spec.OperationID == "listRunFanoutExecutions" {
			requestPath += "?plan_id=plan-openapi-fanout"
		} else if spec.OperationID == "queryDebugTimeline" ||
			spec.OperationID == "exportDiagnosticBundle" {
			requestPath += "?run_id=" + fixture.run.ID
		} else if spec.OperationID == "getGitHubReviewProjection" {
			requestPath += "?connection_id=github-connection-openapi"
		} else if spec.OperationID == "readRunFullCDPPreviewImage" {
			requestPath += "?session_id=" + previewView.SessionID + "&sha256=" + previewView.Image.SHA256
		} else if spec.OperationID == "readRunAgentBrowserScreenshot" {
			requestPath += "?session_id=agent-browser-openapi&artifact_locator=openapi-browser.png&sha256=" + imageReceipt.SHA256
		} else if spec.OperationID == "inspectThreadCreationRequest" {
			requestPath += "?workspace_id=" + fixture.workspace.ID
		} else if spec.OperationID == "inspectThreadPlanRequest" {
			requestPath += "?run_id=" + openAPIThreadRun.ID + "&action=enter_plan"
		} else if spec.OperationID == "discoverThreadPullRequest" {
			requestPath += "?connection_id=github-connection-openapi&base_branch=main"
		} else if spec.OperationID == "observeThreadPullRequest" {
			requestPath += "?operation_key=openapi-thread-pr-operation-0001"
		} else if spec.OperationID == "getMCPCredentialStatus" {
			requestPath = mcpCredentialStatusPath(credentialBinding)
		}
		t.Run(spec.OperationID, func(t *testing.T) {
			prFollowupCallsBefore := prFollowups.calls
			projectHookCallsBefore := projectHooks.calls
			requestAPI := fixture.api
			var threadServiceJob threadApplicationServicesLiveJob
			if spec.Path == StandardCodePresetCreatePath ||
				spec.Path == StandardCodePresetRunPathTemplate ||
				spec.Path == StandardCodePauseAndConfigurePathTemplate {
				requestAPI = standardCodeAPI
			} else if spec.Path == ExtensionMCPRegistrationPath || spec.Path == ExtensionPluginImportPath {
				requestAPI = onboardingFixture.api
			} else if spec.Path == MCPCredentialPathTemplate {
				requestAPI = credentialFixture.api
				if spec.Control {
					requestPath = "/api/v1/extensions/mcp/" + credentialBinding.ServerID + "/credential"
				}
			} else if spec.Path == ThreadApplicationServicesPathTemplate ||
				spec.Path == ThreadApplicationServicePathTemplate || spec.Path == ThreadApplicationServiceStopPathTemplate {
				// Bind these routes to an authorized, reaped Command Runtime Job,
				// rather than the scheduler's generic job identity. Keep the native
				// fixture inside this route subtest: an unavailable native runtime
				// may skip this route, but cannot skip the rest of the HTTP catalog.
				threadServicesFixture := newThreadApplicationServicesLiveFixture(t)
				threadServiceJob = threadServicesFixture.start(t, "fail")
				threadServicesFixture.waitDetail(t, threadServiceJob, func(detail threadApplicationServicesLiveDetail) bool {
					return detail.Service.State == string(runner.CommandRuntimeJobFailed)
				})
				requestPath = strings.ReplaceAll(spec.Path, "{thread_id}", threadServiceJob.threadID)
				requestPath = strings.ReplaceAll(requestPath, "{job_id}", threadServiceJob.jobID)
				requestAPI = threadServicesFixture.api
			}
			var response *httptest.ResponseRecorder
			expectedStatus := http.StatusOK
			if spec.OperationID == "getRunFindingReport" {
				expectedStatus = http.StatusNotFound
			} else if spec.OperationID == "cancelRunFanoutExecution" ||
				spec.OperationID == "getRunSpecialistToolReceipts" ||
				spec.OperationID == "reviewRunChildTaskProposal" ||
				spec.OperationID == "admitRunChildTaskProposal" ||
				strings.Contains(spec.OperationID, "RunBatchDelivery") ||
				strings.Contains(spec.OperationID, "BatchWorkbench") ||
				spec.OperationID == "prepareRunBatchDelivery" {
				expectedStatus = http.StatusNotFound
			} else if spec.OperationID == "getWorkspaceRepositoryCommitFilePreview" {
				expectedStatus = http.StatusPreconditionFailed
			}
			if spec.OperationID == "discoverProviderModels" {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"data":[{"id":"catalog-model"}]}`))
				}))
				defer upstream.Close()
				body := `{"version":"provider_model_discovery.v1","provider_id":"model-discovery-draft","endpoint_url":"` + upstream.URL + `/v1/responses","transport":"openai_responses","advanced_config":{},"confirm_discovery":true}`
				response = performControlMethodPathRequest(t, requestAPI, http.MethodPost, requestPath,
					"", strings.NewReader(body))
			} else if spec.OperationID == "discoverGitAdvancedHunks" {
				body := `{"spec":{"protocol_version":"git-advanced.v1",` +
					`"operation":"hunk_stage","paths":["README.md"]}}`
				request := httptest.NewRequest(http.MethodPost,
					"http://127.0.0.1"+requestPath, strings.NewReader(body))
				request.Host = "127.0.0.1:8765"
				request.RemoteAddr = "127.0.0.1:45000"
				request.Header.Set("Authorization", "Bearer "+testAccessToken)
				request.Header.Set("Content-Type", "application/json")
				response = httptest.NewRecorder()
				fixture.api.ServeHTTP(response, request)
			} else if spec.Path == DockerSandboxReadinessPath {
				body, marshalErr := json.Marshal(DockerSandboxReadinessRequestView{
					PlanID: "sandbox-docker-plan-openapi", Manifest: dockerSandboxHTTPTestManifest(),
				})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				request := httptest.NewRequest(http.MethodPost,
					"http://127.0.0.1"+requestPath, bytes.NewReader(body))
				request.Host = "127.0.0.1:8765"
				request.RemoteAddr = "127.0.0.1:45000"
				request.Header.Set("Authorization", "Bearer "+testAccessToken)
				request.Header.Set("Content-Type", "application/json")
				response = httptest.NewRecorder()
				fixture.api.ServeHTTP(response, request)
			} else if spec.OperationID == "inspectThreadCreationRequest" || spec.OperationID == "inspectThreadTurnRequest" || spec.OperationID == "inspectThreadPlanRequest" || spec.OperationID == "inspectWorkspaceFileUpload" {
				request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+requestPath, nil)
				request.Host, request.RemoteAddr = "127.0.0.1:8765", "127.0.0.1:45000"
				request.Header.Set("Authorization", "Bearer "+testAccessToken)
				request.Header.Set("Idempotency-Key", "openapi-observe-original-missing-key")
				response = httptest.NewRecorder()
				fixture.api.ServeHTTP(response, request)
			} else if spec.Path == TaskConfigurationPreviewPath {
				response = previewConfigurationRequest(fixture.api, testAccessToken, `{"workspace_id":"`+fixture.workspace.ID+`"}`)
			} else if spec.Control {
				body := `{"profile":"docker"}`
				if spec.OperationID == "saveWorkflowDefinition" {
					encoded, err := json.Marshal(workflowDefinition.Spec)
					if err != nil {
						t.Fatal(err)
					}
					body = string(encoded)
				} else if spec.OperationID == "undoThreadChanges" {
					body = `{"version":"thread_change_undo.v1","source_event_id":"` + branchSourceEventID + `","confirm":false}`
				} else if spec.OperationID == "startRunWorkflow" {
					body = `{"version":"workflow_start.v1","definition_id":"` + workflowDefinition.ID + `","inputs":{},"confirm_start":true}`
				} else if strings.HasPrefix(spec.Path, "/api/v1/runs/{run_id}/workflows/{execution_id}/") {
					// Each control receives its own state so route ordering cannot
					// change the revision or accidentally dispatch a model turn.
					execution, err := workflowController.Start(t.Context(), application.StartWorkflowRequest{
						RunID: workflowRun.ID, DefinitionID: workflowDefinition.ID,
						OperationKey: "openapi-workflow-target-" + spec.OperationID,
					})
					if err != nil {
						t.Fatal(err)
					}
					if spec.OperationID == "advanceRunWorkflow" || spec.OperationID == "resumeRunWorkflow" {
						execution, err = workflowController.Pause(t.Context(), execution.ID, execution.Version)
						if err != nil {
							t.Fatal(err)
						}
					}
					if spec.OperationID == "retryFailedRunWorkflow" {
						execution = prepareWorkflowRetryTarget(t, fixture, execution)
					}
					requestPath = strings.ReplaceAll(requestPath, workflowExecution.ID, execution.ID)
					body = fmt.Sprintf(`{"version":"workflow_control.v1","revision":%d,"confirm_start":true}`, execution.Version)
				} else if spec.OperationID == "selectProjectSkills" || spec.OperationID == "selectInstalledSkills" {
					body = `{"refs":["` + projectSkillRef + `"],"confirm_untrusted_context":false}`
				} else if spec.OperationID == "executeRunSpecialist" {
					body = `{"version":"specialist_tools.v1","operation_key":"openapi-child-execute-0001","workspace_tools":true,"confirm_execute":true}`
				} else if spec.OperationID == "revokeSpecialistReadTools" {
					body = `{"version":"specialist_tools.v1"}`
				} else if spec.OperationID == "continueRunSpecialist" {
					body = `{"version":"specialist_continuation.v1","operation_key":"openapi-child-continue-0001","confirm_execute":true}`
				} else if spec.Path == SandboxEnvironmentPath {
					body = sandboxEnvironmentControlTestBody()
				} else if spec.OperationID == "prepareBatchWorkbench" {
					body = `{"version":"batch-delivery-workbench.v1","proposal_id":"proposal-openapi-missing-0001","tasks":[],"confirm":true}`
				} else if spec.OperationID == "controlThreadPlan" {
					body = `{"version":"plan_delivery_control.v1","run_id":"` + openAPIThreadRun.ID + `","action":"enter_plan"}`
				} else if spec.OperationID == "stopThreadApplicationService" {
					body = `{"version":"thread_application_services.v1","expected_run_id":"` + threadServiceJob.runID + `"}`
				} else if spec.OperationID == "previewThreadGit" {
					body = `{"version":"thread_git.v1","run_id":"` + openAPIThreadRun.ID + `","spec":{"operation":"stage","paths":["README.md"]}}`
				} else if spec.OperationID == "executeThreadGit" {
					body = `{"version":"thread_git.v1","run_id":"` + openAPIThreadRun.ID + `","spec":{"operation":"stage","paths":["README.md"]},` +
						`"operation_key":"openapi-thread-git-operation-0001","expected_preview_fingerprint":"` + strings.Repeat("b", 64) + `","requested_by":"openapi_test"}`
				} else if spec.OperationID == "previewThreadPullRequest" {
					body = `{"version":"thread_pull_request.v1","connection_id":"github-connection-openapi","base_branch":"main",` +
						`"expected_run_id":"` + openAPIThreadRun.ID + `","expected_head_sha":"` + threadGitBinding.HeadSHA + `",` +
						`"operation_key":"openapi-thread-pr-operation-0001","title":"OpenAPI draft","body":"Reviewed fixture changes."}`
				} else if spec.OperationID == "createThreadPullRequest" {
					body = `{"version":"thread_pull_request.v1","operation_id":"git-remote-openapi-pr","approval_id":"approval-openapi-pr"}`
				} else if spec.OperationID == "refreshThreadPullRequest" {
					body = `{"version":"thread_pull_request.v1","connection_id":"github-connection-openapi","pull_request":7}`
				} else if spec.OperationID == "setThreadPullRequestCredential" {
					body = `{"version":"thread_pull_request.v1","connection_id":"github-connection-openapi","token":"openapi-fixture-token-not-a-real-credential"}`
				} else if spec.OperationID == "createPRFollowup" {
					body = prFollowupCreateBody
				} else if spec.OperationID == "pausePRFollowup" || spec.OperationID == "resumePRFollowup" || spec.OperationID == "cancelPRFollowup" {
					body = prFollowupTransitionBody
				} else if spec.OperationID == "trustProjectHooks" || spec.OperationID == "disableProjectHooks" {
					body = projectHooksHTTPBody
				} else if spec.OperationID == "uploadWorkspaceFile" {
					body = `{"version":"workspace_file_upload.v1","mime_type":"text/plain","name":"notes.txt","data_base64":"` + base64.StdEncoding.EncodeToString(fileBytes) + `"}`
				} else if spec.OperationID == "uploadWorkspaceImage" {
					body = `{"version":"workspace_image_upload.v1","mime_type":"image/png","name":"image.png","data_base64":"` + base64.StdEncoding.EncodeToString(imageBytes) + `"}`
				} else if spec.OperationID == "captureRunFullCDPPreview" {
					body = `{"version":"full_cdp_preview.v1","expected_session_id":"` + previewView.SessionID + `"}`
				} else if spec.OperationID == "closeRunAgentBrowser" {
					body = `{"version":"agent_browser_close.v1","session_id":"agent-browser-openapi"}`
				} else if spec.OperationID == "actRunFullCDPPreview" {
					body = `{"version":"full_cdp_preview_action.v1","expected_session_id":"` + previewView.SessionID + `","expected_snapshot_id":"` + previewView.Page.SnapshotID + `","action":"click","selector":"#press"}`
				} else if spec.OperationID == "confirmContextMemoryCandidate" {
					encoded, marshalErr := json.Marshal(memoryCandidateConfirmRequestView{
						WorkspaceID: openAPIConfirmCandidate.WorkspaceID, ExpectedVersion: openAPIConfirmCandidate.Version,
						ExpectedFingerprint: openAPIConfirmCandidate.Fingerprint, ExpectedSourceVersion: openAPIConfirmCandidate.Source.NoteVersion, Confirm: true,
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					body = string(encoded)
				} else if spec.OperationID == "rejectContextMemoryCandidate" {
					encoded, marshalErr := json.Marshal(memoryCandidateRejectRequestView{
						WorkspaceID: openAPIRejectCandidate.WorkspaceID, ExpectedVersion: openAPIRejectCandidate.Version,
						ExpectedFingerprint: openAPIRejectCandidate.Fingerprint, ExpectedSourceVersion: openAPIRejectCandidate.Source.NoteVersion,
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					body = string(encoded)
				} else if spec.OperationID == "createContextMemory" {
					body = `{"scope":"user","scope_id":"local-user",` +
						`"title":"OpenAPI created memory","content":"Explicit test memory."}`
				} else if spec.OperationID == "openRunFullCDPSession" {
					body = `{"version":"full_cdp_session.v1",` +
						`"target":"http://127.0.0.1:18080/path",` +
						`"browser":{"product":"chrome","channel":"stable"},` +
						`"expected_execution_permission_revision":1,` +
						`"expected_browser_cdp_permission_revision":1,` +
						`"confirm_full_cdp":true,"reason":"OpenAPI live route coverage"}`
				} else if spec.OperationID == "closeRunFullCDPSession" {
					body = `{"version":"full_cdp_session_close.v1",` +
						`"expected_session_id":"full_cdp_session-openapi-live-0001",` +
						`"reason":"OpenAPI live route coverage complete"}`
				} else if spec.OperationID == "updateContextMemory" {
					body = `{"expected_version":1,"status":"disabled"}`
				} else if spec.OperationID == "deleteContextMemory" {
					body = `{"expected_version":2}`
				} else if spec.OperationID == "refreshRunProjectInstructions" {
					state, inspectErr := application.NewProjectInstructionService(fixture.store).
						Inspect(t.Context(), fixture.run.ID, "")
					if inspectErr != nil {
						t.Fatal(inspectErr)
					}
					encoded, marshalErr := json.Marshal(projectInstructionRefreshRequestView{
						ExpectedFingerprint:     state.Pinned.Snapshot.Fingerprint,
						ExpectedLiveFingerprint: state.Live.Fingerprint, Confirm: true,
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					body = string(encoded)
				} else if spec.OperationID == "reviewGitAdvancedOperation" {
					body = `{"operation_key":"openapi-git-advanced-review",` +
						`"scope":{"capability_generation":"` + strings.Repeat("a", 64) + `",` +
						`"lease_generation":1},"spec":{"protocol_version":"git-advanced.v1",` +
						`"operation":"stash_create","message":"OpenAPI preview"}}`
				} else if spec.OperationID == "executeGitAdvancedOperation" {
					body = `{"operation_id":"git-advanced-operation-openapi",` +
						`"approval_id":"approval-openapi","scope":{` +
						`"capability_generation":"` + strings.Repeat("a", 64) + `",` +
						`"lease_generation":1}}`
				} else if spec.OperationID == "configureGitHubReviewConnection" {
					body = `{"repository":{"host":"github.com","owner":"openai",` +
						`"name":"prayu","full_name":"openai/prayu","private":false},` +
						`"credential":{"name":"github-review-openapi",` +
						`"kind":"fine_grained_pat"},"allowed_log_hosts":[],` +
						`"write_enabled":false,"enabled":true,"expected_generation":0}`
				} else if spec.OperationID == "beginGitHubReviewDeviceFlow" ||
					spec.OperationID == "disconnectGitHubReviewCredential" {
					body = `{}`
				} else if spec.OperationID == "pollGitHubReviewDeviceFlow" {
					body = `{"session_id":"github-device-openapi"}`
				} else if spec.OperationID == "qualifyGitHubReviewConnection" ||
					spec.OperationID == "fetchGitHubReviewSnapshot" {
					body = `{"pull_request":118}`
				} else if spec.OperationID == "buildGitHubReviewEvidence" {
					body = `{"snapshot_id":"github-snapshot-openapi"}`
				} else if spec.OperationID == "reviewGitHubReviewWrite" {
					body = `{"connection_id":"github-connection-openapi",` +
						`"snapshot_id":"github-snapshot-openapi",` +
						`"operation_key":"github-write-openapi",` +
						`"spec":{"protocol_version":"github-review-write.v1",` +
						`"operation":"reply","identity":{"repository":{"host":"github.com",` +
						`"owner":"openai","name":"prayu","full_name":"openai/prayu",` +
						`"private":false},"number":118,"node_id":"PR_openapi",` +
						`"state":"open","draft":false,"merged":false,"fork":false,` +
						`"base_ref":"main","base_sha":"` + strings.Repeat("a", 40) + `",` +
						`"head_ref":"feature","head_sha":"` + strings.Repeat("b", 40) + `",` +
						`"merge_base_sha":"` + strings.Repeat("a", 40) + `",` +
						`"updated_at":"2026-08-20T00:00:00Z"},` +
						`"credential":{"name":"github-review-openapi",` +
						`"kind":"fine_grained_pat"},"capability_generation":"` +
						strings.Repeat("c", 64) + `","body":"review reply"}}`
				} else if spec.OperationID == "executeGitHubReviewWrite" {
					body = `{"operation_id":"github-write-openapi",` +
						`"approval_id":"github-approval-openapi"}`
				} else if spec.OperationID == "createContinuityCheckpoint" {
					body = `{"title":"OpenAPI live checkpoint"}`
				} else if spec.OperationID == "createWorkspaceCheckpoint" {
					body = `{"operation_key":"openapi-workspace-checkpoint-create-0001",` +
						`"title":"OpenAPI Workspace checkpoint"}`
				} else if spec.OperationID == "recordStandardCodeDelivery" {
					body = `{"operation_key":"openapi-standard-code-delivery-0001",` +
						`"declaration":"no_applicable_tests",` +
						`"verification_job_ids":[],"uncovered_items":[]}`
				} else if spec.OperationID == "startRunUIEvidence" {
					body = `{}`
				} else if spec.OperationID == "cancelUIEvidence" {
					body = `{"confirm":true}`
				} else if spec.OperationID == "previewWorkspaceRewind" {
					body = `{"target_checkpoint_id":"checkpoint-target",` +
						`"expected_current_checkpoint_id":"checkpoint-current"}`
				} else if spec.OperationID == "rewindWorkspaceCheckpoint" {
					body = `{"target_checkpoint_id":"checkpoint-target",` +
						`"expected_current_checkpoint_id":"checkpoint-current",` +
						`"operation_key":"openapi-workspace-rewind-0001","confirm":true}`
				} else if spec.OperationID == "undoWorkspaceMutation" ||
					spec.OperationID == "redoWorkspaceMutation" {
					body = `{"expected_current_checkpoint_id":"checkpoint-current",` +
						`"operation_key":"openapi-workspace-cursor-` + spec.OperationID + `",` +
						`"confirm":true}`
				} else if spec.OperationID == "forkWorkspaceCheckpoint" {
					body = `{"target_checkpoint_id":"checkpoint-target",` +
						`"expected_current_checkpoint_id":"checkpoint-current",` +
						`"operation_key":"openapi-workspace-fork-0001",` +
						`"workspace_name":"openapi-fork",` +
						`"branch":"codex/openapi-workspace-fork","confirm":true}`
				} else if spec.OperationID == "forkContinuityNode" ||
					spec.OperationID == "resumeContinuityNode" {
					body = `{"goal":"OpenAPI continuity branch"}`
				} else if spec.Path == DockerSandboxAdmissionPath {
					encoded, marshalErr := json.Marshal(DockerSandboxAdmissionRequestView{
						PlanID: "sandbox-docker-plan-openapi", Manifest: dockerSandboxHTTPTestManifest(),
						RequestedBy: "openapi_test",
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					body = string(encoded)
				} else if spec.Path == DockerSandboxStartPath ||
					spec.Path == DockerSandboxCancelPath {
					body = `{"admission_id":"docker-sandbox-admission-openapi",` +
						`"requested_by":"openapi_test"}`
				} else if spec.OperationID == "compactThreadContext" {
					body = `{"version":"thread_context_compaction.v1","expected_run_id":"` + openAPIThreadRun.ID + `"}`
				} else if spec.OperationID == "recoverThreadContextCompaction" {
					body = `{"version":"thread_context_compaction_recovery.v1","expected_run_id":"` + contextRecoveryRun.ID + `","action":"discard"}`
				} else if spec.OperationID == "branchThread" {
					body = `{"version":"thread_branch.v1","source_event_id":"` + branchSourceEventID + `"}`
				} else if spec.OperationID == "createThread" {
					body = `{"version":"thread_creation.v1","goal":"OpenAPI live Thread",` +
						`"workspace_id":"` + fixture.workspace.ID + `"}`
				} else if spec.OperationID == "submitThreadMessage" ||
					spec.OperationID == "executeThreadTurn" {
					body = `{"version":"thread_message_submission.v1",` +
						`"content":"OpenAPI live Thread message"}`
				} else if spec.OperationID == "controlThreadContextPin" {
					body = `{"version":"thread_context_pins.v1","action":"add","expected_revision":0,"text":"Preserve the public interface."}`
				} else if spec.OperationID == "resolveThreadContextPinProposal" {
					body = `{"version":"thread_context_pins.v1","expected_revision":0,"expected_proposal_version":1,"choice":"discussion_only"}`
				} else if spec.OperationID == "interruptThreadExecution" {
					body = `{"version":"thread_execution.v1","execution_id":"thread-execution-openapi"}`
				} else if spec.OperationID == "archiveThread" ||
					spec.OperationID == "restoreThread" || spec.OperationID == "deleteThread" {
					threadID := openAPIThread.ID
					if spec.OperationID == "deleteThread" {
						threadID = deleteThread.ID
					}
					current, loadErr := fixture.store.GetThread(t.Context(), threadID)
					if loadErr != nil {
						t.Fatal(loadErr)
					}
					body = `{"version":"thread_lifecycle.v1","expected_version":` +
						fmt.Sprint(current.Version) + `}`
				} else if spec.Path == ProjectActionRunPathTemplate {
					body = `{"version":"project-actions.v1","target":"","confirm_execution":true}`
				} else if spec.Path == ProjectPreparationPathTemplate {
					body = `{"version":"project-preparation.v1","target":".",` +
						`"expected_fingerprint":"` + strings.Repeat("a", 64) + `",` +
						`"allowed_sources":["https://registry.npmjs.org"],` +
						`"confirm_execution":true,"run_install_scripts":false}`
				} else if spec.Path == ProjectPreparationCancelPathTemplate {
					body = `{"version":"project-preparation.v1"}`
				} else if spec.Path == StandardCodePresetCreatePath {
					body = `{"version":"standard_code_preset.v1",` +
						`"workspace_id":"` + fixture.workspace.ID + `",` +
						`"goal":"OpenAPI Standard Code Run",` +
						`"backend_intent":"auto","confirm_workspace_trust":false}`
				} else if spec.Path == StandardCodePresetRunPathTemplate ||
					spec.Path == StandardCodePauseAndConfigurePathTemplate {
					body = `{"version":"standard_code_preset.v1",` +
						`"backend_intent":"auto","confirm_workspace_trust":false}`
				} else if spec.Path == WorkspaceImportPath {
					encoded, err := json.Marshal(WorkspaceImportRequestView{
						Version:       WorkspaceImportProtocolVersion,
						DirectoryPath: t.TempDir(), Confirmed: true})
					if err != nil {
						t.Fatal(err)
					}
					body = string(encoded)
				} else if spec.Path == RunCreationControlPath {
					body = `{"version":"run_creation.v1","goal":"OpenAPI live Run",` +
						`"workspace_id":"` + fixture.workspace.ID + `"}`
				} else if spec.Path == SessionMessageControlPathTemplate {
					body = `{"version":"session_message_submission.v1",` +
						`"content":"OpenAPI live Session message"}`
				} else if spec.Path == SessionArchiveControlPathTemplate {
					body = `{"version":"session_archive.v1","confirm":true}`
				} else if spec.Path == SessionSteeringCancellationPathTemplate {
					body = `{"version":"session_steering_cancellation.v1",` +
						`"reason":"OpenAPI live cancellation"}`
				} else if spec.Path == SessionSteeringRevisionPathTemplate {
					body = `{"version":"session_steering_revision.v1","expected_revision":0,"content":"OpenAPI updated body"}`
				} else if spec.Path == SessionSteeringPromotionPathTemplate {
					body = `{"version":"session_steering_promotion.v1","expected_revision":0,"expected_content_sha256":"` + strings.Repeat("a", 64) + `","expected_attempt_id":"attempt-openapi-promotion","expected_execution_id":"execution-openapi-promotion"}`
				} else if spec.Path == RunLifecycleControlPathTemplate {
					body = `{"version":"run_lifecycle_control.v1","action":"start"}`
				} else if spec.Path == ThreadRunRecoveryControlPathTemplate {
					body = `{"version":"thread_run_recovery.v1","run_id":"` +
						recoveryRun.ID + `","handoff_operation_id":"` +
						recoveryOperation.ID + `"}`
				} else if spec.Path == PlanDirectionControlPathTemplate {
					body = `{"version":"plan_delivery_control.v1","proposal_id":"` +
						planProposal.ID + `","direction":1}`
				} else if spec.Path == PlanDeliveryControlPathTemplate {
					body = `{"version":"plan_delivery_control.v1"}`
				} else if spec.Path == PlanDeliveryWorkItemStartPathTemplate || spec.Path == PlanDeliveryWorkItemCompletePathTemplate {
					item := selectedHTTPPlanWorkItem(t, fixture.store, planRun.ID)
					body = fmt.Sprintf(`{"version":"plan_delivery_control.v1","expected_work_item_version":%d}`, item.Version)
				} else if spec.Path == PlanDeliveryWorkItemCheckpointPathTemplate {
					item := selectedHTTPPlanWorkItem(t, fixture.store, planRun.ID)
					body = fmt.Sprintf(`{"version":"plan_delivery_control.v1","expected_work_item_version":%d,"focused_verification":"operator observation","diff_audit":"operator diff review","security_audit":"operator boundary review","handoff_summary":"manual assertion only","functional_verification":"operator functional review","robustness_audit":"operator failure review"}`, item.Version)
				} else if spec.Path == ApprovalDecisionControlPathTemplate {
					body = `{"version":"approval_control.v1","action":"approve_once"}`
				} else if spec.Path == HostCommandProposalResumePathTemplate {
					body = ""
				} else if spec.Path == RunExecutionControlPathTemplate {
					body = `{"version":"run_execution_handoff.v1","max_steps":1}`
				} else if spec.Path == ModelRouteControlPathTemplate {
					body = `{"version":"model_route_control.v1","provider":"mock","model":"mock-code"}`
				} else if spec.Path == ThreadModelRoutePathTemplate {
					body = `{"version":"thread_model_route_control.v1","action":"reset",` +
						`"operation_key":"openapi-thread-model-route-reset-0001",` +
						`"requested_by":"openapi_test"}`
				} else if spec.Path == ProviderDiagnosticPath {
					body = `{"version":"provider_diagnostic.v1","provider":"mock","model":"mock-code","confirm_diagnostic":true}`
				} else if spec.Path == ModelHarnessQualificationPath {
					body = `{"version":"model_harness_qualification.v1","provider":"mock","model":"mock-code","confirm_qualification":true}`
				} else if spec.Path == FanoutExecutionCancelPathTemplate {
					body = `{"version":"readonly_fanout_cancel.v1","confirm_cancel":true}`
				} else if spec.Path == ChildTaskProposalReviewPathTemplate {
					body = `{"version":"child_task_review.v1","action":"deny","reviewer":"openapi_test","fanout_tier":"2","confirm_review":true}`
				} else if spec.Path == ChildTaskProposalAdmitPathTemplate {
					body = `{"version":"child_task_admit.v1","confirm_admit":true}`
				} else if spec.Path == BatchDeliveriesPathTemplate {
					body = `{"version":"batch_delivery_prepare.v1",` +
						`"proposal_id":"child-task-proposal-openapi-missing",` +
						`"spec":{"version":"batch-delivery.v1","tasks":[{` +
						`"ordinal":1,"ownership_hints":[{"path":"internal/openapi",` +
						`"kind":"directory"}],"dependency_ordinals":[],` +
						`"budget":{"turn_limit":1,"token_limit":1,"timeout_millis":1000},` +
						`"validations":[{"id":"diff","kind":"git_diff_check",` +
						`"scope":"."}],"expected_artifacts":[]}],` +
						`"contract":{"require_clean":true,` +
						`"require_independent_review":true,"require_all_validations":true,` +
						`"max_changed_files":8,"max_diff_bytes":65536}},"confirm":true}`
				} else if spec.Path == BatchDeliveryReviewPathTemplate {
					body = `{"version":"batch_delivery_review_control.v1",` +
						`"generation":1,"reviewer":"openapi-reviewer",` +
						`"verdict":"accepted","summary":"reviewed",` +
						`"full_diff_reviewed":true,"call_chain_reviewed":true,` +
						`"tests_reviewed":true}`
				} else if spec.Path == BatchDeliveryRenewPathTemplate {
					body = `{"version":"batch_delivery_renew_owner.v1",` +
						`"expected_generation":1,"retry":false,"confirm":true}`
				} else if spec.Path == BatchDeliveryMergePathTemplate {
					body = `{"version":"batch_delivery_merge.v1",` +
						`"ordered_ordinals":[1],"confirm_replay":false,"confirm":true}`
				} else if spec.Path == BatchDeliveryCancelPathTemplate {
					body = `{"version":"batch_delivery_cancel.v1",` +
						`"reason":"OpenAPI route probe","confirm":true}`
				} else if spec.Path == BatchDeliveryRecoverPathTemplate {
					body = `{"version":"batch_delivery_reconcile.v1","confirm":true}`
				} else if spec.Path == PriceSnapshotsPath {
					encoded, marshalErr := json.Marshal(PriceSnapshotImportRequestView{
						Version:  pricing.ProtocolVersion,
						Document: openAPIPriceSnapshotDocument(t),
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					body = string(encoded)
				} else if spec.Path == EmbeddedAnalyzerExecutionPathTemplate {
					body = `{"version":"` + application.EmbeddedAnalyzerExecutionProtocolVersion +
						`","text":"OpenAPI embedded analyzer fixture\\n","media_type":"text/plain",` +
						`"confirmation":"` + application.EmbeddedAnalyzerExecutionConfirmation + `"}`
				} else if spec.Path == ProviderCredentialPathTemplate {
					body = `{"version":"provider_credential.v1","action":"set",` +
						`"secret":"temporary-openapi-key","confirm":true}`
				} else if spec.Path == ProviderDefinitionPathTemplate {
					body = `{"version":"provider_definition_control.v1",` +
						`"expected_collection_revision":0,"definition":{` +
						`"version":"provider_definition.v1","id":"openapi-custom",` +
						`"display_name":"OpenAPI custom Provider","note":"",` +
						`"website_url":"","endpoint_url":"https://api.example.org/v1/chat/completions",` +
						`"default_model":"openapi-model","models":["openapi-model"],` +
						`"transport":"openai_chat_completions","search_mode":"disabled",` +
						`"native_web_search_capability":"unsupported",` +
						`"advanced_config":{},"enabled":true,"revision":0},"confirm":true}`
				} else if spec.Path == ProviderDefinitionDeletePathTemplate {
					body = `{"version":"provider_definition_control.v1",` +
						`"expected_collection_revision":1,"expected_definition_revision":1,` +
						`"confirm":true}`
				} else if spec.Path == ExtensionMCPRegistrationPath {
					body = extensionJSON(t, extensionRegistrationRequest(onboardingFixture))
				} else if spec.Path == MCPCredentialPathTemplate {
					body = extensionJSON(t, MCPCredentialRequestView{Version: application.MCPCredentialProtocolVersion,
						Binding: credentialBinding, Action: "set", Secret: "synthetic-openapi-token", Confirm: true,
						ExpectedReferenceFingerprint: credentialStatus.ReferenceFingerprint})
				} else if spec.Path == ExtensionPluginImportPath {
					body = extensionJSON(t, extensionPluginImportRequest(extensionPluginArchive(t)))
				} else if spec.Path == CodeIntelConfigurationsPath {
					body = `{"version":"code-intel-configuration.v1","server_id":"onboarding-lsp",` +
						`"name":"Catalog LSP fixture","workspace_id":"` + fixture.workspace.ID +
						`","languages":[{"id":"go","extensions":[".go"]}],` +
						`"executable":"/fixture/lsp","arguments":[],"executable_sha256":"` +
						strings.Repeat("a", 64) + `","request_timeout_ms":15000}`
				} else if spec.Path == CodeIntelConfigurationReviewPath {
					body = `{"version":"code-intel-configuration.v1","workspace_id":"` +
						fixture.workspace.ID + `","expected_descriptor_fingerprint":"` + strings.Repeat("a", 64) + `"}`
				} else if spec.Path == CodeIntelConfigurationTestPath {
					body = `{"version":"code-intel-configuration.v1","workspace_id":"` +
						fixture.workspace.ID + `","expected_descriptor_fingerprint":"` + strings.Repeat("a", 64) +
						`","tool":"code_document_symbols","path":"main.go"}`
				} else if spec.Path == ExtensionMCPReviewPath {
					body = `{"version":"extension-control.v1","action":"disable",` +
						`"expected_descriptor_fingerprint":"` + strings.Repeat("a", 64) + `"}`
				} else if spec.Path == ExtensionMCPRefreshPath {
					body = `{"version":"extension-control.v1"}`
				} else if spec.Path == ExtensionPluginRollbackPath {
					body = `{"version":"plugin-lifecycle.v1","target_installation_id":"plugin-rollback-target","expected_current_fingerprint":"` + strings.Repeat("b", 64) + `","expected_current_generation":2,"expected_target_fingerprint":"` + strings.Repeat("b", 64) + `","expected_target_generation":2,"capabilities":["hooks"],"confirm_untrusted":true}`
				} else if spec.Path == ExtensionPluginPublisherRevocationPath {
					body = extensionJSON(t, PluginPublisherRevocationRequestView{Version: PluginLifecycleProtocol, ExpectedPublisherFingerprint: strings.Repeat("a", 64), ExpectedPublisherGeneration: 1, Confirm: true})
				} else if spec.Path == ExtensionPluginPublisherTrustPath {
					// The revocation probe above advances this fixture publisher
					// to generation 2, so trust must bind the observed value.
					body = extensionJSON(t, PluginPublisherTrustRequestView{Version: PluginLifecycleProtocol, ExpectedPublisherFingerprint: strings.Repeat("a", 64), ExpectedPublisherGeneration: 2, Confirm: true})
				} else if spec.Path == ExtensionPluginReviewPath {
					body = `{"version":"extension-control.v1","action":"disable",` +
						`"expected_package_fingerprint":"` + strings.Repeat("b", 64) + `",` +
						`"expected_generation":1,"confirm_untrusted":false}`
				} else if spec.Path == FileEditProposalPathTemplate {
					body = `{"version":"file_edit_proposal.v1","source_handle":"` +
						proposalSource.Handle + `","proposed_text":"OpenAPI proposal\\n"}`
				} else if spec.Path == FileEditReviewPathTemplate {
					body = `{"version":"file_edit_review.v1","action":"approve_intent"}`
				} else if spec.Path == FileEditRevertProposalPathTemplate {
					body = `{"version":"file_edit_proposal.v1"}`
				} else if spec.Path == FileEditApplyPathTemplate {
					body = `{"version":"file_edit_apply.v1"}`
				} else if spec.Path == RunWakeIntentPathTemplate {
					body = `{"version":"run_wake_control.v1","max_attempts":3,` +
						`"initial_delay_seconds":0,"base_backoff_seconds":5,` +
						`"max_backoff_seconds":60,"max_elapsed_seconds":300}`
				} else if spec.Path == RunWakeCancellationPathTemplate {
					body = `{"version":"run_wake_control.v1"}`
				} else if spec.Path == RunWakeExecutionPathTemplate {
					body = `{"version":"run_wake_consumer.v1","max_steps":1}`
				} else if spec.Path == RunScheduledJobsPathTemplate {
					body = `{"version":"scheduled-job.v1","schedule":{"kind":"once",` +
						`"timezone":"UTC","anchor_at":"` +
						scheduledAnchor.Add(time.Minute).Format(time.RFC3339) + `",` +
						`"misfire_policy":"run_once"},"deadline_at":"` +
						scheduledAnchor.Add(2*time.Hour).Format(time.RFC3339) + `",` +
						`"stop_on_target_terminal":true,"max_rounds":1,"max_model_calls":0,` +
						`"max_elapsed_seconds":3600,"retry":{"max_attempts":2,` +
						`"initial_backoff_seconds":1,"max_backoff_seconds":10},` +
						`"notification":"all","execution_mode":"read_only",` +
						`"confirm_repair":false}`
				} else if spec.Path == ScheduledJobActionPathTemplate {
					body = `{"version":"scheduled-job-control.v1","expected_revision":` +
						fmt.Sprint(scheduledSeed.Job.Revision) + `}`
				} else if spec.Path == ScheduledJobObservationPathTemplate {
					current, getErr := scheduledJobs.Get(t.Context(), scheduledSeed.Job.ID, 1, 1)
					if getErr != nil {
						t.Fatal(getErr)
					}
					body = `{"version":"scheduled-job-control.v1","observation_consent_version":1,"expected_revision":` +
						fmt.Sprint(current.Job.Revision) + `}`
				} else if spec.Path == SkillCatalogPublishersPath {
					publicKey := []byte(strings.Repeat("a", 32))
					fingerprint := sha256.Sum256(publicKey)
					body = `{"version":"skill-catalog-control.v1","action":"trust","expected_updated_at":"","confirm":true,"name":"OpenAPI publisher","public_key":"` +
						base64.StdEncoding.EncodeToString(publicKey) + `","fingerprint":"` + hex.EncodeToString(fingerprint[:]) + `"}`
				} else if spec.Path == SkillPackageInstallPath {
					body = `{"version":"skill_package_installation.v1","archive_base64":"` +
						base64.StdEncoding.EncodeToString(skillArchive) +
						`","surface":"code","confirm_untrusted":true}`
				} else if spec.Path == SkillPackageRemovePath {
					body = `{"protocol_version":"skill_package_removal_request.v1",` +
						`"skill":"` + removalSkillRef + `","confirm_remove":true}`
				} else if spec.Path == EvidenceAttachmentPathTemplate {
					body = `{"version":"session_evidence_attachment.v1",` +
						`"source_kind":"workspace_file","source_ref":"README.md",` +
						`"content_sha256":"` + hex.EncodeToString(evidenceDigest[:]) + `"}`
				} else if spec.Path == VerificationEvidencePathTemplate {
					body = `{"version":"operator_verification_evidence.v1",` +
						`"outcome":"pass","title":"OpenAPI verification",` +
						`"summary":"Live route verified"}`
				} else if spec.Path == VerificationPlanPathTemplate {
					body = `{"version":"operator_verification_plan.v1",` +
						`"title":"OpenAPI verification plan","summary":"Operator guidance",` +
						`"items":[{"title":"Live route",` +
						`"expected_observation":"Observe a successful response"}]}`
				} else if spec.Path == VerificationAssociationPathTemplate {
					body = `{"version":"operator_verification_plan_evidence_association.v1",` +
						`"plan_id":"` + verificationPlan.Plan.ID + `",` +
						`"plan_item_ordinal":1,"evidence_id":"` +
						verificationEvidence.Evidence.ID + `"}`
				} else if spec.Path == VerificationSnapshotReceiptPathTemplate {
					snapshot, err := application.NewVerificationSnapshotExportService(
						fixture.store).Build(t.Context(), fixture.run.ID,
						verificationPlan.Plan.ID, 1, application.VerificationSnapshotExportFormatJSON)
					if err != nil {
						t.Fatal(err)
					}
					body = `{"version":"operator_verification_plan_item_snapshot_receipt.v1",` +
						`"plan_id":"` + verificationPlan.Plan.ID + `",` +
						`"plan_item_ordinal":1,"format":"json",` +
						`"snapshot_high_water_event_sequence":` +
						fmt.Sprint(snapshot.SnapshotHighWaterEventSequence) + `,` +
						`"content_sha256":"` + snapshot.ContentSHA256 + `",` +
						`"confirm_metadata_snapshot":true}`
				} else if spec.Path == VerificationSnapshotReceiptReviewPathTemplate {
					body = `{"version":"operator_verification_plan_item_snapshot_receipt_review.v1",` +
						`"receipt_id":"` + openAPIReceipt.Receipt.ID + `",` +
						`"receipt_content_sha256":"` + openAPIReceipt.Receipt.ContentSHA256 + `",` +
						`"receipt_event_sequence":` +
						fmt.Sprint(openAPIReceipt.Receipt.EventSequence) + `,` +
						`"decision":"metadata_confirmed",` +
						`"confirm_non_authorizing_review":true}`
				} else if spec.Path == MCPInputResponsePathTemplate {
					body = `{"version":"mcp-input.v1","action":"decline"}`
				} else if spec.Path == RunExecutionPermissionControlPathTemplate {
					body = `{"mode":"full","confirm_full":true}`
				} else if spec.Path == "/api/v1/threads/{thread_id}/search-diagnostics" {
					body = `{"version":"search_diagnostics.v1","confirm":true}`
				} else if spec.Path == ThreadExecutionPermissionControlPathTemplate {
					body = `{"mode":"ask"}`
				} else if spec.Path == ThreadCommandServicePolicyPathTemplate {
					current, getErr := fixture.store.GetThreadCommandServicePolicy(t.Context(), openAPIThread.ID)
					if getErr != nil {
						t.Fatal(getErr)
					}
					body = `{"version":"thread_command_service_policy.v1","policy":"auto","expected_revision":` +
						fmt.Sprint(current.Revision) + `}`
				} else if spec.Path == RunBrowserCDPPermissionControlPathTemplate {
					body = `{"mode":"restricted"}`
				} else if spec.Path == RunNetworkAuthorityControlPathTemplate {
					body = `{"version":"run_network_authority_control.v1",` +
						`"expected_mode_revision":1,` +
						`"add_allowed_targets":["search.example.org"]}`
				} else if spec.Path == RunWebAccessControlPathTemplate {
					body = `{"version":"run_web_access_control.v1",` +
						`"expected_mode_revision":1,"web_access_mode":"full"}`
				} else if spec.Path == RunExecutionInteractionControlPathTemplate {
					body = `{"mode":"controlled","trust":"trusted",` +
						`"confirm_workspace_trust":true}`
				} else if spec.Path == PlanModeControlPathTemplate {
					body = `{"version":"plan_delivery_control.v1"}`
				} else if strings.HasPrefix(spec.Path, ChatGPTAuthPath+"/") {
					body = `{"version":"chatgpt_auth.v1","confirm":true}`
				} else if spec.Path != RunExecutionProfileControlPathTemplate {
					attemptID := fixture.checkpoint.AttemptID
					modelAttempt := 1
					if spec.Path == SpecialistModelCancellationPathTemplate {
						attemptID = childAttempt.ID
						modelAttempt = childModel.Number
					}
					body = `{"attempt_id":"` + attemptID + `","model_attempt":` +
						fmt.Sprint(modelAttempt) + `}`
				}
				method := spec.Method
				if method == "" {
					method = http.MethodPost
				}
				operationKey := "openapi-live-operation-012345-" + spec.OperationID
				if spec.OperationID == "stopThreadApplicationService" {
					operationKey = "application-stop-" + threadServiceJob.jobID
				}
				response = performControlMethodPathRequest(t, requestAPI, method, requestPath, operationKey, strings.NewReader(body))
				status, statusErr := openAPISuccessStatus(spec)
				if statusErr != nil {
					t.Fatal(statusErr)
				}
				if spec.OperationID != "cancelRunFanoutExecution" &&
					spec.OperationID != "reviewRunChildTaskProposal" &&
					spec.OperationID != "admitRunChildTaskProposal" &&
					!strings.Contains(spec.OperationID, "RunBatchDelivery") &&
					!strings.Contains(spec.OperationID, "BatchWorkbench") &&
					spec.OperationID != "prepareRunBatchDelivery" {
					expectedStatus, statusErr = strconv.Atoi(status)
					if statusErr != nil {
						t.Fatal(statusErr)
					}
				}
				if spec.Path == FileEditApplyPathTemplate {
					// This fixture intentionally retains a live model execution lease.
					// Manual apply must now refuse to write concurrently with it.
					expectedStatus = http.StatusConflict
				}
			} else {
				response = performRequest(t, requestAPI, http.MethodGet, requestPath, testAccessToken,
					"127.0.0.1:8765", "127.0.0.1:45000", nil)
			}
			switch spec.OperationID {
			case "getProjectHooks", "trustProjectHooks", "disableProjectHooks":
				if projectHooks.calls != projectHookCallsBefore+1 || projectHooks.threadID != openAPIThread.ID {
					t.Fatalf("project hook route did not reach the task controller: calls=%d thread=%s", projectHooks.calls-projectHookCallsBefore, projectHooks.threadID)
				}
				if spec.OperationID != "getProjectHooks" {
					want := application.ProjectHooksReviewRequest{
						Version: application.ProjectHooksVersion, ExpectedFingerprint: projectHooksHTTPFingerprint,
					}
					if projectHooks.action != strings.TrimSuffix(spec.OperationID, "ProjectHooks") || projectHooks.request != want {
						t.Fatalf("project hook route changed the action or reviewed configuration: %#v", projectHooks)
					}
				}
			case "listPRFollowups", "createPRFollowup", "pausePRFollowup", "resumePRFollowup", "cancelPRFollowup":
				if prFollowups.calls != prFollowupCallsBefore+1 || prFollowups.threadID != openAPIThread.ID {
					t.Fatalf("PR followup route did not reach the task controller: calls=%d thread=%s", prFollowups.calls-prFollowupCallsBefore, prFollowups.threadID)
				}
				if spec.OperationID == "createPRFollowup" {
					var expected application.PRFollowupCreateRequest
					if err := json.Unmarshal([]byte(prFollowupCreateBody), &expected); err != nil {
						t.Fatal(err)
					}
					if prFollowups.key != "openapi-live-operation-012345-createPRFollowup" || !reflect.DeepEqual(prFollowups.create, expected) {
						t.Fatalf("PR followup creation route changed the original key or settings: %#v", prFollowups)
					}
				} else if spec.OperationID != "listPRFollowups" {
					if prFollowups.id != "followup-one" || prFollowups.action != strings.TrimSuffix(spec.OperationID, "PRFollowup") ||
						prFollowups.transition.Version != application.PRFollowupVersion || prFollowups.transition.ExpectedRevision != 7 {
						t.Fatalf("PR followup transition route changed the target or expected revision: %#v", prFollowups)
					}
				}
			}
			if spec.OperationID == "getBrowserSafeWebReadiness" ||
				spec.OperationID == "getThreadActivityDetail" ||
				spec.OperationID == "getThreadActivityArtifact" {
				// Browser availability is environment-dependent: a machine with an
				// accepted browser returns 200 with a fail-closed readiness receipt,
				// otherwise the route is live with 404 (no accepted browser). Activity
				// detail likewise returns indistinguishable 404 for an opaque unknown ref.
				if response.Code != http.StatusOK && response.Code != http.StatusNotFound {
					t.Fatalf("documented route is not live: path=%s status=%d body=%s",
						requestPath, response.Code, response.Body.String())
				}
			} else if response.Code != expectedStatus {
				if spec.OperationID == "getRun" {
					_, _, diagnostic := fixture.api.run(httptest.NewRequest(http.MethodGet, requestPath, nil), fixture.run.ID)
					t.Logf("Run projection diagnostic: %v", diagnostic)
				}
				t.Fatalf("documented route is not live: path=%s status=%d body=%s",
					requestPath, response.Code, response.Body.String())
			}
			assertSecurityHeaders(t, response)
			if spec.OperationID == "searchSavedThreadMessages" {
				var matches []ThreadSearchResultView
				decodeDataStatus(t, response, http.StatusOK, &matches)
				found := false
				for _, match := range matches {
					if match.MessageID == branchSourceEventID && match.ThreadID == domain.InitialThreadID(branchSourceRun.ID) {
						found = true
					}
				}
				if !found {
					t.Fatalf("saved message search did not return the stored reply: %+v", matches)
				}
			} else if spec.OperationID == "getSavedThreadMessage" {
				var message ThreadSavedMessageView
				decodeDataStatus(t, response, http.StatusOK, &message)
				if message.MessageID != branchSourceEventID || message.Content != "Selected OpenAPI reply" || message.TranscriptCursor == "" {
					t.Fatalf("saved message route lost its content or location: %+v", message)
				}
			}
			if spec.OperationID == "inspectProjectPreparation" || spec.OperationID == "prepareProject" {
				var view application.ProjectPreparationView
				decodeDataStatus(t, response, expectedStatus, &view)
				if view.ProtocolVersion != application.ProjectPreparationProtocolVersion || view.RunID != fixture.run.ID || view.Target != "." {
					t.Fatalf("project preparation route lost its protocol or target: %+v", view)
				}
				if spec.OperationID == "inspectProjectPreparation" && !view.Available {
					t.Fatal("enabled project preparation route reported unavailable")
				}
				if spec.OperationID == "prepareProject" {
					want := application.PrepareProjectRequest{RunID: fixture.run.ID,
						OperationKey: "openapi-live-operation-012345-prepareProject", RequestedBy: "web_operator",
						Target: ".", ExpectedFingerprint: strings.Repeat("a", 64),
						AllowedSources: []string{"https://registry.npmjs.org"}, ConfirmExecution: true}
					if preparationController.starts != 1 || !reflect.DeepEqual(preparationController.request, want) ||
						view.Latest == nil || view.Latest.ID != "prep-saved" || view.Latest.State != "pending" {
						t.Fatalf("preparation route changed operator intent or result: request=%+v view=%+v", preparationController.request, view)
					}
				}
			} else if spec.OperationID == "getProjectPreparation" || spec.OperationID == "cancelProjectPreparation" {
				var view application.ProjectPreparationOperationView
				decodeDataStatus(t, response, http.StatusOK, &view)
				if view.ProtocolVersion != application.ProjectPreparationProtocolVersion || view.RunID != fixture.run.ID || view.ID != "prep-saved" {
					t.Fatalf("preparation operation route lost its identity: %+v", view)
				}
				if spec.OperationID == "getProjectPreparation" && (view.State != "installing" || !view.CanCancel) {
					t.Fatalf("preparation operation lost progress: %+v", view)
				}
				if spec.OperationID == "cancelProjectPreparation" && (preparationController.cancels != 1 || view.State != "cancelled") {
					t.Fatalf("preparation cancellation lost its result: %+v", view)
				}
			} else if spec.OperationID == "selectProjectSkills" ||
				spec.OperationID == "selectInstalledSkills" {
				expectedRun := projectSkillRun.ID
				if spec.OperationID == "selectInstalledSkills" {
					expectedRun = installedSkillRun.ID
				}
				var view application.ProjectSkillSelectionResult
				decodeDataStatus(t, response, http.StatusOK, &view)
				if view.RunID != expectedRun || !reflect.DeepEqual(view.Refs, []string{projectSkillRef}) ||
					view.BuiltinSelectionID == "" || view.ExternalSelectionID != "" || view.CapabilityGrant || view.Replayed {
					t.Fatalf("%s route did not select the requested Skill: %+v",
						spec.OperationID, view)
				}
			} else if spec.OperationID == "executeRunSpecialist" {
				var view SpecialistExecutionView
				decodeDataStatus(t, response, http.StatusOK, &view)
				if len(childExecution.requests) != 1 || childExecution.requests[0] != (application.ExecuteSpecialistRequest{
					RunID: childRun.ID, AgentID: child.ID, OperationKey: "openapi-child-execute-0001", WorkspaceTools: true,
				}) || view.RunID != childRun.ID || view.AgentID != child.ID || view.AttemptID != "sealed-child-attempt" ||
					view.Status != string(domain.AgentAttemptContinued) || view.ModelAttempts != 2 || view.ToolCalls != 1 || view.ToolRounds != 1 {
					t.Fatalf("child execution route changed the admitted intent or result: requests=%+v view=%+v", childExecution.requests, view)
				}
			} else if spec.OperationID == "listThreadApplicationServices" {
				var view application.ThreadApplicationServicesView
				decodeDataStatus(t, response, http.StatusOK, &view)
				if view.ThreadID != threadServiceJob.threadID || len(view.Services) != 1 || view.Services[0].JobID != threadServiceJob.jobID || view.Services[0].RunID != threadServiceJob.runID {
					t.Fatalf("application-service list lost the exact Thread Job: %+v", view)
				}
			} else if spec.OperationID == "getThreadApplicationService" {
				var view application.ThreadApplicationServiceDetailView
				decodeDataStatus(t, response, http.StatusOK, &view)
				if view.Service.ThreadID != threadServiceJob.threadID || view.Service.JobID != threadServiceJob.jobID || view.Service.RunID != threadServiceJob.runID || view.Service.State != string(runner.CommandRuntimeJobFailed) {
					t.Fatalf("application-service detail lost the exact Thread Job: %+v", view)
				}
			} else if spec.OperationID == "stopThreadApplicationService" {
				var view application.ThreadApplicationServiceStopView
				decodeDataStatus(t, response, http.StatusOK, &view)
				if !view.Replayed || view.Service.ThreadID != threadServiceJob.threadID || view.Service.JobID != threadServiceJob.jobID || view.Service.RunID != threadServiceJob.runID || view.Service.CanStop {
					t.Fatalf("application-service stop did not replay the exact reaped Job: %+v", view)
				}
			}
			contentType := response.Header().Get("Content-Type")
			if spec.Streaming {
				streamEvents := parseSSEEvents(t, response.Body.Bytes())
				if !strings.HasPrefix(contentType, "text/event-stream") || len(streamEvents) != 1 {
					t.Fatalf("SSE response is invalid: content-type=%q body=%s", contentType, response.Body.String())
				}
			} else if spec.RawDocument {
				if !strings.HasPrefix(contentType, openAPIContentType) ||
					!bytes.Contains(response.Body.Bytes(), []byte(`"openapi": "3.1.0"`)) {
					t.Fatalf("raw OpenAPI response is invalid: content-type=%q body=%s", contentType, response.Body.String())
				}
			} else if spec.OperationID == "readWorkspaceImageContent" || spec.OperationID == "readRunFullCDPPreviewImage" || spec.OperationID == "readRunAgentBrowserScreenshot" {
				if contentType != "image/png" || !bytes.Equal(response.Body.Bytes(), imageBytes) || response.Header().Get("ETag") != `"`+imageReceipt.SHA256+`"` || response.Header().Get("X-Cyberagent-Content-SHA256") != imageReceipt.SHA256 {
					t.Fatalf("raw image did not match saved identity: content-type=%q", contentType)
				}
			} else if spec.OperationID == "readWorkspaceFileAttachmentContent" {
				if contentType != "application/octet-stream" || !bytes.Equal(response.Body.Bytes(), fileBytes) || response.Header().Get("ETag") != `"`+fileReceipt.SHA256+`"` || response.Header().Get("X-Cyberagent-Content-SHA256") != fileReceipt.SHA256 {
					t.Fatal("raw attachment did not match saved identity")
				}
			} else if spec.RawArtifact {
				if !strings.HasPrefix(contentType, "application/octet-stream") ||
					response.Body.String() != "evidence" {
					t.Fatalf("raw UI evidence artifact is invalid: content-type=%q body=%q",
						contentType, response.Body.String())
				}
			} else if !strings.HasPrefix(contentType, "application/json") || !json.Valid(response.Body.Bytes()) {
				t.Fatalf("API envelope has wrong content type %q", contentType)
			}
		})
	}

	unauthorized := fixture.request(t, http.MethodGet, OpenAPIPath, "",
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "POLICY_DENIED")
	assertAPIError(t, fixture.get(t, OpenAPIPath+"?format=yaml"), http.StatusBadRequest, "INVALID_ARGUMENT")
}

// seedOpenAPILegacySkillForRemoval writes one legacy Registry installation that
// no Run pins, so the documented removal route is reachable without weakening
// the pinned-version refusal. Its retained object is intentionally absent: the
// inventory read must report it as unverified instead of hiding the Registry.
func seedOpenAPILegacySkillForRemoval(t *testing.T, fixture *apiFixture) string {
	t.Helper()
	const name = "openapi-removal-review"
	const version = "1.0.0"
	content := []byte("# OpenAPI removal review\n")
	contentDigest := sha256.Sum256(content)
	archiveDigest := sha256.Sum256([]byte("archive:" + name + "@" + version))
	createdAt := time.Now().UTC().Add(-time.Minute)
	installation := skills.PackageInstallation{
		ID: idgen.New("skill-install"), ProtocolVersion: skills.PackageInstallationProtocolVersion,
		Name: name, Version: version, Surface: domain.ExecutionSurfaceCode,
		Manifest: skills.Manifest{
			Protocol: skills.ProtocolVersion, Name: name, Version: version,
			Description: "OpenAPI legacy removal projection fixture.",
			Profiles:    []domain.Profile{domain.ProfileReview},
			ToolDependencies: []toolgateway.ToolName{
				toolgateway.ListWorkspaceTool, toolgateway.ReadFileTool,
			},
			ContentPath:   skills.PackageContentPath,
			ContentSHA256: hex.EncodeToString(contentDigest[:]),
			ContentBytes:  len(content), ContentTokenUpperBound: skills.ContentTokenUpperBound(content),
		},
		ArchiveSHA256: hex.EncodeToString(archiveDigest[:]), ArchiveBytes: 512,
		UncompressedBytes: 256, EntryCount: skills.PackageEntryCount,
		PackageFingerprint: runmutation.Fingerprint("package", name, version,
			hex.EncodeToString(contentDigest[:])),
		TrustClass: skills.PackageTrustOperatorInstalledUntrusted,
		RiskCodes: []skills.PackageRiskCode{
			skills.PackageRiskUntrustedInstructions, skills.PackageRiskDeclaredToolsOnly,
		},
		OperatorConfirmed: true,
		OperationKeyDigest: runmutation.Fingerprint("skill_package_install_operation.v1",
			"openapi-removal-operation"),
		InstalledBy: "openapi_fixture_operator", CreatedAt: createdAt,
	}
	installation.RequestFingerprint = skills.PackageInstallationIntentFingerprint(installation)
	installation.InstallationFingerprint = skills.PackageInstallationFingerprint(installation)
	objectKey, err := skills.PackageObjectKey(installation.ArchiveSHA256)
	if err != nil {
		t.Fatal(err)
	}
	result, err := skills.NewPackageInstallResult(installation, skills.PackageObjectReceipt{
		Descriptor: skills.DescriptorForInstallation(installation), ObjectKey: objectKey,
	}, createdAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	legacyskill.Seed(t, fixture.dbPath, installation)
	if _, _, err := fixture.store.CompletePackageInstallation(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	return name + "@" + version
}

func buildOpenAPISkillPackage(t *testing.T) []byte {
	t.Helper()
	content := []byte("# OpenAPI external review\n\nInspect workspace evidence only.\n")
	digest := sha256.Sum256(content)
	manifest := skills.Manifest{
		Protocol: skills.ProtocolVersion, Name: "openapi-external-review", Version: "1.0.0",
		Description: "OpenAPI inert Skill installation fixture.",
		Profiles:    []domain.Profile{domain.ProfileReview},
		ToolDependencies: []toolgateway.ToolName{
			toolgateway.ListWorkspaceTool, toolgateway.ReadFileTool,
		},
		ContentPath: skills.PackageContentPath, ContentSHA256: hex.EncodeToString(digest[:]),
		ContentBytes: len(content), ContentTokenUpperBound: skills.ContentTokenUpperBound(content),
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range []struct {
		name string
		data []byte
	}{{skills.PackageManifestPath, manifestRaw}, {skills.PackageContentPath, content}} {
		file, createErr := writer.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Deflate})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := file.Write(entry.data); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func prepareOpenAPIPlanControlTarget(t *testing.T,
	fixture *apiFixture,
) (domain.Run, domain.PlanDeliveryProposal) {
	t.Helper()
	ctx := t.Context()
	runs := application.NewRunService(fixture.store)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "OpenAPI Plan control target", Profile: "review", Phase: "plan",
		ModelRoute: "http-plan/model",
		Budget:     domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &httpPlanProvider{responses: []*llm.ChatResponse{
		{Provider: "http-plan", Model: "model",
			Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
			ToolCalls: []llm.ToolCall{{ID: "openapi-plan-control-call",
				Name: "plan_delivery_propose", Arguments: json.RawMessage(httpPlanDeliveryPayload)}}},
		{Text: httpRootWaitResponse(t), Provider: "http-plan", Model: "model",
			Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}},
	}}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	if _, err := application.NewAgentRunner(fixture.store, router,
		policy.NewDefaultChecker()).Step(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	proposals, err := fixture.store.ListPlanDeliveryProposals(ctx, run.ID, 2)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("OpenAPI Plan proposals=%#v err=%v", proposals, err)
	}
	run, err = fixture.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, proposals[0]
}

func prepareOpenAPISpecialistCancellationTarget(t *testing.T,
	fixture *apiFixture,
) (domain.Run, domain.AgentNode, domain.AgentAttempt, llm.ModelAttempt) {
	t.Helper()
	ctx := t.Context()
	runs := application.NewRunService(fixture.store)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "OpenAPI Specialist cancellation target", Profile: "code",
		Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := fixture.store.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("OpenAPI Specialist root missing: found=%t err=%v", found, err)
	}
	coord, err := coordinator.NewWithSpecialistAdmission(fixture.store,
		coordinator.SpecialistAdmissionPolicy{
			MaxChildren: 1, MaxTurnsPerChild: 2, MaxTokensPerChild: 32,
		})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := coord.AdmitSpecialist(ctx, coordinator.AdmitSpecialistRequest{
		RunID: run.ID, ParentAgentID: root.ID,
		Title: "OpenAPI cancellation target", Skills: []string{"model.chat"},
		TurnLimit: 2, TokenLimit: 32,
		IdempotencyKey: "openapi-specialist-admission-012345",
	})
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := fixture.store.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{
			RunID: run.ID, OwnerID: "openapi-specialist-worker", TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt-openapi-specialist-0001"
	attempt, _, err := fixture.store.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
		AttemptID: attemptID, RunID: run.ID, AgentID: admitted.Agent.ID,
		ParentAgentID: root.ID, Lease: acquired.Lease, StartedAt: time.Now().UTC(),
	}, "openapi-specialist-start-012345")
	if err != nil {
		t.Fatal(err)
	}
	modelAttempt := llm.ModelAttempt{
		Number: 1, TransportAttempt: 1, MaxAttempts: 3,
		Provider: "openapi-specialist", Model: "test-model",
	}
	if inserted, err := fixture.store.RecordSpecialistModelStarted(ctx,
		domain.AgentAttemptRef{RunID: attempt.RunID, AgentID: attempt.AgentID,
			AttemptID: attempt.ID}, modelAttempt); err != nil || !inserted {
		t.Fatalf("OpenAPI Specialist model start inserted=%t err=%v", inserted, err)
	}
	return run, admitted.Agent, attempt, modelAttempt
}

// openAPIPriceSnapshotDocument builds one minimal valid operator price
// snapshot wire document whose validity window includes the current time.
func openAPIPriceSnapshotDocument(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	wire := pricing.Wire{
		ProtocolVersion: pricing.ProtocolVersion, ID: "openapi-live-price-table",
		Source: pricing.SourceOperatorImport, Currency: pricing.CurrencyUSD,
		ImportedBy: "openapi_test",
		ValidFrom:  now.Add(-time.Minute).Format(time.RFC3339),
		ValidUntil: now.Add(time.Hour).Format(time.RFC3339),
		Entries: []struct {
			Provider                        string `json:"provider"`
			Model                           string `json:"model"`
			InputPerMillionMicros           int64  `json:"input_per_million_micros"`
			OutputPerMillionMicros          int64  `json:"output_per_million_micros"`
			CacheReadPerMillionMicros       *int64 `json:"cache_read_per_million_micros,omitempty"`
			CacheCreationPerMillionMicros   *int64 `json:"cache_creation_per_million_micros,omitempty"`
			CacheCreation5mPerMillionMicros *int64 `json:"cache_creation_5m_per_million_micros,omitempty"`
			CacheCreation1hPerMillionMicros *int64 `json:"cache_creation_1h_per_million_micros,omitempty"`
			ToolCallMicros                  int64  `json:"tool_call_micros"`
		}{{Provider: "mock", Model: "mock-code", InputPerMillionMicros: 1000000,
			OutputPerMillionMicros: 2000000, CacheReadPerMillionMicros: func() *int64 { value := int64(250000); return &value }(),
			ToolCallMicros: 50000}},
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertOpenAPISchemaOmits(t *testing.T, schemas map[string]map[string]any, name string, property string) {
	t.Helper()
	schema, ok := schemas[name]
	if !ok {
		t.Fatalf("OpenAPI component %s is missing", name)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI component %s has no properties", name)
	}
	if _, exposed := properties[property]; exposed {
		t.Fatalf("OpenAPI component %s exposed forbidden property %s", name, property)
	}
}

func assertOpenAPISchemaOptional(t *testing.T, schemas map[string]map[string]any,
	name string, property string,
) {
	t.Helper()
	schema, ok := schemas[name]
	if !ok {
		t.Fatalf("OpenAPI component %s is missing", name)
	}
	required, _ := schema["required"].([]any)
	for _, current := range required {
		if current == property {
			t.Fatalf("OpenAPI component %s unexpectedly requires %s", name, property)
		}
	}
}

func assertOpenAPIPropertyFlag(t *testing.T, schemas map[string]map[string]any,
	name string, property string, flag string, expected any,
) {
	t.Helper()
	schema, ok := schemas[name]
	if !ok {
		t.Fatalf("OpenAPI component %s is missing", name)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI component %s has no properties", name)
	}
	value, ok := properties[property].(map[string]any)
	if !ok || !reflect.DeepEqual(value[flag], expected) {
		t.Fatalf("OpenAPI component %s property %s flag %s=%v want=%v",
			name, property, flag, value[flag], expected)
	}
}

func assertOpenAPIEnum(t *testing.T, schemas map[string]map[string]any,
	name string, property string, expected []string,
) {
	t.Helper()
	schema, ok := schemas[name]
	if !ok {
		t.Fatalf("OpenAPI component %s is missing", name)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI component %s has no properties", name)
	}
	value, ok := properties[property].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI component %s property %s is missing", name, property)
	}
	raw, ok := value["enum"].([]any)
	if !ok {
		t.Fatalf("OpenAPI component %s property %s has no enum", name, property)
	}
	actual := make([]string, len(raw))
	for index, current := range raw {
		actual[index], ok = current.(string)
		if !ok {
			t.Fatalf("OpenAPI component %s property %s has a non-string enum", name, property)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("OpenAPI component %s property %s enum=%v want=%v",
			name, property, actual, expected)
	}
}

func (c *openAPIThreadPlanController) ResumeApproval(_ context.Context, request application.ApprovalContinuationRequest) application.ApprovalContinuationResult {
	if request.Kind != "host_command" || request.ProposalID != "controlled-command-proposal-openapi" {
		return application.ApprovalContinuationResult{State: "failed", ErrorCode: "INVALID_ARGUMENT"}
	}
	return application.ApprovalContinuationResult{State: "completed", HandoffID: "openapi-historical-continuation"}
}
