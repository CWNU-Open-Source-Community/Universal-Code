package skills

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"cyberagent-workbench/internal/domain"
)

func TestRegistryAssemblesDeterministicSelectionBoundContext(t *testing.T) {
	registry, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	selection, err := registry.ResolveSelection(ResolveSelectionRequest{
		SelectionID: "selection-context-1", RunID: "run-context-1",
		MissionID: "mission-context-1", Profile: domain.ProfileCode,
		Names: []string{"code"}, TokenBudget: DefaultSelectionTokenBudget,
		RequestedBy: "operator", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.AssembleContext(selection)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.AssembleContext(selection)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint || first.ItemCount != 1 ||
		first.Items[0].Name != "code" || first.TokenUpperBound != len(first.Items[0].Content) ||
		!strings.Contains(first.Items[0].Content, "Code workflow") {
		t.Fatalf("assembled Skill context drifted: %#v", first)
	}
	if request := first.Preparation("agent-root", "attempt-root", 2); request.ContextFingerprint != first.Fingerprint ||
		request.SelectionFingerprint != selection.Fingerprint || request.TokenBudget != selection.TokenBudget {
		t.Fatalf("preparation provenance drifted: %#v", request)
	}

	tampered := first
	tampered.Items = append([]ContextItem(nil), first.Items...)
	tampered.Items[0].Content += " changed"
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "content is invalid") {
		t.Fatalf("tampered delivery validation error = %v", err)
	}

	drifted := CloneSelection(selection)
	drifted.Items[0].Version = "1.0.1"
	drifted.Fingerprint = SelectionFingerprint(drifted)
	if err := drifted.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.AssembleContext(drifted); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("pinned Registry drift error = %v", err)
	}
}

func TestBuiltinRegistryRetainsArchivedPinnedContext(t *testing.T) {
	registry, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	selection, err := registry.ResolveSelection(ResolveSelectionRequest{
		SelectionID: "selection-archived", RunID: "run-archived",
		MissionID: "mission-archived", Profile: domain.ProfileCode,
		Names: []string{"code"}, TokenBudget: DefaultSelectionTokenBudget,
		RequestedBy: "operator", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	archived := registry.versions["code"]["1.0.0"].manifest
	selection.Items[0].Version = archived.Version
	selection.Items[0].ContentSHA256 = archived.ContentSHA256
	selection.Items[0].ContentBytes = archived.ContentBytes
	selection.Items[0].TokenUpperBound = archived.ContentTokenUpperBound
	selection.TokenUpperBound = archived.ContentTokenUpperBound
	selection.Fingerprint = SelectionFingerprint(selection)
	if err := selection.Validate(); err != nil {
		t.Fatal(err)
	}
	assembly, err := registry.AssembleContext(selection)
	if err != nil {
		t.Fatal(err)
	}
	if assembly.Items[0].Version != "1.0.0" ||
		!strings.Contains(assembly.Items[0].Content, "Metadata placeholder") ||
		registry.List(domain.ProfileCode)[0].Version != "1.3.0" {
		t.Fatalf("archived Skill delivery drifted: %#v", assembly)
	}
	if _, found := registry.version("code", "1.1.0"); !found {
		t.Fatal("previous mode-unaware code version was not retained for exact resumption")
	}
	if archivedReview, found := registry.version("review", "1.2.0"); !found ||
		archivedReview.manifest.ContentSHA256 !=
			"b8e55af0094db9844cd47fa4db459897a8200ec9f39fe9f31aa74316113563c3" {
		t.Fatal("previous review version was not retained for exact resumption")
	}
	if archivedReview, found := registry.version("review", "1.3.0"); !found ||
		archivedReview.manifest.ContentSHA256 !=
			"0e0b45f5ea9499721dbff485e502ae332a96ed781e11c5a9b652776f3360f9a4" {
		t.Fatal("previous full-diff review version was not retained for exact resumption")
	}
	if archivedReview, found := registry.version("review", "1.4.0"); !found ||
		archivedReview.manifest.Version != "1.4.0" {
		t.Fatalf("archived review 1.4.0 is unavailable: found=%t value=%#v", found, archivedReview)
	}
	if archivedChecks, found := registry.version("focused-checks", "1.0.0"); !found ||
		archivedChecks.manifest.ContentSHA256 !=
			"fdc0172a255a40c71884acebbad58b41dcb86f1cf8fa7662aec1c6588460c335" {
		t.Fatal("previous focused-checks version was not retained for exact resumption")
	}
	if archivedChecks, found := registry.version("focused-checks", "1.1.0"); !found ||
		archivedChecks.manifest.Version != "1.1.0" {
		t.Fatalf("archived focused-checks 1.1.0 is unavailable: found=%t value=%#v", found, archivedChecks)
	}
}

func TestPlanDeliveryCurrentGuidanceAndPinnedHistory(t *testing.T) {
	registry, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	selection, err := registry.ResolveSelection(ResolveSelectionRequest{
		SelectionID: "selection-plan", RunID: "run-plan", MissionID: "mission-plan",
		Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhasePlan,
		Profile: domain.ProfileCode, Names: []string{"plan-delivery"},
		TokenBudget: DefaultSelectionTokenBudget, RequestedBy: "operator", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mode := ExecutionContext{Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhasePlan, Profile: domain.ProfileCode, Role: domain.AgentRoleRoot}
	current, err := registry.AssembleContextFor(selection, mode)
	if err != nil || len(current.Items) != 1 {
		t.Fatal("current plan guidance unavailable", err)
	}
	manifest, _ := registry.Get("plan-delivery")
	if manifest.Version != "1.3.0" || strings.Contains(manifest.Description, "Three-direction") ||
		!strings.Contains(current.Items[0].Content, "submit one to three tailored directions") ||
		!strings.Contains(current.Items[0].Content, "Prefer one direction for a clear, bounded task") ||
		!strings.Contains(current.Items[0].Content, "actual proposed directions") ||
		strings.Contains(current.Items[0].Content, "direction 1, 2, or 3") {
		t.Fatal("current Plan Skill contradicts proportionate proposals")
	}
	archived := registry.versions["plan-delivery"]["1.2.0"]
	selection.Items[0].Version = archived.manifest.Version
	selection.Items[0].ContentSHA256 = archived.manifest.ContentSHA256
	selection.Items[0].ContentBytes = archived.manifest.ContentBytes
	selection.Items[0].TokenUpperBound = archived.manifest.ContentTokenUpperBound
	selection.TokenUpperBound = archived.manifest.ContentTokenUpperBound
	selection.Fingerprint = SelectionFingerprint(selection)
	restored, err := registry.AssembleContextFor(selection, mode)
	if err != nil || len(restored.Items) != 1 || restored.Items[0].Content != string(archived.content) ||
		restored.Items[0].SourceSHA256 != "64459bd1e9138b5ca107540c3d498d00b585f303917228a834b88cb208578b6f" ||
		!strings.Contains(restored.Items[0].Content, "submit exactly three tailored directions") {
		t.Fatal("existing Plan Skill pin no longer restores its exact body", err)
	}
}

func TestRegistryRedactsSkillContextBeforeBudgeting(t *testing.T) {
	secret := "s" + "k-" + strings.Repeat("z", 28)
	content := []byte("# Safe workflow\n\nAPI_KEY=" + secret + "\n")
	manifest := fixtureManifest(content)
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := LoadFS(fstest.MapFS{
		"skills/code/manifest.json": &fstest.MapFile{Data: rawManifest},
		"skills/code/SKILL.md":      &fstest.MapFile{Data: content},
	}, "skills")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := registry.ResolveSelection(ResolveSelectionRequest{
		SelectionID: "selection-redaction", RunID: "run-redaction",
		MissionID: "mission-redaction", Profile: domain.ProfileCode,
		Names: []string{"code"}, TokenBudget: DefaultSelectionTokenBudget,
		RequestedBy: "operator", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assembly, err := registry.AssembleContext(selection)
	if err != nil {
		t.Fatal(err)
	}
	if assembly.RedactionCount < 1 || strings.Contains(assembly.Items[0].Content, secret) ||
		!strings.Contains(assembly.Items[0].Content, "[REDACTED:secret]") ||
		assembly.TokenUpperBound > selection.TokenUpperBound {
		t.Fatalf("redacted Skill context is invalid: %#v", assembly)
	}
}
