package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/plugins"
)

func TestExtensionPinnedHTTPSImportStagesAndReplaysRevokedState(t *testing.T) {
	f, _, _, service := newExtensionOnboardingFixture(t)
	raw := extensionPluginArchive(t)
	var fetches atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/plugin.zip", http.StatusFound)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	// Trust only this fixture's CA while exercising the real production fetcher.
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	request := extensionPluginImportRequest(raw)
	request.ArchiveBase64 = ""
	request.Source = &ExtensionPluginImportSourceView{Kind: "https", URL: server.URL + "/plugin.zip"}
	var staged ExtensionPluginImportView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request), http.StatusAccepted, &staged)
	if staged.Replayed || staged.Installation.State != "staged" || staged.Installation.Source.Kind != "https" ||
		staged.Installation.Source.URI != request.Source.URL || len(staged.Installation.EnabledCapabilities) != 0 || fetches.Load() != 1 ||
		staged.Acquisition == nil || staged.Acquisition.Kind != "https" || staged.Acquisition.URL != request.Source.URL ||
		staged.Acquisition.SHA256 != request.ArchiveSHA256 || staged.Acquisition.Commit != "" || staged.Acquisition.ArchivePath != "" {
		t.Fatalf("pinned import lost provenance or enabled content: %+v", staged)
	}
	revoked, err := service.Review(t.Context(), staged.Installation.ID, plugins.ReviewRequest{
		Action: plugins.ReviewRevoke, ExpectedGeneration: staged.Installation.Generation,
		ExpectedPackageFingerprint: staged.Installation.PackageFingerprint, ReviewedBy: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var replay ExtensionPluginImportView
	decodeDataStatus(t, extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request), http.StatusAccepted, &replay)
	if !replay.Replayed || replay.Installation.ID != revoked.ID || replay.Installation.State != "revoked" ||
		replay.Installation.Generation != revoked.Generation || replay.NextStep != "inspect_state" ||
		replay.Acquisition == nil || *replay.Acquisition != *staged.Acquisition {
		t.Fatalf("source retry revived a revoked installation: %+v", replay)
	}
	request.Source.URL = server.URL + "/redirect"
	before := fetches.Load()
	if response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request); response.Code != http.StatusBadRequest || fetches.Load() != before+1 {
		t.Fatal("redirect was followed or accepted", response.Code, response.Body.String())
	}
	request.Source.URL = server.URL + "/plugin.zip"
	request.ArchiveSHA256 = strings.Repeat("0", 64)
	if response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request); response.Code != http.StatusBadRequest {
		t.Fatal("digest drift was accepted", response.Code, response.Body.String())
	}
}

func TestExtensionPinnedSourceReplayBindsAcquisitionWithoutRewritingPriorSource(t *testing.T) {
	f, _, _, service := newExtensionOnboardingFixture(t)
	raw := extensionPluginArchive(t)
	request := extensionPluginImportRequest(raw)
	prior, _, err := service.Stage(t.Context(), raw, plugins.InstallSource{Kind: "local_file",
		URI: filepath.Join(t.TempDir(), "private-source.zip"), SHA256: request.ArchiveSHA256}, "", "cli_operator")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer upstream.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	request.ArchiveBase64 = ""
	request.Source = &ExtensionPluginImportSourceView{Kind: "https", URL: upstream.URL + "/plugin.zip"}
	var replay ExtensionPluginImportView
	response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request)
	decodeDataStatus(t, response, http.StatusAccepted, &replay)
	if !replay.Replayed || replay.Installation.ID != prior.ID || replay.Installation.Source.Kind != "local_file" ||
		replay.Installation.Source.URI != "sha256:"+request.ArchiveSHA256 || strings.Contains(response.Body.String(), "private-source") ||
		replay.Acquisition == nil || replay.Acquisition.Kind != "https" || replay.Acquisition.URL != request.Source.URL ||
		replay.Acquisition.SHA256 != replay.Installation.ArchiveSHA256 {
		t.Fatalf("source replay lost acquisition binding or rewrote provenance: %s", response.Body.String())
	}
}

func TestExtensionPinnedSourceRejectsReplayWithDifferentArchiveEncoding(t *testing.T) {
	f, _, _, service := newExtensionOnboardingFixture(t)
	raw := extensionPluginArchive(t)
	request := extensionPluginImportRequest(raw)
	prior, _, err := service.Stage(t.Context(), raw, plugins.InstallSource{Kind: "upload",
		URI: "sha256:" + request.ArchiveSHA256, SHA256: request.ArchiveSHA256}, "", "operator")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	writer := zip.NewWriter(&encoded)
	if err := writer.SetComment("same package contents, different ZIP digest"); err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if err := writer.Copy(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encoded.Bytes())
	}))
	defer upstream.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	digest := sha256.Sum256(encoded.Bytes())
	request.ArchiveSHA256 = hex.EncodeToString(digest[:])
	request.ArchiveBase64 = ""
	request.Source = &ExtensionPluginImportSourceView{Kind: "https", URL: upstream.URL + "/plugin.zip"}
	response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request)
	assertAPIError(t, response, http.StatusConflict, "CONFLICT")
	retained, err := f.store.GetPluginInstallation(t.Context(), prior.ID)
	if err != nil || retained.ArchiveSHA256 != prior.ArchiveSHA256 || retained.Generation != prior.Generation {
		t.Fatalf("conflicting archive encoding changed retained state: %+v %v", retained, err)
	}
}

func TestExtensionPinnedSourceRejectsUnpinnedOrAmbiguousInput(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	for _, source := range []ExtensionPluginImportSourceView{
		{Kind: "https", URL: "http://127.0.0.1/plugin.zip"},
		{Kind: "https", URL: "https://user:password@example.invalid/plugin.zip"},
		{Kind: "https", URL: "https://example.invalid/plugin.zip?token=secret"},
		{Kind: "https", URL: "https://example.invalid/plugin.zip", Commit: strings.Repeat("a", 40)},
		{Kind: "git", URL: "https://example.invalid/repository.git", Commit: "main"},
		{Kind: "git", URL: "https://example.invalid/repository.git", Commit: strings.Repeat("a", 40), ArchivePath: "../plugin.zip"},
		{Kind: "local_file", URL: "C:/private/plugin.zip"},
	} {
		request := ExtensionPluginImportRequestView{Version: ExtensionControlProtocol, Source: &source, ArchiveSHA256: strings.Repeat("a", 64)}
		if response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request); response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe source accepted: %+v: %d %s", source, response.Code, response.Body.String())
		}
	}
	request := extensionPluginImportRequest(extensionPluginArchive(t))
	request.Source = &ExtensionPluginImportSourceView{Kind: "https", URL: "https://example.invalid/plugin.zip"}
	if response := extensionOnboardingRequest(t, f.api, ExtensionPluginImportPath, request); response.Code != http.StatusBadRequest {
		t.Fatal("mixed upload and source accepted", response.Code, response.Body.String())
	}
}

func TestExtensionPinnedSourceRespondsAfterOrdinaryWriteTimeout(t *testing.T) {
	f, _, _, _ := newExtensionOnboardingFixture(t)
	raw := extensionPluginArchive(t)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(150 * time.Millisecond):
			_, _ = w.Write(raw)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	server := httptest.NewUnstartedServer(f.api)
	server.Config.WriteTimeout = 30 * time.Millisecond
	server.Start()
	defer server.Close()
	view := extensionPluginImportRequest(raw)
	view.ArchiveBase64 = ""
	view.Source = &ExtensionPluginImportSourceView{Kind: "https", URL: upstream.URL + "/plugin.zip"}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+ExtensionPluginImportPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	client := server.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("lost pinned import response: %v", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data ExtensionPluginImportView `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted || envelope.Data.Installation.State != "staged" ||
		envelope.Data.Installation.Source.URI != view.Source.URL || envelope.Data.Installation.ArchiveSHA256 != view.ArchiveSHA256 {
		t.Fatalf("pinned import receipt: status=%d data=%+v", response.StatusCode, envelope.Data)
	}
	if server.Config.WriteTimeout != 30*time.Millisecond {
		t.Fatal("pinned import changed the global server timeout")
	}
}

func TestExtensionPinnedSourcePreservesCancellationCodes(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancelled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			f, controller, _, _ := newExtensionOnboardingFixture(t)
			started := make(chan struct{})
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
			}))
			defer upstream.Close()
			previous := http.DefaultTransport
			http.DefaultTransport = upstream.Client().Transport
			defer func() { http.DefaultTransport = previous }()
			ctx, cancel := context.WithCancel(t.Context())
			wantCode := apperror.CodeCancelled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
				wantCode = apperror.CodeDeadlineExceeded
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := controller.ImportPluginSource(ctx, application.ImportPluginSourceRequest{
					Kind: "https", URL: upstream.URL + "/plugin.zip", SHA256: strings.Repeat("a", 64)})
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("pinned fetch did not start")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				if apperror.CodeOf(err) != wantCode {
					t.Fatalf("fetch cancellation classified as %s, want %s: %v", apperror.CodeOf(err), wantCode, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("pinned fetch ignored cancellation")
			}
			inventory, err := f.store.ListPluginInstallations(t.Context(), "", 100)
			if err != nil || len(inventory) != 0 {
				t.Fatalf("cancelled pinned fetch left durable state: %+v %v", inventory, err)
			}
		})
	}
}
