package toolcontract

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestDiscoveryProfileBindsModernProbeAndLegacyFallback(t *testing.T) {
	for _, profile := range []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"} {
		t.Run(profile, func(t *testing.T) {
			scope := discovery()
			scope.Profile = profile
			scope.Methods = []string{"initialize", "notifications/initialized", "tools/list",
				"resources/list", "resources/templates/list", "prompts/list", "ping"}
			if err := scope.Validate(); err != nil {
				t.Fatal(err)
			}
			scope.Methods = append(scope.Methods, "server/discover")
			err := scope.Validate()
			if (err == nil) != (profile == "2026-07-28") {
				t.Fatalf("probe profile=%s err=%v", profile, err)
			}
		})
	}
	for _, profile := range []string{"", "mcp-test-v1", "plugin.v1", "2026-07-29", "2026-07-28 ", "2025-01-01"} {
		scope := discovery()
		scope.Profile = profile
		if scope.Validate() == nil {
			t.Fatalf("unknown/ambiguous profile accepted: %q", profile)
		}
	}
}

func TestModernDiscoveryDoesNotAdmitCallsReadsOrUnboundCancellation(t *testing.T) {
	for _, method := range []string{"tools/call", "resources/read", "prompts/get",
		"notifications/subscriptions/acknowledged", "logging/setLevel", "sampling/createMessage",
		"elicitation/create", "notifications/cancelled", "extra/approval"} {
		scope := discovery()
		scope.Profile = "2026-07-28"
		scope.Methods = []string{"server/discover", method}
		if scope.Validate() == nil {
			t.Fatalf("non-discovery method accepted: %s", method)
		}
	}
	scope := discovery()
	scope.Profile = "2026-07-28"
	scope.Methods = []string{"server/discover", "server/discover"}
	if scope.Validate() == nil {
		t.Fatal("repeated modern probe accepted in method set")
	}
}

func TestModernDiscoveryUsesOneBoundedPermitForActualProbeAndPages(t *testing.T) {
	scope := discovery()
	scope.Profile = "2026-07-28"
	scope.Methods = []string{"server/discover", "tools/list"}
	scope.MaxRequests, scope.MaxBytes = 3, 90
	checks := 0
	guard, err := NewDiscoverySendGuard(scope, func(context.Context) error { checks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	// A maximum profile is not an implicit grant for methods omitted by C.
	if guard(context.Background(), DiscoverySend{testDigest, "initialize", 10}) == nil {
		t.Fatal("unrequested legacy fallback was authorized")
	}
	for _, method := range []string{"server/discover", "tools/list", "tools/list"} {
		if err := guard(context.Background(), DiscoverySend{testDigest, method, 30}); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 3 {
		t.Fatalf("authority checks=%d", checks)
	}
	if guard(context.Background(), DiscoverySend{testDigest, "server/discover", 1}) == nil {
		t.Fatal("probe retry reset the shared request or byte budget")
	}
}

func TestModernDiscoveryRechecksRevocationAndBoundCredentialFingerprint(t *testing.T) {
	scope := discovery()
	scope.Profile = "2026-07-28"
	scope.Methods = []string{"server/discover", "tools/list"}
	launch := httpLaunch()
	launch.ProtocolVersions = []string{scope.Profile}
	key := bytes.Repeat([]byte{7}, 32)
	fingerprint, err := FingerprintLaunch(key, launch)
	if err != nil {
		t.Fatal(err)
	}
	scope.ConnectionFingerprint = fingerprint
	revoked := false
	errRevoked := errors.New("host authority revoked")
	guard, err := NewDiscoverySendGuard(scope, func(context.Context) error {
		if revoked {
			return errRevoked
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard(context.Background(), DiscoverySend{fingerprint, "server/discover", 20}); err != nil {
		t.Fatal(err)
	}
	// The final launch fingerprint includes HTTP credentials and their revision.
	launch.HTTP.Credential.Revision = "revision-2"
	changed, err := FingerprintLaunch(key, launch)
	if err != nil || changed == fingerprint {
		t.Fatalf("credential revision drift missing: %v", err)
	}
	if guard(context.Background(), DiscoverySend{changed, "tools/list", 20}) == nil {
		t.Fatal("changed connection/credential fingerprint accepted")
	}
	revoked = true
	if err := guard(context.Background(), DiscoverySend{fingerprint, "tools/list", 20}); !errors.Is(err, errRevoked) {
		t.Fatalf("revocation lost between discover and list: %v", err)
	}
}
