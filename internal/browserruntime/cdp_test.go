package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCDPRequestsBindAllReadOnlyActionsToExactScopeAndLimits(t *testing.T) {
	session, executable, ownership, _ := profileLifecycleFixture(t)
	expectations := map[CDPAction]struct {
		maxBytes   int
		maxEntries int
	}{
		CDPActionNavigate:       {0, 0},
		CDPActionDOMSnapshot:    {MaxDOMSnapshotBytes, 0},
		CDPActionScreenshot:     {MaxScreenshotBytes, 0},
		CDPActionRequestCapture: {MaxRequestCaptureBytes, MaxCDPCaptureEntries},
	}
	for action, expected := range expectations {
		request, err := BuildCDPRequest(session, executable, ownership,
			"cdp-request-"+string(action), action, "https://EXAMPLE.com/path?q=1")
		if err != nil {
			t.Fatalf("build %s request: %v", action, err)
		}
		if err := ValidateCDPRequest(request, session, executable, ownership); err != nil {
			t.Fatal(err)
		}
		if request.CanonicalURL != "https://example.com:443/path?q=1" ||
			request.MaxPayloadBytes != expected.maxBytes ||
			request.MaxCaptureEntries != expected.maxEntries ||
			request.DeadlineMS != DefaultCDPDeadlineMS ||
			!request.RedirectRevalidationRequired ||
			!request.ResolvedAddressRevalidationRequired ||
			!request.ProductExecutionBlocked || request.Authority != (RuntimeAuthority{}) {
			t.Fatalf("unsafe or incomplete %s request: %#v", action, request)
		}
	}

	if _, err := BuildCDPRequest(session, executable, ownership, "out-of-scope",
		CDPActionNavigate, "https://example.net/"); err == nil {
		t.Fatal("out-of-scope CDP navigation unexpectedly passed")
	}
	if _, err := BuildCDPRequest(session, executable, ownership, "unknown-action",
		CDPAction("evaluate_javascript"), "https://example.com/"); err == nil {
		t.Fatal("unknown CDP action unexpectedly passed")
	}
}

func TestDisabledCDPTransportReturnsMetadataOnlyWithoutAuthority(t *testing.T) {
	session, executable, ownership, _ := profileLifecycleFixture(t)
	request := mustCDPRequest(t, session, executable, ownership, CDPActionNavigate)
	bridge, err := NewCDPBridge(DisabledCDPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := bridge.Execute(context.Background(), session, executable, ownership, request)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != CDPOutcomeDisabled || outcome.FailureCode != CDPFailureDisabled ||
		outcome.Transport != DisabledCDPTransportName || !outcome.Completed ||
		!outcome.DeadlineEnforced || !outcome.ScopeValidated || !outcome.MetadataOnly ||
		outcome.RawPayloadIncluded || outcome.ProcessStarted || outcome.NetworkUsed ||
		outcome.ProfileWritten || outcome.RequestMutationUsed || outcome.RequestReplayUsed ||
		outcome.ArtifactCommitted || outcome.ProductExecutionEnabled ||
		outcome.Authority != (RuntimeAuthority{}) {
		t.Fatalf("unsafe disabled CDP outcome: %#v", outcome)
	}
	assertCDPOutcomeRoundTrip(t, request, outcome)
}

func TestFakeCDPTransportProducesBoundedMetadataForEveryAction(t *testing.T) {
	session, executable, ownership, _ := profileLifecycleFixture(t)
	tests := []struct {
		action    CDPAction
		payload   []byte
		mediaType string
		requests  int
	}{
		{CDPActionNavigate, nil, "", 0},
		{CDPActionDOMSnapshot, []byte(`{"nodes":[]}`),
			"application/vnd.prayu.dom-snapshot+json", 0},
		{CDPActionScreenshot, []byte("\x89PNG\r\n\x1a\nfixture"), "image/png", 0},
		{CDPActionRequestCapture, []byte(`{"requests":[{},{}]}`),
			"application/vnd.prayu.request-capture+json", 2},
	}
	for _, testCase := range tests {
		t.Run(string(testCase.action), func(t *testing.T) {
			request := mustCDPRequest(t, session, executable, ownership, testCase.action)
			transport, err := NewFakeCDPTransport(request, FakeCDPTransportPlan{
				Payload: testCase.payload, MediaType: testCase.mediaType,
				CapturedRequestCount: testCase.requests,
			})
			if err != nil {
				t.Fatal(err)
			}
			bridge, err := NewCDPBridge(transport)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := bridge.Execute(context.Background(), session, executable,
				ownership, request)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != CDPOutcomeSucceeded || outcome.FailureCode != CDPFailureNone ||
				outcome.PayloadBytes != len(testCase.payload) ||
				outcome.MediaType != testCase.mediaType ||
				outcome.CapturedRequestCount != testCase.requests || !outcome.PayloadWithinLimit ||
				!outcome.MetadataOnly || !outcome.Synthetic || outcome.RawPayloadIncluded || outcome.ProcessStarted ||
				outcome.NetworkUsed || outcome.ProfileWritten || outcome.ProductExecutionEnabled ||
				outcome.Authority != (RuntimeAuthority{}) {
				t.Fatalf("unsafe or incomplete fake CDP outcome: %#v", outcome)
			}
			if len(testCase.payload) == 0 {
				if outcome.PayloadSHA256 != "" {
					t.Fatalf("empty payload retained digest %q", outcome.PayloadSHA256)
				}
			} else {
				digest := sha256.Sum256(testCase.payload)
				if outcome.PayloadSHA256 != hex.EncodeToString(digest[:]) {
					t.Fatalf("unexpected payload digest %q", outcome.PayloadSHA256)
				}
			}
			assertCDPOutcomeRoundTrip(t, request, outcome)
		})
	}
}

func TestFakeCDPTransportEnforcesCancellationDeadlineAndResultBounds(t *testing.T) {
	session, executable, ownership, _ := profileLifecycleFixture(t)
	request := mustCDPRequest(t, session, executable, ownership, CDPActionDOMSnapshot)

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledTransport := mustFakeCDPTransport(t, request, FakeCDPTransportPlan{
		Payload:   []byte(`{"nodes":[]}`),
		MediaType: "application/vnd.prayu.dom-snapshot+json",
	})
	cancelled := executeCDP(t, cancelledContext, session, executable, ownership,
		request, cancelledTransport)
	if cancelled.Status != CDPOutcomeCancelled || cancelled.FailureCode != CDPFailureCancelled {
		t.Fatalf("unexpected cancellation outcome: %#v", cancelled)
	}

	timeoutContext, timeoutCancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer timeoutCancel()
	timedTransport := mustFakeCDPTransport(t, request, FakeCDPTransportPlan{
		Payload:   []byte(`{"nodes":[]}`),
		MediaType: "application/vnd.prayu.dom-snapshot+json", Delay: 100 * time.Millisecond,
	})
	timedOut := executeCDP(t, timeoutContext, session, executable, ownership,
		request, timedTransport)
	if timedOut.Status != CDPOutcomeTimedOut || timedOut.FailureCode != CDPFailureDeadline {
		t.Fatalf("unexpected timeout outcome: %#v", timedOut)
	}

	oversizedTransport := mustFakeCDPTransport(t, request, FakeCDPTransportPlan{
		Payload: []byte(strings.Repeat("x", request.MaxPayloadBytes+1)), MediaType: "application/json",
	})
	oversized := executeCDP(t, context.Background(), session, executable, ownership,
		request, oversizedTransport)
	if oversized.Status != CDPOutcomeFailed ||
		oversized.FailureCode != CDPFailurePayloadLimit || oversized.PayloadWithinLimit {
		t.Fatalf("unexpected payload-limit outcome: %#v", oversized)
	}

	invalidTransport := mustFakeCDPTransport(t, request, FakeCDPTransportPlan{
		Payload: []byte("not-json"), MediaType: "application/vnd.prayu.dom-snapshot+json",
	})
	invalid := executeCDP(t, context.Background(), session, executable, ownership,
		request, invalidTransport)
	if invalid.Status != CDPOutcomeFailed || invalid.FailureCode != CDPFailureInvalidResult ||
		!invalid.PayloadWithinLimit {
		t.Fatalf("unexpected invalid-result outcome: %#v", invalid)
	}

	captureRequest := mustCDPRequest(t, session, executable, ownership,
		CDPActionRequestCapture)
	countMismatchTransport := mustFakeCDPTransport(t, captureRequest, FakeCDPTransportPlan{
		Payload:              []byte(`{"requests":[]}`),
		MediaType:            "application/vnd.prayu.request-capture+json",
		CapturedRequestCount: 1,
	})
	countMismatch := executeCDP(t, context.Background(), session, executable, ownership,
		captureRequest, countMismatchTransport)
	if countMismatch.Status != CDPOutcomeFailed ||
		countMismatch.FailureCode != CDPFailureInvalidResult {
		t.Fatalf("capture count mismatch unexpectedly passed: %#v", countMismatch)
	}

	screenshotRequest := mustCDPRequest(t, session, executable, ownership,
		CDPActionScreenshot)
	badImageTransport := mustFakeCDPTransport(t, screenshotRequest, FakeCDPTransportPlan{
		Payload: []byte("not-a-png"), MediaType: "image/png",
	})
	badImage := executeCDP(t, context.Background(), session, executable, ownership,
		screenshotRequest, badImageTransport)
	if badImage.Status != CDPOutcomeFailed || badImage.FailureCode != CDPFailureInvalidResult {
		t.Fatalf("invalid screenshot bytes unexpectedly passed: %#v", badImage)
	}

	crashedTransport := mustFakeCDPTransport(t, request, FakeCDPTransportPlan{Crash: true})
	crashed := executeCDP(t, context.Background(), session, executable, ownership,
		request, crashedTransport)
	if crashed.Status != CDPOutcomeFailed || crashed.FailureCode != CDPFailureTransport ||
		crashed.PayloadBytes != 0 {
		t.Fatalf("unexpected transport-failure outcome: %#v", crashed)
	}
}

func TestCDPContractsRejectTamperingSchemaWideningAndUnsealedTransport(t *testing.T) {
	session, executable, ownership, _ := profileLifecycleFixture(t)
	request := mustCDPRequest(t, session, executable, ownership, CDPActionScreenshot)
	requestMutations := []func(*CDPRequest){
		func(value *CDPRequest) { value.CanonicalURL = "https://example.net/" },
		func(value *CDPRequest) { value.MaxPayloadBytes++ },
		func(value *CDPRequest) { value.ProductExecutionBlocked = false },
		func(value *CDPRequest) { value.Authority.NetworkAccess = true },
		func(value *CDPRequest) { value.Fingerprint = strings.Repeat("a", 64) },
	}
	for index, mutate := range requestMutations {
		candidate := request
		mutate(&candidate)
		if err := ValidateCDPRequest(candidate, session, executable, ownership); err == nil {
			t.Fatalf("CDP request mutation %d unexpectedly passed", index)
		}
	}

	transport := mustFakeCDPTransport(t, request, FakeCDPTransportPlan{
		Payload: []byte("image"), MediaType: "image/png",
	})
	outcome := executeCDP(t, context.Background(), session, executable, ownership,
		request, transport)
	outcomeMutations := []func(*CDPOutcome){
		func(value *CDPOutcome) { value.NetworkUsed = true },
		func(value *CDPOutcome) { value.ProcessStarted = true },
		func(value *CDPOutcome) { value.RawPayloadIncluded = true },
		func(value *CDPOutcome) { value.Authority.ArtifactCommit = true },
		func(value *CDPOutcome) { value.Fingerprint = strings.Repeat("b", 64) },
	}
	for index, mutate := range outcomeMutations {
		candidate := outcome
		mutate(&candidate)
		if err := ValidateCDPOutcome(request, candidate); err == nil {
			t.Fatalf("CDP outcome mutation %d unexpectedly passed", index)
		}
	}

	encoded, err := EncodeCDPOutcome(request, outcome)
	if err != nil {
		t.Fatal(err)
	}
	widened := append([]byte(nil), encoded[:len(encoded)-1]...)
	widened = append(widened, []byte(`,"future":true}`)...)
	if _, err := DecodeCDPOutcome(request, widened); err == nil {
		t.Fatal("CDP outcome schema widening unexpectedly passed")
	}
	var decodedMap map[string]any
	if err := json.Unmarshal(encoded, &decodedMap); err != nil {
		t.Fatal(err)
	}
	pretty, err := json.MarshalIndent(decodedMap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCDPOutcome(request, pretty); err == nil {
		t.Fatal("non-canonical CDP outcome unexpectedly passed")
	}

	if _, err := NewCDPBridge(rogueCDPTransport{}); err == nil {
		t.Fatal("unadmitted package transport unexpectedly passed")
	}
}

func TestBrowserRuntimeProductionPrimitivesStayInAuditedAdapters(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]map[string]bool{
		`"github.com/gorilla/websocket"`: {"restricted_cdp_transport.go": true},
		`"net/http"`:                     {"network_probe_windows.go": true},
		"websocket.Dialer":               {"restricted_cdp_transport.go": true},
		"windows.CreateProcess":          {"browser_process_windows.go": true},
		"os.Remove": {
			"profile_materializer.go":  true,
			"network_probe_windows.go": true,
		},
		"os.RemoveAll": {
			"profile_materializer.go":  true,
			"network_probe_windows.go": true,
		},
		"os.Mkdir": {
			"profile_materializer.go":          true,
			"network_probe_windows.go":         true,
			"agent_browser_platform_darwin.go": true,
		},
		"os.Rename": {
			"profile_materializer.go": true,
			"profile_path_other.go":   true,
		},
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(".", entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for primitive, files := range allowed {
			if strings.Contains(string(raw), primitive) && !files[entry.Name()] {
				t.Fatalf("browser runtime primitive %q escaped audited adapter into %s",
					primitive, entry.Name())
			}
		}
		for _, forbidden := range []string{`"os/exec"`,
			"exec.Command", "http.Client", "os.MkdirAll"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("browser runtime production source contains forbidden adapter %q in %s",
					forbidden, entry.Name())
			}
		}
	}
}

func mustCDPRequest(t *testing.T, session SessionPlan,
	executable BrowserExecutableIdentity, ownership ProfileOwnershipPlan,
	action CDPAction,
) CDPRequest {
	t.Helper()
	request, err := BuildCDPRequest(session, executable, ownership,
		"cdp-fixture-"+string(action), action, "https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustFakeCDPTransport(t *testing.T, request CDPRequest,
	plan FakeCDPTransportPlan,
) *FakeCDPTransport {
	t.Helper()
	transport, err := NewFakeCDPTransport(request, plan)
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func executeCDP(t *testing.T, ctx context.Context, session SessionPlan,
	executable BrowserExecutableIdentity, ownership ProfileOwnershipPlan,
	request CDPRequest, transport CDPTransport,
) CDPOutcome {
	t.Helper()
	bridge, err := NewCDPBridge(transport)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := bridge.Execute(ctx, session, executable, ownership, request)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func assertCDPOutcomeRoundTrip(t *testing.T, request CDPRequest, outcome CDPOutcome) {
	t.Helper()
	encoded, err := EncodeCDPOutcome(request, outcome)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCDPOutcome(request, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != outcome {
		t.Fatalf("CDP outcome round trip changed metadata:\nwant %#v\ngot  %#v", outcome, decoded)
	}
}

type rogueCDPTransport struct{}

func (rogueCDPTransport) browserCDPTransport() {}
func (rogueCDPTransport) name() string         { return "rogue" }
func (rogueCDPTransport) exchange(context.Context, CDPRequest) (cdpExchange, error) {
	return cdpExchange{}, nil
}
