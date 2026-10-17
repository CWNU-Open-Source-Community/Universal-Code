package skills

import (
	"cyberagent-workbench/internal/domain"
	"strings"
	"testing"
)

func TestModelSkillReadRequiresExactVersionAndCurrentPolicy(t *testing.T) {
	r, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	mode := ExecutionContext{Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Profile: domain.ProfileCode, Role: domain.AgentRoleRoot}
	old := r.versions["code"]["1.2.0"]
	item, err := r.ReadForModel("code", "1.2.0", old.manifest.ContentSHA256, mode)
	if err != nil || item.Content != string(old.content) || item.SourceSHA256 != old.manifest.ContentSHA256 {
		t.Fatalf("old exact body: %+v %v", item, err)
	}
	if _, err := r.ReadForModel("code", "1.2.0", strings.Repeat("0", 64), mode); err == nil {
		t.Fatal("accepted wrong digest")
	}
	for _, name := range []string{"plan-delivery", "loop-monitor", "run-skill-generator"} {
		m, _ := r.Get(name)
		if _, err := r.ReadForModel(m.Name, m.Version, m.ContentSHA256, mode); err == nil {
			t.Fatalf("activated explicit-only %s", name)
		}
	}
	frontend, _ := r.Get("frontend-design")
	if _, err := r.ReadForModel(frontend.Name, frontend.Version, frontend.ContentSHA256, mode); err != nil {
		t.Fatal(err)
	}
	cyber := mode
	cyber.Surface = domain.ExecutionSurfaceCyber
	if _, err := r.ReadForModel(frontend.Name, frontend.Version, frontend.ContentSHA256, cyber); err == nil {
		t.Fatal("delivered frontend guide on Cyber surface")
	}
	current := r.entries["code"]
	current.manifest.ModelInvocable = false
	r.entries["code"] = current
	if _, err := r.ReadForModel("code", "1.2.0", old.manifest.ContentSHA256, mode); err == nil {
		t.Fatal("archive bypassed current invocation policy")
	}
}

func TestNewSkillVersionsRetainPreviousBytes(t *testing.T) {
	r, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ name, version, digest string }{
		{"code", "1.2.0", "279113f96392f4a0a86cf09246f9146ce4bf661892f9f085f6e19c998e3ccd0f"},
		{"run-verify", "1.1.0", "93a5ccb961c1169f7f08e419514a1ab957f1d6a410440acd766e35b45aefbd6a"},
		{"plan-delivery", "1.2.0", "64459bd1e9138b5ca107540c3d498d00b585f303917228a834b88cb208578b6f"},
	} {
		entry, ok := r.version(want.name, want.version)
		if !ok || entry.manifest.ContentSHA256 != want.digest {
			t.Fatalf("previous pin changed: %+v", want)
		}
	}
}
