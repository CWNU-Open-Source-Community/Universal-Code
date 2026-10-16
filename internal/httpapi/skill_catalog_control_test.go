package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/skills"
)

var _ SkillCatalogController = (*application.SkillCatalogService)(nil)

func catalogControlFixture(t *testing.T) (*apiFixture, *application.SkillCatalogService, SkillCatalogPublisherControlRequestView) {
	t.Helper()
	f := newAPIFixture(t)
	service := application.NewSkillCatalogService(f.store, nil)
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, SkillCatalogControlEnabled: true, SkillCatalogController: service})
	if err != nil {
		t.Fatal(err)
	}
	f.api = api
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	encoded := base64.StdEncoding.EncodeToString(key)
	fingerprint, _ := skills.PublisherFingerprintFromBase64(encoded)
	empty := ""
	return f, service, SkillCatalogPublisherControlRequestView{Version: application.SkillCatalogControlProtocol, Action: "trust", ExpectedUpdatedAt: &empty, Fingerprint: fingerprint, PublicKey: encoded, Name: "Publisher", Team: "Team", Confirm: true}
}

func TestSkillCatalogHTTPTrustRevokeAndExplicitRegrant(t *testing.T) {
	f, service, request := catalogControlFixture(t)
	post := func(request SkillCatalogPublisherControlRequestView, want int, target *SkillCatalogPublisherView) {
		t.Helper()
		body, _ := json.Marshal(request)
		response := performSessionMessageRequest(t, f.api, http.MethodPost, SkillCatalogPublishersPath, testControlToken, "", "application/json", bytes.NewReader(body))
		if want == http.StatusOK {
			decodeDataStatus(t, response, want, target)
		} else {
			assertAPIError(t, response, want, "CONFLICT")
		}
	}
	var initial SkillCatalogPublisherView
	post(request, http.StatusOK, &initial)
	if initial.Fingerprint != request.Fingerprint || initial.PublicKey != request.PublicKey || initial.TrustClass != "trusted" || initial.TrustedAt == nil || initial.UpdatedAt.IsZero() {
		t.Fatalf("trust view=%+v", initial)
	}
	var listed SkillCatalogPublishersView
	decodeData(t, f.get(t, SkillCatalogPublishersPath), &listed)
	if listed.ProtocolVersion != application.SkillCatalogControlProtocol || !listed.ControlEnabled || len(listed.Publishers) != 1 || !listed.Publishers[0].UpdatedAt.Equal(initial.UpdatedAt) {
		t.Fatalf("list=%+v", listed)
	}
	post(request, http.StatusConflict, nil)
	revokeVersion := initial.UpdatedAt.Format(time.RFC3339Nano)
	revoke := SkillCatalogPublisherControlRequestView{Version: request.Version, Action: "revoke", ExpectedUpdatedAt: &revokeVersion, Fingerprint: request.Fingerprint, Confirm: true}
	var revoked SkillCatalogPublisherView
	post(revoke, http.StatusOK, &revoked)
	if revoked.TrustClass != "revoked" || revoked.RevokedAt == nil {
		t.Fatalf("revoke=%+v", revoked)
	}
	request.ExpectedUpdatedAt = &revokeVersion
	post(request, http.StatusConflict, nil)
	latestVersion := revoked.UpdatedAt.Format(time.RFC3339Nano)
	request.ExpectedUpdatedAt = &latestVersion
	var regranted SkillCatalogPublisherView
	post(request, http.StatusOK, &regranted)
	if regranted.TrustClass != "trusted" || regranted.RevokedAt != nil {
		t.Fatalf("regrant=%+v", regranted)
	}
	if err := service.RevokePublisher(t.Context(), application.RevokeSkillPublisherRequest{Fingerprint: request.Fingerprint, Actor: "cli"}); err != nil {
		t.Fatal(err)
	}
	latestVersion = regranted.UpdatedAt.Format(time.RFC3339Nano)
	post(request, http.StatusConflict, nil)
	audit, err := f.store.ListSkillCatalogAudit(t.Context(), 20)
	if err != nil || len(audit) != 4 {
		t.Fatalf("audit=%+v %v", audit, err)
	}
}

func TestSkillCatalogHTTPCanonicalizesLegacyCLIKeyWithoutChangingVersion(t *testing.T) {
	f, service, request := catalogControlFixture(t)
	wrapped := request.PublicKey[:20] + "\r\n" + request.PublicKey[20:]
	legacy, err := service.TrustPublisher(t.Context(), application.TrustSkillPublisherRequest{
		Name: "Legacy publisher", Team: strings.Repeat("t", 128), PublicKey: wrapped, Actor: "cli",
	})
	if err != nil {
		t.Fatal(err)
	}
	var listed SkillCatalogPublishersView
	decodeData(t, f.get(t, SkillCatalogPublishersPath), &listed)
	if len(listed.Publishers) != 1 {
		t.Fatalf("publishers=%+v", listed.Publishers)
	}
	view := listed.Publishers[0]
	if view.PublicKey != request.PublicKey || view.Fingerprint != legacy.Fingerprint || view.Team != legacy.Team || !view.UpdatedAt.Equal(legacy.UpdatedAt) || !view.CreatedAt.Equal(legacy.CreatedAt) || view.TrustedAt == nil || !view.TrustedAt.Equal(*legacy.TrustedAt) {
		t.Fatalf("legacy=%+v view=%+v", legacy, view)
	}
	stored, found, err := f.store.GetSkillCatalogPublisher(t.Context(), legacy.Fingerprint)
	if err != nil || !found || stored.PublicKey != wrapped || !stored.UpdatedAt.Equal(legacy.UpdatedAt) {
		t.Fatalf("projection changed stored publisher: %+v found=%t err=%v", stored, found, err)
	}
}

func TestSkillCatalogHTTPControlBoundariesAndStrictInputs(t *testing.T) {
	f, service, request := catalogControlFixture(t)
	body, _ := json.Marshal(request)
	for _, tc := range []struct {
		name, method, token string
		status              int
	}{
		{"GET requires read token", http.MethodGet, testControlToken, http.StatusUnauthorized},
		{"POST requires control token", http.MethodPost, testAccessToken, http.StatusUnauthorized},
		{"missing token", http.MethodPost, "", http.StatusUnauthorized},
		{"method", http.MethodDelete, testControlToken, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := performSessionMessageRequest(t, f.api, tc.method, SkillCatalogPublishersPath, tc.token, "", "application/json", bytes.NewReader(body))
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	raw := string(body)
	for _, invalid := range []string{
		strings.Replace(raw, `"confirm":true`, `"confirm":false`, 1),
		strings.Replace(raw, `"expected_updated_at":"",`, "", 1),
		strings.Replace(raw, `"expected_updated_at":""`, `"expected_updated_at":null`, 1),
		strings.Replace(raw, `"version":`, `"unknown":true,"version":`, 1),
		strings.Replace(raw, `"confirm":true`, `"confirm":true,"confirm":true`, 1),
		strings.Replace(raw, request.Fingerprint, strings.Repeat("f", 64), 1),
	} {
		response := performSessionMessageRequest(t, f.api, http.MethodPost, SkillCatalogPublishersPath, testControlToken, "", "application/json", strings.NewReader(invalid))
		assertAPIError(t, response, http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	publishers, err := service.ListPublishers(t.Context())
	if err != nil || len(publishers) != 0 {
		t.Fatalf("invalid request wrote state: %+v %v", publishers, err)
	}
	readOnly, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, SkillCatalogController: service})
	if err != nil {
		t.Fatal(err)
	}
	var listed SkillCatalogPublishersView
	decodeDataStatus(t, performSessionMessageRequest(t, readOnly, http.MethodGet, SkillCatalogPublishersPath, testAccessToken, "", "", nil), http.StatusOK, &listed)
	if listed.ControlEnabled || listed.Publishers == nil {
		t.Fatalf("read-only view=%+v", listed)
	}
	assertAPIError(t, performSessionMessageRequest(t, readOnly, http.MethodPost, SkillCatalogPublishersPath, testControlToken, "", "application/json", bytes.NewReader(body)), http.StatusNotFound, "NOT_FOUND")
	_, err = New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, SkillCatalogControlEnabled: true})
	if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("enabled missing controller=%v", err)
	}
	_, err = New(f.store, Config{AccessToken: testAccessToken, SkillCatalogControlEnabled: true, SkillCatalogController: service})
	if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("enabled missing control token=%v", err)
	}
}
