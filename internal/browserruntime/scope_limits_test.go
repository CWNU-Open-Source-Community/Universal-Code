package browserruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func scopeLimitsPlanRequest() NewSessionPlanRequest {
	return NewSessionPlanRequest{
		SessionID: "scope-limits-session", RunID: "scope-limits-run", WorkspaceID: "scope-limits-workspace",
		ProfileID: ProfileSafeWeb, Targets: []string{"https://example.com"},
	}
}

func TestSessionPlanEnforcesNarrowedScopeBounds(t *testing.T) {
	t.Run("initial URL", func(t *testing.T) {
		request := scopeLimitsPlanRequest()
		request.Targets = []string{"https://example.com/" + strings.Repeat("a", 100)}
		request.LimitsOverride = &ProfileLimits{MaxURLBytes: 32}
		if _, err := BuildSessionPlan(request); err == nil {
			t.Fatal("accepted oversized initial target")
		}
	})
	t.Run("origin count", func(t *testing.T) {
		request := scopeLimitsPlanRequest()
		request.Targets = append(request.Targets, "https://example.org")
		request.LimitsOverride = &ProfileLimits{MaxOrigins: 1}
		if _, err := BuildSessionPlan(request); err == nil {
			t.Fatal("accepted more than one origin")
		}
	})
	t.Run("later navigation", func(t *testing.T) {
		request := scopeLimitsPlanRequest()
		request.LimitsOverride = &ProfileLimits{MaxURLBytes: 32}
		plan, err := BuildSessionPlan(request)
		if err != nil {
			t.Fatal(err)
		}
		if decision := plan.Scope.AuthorizeNavigation("https://example.com/" + strings.Repeat("a", 100)); decision.Allowed {
			t.Fatal("accepted oversized later navigation")
		}
	})
}

func TestDefaultScopePlanLegacyFingerprints(t *testing.T) {
	plan, err := BuildSessionPlan(scopeLimitsPlanRequest())
	if err != nil {
		t.Fatal(err)
	}
	// Captured at c2c21a97 before adding optional scope bounds.
	if plan.ProfileFingerprint != "d09849dad3b43c055d8c51cd49e46b350c5d7c1ce9af9a70e99ca17844353bc6" ||
		plan.Scope.Fingerprint != "406dd13d45451e6a56ba9d121afbcb0df30592ef0581fe97c1d75aa227f37e9e" ||
		plan.ProfileToken != "c95397c6915a624ce7c23830be49ed0bddc031a04aa4cc52d7e3f21420d9c2d3" ||
		plan.Fingerprint != "6ab3075e8f0df83bf33a06c57c00b445a3407100a06b0beb5f0305a07b0c3b8b" {
		t.Fatalf("legacy default plan fingerprints changed: %+v", plan)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var restored SessionPlan
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNarrowedScopeRoundTripAndURLBoundary(t *testing.T) {
	request := scopeLimitsPlanRequest()
	request.LimitsOverride = &ProfileLimits{MaxOrigins: 1, MaxURLBytes: 32}
	plan, err := BuildSessionPlan(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var restored SessionPlan
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatal(err)
	}
	baseURL := "https://example.com:443/"
	boundary := baseURL + strings.Repeat("a", 32-len(baseURL))
	for _, tc := range []struct {
		name, url string
		allowed   bool
	}{
		{"at byte limit", boundary, true},
		{"above byte limit", boundary + "a", false},
		{"port normalization exceeds limit", "https://example.com/" + strings.Repeat("a", 32-len("https://example.com/")), false},
		{"UTF-8 escaping exceeds limit", "https://example.com/éé", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := restored.Scope.AuthorizeNavigation(tc.url)
			if decision.Allowed != tc.allowed {
				t.Fatalf("navigation decision=%+v, want allowed=%t", decision, tc.allowed)
			}
			request.Targets = []string{tc.url}
			if _, err := BuildSessionPlan(request); (err == nil) != tc.allowed {
				t.Fatalf("initial target err=%v, want allowed=%t", err, tc.allowed)
			}
		})
	}
	if restored.ProfileFingerprint != plan.ProfileFingerprint || restored.Scope.MaxOrigins != 1 ||
		restored.Scope.MaxURLBytes != 32 || restored.Scope.Authority != (ScopeAuthority{}) {
		t.Fatalf("scope bounds or authority changed during round trip: %+v", restored.Scope)
	}
}

func TestNarrowedScopeValidationAndPlanBinding(t *testing.T) {
	request := scopeLimitsPlanRequest()
	request.LimitsOverride = &ProfileLimits{MaxOrigins: 1, MaxURLBytes: 32}
	plan, err := BuildSessionPlan(request)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := BuiltinRegistry().Lookup(plan.ProfileID)
	for _, tc := range []struct {
		name   string
		mutate func(*TargetScope)
	}{
		{"negative origins", func(scope *TargetScope) { scope.MaxOrigins = -1 }},
		{"negative URL bytes", func(scope *TargetScope) { scope.MaxURLBytes = -1 }},
		{"widen builtin origins", func(scope *TargetScope) { scope.MaxOrigins = profile.Limits.MaxOrigins + 1 }},
		{"widen builtin URL bytes", func(scope *TargetScope) { scope.MaxURLBytes = profile.Limits.MaxURLBytes + 1 }},
		{"too many origins", func(scope *TargetScope) {
			second := scope.Origins[0]
			second.Host = "example.org"
			scope.Origins = append(scope.Origins, second)
		}},
		{"oversized stored origin", func(scope *TargetScope) { scope.MaxURLBytes = len(scope.Origins[0].String()) - 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := plan.Scope
			scope.Origins = append([]Origin(nil), scope.Origins...)
			tc.mutate(&scope)
			scope.Fingerprint, err = targetScopeFingerprint(scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := scope.Validate(); err == nil {
				t.Fatal("re-signed invalid scope was accepted")
			}
			if scope.AuthorizeNavigation(request.Targets[0]).Allowed ||
				scope.AuthorizeResolvedAddress(scope.Origins[0], "93.184.216.34").Allowed {
				t.Fatal("invalid scope authorized navigation or a resolved address")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*TargetScope)
	}{
		{"remove narrowed origins", func(scope *TargetScope) { scope.MaxOrigins = 0 }},
		{"remove narrowed URL bytes", func(scope *TargetScope) { scope.MaxURLBytes = 0 }},
		{"change narrowed URL bytes", func(scope *TargetScope) { scope.MaxURLBytes++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := plan
			tc.mutate(&candidate.Scope)
			candidate.Scope.Fingerprint, err = targetScopeFingerprint(candidate.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := candidate.Scope.Validate(); err != nil {
				t.Fatalf("expected independently valid scope: %v", err)
			}
			candidate.ProfileToken = profileToken(candidate)
			candidate.Fingerprint, err = sessionPlanFingerprint(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "scope limits") {
				t.Fatalf("plan accepted mismatched scope limits after re-signing: %v", err)
			}
		})
	}
}

func TestLegacyScopeReaderRejectsNarrowedPayload(t *testing.T) {
	request := scopeLimitsPlanRequest()
	request.LimitsOverride = &ProfileLimits{MaxOrigins: 1, MaxURLBytes: 32}
	plan, err := BuildSessionPlan(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan.Scope)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-bound reader discards unknown JSON fields before recomputing its
	// fingerprint. Emulate that projection with both optional fields removed.
	var legacyFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacyFields); err != nil {
		t.Fatal(err)
	}
	delete(legacyFields, "max_origins")
	delete(legacyFields, "max_url_bytes")
	raw, err = json.Marshal(legacyFields)
	if err != nil {
		t.Fatal(err)
	}
	var legacyScope TargetScope
	if err := json.Unmarshal(raw, &legacyScope); err != nil {
		t.Fatal(err)
	}
	if err := legacyScope.Validate(); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("legacy scope reader did not fail closed: %v", err)
	}
}

func TestCDPRequestEnforcesNarrowedURLBytes(t *testing.T) {
	_, executable, _, runtimeRoot := profileLifecycleFixture(t)
	input := scopeLimitsPlanRequest()
	input.LimitsOverride = &ProfileLimits{MaxURLBytes: 32}
	session, err := BuildSessionPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	ownership, err := BuildProfileOwnershipPlan(session, executable, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	url := "https://example.com:443/" + strings.Repeat("a", 32-len("https://example.com:443/"))
	request, err := BuildCDPRequest(session, executable, ownership, "at-limit", CDPActionNavigate, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildCDPRequest(session, executable, ownership, "over-limit", CDPActionNavigate, url+"a"); err == nil {
		t.Fatal("CDP request builder accepted oversized URL")
	}
	request.CanonicalURL += "a"
	request.Fingerprint, err = cdpRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCDPRequest(request, session, executable, ownership); err == nil {
		t.Fatal("CDP request validator accepted re-signed oversized URL")
	}
}

func TestCDPInterceptedRequestsEnforceNarrowedURLBytes(t *testing.T) {
	input := scopeLimitsPlanRequest()
	input.LimitsOverride = &ProfileLimits{MaxURLBytes: 32}
	session, err := BuildSessionPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	server := newScriptedCDPServer(t, input.Targets[0])
	defer server.Close(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, "ws://"+server.listener.Addr().String()+server.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client := restrictedCDPClient{conn: connection, sessionID: "session-test", scope: session.Scope,
		maxRequests: session.Limits.MaxRequests}
	for _, url := range []string{"https://example.com:443/a", "https://example.com:443/" + strings.Repeat("a", 100)} {
		params, err := json.Marshal(map[string]any{
			"requestId": "request-test", "resourceType": "Script",
			"request": map[string]any{"url": url, "method": "GET"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.handleEvent(ctx, cdpWireMessage{
			SessionID: client.sessionID, Method: "Fetch.requestPaused", Params: params,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.readMessage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	methods := server.Methods()
	if len(methods) != 2 || methods[0] != "Fetch.continueRequest" || methods[1] != "Fetch.failRequest" ||
		client.allowedRequests != 1 || client.blockedRequests != 1 {
		t.Fatalf("intercepted URL bounds not enforced: methods=%v allowed=%d blocked=%d", methods, client.allowedRequests, client.blockedRequests)
	}
}
