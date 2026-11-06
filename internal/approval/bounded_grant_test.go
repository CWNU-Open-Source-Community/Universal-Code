package approval

import (
	"strings"
	"testing"
	"time"
)

func boundedGrantFixture() SessionGrant {
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(10 * time.Minute)
	return SessionGrant{
		ID: "grant-test", RunID: "run-test", SessionID: "session-test", WorkspaceID: "workspace-test",
		ToolName: "command_runtime", ActionClass: "command_process", Status: GrantActive,
		RequestFingerprint: Fingerprint("request"),
		ScopeFingerprint:   Fingerprint("scope"),
		Generation:         1, MaxUses: 2, UsesRemaining: 1, ExpiresAt: &expires,
		ModeSnapshotID: "mode-test", ModeRevision: 1,
		InteractionSnapshotID: "interaction-test", InteractionRevision: 1,
		ExecutionProfileSnapshotID: "profile-test", ExecutionProfileRevision: 1,
		PermissionSnapshotID: "permission-test", PermissionRevision: 1,
		PermissionMode:           "ask",
		WorkspaceRootFingerprint: Fingerprint("root"),
		CapabilityGeneration:     Fingerprint("capability"),
		Reason:                   "bounded escalation", GrantedBy: "operator",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func legacyGrantFixture() SessionGrant {
	now := time.Now().UTC().Truncate(time.Second)
	return SessionGrant{
		ID: "grant-legacy", RunID: "run-test", SessionID: "session-test", WorkspaceID: "workspace-test",
		ToolName: "shell", ActionClass: "shell", Status: GrantActive,
		RequestFingerprint: Fingerprint("request"),
		Reason:             "legacy grant", GrantedBy: "operator",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func boundedGrantQuery(g SessionGrant) GrantQuery {
	return GrantQuery{
		RunID: g.RunID, SessionID: g.SessionID, WorkspaceID: g.WorkspaceID,
		ToolName: g.ToolName, ActionClass: g.ActionClass, ScopeFingerprint: g.ScopeFingerprint,
		ModeSnapshotID: g.ModeSnapshotID, ModeRevision: g.ModeRevision,
		InteractionSnapshotID: g.InteractionSnapshotID, InteractionRevision: g.InteractionRevision,
		ExecutionProfileSnapshotID: g.ExecutionProfileSnapshotID, ExecutionProfileRevision: g.ExecutionProfileRevision,
		PermissionSnapshotID: g.PermissionSnapshotID, PermissionRevision: g.PermissionRevision,
		PermissionMode:           g.PermissionMode,
		WorkspaceRootFingerprint: g.WorkspaceRootFingerprint,
		CapabilityGeneration:     g.CapabilityGeneration,
	}
}

func approvedRecordFixture(g SessionGrant) Record {
	decided := g.CreatedAt.Add(time.Second)
	return Record{
		ID: "approval-test", IdempotencyKey: "proposal:command_runtime:proposal-test",
		ProposalID: "proposal-test", GrantID: g.ID, RunID: g.RunID, SessionID: g.SessionID,
		WorkspaceID: g.WorkspaceID, ToolName: g.ToolName, ActionClass: g.ActionClass,
		Mode: "ask", Status: StatusApproved, RequestFingerprint: Fingerprint("request"),
		RequestedBy: "agent", ReviewedBy: "operator", Version: 1,
		CreatedAt: g.CreatedAt, UpdatedAt: decided, DecidedAt: &decided,
	}
}

func grantConsumptionFixture(g SessionGrant, record Record, ordinal int) GrantConsumption {
	consumption := GrantConsumption{
		ID: "consumption-test", GrantID: g.ID, ProposalID: record.ProposalID,
		ApprovalID: record.ID, RunID: g.RunID, ScopeFingerprint: g.ScopeFingerprint,
		GrantGeneration: g.Generation, UseOrdinal: ordinal,
		CreatedAt: g.CreatedAt.Add(time.Second),
	}
	consumption.Fingerprint = GrantConsumptionFingerprint(consumption)
	return consumption
}

func TestBoundedGrantPermission(t *testing.T) {
	cases := []struct {
		name          string
		tool          string
		action        string
		mode          string
		wantPermitted bool
	}{
		{"historical workspace access permits any tool", "shell", "shell", "workspace_access", true},
		{"command process ask", "command_runtime", "command_process", "ask", true},
		{"command process auto", "command_runtime", "command_process", "auto", true},
		{"command process full", "command_runtime", "command_process", "full", true},
		{"wrong tool", "shell", "command_process", "ask", false},
		{"wrong action", "command_runtime", "shell", "ask", false},
		{"unknown mode", "command_runtime", "command_process", "readonly", false},
		{"empty mode", "command_runtime", "command_process", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundedGrantPermission(tc.tool, tc.action, tc.mode); got != tc.wantPermitted {
				t.Fatalf("boundedGrantPermission(%q, %q, %q) = %v, want %v",
					tc.tool, tc.action, tc.mode, got, tc.wantPermitted)
			}
		})
	}
}

func TestMatchesBoundedGrantScopeRequiresEveryBinding(t *testing.T) {
	mutations := map[string]func(*GrantQuery){
		"run id":                     func(q *GrantQuery) { q.RunID = "run-other" },
		"session id":                 func(q *GrantQuery) { q.SessionID = "session-other" },
		"workspace id":               func(q *GrantQuery) { q.WorkspaceID = "workspace-other" },
		"tool name":                  func(q *GrantQuery) { q.ToolName = "shell" },
		"action class":               func(q *GrantQuery) { q.ActionClass = "shell" },
		"scope fingerprint":          func(q *GrantQuery) { q.ScopeFingerprint = Fingerprint("other-scope") },
		"mode snapshot":              func(q *GrantQuery) { q.ModeSnapshotID = "mode-other" },
		"mode revision":              func(q *GrantQuery) { q.ModeRevision = 2 },
		"interaction snapshot":       func(q *GrantQuery) { q.InteractionSnapshotID = "interaction-other" },
		"interaction revision":       func(q *GrantQuery) { q.InteractionRevision = 2 },
		"execution profile snapshot": func(q *GrantQuery) { q.ExecutionProfileSnapshotID = "profile-other" },
		"execution profile revision": func(q *GrantQuery) { q.ExecutionProfileRevision = 2 },
		"permission snapshot":        func(q *GrantQuery) { q.PermissionSnapshotID = "permission-other" },
		"permission revision":        func(q *GrantQuery) { q.PermissionRevision = 2 },
		"permission mode":            func(q *GrantQuery) { q.PermissionMode = "full" },
		"workspace root fingerprint": func(q *GrantQuery) { q.WorkspaceRootFingerprint = Fingerprint("other-root") },
		"capability generation":      func(q *GrantQuery) { q.CapabilityGeneration = Fingerprint("other-capability") },
	}
	for name, mutate := range mutations {
		t.Run("rejects changed "+name, func(t *testing.T) {
			grant := boundedGrantFixture()
			query := boundedGrantQuery(grant)
			mutate(&query)
			if MatchesBoundedGrantScope(grant, query) {
				t.Fatalf("scope match accepted a changed %s", name)
			}
		})
	}
	t.Run("accepts identical bindings", func(t *testing.T) {
		grant := boundedGrantFixture()
		if !MatchesBoundedGrantScope(grant, boundedGrantQuery(grant)) {
			t.Fatal("scope match rejected an identical query")
		}
	})
	t.Run("rejects unbounded grant", func(t *testing.T) {
		grant := legacyGrantFixture()
		if MatchesBoundedGrantScope(grant, boundedGrantQuery(grant)) {
			t.Fatal("scope match accepted a grant without a scope fingerprint")
		}
	})
}

func TestCheckBoundedConsumption(t *testing.T) {
	happy := func() (SessionGrant, GrantConsumption, Record, GrantQuery) {
		grant := boundedGrantFixture()
		record := approvedRecordFixture(grant)
		consumption := grantConsumptionFixture(grant, record, 1)
		return grant, consumption, record, boundedGrantQuery(grant)
	}
	now := func(g SessionGrant) time.Time { return g.CreatedAt.Add(2 * time.Second) }

	t.Run("accepts a consistent first use", func(t *testing.T) {
		grant, consumption, record, query := happy()
		if err := CheckBoundedConsumption(grant, consumption, record, query, false, now(grant)); err != nil {
			t.Fatalf("consistent consumption rejected: %v", err)
		}
	})

	t.Run("accepts the exhausting final use of a revoked grant", func(t *testing.T) {
		grant, consumption, record, query := happy()
		revokedAt := grant.CreatedAt.Add(1500 * time.Millisecond)
		grant.Status = GrantRevoked
		grant.UsesRemaining = 0
		grant.RevokedAt = &revokedAt
		grant.RevokedBy = "approval_store"
		grant.RevocationReason = "bounded grant uses exhausted"
		grant.UpdatedAt = revokedAt
		record = approvedRecordFixture(grant)
		consumption = grantConsumptionFixture(grant, record, grant.MaxUses)
		if err := CheckBoundedConsumption(grant, consumption, record, query, false, now(grant)); err != nil {
			t.Fatalf("final legal use of an exhausted grant rejected: %v", err)
		}
	})

	rejections := []struct {
		name   string
		mutate func(*SessionGrant, *GrantConsumption, *Record, *GrantQuery, *bool, *time.Time)
	}{
		{"invalid grant fingerprint", func(g *SessionGrant, _ *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			g.RequestFingerprint = "not-a-fingerprint"
		}},
		{"consumption bound to another proposal", func(_ *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.ProposalID = "proposal-other"
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"consumption bound to another approval", func(_ *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.ApprovalID = "approval-other"
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"consumption bound to another run", func(_ *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.RunID = "run-other"
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"consumption bound to another scope", func(_ *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.ScopeFingerprint = Fingerprint("other-scope")
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"consumption bound to another generation", func(_ *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.GrantGeneration = 99
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"consumption fingerprint tampered", func(_ *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.Fingerprint = Fingerprint("tampered")
		}},
		{"record still pending", func(_ *SessionGrant, _ *GrantConsumption, r *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			r.Status = StatusPending
			r.DecidedAt = nil
			r.ReviewedBy = ""
			r.GrantID = ""
		}},
		{"record bound to another grant", func(_ *SessionGrant, _ *GrantConsumption, r *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			r.GrantID = "grant-other"
		}},
		{"query changed tool", func(g *SessionGrant, _ *GrantConsumption, _ *Record, q *GrantQuery, _ *bool, _ *time.Time) {
			q.ToolName = "shell"
		}},
		{"query changed scope", func(g *SessionGrant, _ *GrantConsumption, _ *Record, q *GrantQuery, _ *bool, _ *time.Time) {
			q.ScopeFingerprint = Fingerprint("other-scope")
		}},
		{"explicitly revoked", func(_ *SessionGrant, _ *GrantConsumption, _ *Record, _ *GrantQuery, revoked *bool, _ *time.Time) {
			*revoked = true
		}},
		{"grant expired at validation time", func(g *SessionGrant, _ *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, at *time.Time) {
			*at = g.ExpiresAt.Add(time.Second)
		}},
		{"grant expires exactly at validation time", func(g *SessionGrant, _ *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, at *time.Time) {
			*at = *g.ExpiresAt
		}},
		{"consumption after expiry", func(g *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.CreatedAt = g.ExpiresAt.Add(time.Second)
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"consumption created exactly at expiry", func(g *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.CreatedAt = *g.ExpiresAt
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"consumption before grant creation", func(g *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.CreatedAt = g.CreatedAt.Add(-time.Second)
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"ordinal beyond max uses", func(g *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.UseOrdinal = g.MaxUses + 1
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"ordinal beyond remaining uses", func(g *SessionGrant, c *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			c.UseOrdinal = g.MaxUses - g.UsesRemaining + 1
			c.Fingerprint = GrantConsumptionFingerprint(*c)
		}},
		{"revoked before exhaustion", func(g *SessionGrant, _ *GrantConsumption, _ *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			revokedAt := g.CreatedAt.Add(time.Second)
			g.Status = GrantRevoked
			g.RevokedAt = &revokedAt
			g.RevokedBy = "operator"
			g.RevocationReason = "changed my mind"
			g.UsesRemaining = 1
			g.UpdatedAt = revokedAt
		}},
		{"revoked exhausted by the wrong revoker", func(g *SessionGrant, c *GrantConsumption, r *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			revokedAt := g.CreatedAt.Add(1500 * time.Millisecond)
			g.Status = GrantRevoked
			g.UsesRemaining = 0
			g.RevokedAt = &revokedAt
			g.RevokedBy = "operator"
			g.RevocationReason = "bounded grant uses exhausted"
			g.UpdatedAt = revokedAt
			*c = grantConsumptionFixture(*g, *r, g.MaxUses)
		}},
		{"revoked exhausted with the wrong reason", func(g *SessionGrant, c *GrantConsumption, r *Record, _ *GrantQuery, _ *bool, _ *time.Time) {
			revokedAt := g.CreatedAt.Add(1500 * time.Millisecond)
			g.Status = GrantRevoked
			g.UsesRemaining = 0
			g.RevokedAt = &revokedAt
			g.RevokedBy = "approval_store"
			g.RevocationReason = "manual revoke"
			g.UpdatedAt = revokedAt
			*c = grantConsumptionFixture(*g, *r, g.MaxUses)
		}},
	}
	for _, tc := range rejections {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			grant, consumption, record, query := happy()
			explicitlyRevoked := false
			at := now(grant)
			tc.mutate(&grant, &consumption, &record, &query, &explicitlyRevoked, &at)
			if err := CheckBoundedConsumption(grant, consumption, record, query, explicitlyRevoked, at); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestSessionGrantValidateBoundedMatrix(t *testing.T) {
	t.Run("accepts a bounded grant", func(t *testing.T) {
		if err := boundedGrantFixture().Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("accepts a legacy grant", func(t *testing.T) {
		if err := legacyGrantFixture().Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("accepts max uses at the ceiling of eight", func(t *testing.T) {
		grant := boundedGrantFixture()
		grant.MaxUses = 8
		grant.UsesRemaining = 8
		if err := grant.Validate(); err != nil {
			t.Fatalf("max uses at the ceiling was rejected: %v", err)
		}
	})
	t.Run("accepts expiry at the fifteen minute ceiling", func(t *testing.T) {
		grant := boundedGrantFixture()
		at := grant.CreatedAt.Add(15 * time.Minute)
		grant.ExpiresAt = &at
		if err := grant.Validate(); err != nil {
			t.Fatalf("expiry at the ceiling was rejected: %v", err)
		}
	})
	rejections := []struct {
		name   string
		mutate func(*SessionGrant)
	}{
		{"untrimmed identity", func(g *SessionGrant) { g.ID = " grant-test" }},
		{"identity beyond 256 runes", func(g *SessionGrant) { g.SessionID = strings.Repeat("长", 257) }},
		{"request fingerprint not sha-256 hex", func(g *SessionGrant) { g.RequestFingerprint = "abc" }},
		{"scope fingerprint not sha-256 hex", func(g *SessionGrant) { g.ScopeFingerprint = strings.ToUpper(Fingerprint("scope")) }},
		{"max uses above eight", func(g *SessionGrant) { g.MaxUses = 9; g.UsesRemaining = 8 }},
		{"max uses not positive", func(g *SessionGrant) { g.MaxUses = 0; g.UsesRemaining = 0 }},
		{"uses remaining above max uses", func(g *SessionGrant) { g.UsesRemaining = g.MaxUses + 1 }},
		{"missing expiry", func(g *SessionGrant) { g.ExpiresAt = nil }},
		{"expiry at creation", func(g *SessionGrant) { at := g.CreatedAt; g.ExpiresAt = &at }},
		{"expiry beyond fifteen minutes", func(g *SessionGrant) {
			at := g.CreatedAt.Add(15*time.Minute + time.Second)
			g.ExpiresAt = &at
		}},
		{"zero mode revision", func(g *SessionGrant) { g.ModeRevision = 0 }},
		{"empty permission snapshot", func(g *SessionGrant) { g.PermissionSnapshotID = "" }},
		{"permission outside bounded allowlist", func(g *SessionGrant) { g.PermissionMode = "readonly" }},
		{"legacy grant carrying bounded fields", func(g *SessionGrant) {
			legacy := legacyGrantFixture()
			*g = legacy
			g.MaxUses = 1
		}},
		{"active grant with revoker", func(g *SessionGrant) { g.RevokedBy = "operator" }},
		{"revoked grant without revoker", func(g *SessionGrant) {
			revokedAt := g.CreatedAt.Add(time.Second)
			g.Status = GrantRevoked
			g.RevokedAt = &revokedAt
			g.UpdatedAt = revokedAt
		}},
		{"revoked before creation", func(g *SessionGrant) {
			revokedAt := g.CreatedAt.Add(-time.Second)
			g.Status = GrantRevoked
			g.RevokedAt = &revokedAt
			g.RevokedBy = "operator"
			g.UpdatedAt = g.CreatedAt
		}},
		{"unknown status", func(g *SessionGrant) { g.Status = "expired" }},
		{"zero version", func(g *SessionGrant) { g.Version = 0 }},
		{"updated before created", func(g *SessionGrant) { g.UpdatedAt = g.CreatedAt.Add(-time.Second) }},
	}
	for _, tc := range rejections {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			grant := boundedGrantFixture()
			tc.mutate(&grant)
			if err := grant.Validate(); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestCreateGrantRequestNormalize(t *testing.T) {
	boundedRequest := func() CreateGrantRequest {
		return CreateGrantRequest{
			SessionID: " session-test ", WorkspaceID: "workspace-test",
			ToolName: "command_runtime", ActionClass: "command_process",
			Reason: "bounded escalation", GrantedBy: "operator", IdempotencyKey: "grant-op-1",
			ScopeFingerprint: strings.ToUpper(Fingerprint("scope")),
			Generation:       1, MaxUses: 2, TTL: 10 * time.Minute,
			ModeSnapshotID: "mode-test", ModeRevision: 1,
			InteractionSnapshotID: "interaction-test", InteractionRevision: 1,
			ExecutionProfileSnapshotID: "profile-test", ExecutionProfileRevision: 1,
			PermissionSnapshotID: "permission-test", PermissionRevision: 1,
			PermissionMode:           "ask",
			WorkspaceRootFingerprint: strings.ToUpper(Fingerprint("root")),
			CapabilityGeneration:     strings.ToUpper(Fingerprint("capability")),
		}
	}

	t.Run("trims identities and lowercases fingerprints", func(t *testing.T) {
		normalized, err := boundedRequest().Normalize()
		if err != nil {
			t.Fatal(err)
		}
		if normalized.SessionID != "session-test" {
			t.Fatalf("session id was not trimmed: %q", normalized.SessionID)
		}
		if normalized.ScopeFingerprint != strings.ToLower(normalized.ScopeFingerprint) {
			t.Fatalf("scope fingerprint was not lowercased: %q", normalized.ScopeFingerprint)
		}
	})

	t.Run("accepts the fifteen minute ttl ceiling", func(t *testing.T) {
		request := boundedRequest()
		request.TTL = 15 * time.Minute
		if _, err := request.Normalize(); err != nil {
			t.Fatalf("ttl at the ceiling was rejected: %v", err)
		}
	})

	t.Run("accepts a legacy request", func(t *testing.T) {
		normalized, err := CreateGrantRequest{
			SessionID: "session-test", WorkspaceID: "workspace-test", ToolName: "shell",
			ActionClass: "shell", Reason: "trusted build", GrantedBy: "operator",
			IdempotencyKey: "grant-op-2",
		}.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		if normalized.ScopeFingerprint != "" || normalized.MaxUses != 0 {
			t.Fatal("legacy request gained bounded fields during normalization")
		}
	})

	rejections := []struct {
		name   string
		mutate func(*CreateGrantRequest)
	}{
		{"missing idempotency key", func(r *CreateGrantRequest) { r.IdempotencyKey = "" }},
		{"missing grantor", func(r *CreateGrantRequest) { r.GrantedBy = "" }},
		{"max uses above eight", func(r *CreateGrantRequest) { r.MaxUses = 9 }},
		{"zero ttl", func(r *CreateGrantRequest) { r.TTL = 0 }},
		{"ttl above fifteen minutes", func(r *CreateGrantRequest) { r.TTL = 15*time.Minute + time.Second }},
		{"invalid workspace root fingerprint", func(r *CreateGrantRequest) { r.WorkspaceRootFingerprint = "root" }},
		{"permission outside bounded allowlist", func(r *CreateGrantRequest) { r.PermissionMode = "readonly" }},
		{"reason beyond 2048 runes", func(r *CreateGrantRequest) { r.Reason = strings.Repeat("长", 2049) }},
		{"legacy request carrying bounded fields", func(r *CreateGrantRequest) {
			*r = CreateGrantRequest{SessionID: "session-test", ToolName: "shell", ActionClass: "shell",
				GrantedBy: "operator", IdempotencyKey: "grant-op-3", TTL: time.Minute}
		}},
	}
	for _, tc := range rejections {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			request := boundedRequest()
			tc.mutate(&request)
			if _, err := request.Normalize(); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestRevokeGrantRequestNormalize(t *testing.T) {
	t.Run("normalizes a revoke request", func(t *testing.T) {
		normalized, err := RevokeGrantRequest{
			GrantID: " grant-test ", Reason: " no longer needed ", RevokedBy: " operator ",
			IdempotencyKey: "revoke-op-1",
		}.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		if normalized.GrantID != "grant-test" || normalized.RevokedBy != "operator" {
			t.Fatalf("revoke request was not trimmed: %+v", normalized)
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*RevokeGrantRequest)
	}{
		{"empty grant id", func(r *RevokeGrantRequest) { r.GrantID = "" }},
		{"empty revoker", func(r *RevokeGrantRequest) { r.RevokedBy = "" }},
		{"empty idempotency key", func(r *RevokeGrantRequest) { r.IdempotencyKey = "" }},
		{"reason beyond 2048 runes", func(r *RevokeGrantRequest) { r.Reason = strings.Repeat("长", 2049) }},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			request := RevokeGrantRequest{GrantID: "grant-test", RevokedBy: "operator", IdempotencyKey: "revoke-op-2"}
			tc.mutate(&request)
			if _, err := request.Normalize(); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestGrantConsumptionValidate(t *testing.T) {
	grant := boundedGrantFixture()
	record := approvedRecordFixture(grant)
	t.Run("accepts a consistent consumption", func(t *testing.T) {
		if err := grantConsumptionFixture(grant, record, 1).Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejects a wrong binding fingerprint", func(t *testing.T) {
		consumption := grantConsumptionFixture(grant, record, 1)
		consumption.Fingerprint = Fingerprint("tampered")
		if err := consumption.Validate(); err == nil {
			t.Fatal("expected tampered consumption fingerprint to be rejected")
		}
	})
	t.Run("rejects an empty identity", func(t *testing.T) {
		consumption := grantConsumptionFixture(grant, record, 1)
		consumption.ApprovalID = ""
		consumption.Fingerprint = GrantConsumptionFingerprint(consumption)
		if err := consumption.Validate(); err == nil {
			t.Fatal("expected empty approval id to be rejected")
		}
	})
	t.Run("rejects a zero ordinal", func(t *testing.T) {
		consumption := grantConsumptionFixture(grant, record, 0)
		if err := consumption.Validate(); err == nil {
			t.Fatal("expected zero use ordinal to be rejected")
		}
	})
}

func TestGrantConsumptionFingerprintBindsEveryField(t *testing.T) {
	grant := boundedGrantFixture()
	record := approvedRecordFixture(grant)
	base := grantConsumptionFixture(grant, record, 1)
	baseFingerprint := GrantConsumptionFingerprint(base)
	mutations := map[string]func(*GrantConsumption){
		"id":                func(c *GrantConsumption) { c.ID = "consumption-other" },
		"grant id":          func(c *GrantConsumption) { c.GrantID = "grant-other" },
		"proposal id":       func(c *GrantConsumption) { c.ProposalID = "proposal-other" },
		"approval id":       func(c *GrantConsumption) { c.ApprovalID = "approval-other" },
		"run id":            func(c *GrantConsumption) { c.RunID = "run-other" },
		"scope fingerprint": func(c *GrantConsumption) { c.ScopeFingerprint = Fingerprint("other-scope") },
		"grant generation":  func(c *GrantConsumption) { c.GrantGeneration = 9 },
		"use ordinal":       func(c *GrantConsumption) { c.UseOrdinal = 2 },
		"created at":        func(c *GrantConsumption) { c.CreatedAt = c.CreatedAt.Add(time.Second) },
	}
	for name, mutate := range mutations {
		t.Run("binds "+name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if GrantConsumptionFingerprint(changed) == baseFingerprint {
				t.Fatalf("consumption fingerprint ignored the %s", name)
			}
		})
	}
}

func TestComputerUseAppScopeFingerprintAndActionClass(t *testing.T) {
	scope := ComputerUseAppScope{
		RunID: "run-test", SessionID: "session-test", WorkspaceID: "workspace-test",
		AppID: "app-test", PermissionSnapshotID: "permission-test", PermissionRevision: 1,
		PermissionMode: "ask", PermissionGeneration: 7, PermissionRuntimeEpoch: "epoch-test",
		RunAuthorizationFence: 9, ServerID: "server-test", DescriptorFingerprint: Fingerprint("descriptor"),
		ServerGeneration: 3, CapabilityFingerprint: Fingerprint("capability"),
	}
	if scope.Fingerprint() != scope.Fingerprint() {
		t.Fatal("scope fingerprint is not deterministic")
	}
	if len(scope.Fingerprint()) != 64 {
		t.Fatalf("unexpected scope fingerprint length: %d", len(scope.Fingerprint()))
	}
	scopeDrifts := map[string]func(*ComputerUseAppScope){
		"run id":                   func(s *ComputerUseAppScope) { s.RunID = "run-other" },
		"session id":               func(s *ComputerUseAppScope) { s.SessionID = "session-other" },
		"workspace id":             func(s *ComputerUseAppScope) { s.WorkspaceID = "workspace-other" },
		"app id":                   func(s *ComputerUseAppScope) { s.AppID = "app-other" },
		"permission snapshot":      func(s *ComputerUseAppScope) { s.PermissionSnapshotID = "permission-other" },
		"permission revision":      func(s *ComputerUseAppScope) { s.PermissionRevision = 2 },
		"permission mode":          func(s *ComputerUseAppScope) { s.PermissionMode = "full" },
		"permission generation":    func(s *ComputerUseAppScope) { s.PermissionGeneration = 8 },
		"permission runtime epoch": func(s *ComputerUseAppScope) { s.PermissionRuntimeEpoch = "epoch-other" },
		"run authorization fence":  func(s *ComputerUseAppScope) { s.RunAuthorizationFence = 10 },
		"server id":                func(s *ComputerUseAppScope) { s.ServerID = "server-other" },
		"descriptor fingerprint":   func(s *ComputerUseAppScope) { s.DescriptorFingerprint = Fingerprint("other-descriptor") },
		"server generation":        func(s *ComputerUseAppScope) { s.ServerGeneration = 4 },
		"capability fingerprint":   func(s *ComputerUseAppScope) { s.CapabilityFingerprint = Fingerprint("other-capability") },
	}
	for name, mutate := range scopeDrifts {
		t.Run("scope fingerprint binds "+name, func(t *testing.T) {
			changed := scope
			mutate(&changed)
			if changed.Fingerprint() == scope.Fingerprint() {
				t.Fatalf("scope fingerprint ignored the %s", name)
			}
		})
	}
	if !strings.HasPrefix(scope.GrantActionClass(), "computer_use_app:") ||
		scope.GrantActionClass() != "computer_use_app:"+scope.Fingerprint() {
		t.Fatalf("grant action class did not bind the scope fingerprint: %q", scope.GrantActionClass())
	}
}

func TestIdempotencyKeyHelpers(t *testing.T) {
	if got := ProposalIdempotencyKey(" shell ", " proposal-1 "); got != "proposal:shell:proposal-1" {
		t.Fatalf("unexpected proposal idempotency key: %q", got)
	}
	if got := ReviewIdempotencyKey(" shell ", " proposal-1 ", ActionDeny); got != "review:shell:proposal-1:deny" {
		t.Fatalf("unexpected review idempotency key: %q", got)
	}
	if ProposalIdempotencyKey("shell", "p1") == ProposalIdempotencyKey("shell", "p2") {
		t.Fatal("proposal idempotency key did not bind the proposal id")
	}
	if ReviewIdempotencyKey("shell", "p1", ActionApprove) == ReviewIdempotencyKey("shell", "p1", ActionDeny) {
		t.Fatal("review idempotency key did not bind the action")
	}
}

func TestRequestFingerprintHelpersBindTheirArguments(t *testing.T) {
	if ShellFingerprint("s", "w", "echo hi") != ShellFingerprint("s", "w", "echo hi") {
		t.Fatal("shell fingerprint is not deterministic")
	}
	if ShellFingerprint("s", "w", "echo hi") == ShellFingerprint("s", "w", "echo bye") {
		t.Fatal("shell fingerprint ignored the command")
	}
	if FileEditFingerprint("s", "w", "main.go", "hash-1") == FileEditFingerprint("s", "w", "main.go", "hash-2") {
		t.Fatal("file edit fingerprint ignored the proposed hash")
	}
	if FileMutationFingerprint("move_file", "s", "w", "move", "a", "b", "h1", "h2", "h3", "h4") !=
		FileMutationFingerprint("move_file", "s", "w", "move", "a", "b", "h1", "h2", "h3", "h4") {
		t.Fatal("file mutation fingerprint is not deterministic")
	}
	if FileMutationFingerprint("move_file", "s", "w", "move", "a", "b", "h1", "h2", "h3", "h4") ==
		FileMutationFingerprint("move_file", "s", "w", "move", "a", "c", "h1", "h2", "h3", "h4") {
		t.Fatal("file mutation fingerprint ignored the destination path")
	}
	revocation := RevokeGrantRequest{GrantID: "grant-test", Reason: "unused", RevokedBy: "operator"}
	if GrantRevocationFingerprint(revocation) != GrantRevocationFingerprint(revocation) {
		t.Fatal("revocation fingerprint is not deterministic")
	}
	tampered := revocation
	tampered.Reason = "changed"
	if GrantRevocationFingerprint(revocation) == GrantRevocationFingerprint(tampered) {
		t.Fatal("revocation fingerprint ignored the reason")
	}
	if GrantOperationKeyDigest(" key-1 ") != GrantOperationKeyDigest("key-1") {
		t.Fatal("operation key digest did not normalize whitespace")
	}
}

func TestRecordFingerprintBindsDecisionState(t *testing.T) {
	grant := boundedGrantFixture()
	record := approvedRecordFixture(grant)
	first := RecordFingerprint(record)
	if first != RecordFingerprint(record) {
		t.Fatal("record fingerprint is not deterministic")
	}
	mutations := map[string]func(*Record){
		"grant id":    func(r *Record) { r.GrantID = "grant-other" },
		"status":      func(r *Record) { r.Status = StatusDenied },
		"decided at":  func(r *Record) { at := r.DecidedAt.Add(time.Second); r.DecidedAt = &at },
		"reviewed by": func(r *Record) { r.ReviewedBy = "reviewer-other" },
	}
	for name, mutate := range mutations {
		t.Run("binds "+name, func(t *testing.T) {
			changed := record
			mutate(&changed)
			if first == RecordFingerprint(changed) {
				t.Fatalf("record fingerprint ignored the %s", name)
			}
		})
	}
}

func TestRecordValidateDecisionStates(t *testing.T) {
	grant := boundedGrantFixture()
	base := approvedRecordFixture(grant)
	t.Run("accepts a decided denial without a grant", func(t *testing.T) {
		denied := base
		denied.Status = StatusDenied
		denied.GrantID = ""
		denied.DecisionReason = "unsafe command"
		if err := denied.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("accepts a pending record and rejects each decision field independently", func(t *testing.T) {
		pending := base
		pending.Status = StatusPending
		pending.DecidedAt = nil
		pending.ReviewedBy = ""
		pending.GrantID = ""
		if err := pending.Validate(); err != nil {
			t.Fatalf("valid pending record rejected: %v", err)
		}
		for name, mutate := range map[string]func(*Record){
			"decided at":    func(r *Record) { r.DecidedAt = base.DecidedAt },
			"reviewer":      func(r *Record) { r.ReviewedBy = "operator" },
			"session grant": func(r *Record) { r.GrantID = grant.ID },
		} {
			t.Run("rejects pending with only "+name, func(t *testing.T) {
				record := pending
				mutate(&record)
				if err := record.Validate(); err == nil {
					t.Fatalf("pending record with only %s was accepted", name)
				}
			})
		}
	})
	rejections := []struct {
		name   string
		mutate func(*Record)
	}{
		{"untrimmed id", func(r *Record) { r.ID = " approval-test" }},
		{"identity beyond 256 runes", func(r *Record) { r.ProposalID = strings.Repeat("长", 257) }},
		{"request fingerprint not sha-256 hex", func(r *Record) { r.RequestFingerprint = "abc" }},
		{"unknown status", func(r *Record) { r.Status = "deferred" }},
		{"decision reason beyond 2048 runes", func(r *Record) { r.DecisionReason = strings.Repeat("长", 2049) }},
		{"zero version", func(r *Record) { r.Version = 0 }},
		{"updated before created", func(r *Record) { r.UpdatedAt = r.CreatedAt.Add(-time.Second) }},
		{"decided without reviewer", func(r *Record) { r.ReviewedBy = "" }},
		{"decided before creation", func(r *Record) {
			before := r.CreatedAt.Add(-time.Second)
			r.DecidedAt = &before
		}},
		{"grant attached to a denial", func(r *Record) { r.Status = StatusDenied }},
	}
	for _, tc := range rejections {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			record := base
			tc.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestDecisionRequestNormalizeDenyPath(t *testing.T) {
	t.Run("accepts a denial with a reason", func(t *testing.T) {
		normalized, err := DecisionRequest{
			ProposalID: "proposal-test", IdempotencyKey: "review:shell:proposal-test:deny",
			Action: ActionDeny, Reason: "unsafe command", ReviewedBy: "operator",
		}.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		if normalized.Reason != "unsafe command" {
			t.Fatalf("deny reason was dropped: %q", normalized.Reason)
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*DecisionRequest)
	}{
		{"approve with a denial reason", func(r *DecisionRequest) {
			r.Action = ActionApprove
			r.Reason = "unsafe command"
		}},
		{"unknown action", func(r *DecisionRequest) { r.Action = "defer" }},
		{"missing reviewer", func(r *DecisionRequest) { r.ReviewedBy = "" }},
		{"missing idempotency key", func(r *DecisionRequest) { r.IdempotencyKey = "" }},
		{"reviewer beyond 256 runes", func(r *DecisionRequest) { r.ReviewedBy = strings.Repeat("长", 257) }},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			request := DecisionRequest{
				ProposalID: "proposal-test", IdempotencyKey: "review:shell:proposal-test:approve",
				Action: ActionApprove, ReviewedBy: "operator",
			}
			tc.mutate(&request)
			if _, err := request.Normalize(); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}
