package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/rootfs"
)

func TestProposalAndCredentialHTTPControlsAreDefaultOff(t *testing.T) {
	fixture := newAPIFixture(t)
	sourcePath := "/api/v1/runs/" + fixture.run.ID +
		"/file-edit-proposal-source?path=README.md"
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodGet,
		sourcePath, testAccessToken, "", "", nil), http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodGet,
		"/api/v1/runs/"+fixture.run.ID+"/file-edit-proposal-recovery/edit-missing",
		testAccessToken, "", "", nil), http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodPost,
		"/api/v1/runs/"+fixture.run.ID+"/file-edit-proposals", testControlToken,
		"", "application/json", strings.NewReader(`{}`)), http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodGet,
		ProviderCredentialsPath, testAccessToken, "", "", nil),
		http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, performSessionMessageRequest(t, fixture.api, http.MethodPost,
		"/api/v1/models/credentials/mimo", testControlToken, "", "application/json",
		strings.NewReader(`{}`)), http.StatusNotFound, "NOT_FOUND")
}

func TestFileEditProposalHTTPRequiresGoSourceAndNeverWritesFile(t *testing.T) {
	fixture := newAPIFixture(t)
	const relativePath = " "
	original := "original Workspace content\n"
	proposed := "operator-proposed content\n"
	absolutePath, err := rootfs.ExactAbsolutePath(filepath.Join(fixture.workspace.RootPath, relativePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolutePath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(absolutePath) })
	proposalService := application.NewFileEditProposalService(fixture.store,
		policy.NewDefaultChecker())
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, FileEditProposalEnabled: true,
		FileEditProposalController: proposalService, AppVersion: "proposal-http-test"})
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := "/api/v1/runs/" + fixture.run.ID +
		"/file-edit-proposal-source?path=" + url.QueryEscape(relativePath)
	sourceResponse := performSessionMessageRequest(t, api, http.MethodGet, sourcePath,
		testAccessToken, "", "", nil)
	var source FileEditProposalSourceView
	decodeDataStatus(t, sourceResponse, http.StatusOK, &source)
	if source.Content != original || source.SourceHandle == "" || !source.Editable ||
		source.FileWrite || source.Path != relativePath {
		t.Fatalf("unsafe or incomplete proposal source: %#v", source)
	}
	reissuePath := sourcePath + "&expected_sha256=" + source.ContentSHA256
	reissueResponse := performSessionMessageRequest(t, api, http.MethodGet, reissuePath,
		testAccessToken, "", "", nil)
	var reissued FileEditProposalSourceView
	decodeDataStatus(t, reissueResponse, http.StatusOK, &reissued)
	if reissued.SourceHandle == source.SourceHandle ||
		reissued.ContentSHA256 != source.ContentSHA256 || reissued.Content != source.Content {
		t.Fatalf("proposal source was not safely reissued: %#v", reissued)
	}

	body := `{"version":"file_edit_proposal.v1","source_handle":"` +
		reissued.SourceHandle + `","proposed_text":"operator-proposed content\n"}`
	proposalPath := "/api/v1/runs/" + fixture.run.ID + "/file-edit-proposals"
	readToken := performSessionMessageRequest(t, api, http.MethodPost, proposalPath,
		testAccessToken, "", "application/json", strings.NewReader(body))
	assertAPIError(t, readToken, http.StatusUnauthorized, "POLICY_DENIED")

	proposalResponse := performSessionMessageRequest(t, api, http.MethodPost, proposalPath,
		testControlToken, "", "application/json", strings.NewReader(body))
	var result FileEditProposalView
	decodeDataStatus(t, proposalResponse, http.StatusAccepted, &result)
	if result.FileWritten || !result.ApprovalRequired || result.Edit.Status != "proposed" ||
		result.Edit.Path != relativePath || result.Edit.ApplyEnabled {
		t.Fatalf("proposal widened into write authority: %#v", result)
	}
	current, err := os.ReadFile(absolutePath)
	if err != nil || string(current) != original || string(current) == proposed {
		t.Fatalf("proposal changed the Workspace file: content=%q err=%v", current, err)
	}
	recoveryPath := "/api/v1/runs/" + fixture.run.ID +
		"/file-edit-proposal-recovery/" + result.Edit.ID
	recoveryResponse := performSessionMessageRequest(t, api, http.MethodGet,
		recoveryPath, testAccessToken, "", "", nil)
	var recovery FileEditProposalRecoveryView
	decodeDataStatus(t, recoveryResponse, http.StatusOK, &recovery)
	if recovery.EditID != result.Edit.ID || recovery.Path != relativePath || recovery.OriginalContent != original ||
		recovery.ProposedContent != proposed || recovery.Stale || recovery.Editable ||
		!recovery.ReviewRequired || recovery.FileWrite {
		t.Fatalf("durable proposal recovery widened authority: %#v", recovery)
	}
	if err := os.WriteFile(absolutePath, []byte("changed outside editor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleResponse := performSessionMessageRequest(t, api, http.MethodGet,
		recoveryPath, testAccessToken, "", "", nil)
	decodeDataStatus(t, staleResponse, http.StatusOK, &recovery)
	if !recovery.Stale || recovery.CurrentContentSHA256 == recovery.OriginalSHA256 {
		t.Fatalf("stale proposal recovery was not projected: %#v", recovery)
	}
	staleReissue := performSessionMessageRequest(t, api, http.MethodGet,
		reissuePath, testAccessToken, "", "", nil)
	assertAPIError(t, staleReissue, http.StatusConflict, "CONFLICT")
}

func TestProviderCredentialHTTPNeverReturnsPlaintext(t *testing.T) {
	fixture := newAPIFixture(t)
	ownedStore := credential.NewMemoryStore()
	service := application.NewProviderCredentialService(ownedStore)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, ProviderCredentialEnabled: true,
		ProviderCredentialController: service, AppVersion: "credential-http-test"})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "temporary-provider-http-key"
	path := "/api/v1/models/credentials/mimo"
	body := `{"version":"provider_credential.v1","action":"set",` +
		`"secret":"` + secret + `","confirm":true}`
	readToken := performSessionMessageRequest(t, api, http.MethodPost, path,
		testAccessToken, "", "application/json", strings.NewReader(body))
	assertAPIError(t, readToken, http.StatusUnauthorized, "POLICY_DENIED")

	response := performSessionMessageRequest(t, api, http.MethodPost, path,
		testControlToken, "", "application/json", strings.NewReader(body))
	if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), `"secret"`) {
		t.Fatalf("credential control response exposed plaintext: %s", response.Body.String())
	}
	var status ProviderCredentialStatusView
	decodeDataStatus(t, response, http.StatusAccepted, &status)
	if !status.Configured || status.PlaintextReturned || !status.RestartRequired {
		t.Fatalf("credential status violated its metadata-only contract: %#v", status)
	}
	stored, found, err := ownedStore.Get(t.Context(), "mimo")
	if err != nil || !found || stored != secret {
		t.Fatalf("credential did not reach the Go-owned Store: found=%t err=%v", found, err)
	}

	list := performSessionMessageRequest(t, api, http.MethodGet,
		ProviderCredentialsPath, testAccessToken, "", "", nil)
	if strings.Contains(list.Body.String(), secret) || strings.Contains(list.Body.String(), `"secret"`) {
		t.Fatalf("credential status response exposed plaintext: %s", list.Body.String())
	}
	var statuses ProviderCredentialListView
	decodeDataStatus(t, list, http.StatusOK, &statuses)
	if len(statuses.Items) != 4 {
		t.Fatalf("credential status count=%d want=4", len(statuses.Items))
	}
}
