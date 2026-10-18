package domain

import (
	"encoding/json"
	"testing"
)

func TestWorkerProfilePreservesExplicitEmptyAndInheritedTools(t *testing.T) {
	for _, tt := range []struct {
		raw                       string
		enabled, inherits, allows bool
	}{
		{`null`, false, false, false},
		{`{}`, true, true, true},
		{`{"tools":null}`, true, true, true},
		{`{"tools":[]}`, true, false, false},
		{`{"tools":["command_runtime"]}`, true, false, true},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			var p *WorkerProfile
			if err := json.Unmarshal([]byte(tt.raw), &p); err != nil {
				t.Fatal(err)
			}
			n, err := NormalizeWorkerProfile(p)
			if err != nil {
				t.Fatal(err)
			}
			if (n != nil) != tt.enabled || (n != nil && n.Tools == nil) != tt.inherits || n.AllowsTool("command_runtime") != tt.allows {
				t.Fatalf("profile semantics changed: %+v", n)
			}
			raw, err := WorkerProfileJSON(n)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip *WorkerProfile
			if err = json.Unmarshal([]byte(raw), &roundtrip); err != nil || !EqualWorkerProfiles(n, roundtrip) {
				t.Fatalf("profile roundtrip changed: %s %v", raw, err)
			}
		})
	}
	profile, err := NormalizeWorkerProfile(&WorkerProfile{Tools: []string{"workspace_read", " command_runtime ", "workspace_read"}})
	if err != nil || len(profile.Tools) != 2 || profile.Tools[0] != "command_runtime" {
		t.Fatalf("normalization failed: %+v %v", profile, err)
	}
	if (&WorkerProfile{Tools: []string{"command_runtime", "workspace_read"}}).AllowsTool("mcp_tool_call") {
		t.Fatal("explicit tools widened")
	}
}

func TestSpecialistApprovalIdentityIncludesChildAndAttempt(t *testing.T) {
	a := AgentAttemptRef{RunID: "run", AgentID: "first", AttemptID: "attempt"}
	b := AgentAttemptRef{RunID: "run", AgentID: "second", AttemptID: "attempt"}
	if SpecialistToolApprovalID(a, "call") == SpecialistToolApprovalID(b, "call") {
		t.Fatal("child call IDs collided")
	}
	c := SpecialistToolCall{Ref: a, CallID: "call", Name: "command_runtime", PayloadJSON: `{}`, AuthorityJSON: `{"generation":1}`}
	before := SpecialistToolApprovalFingerprint(c)
	c.AuthorityJSON = `{"generation":2}`
	if before == SpecialistToolApprovalFingerprint(c) {
		t.Fatal("changed authority retained approval fingerprint")
	}
}

func TestWorkerProfileHistoryScopeIsExplicitAndImmutable(t *testing.T) {
	legacy := &WorkerProfile{}
	raw, err := WorkerProfileJSON(legacy)
	if err != nil || raw != `{"tools":null}` || legacy.EffectiveHistoryScope() != WorkerHistoryDelegated {
		t.Fatalf("legacy worker profile changed: %s %v", raw, err)
	}
	for _, scope := range []string{WorkerHistoryDelegated, WorkerHistoryParentSnapshot} {
		profile, err := NormalizeWorkerProfile(&WorkerProfile{HistoryScope: scope})
		if err != nil || profile.EffectiveHistoryScope() != scope || EqualWorkerProfiles(profile, legacy) {
			t.Fatalf("scope was lost from delegation identity: %+v %v", profile, err)
		}
	}
	if _, err := NormalizeWorkerProfile(&WorkerProfile{HistoryScope: "all_agents"}); err == nil {
		t.Fatal("unknown history scope accepted")
	}
}
