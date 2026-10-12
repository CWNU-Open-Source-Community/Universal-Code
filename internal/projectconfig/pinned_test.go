package projectconfig

import (
	"encoding/json"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestPinnedProjectFilePolicy(t *testing.T) {
	effective := Effective{Protocol: ProtocolVersion, ExcludePaths: []string{"secrets/**"}}
	raw, _ := json.Marshal(effective)
	config := domain.RunConfig{ProjectConfig: raw, ProjectConfigFingerprint: effective.Fingerprint()}
	policy, err := PathPolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Check("secrets/password.txt"); err == nil {
		t.Fatal("excluded file is visible")
	}
	if err := policy.Check("src/main.go"); err != nil {
		t.Fatal(err)
	}
	if err := policy.Check("src/[id].go"); err != nil {
		t.Fatal(err)
	}
	// Inherited snapshots keep the same policy without consulting a live file.
	successor := config
	effective.ExcludePaths = nil
	raw, _ = json.Marshal(effective)
	config.ProjectConfig = raw
	if _, err := PathPolicy(config); err == nil {
		t.Fatal("changed snapshot accepted with old fingerprint")
	}
	if inherited, err := PathPolicy(successor); err != nil || inherited.Check("secrets/key") == nil {
		t.Fatalf("successor changed its exclusions: %v", err)
	}
	for _, invalid := range []domain.RunConfig{
		{ProjectConfig: raw},
		{ProjectConfigFingerprint: "missing"},
		{ProjectConfig: json.RawMessage(`{`), ProjectConfigFingerprint: "invalid"},
	} {
		if _, err := PathPolicy(invalid); err == nil {
			t.Fatal("invalid pinned binding accepted")
		}
	}
	if empty, err := PathPolicy(domain.RunConfig{}); err != nil || empty.Check("src/main.go") != nil {
		t.Fatalf("absent project config: %v", err)
	}
}
