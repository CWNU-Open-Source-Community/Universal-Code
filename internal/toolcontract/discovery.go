package toolcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"
)

// DiscoveryScope authorizes a bounded protocol operation, not arbitrary calls.
// Profile is the preferred MCP protocol revision, equal to the first entry in
// the fingerprint-bound ResolvedLaunch.ProtocolVersions, NOT the negotiated
// revision or a plugin format version. Methods is the exact outbound subset.
// A 2026 profile can include legacy handshake methods for explicit fallback;
// B must still validate every actual initialize / per-request _meta version
// against that launch's allowed versions before sending. This method whitelist
// does not authorize a protocol downgrade. Inbound progress uses no SEND budget.
type DiscoveryScope struct {
	OperationID           string
	ConnectionFingerprint string // final endpoint/launch, credential revision, instance
	Profile               string
	Methods               []string
	MaxRequests           int
	MaxBytes              int64 // total outgoing wire bytes, not a per-request allowance
	ExpiresAt             time.Time
}

func (s DiscoveryScope) Validate() error {
	methods := discoveryProfileMethods(s.Profile)
	if !normalized(s.OperationID, 256) || !digest(s.ConnectionFingerprint) ||
		len(methods) == 0 || len(s.Methods) == 0 || len(s.Methods) > len(methods) ||
		s.MaxRequests < 1 || s.MaxRequests > 1024 || s.MaxBytes < 1 || s.MaxBytes > 16*1024*1024 ||
		s.ExpiresAt.IsZero() {
		return errors.New("discovery scope identity or bounds are invalid")
	}
	seen := make(map[string]bool, len(s.Methods))
	for _, method := range s.Methods {
		if seen[method] || !slices.Contains(methods, method) {
			return errors.New("discovery scope contains an invalid or repeated method")
		}
		seen[method] = true
	}
	return nil
}

func discoveryProfileMethods(profile string) []string {
	legacy := []string{"initialize", "notifications/initialized", "tools/list",
		"resources/list", "resources/templates/list", "prompts/list", "ping"}
	switch profile {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
		return legacy
	case "2026-07-28":
		// The modern lifecycle uses server/discover and per-request metadata.
		// initialize/initialized and ping are only legacy fallback traffic;
		// retaining them in this maximum set does not make them modern RPCs.
		// See https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning
		return append(legacy, "server/discover", "subscriptions/listen")
	default:
		return nil
	}
}

func FingerprintDiscovery(scope DiscoveryScope) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	scope.Methods = slices.Clone(scope.Methods)
	slices.Sort(scope.Methods)
	scope.ExpiresAt = scope.ExpiresAt.UTC()
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", errors.New("could not encode discovery scope")
	}
	sum := sha256.Sum256(append([]byte("toolcontract.discovery.v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

type DiscoverySend struct {
	ConnectionFingerprint string
	Method                string // extracted from the ACTUAL outbound protocol frame
	Bytes                 int64  // actual encoded outgoing wire size
}

type DiscoverySendGuard func(context.Context, DiscoverySend) error
type BeginDiscoveryGuard func(context.Context, DiscoveryScope) (DiscoverySendGuard, error)

// ExecutionGuards is injected by C; B checks each real boundary. BeforeConnect
// gates BOTH stdio spawn and the first HTTP connection/authentication egress.
// BeginDiscovery consumes its operation once and returns a separate per-send
// check; SDK negotiation/pagination must not repeatedly consume DispatchGuard.
// BeforeToolCall has its own ID and runs immediately before the actual send.
// Nil at a traversed boundary is denial. Redirect/reconnect/credential drift or
// extra SDK approval/retry traffic cannot silently expand any of these scopes.
type ExecutionGuards struct {
	BeforeConnect  DispatchGuard
	BeginDiscovery BeginDiscoveryGuard
	BeforeToolCall DispatchGuard
}

// NewDiscoverySendGuard bounds an ALREADY authorized discovery. Recheck must
// validate current host authority without consuming its one-shot approval again.
// It must not reenter this guard. B invokes the result immediately before EACH
// outbound request/notification, validates parameters against Profile, and never
// substitutes a guessed method/size for the actual encoded message. This helper
// alone provides neither approval nor OS/network isolation.
func NewDiscoverySendGuard(scope DiscoveryScope,
	recheck func(context.Context) error,
) (DiscoverySendGuard, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if recheck == nil || !time.Now().Before(scope.ExpiresAt) {
		return nil, errors.New("discovery requires current authority and an unexpired scope")
	}
	methods := slices.Clone(scope.Methods)
	var mu sync.Mutex
	requests, bytes := 0, int64(0)
	return func(ctx context.Context, send DiscoverySend) error {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(scope.ExpiresAt) || send.ConnectionFingerprint != scope.ConnectionFingerprint ||
			!slices.Contains(methods, send.Method) || send.Bytes <= 0 ||
			requests >= scope.MaxRequests || send.Bytes > scope.MaxBytes-bytes {
			return errors.New("discovery send exceeds its bound connection, methods, or budget")
		}
		if err := recheck(ctx); err != nil {
			return err
		}
		// A check that blocked must not revive a cancelled/expired operation.
		if err := ctx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(scope.ExpiresAt) {
			return errors.New("discovery expired during authority verification")
		}
		requests++
		bytes += send.Bytes
		return nil
	}, nil
}
