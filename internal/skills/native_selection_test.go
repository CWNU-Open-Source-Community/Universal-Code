package skills

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func nativeSelectionSourceFixture(t *testing.T, component string) (ExternalSelectionSource, []byte) {
	t.Helper()
	body := []byte("---\nname: 证据-review\ndescription: Read selected evidence\n---\nKeep the original native instructions.\n")
	m := BindManifestContent(Manifest{Protocol: ProtocolVersion, Name: "证据-review", Description: "Native instructions",
		Profiles: []domain.Profile{domain.ProfileCode}, Surfaces: []domain.ExecutionSurface{domain.ExecutionSurfaceCode},
		Phases: []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver},
		Roles:  []domain.AgentRole{domain.AgentRoleRoot}, UserInvocable: true, ExplicitOnly: true,
		ToolDependencies: []toolgateway.ToolName{}}, body)
	hash := strings.Repeat("a", 64)
	return ExternalSelectionSource{Manifest: m, Item: ExternalSelectionItem{
		Plugin:         &PluginSkillBinding{Native: true, PackageID: "native-package", ComponentID: component, Revision: hash, Generation: 3},
		InstallationID: "native-installation", InstallationFingerprint: hash,
		Name: m.Name, Version: "", Surface: domain.ExecutionSurfaceCode, ContentSHA256: m.ContentSHA256,
		ContentBytes: m.ContentBytes, TokenUpperBound: m.ContentTokenUpperBound, ArchiveSHA256: hash,
		ArchiveBytes: 65537, PackageFingerprint: hash, TrustClass: PackageTrustOperatorInstalledUntrusted,
	}}, body
}

func TestNativeExternalSelectionUsesExactComponentIdentity(t *testing.T) {
	a, _ := nativeSelectionSourceFixture(t, "skill:a")
	b, _ := nativeSelectionSourceFixture(t, "skill:b")
	request := ResolveExternalSelectionRequest{SelectionID: "native-selection", RunID: "native-run", MissionID: "native-mission",
		ModeSnapshotID: "native-mode", ModeRevision: 1, Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver,
		Profile: domain.ProfileCode, Sources: []ExternalSelectionSource{b, a}, TokenBudget: MaxExternalSelectionTokenBudget,
		RequestedBy: "operator", Confirmed: true, CreatedAt: time.Now().UTC()}
	selected, err := ResolveExternalSelection(request)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Items[0].Plugin.ComponentID != "skill:a" || selected.Items[1].Plugin.ComponentID != "skill:b" ||
		selected.Items[0].Name != "证据-review" || selected.Items[0].Version != "" {
		t.Fatalf("native identity or exact ref ordering changed: %+v", selected.Items)
	}
	ref := ExternalSelectionItemRef(selected.Items[0])
	pin, canonical, err := toolgateway.NormalizeSkillReadPayload(json.RawMessage(ref))
	if err != nil || !pin.Portable() || pin.Resource != "" || string(canonical) != ref || pin.ComponentID != "skill:a" {
		t.Fatalf("selection did not use the canonical skill_read pin: %s %v", ref, err)
	}
	request.Sources = []ExternalSelectionSource{a, a}
	if _, err := ResolveExternalSelection(request); err == nil {
		t.Fatal("duplicate native component was selected")
	}
	changed := CloneExternalSelection(selected)
	changed.Items[0].Plugin.Native = false
	if ExternalSelectionFingerprint(changed) == selected.Fingerprint || changed.Validate() == nil {
		t.Fatal("native binding was interchangeable with a legacy identity")
	}
}

func TestNativeExternalContentKeepsBoundsAndLegacyValidation(t *testing.T) {
	source, body := nativeSelectionSourceFixture(t, "skill:a")
	if err := ValidateExternalSelectionContent(source.Manifest, body, true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExternalSelectionContent(source.Manifest, body, false); err == nil {
		t.Fatal("native identity relaxed legacy manifest validation")
	}
	for _, test := range []struct {
		name string
		body []byte
	}{
		{"oversized", []byte(strings.Repeat("x", MaxExternalSelectionTokenBudget+1))},
		{"invalid-utf8", []byte{0xff}},
		{"control", []byte("guidance\x00injected")},
		{"whitespace", []byte(" \n\t")},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := BindManifestContent(source.Manifest, test.body)
			if err := ValidateExternalSelectionContent(m, test.body, true); err == nil {
				t.Fatal("invalid native body was admitted")
			}
		})
	}
	if err := ValidateExternalSelectionContent(source.Manifest, append(body, 'x'), true); err == nil {
		t.Fatal("changed native bytes were admitted under the original content pin")
	}
	legacy := &PluginSkillBinding{PackageID: "pkg", ComponentID: "skill:legacy", Revision: strings.Repeat("a", 64), Generation: 3}
	raw, _ := json.Marshal(legacy)
	if strings.Contains(string(raw), "native") || len(pluginSelectionFingerprintParts(legacy)) != 5 {
		t.Fatal("old Plugin binding encoding or fingerprint parts changed")
	}
}
