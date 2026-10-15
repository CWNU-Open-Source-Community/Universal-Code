package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
)

func candidateScope(workspaceID string) application.TaskSkillCandidatesRequest {
	return application.TaskSkillCandidatesRequest{WorkspaceID: workspaceID, Surface: domain.ExecutionSurfaceCode,
		Phase: domain.ExecutionPhaseDeliver, Profile: domain.ProfileCode}
}

func candidateRefs(view application.TaskSkillCandidatesView) []string {
	refs := make([]string, len(view.Items))
	for index, item := range view.Items {
		refs[index] = item.Ref
	}
	return refs
}

func writeTaskNativeSkill(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Task-selected original guidance.\n---\nUse the exact operator-selected evidence.\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTaskSkillCandidatesUseNewPluginWriterAndSelectNativeExactPin(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "task-skills.db"))
	defer st.Close()
	workspace := saveRunCreationWorkspace(t, st)
	legacy, _ := explicitPluginFixture(t, st, "task-user-only", false)
	nativeRoot := filepath.Join(t.TempDir(), "native-task")
	writeTaskNativeSkill(t, nativeRoot, "native-task")
	native := importPortableFixture(t, st, nativeRoot, "task-native-import")
	pin, _ := json.Marshal(installedReadPin(native))
	legacyRef := "task-user-only@1.0.0"
	view, err := application.ListTaskSkillCandidates(t.Context(), st, candidateScope(workspace.ID))
	if err != nil || view.CapabilityGrant || !slices.Equal(candidateRefs(view), []string{legacyRef, string(pin)}) {
		t.Fatalf("new Plugin candidates=%+v err=%v", view, err)
	}
	for _, item := range view.Items {
		if item.Ref == string(pin) && (item.Name != "native-task" || item.Version != "") {
			t.Fatalf("native metadata fabricated a version: %+v", item)
		}
	}
	old, err := st.ListInstalledPackages(t.Context(), "", "", true)
	if err != nil || len(old) != 0 {
		t.Fatalf("new imports wrote old installation rows: %+v %v", old, err)
	}
	run := createExplicitRun(t, st, "tool-loop")
	request := application.ProjectSkillSelectionRequest{RunID: run.ID, Refs: []string{string(pin), legacyRef},
		OperationKey: "task-skills-mixed", RequestedBy: "operator", ConfirmUntrustedContext: true}
	selected, err := application.SelectInstalledSkills(t.Context(), st, request)
	if err != nil || selected.ExternalSelectionID == "" || selected.CapabilityGrant {
		t.Fatalf("mixed installed selection=%+v err=%v", selected, err)
	}
	selection, found, err := st.GetExternalSkillSelectionByRun(t.Context(), run.ID)
	if err != nil || !found || len(selection.Items) != 2 || selection.Items[0].InstallationID != legacy.ID ||
		selection.Items[1].InstallationID != native.ID || selection.Items[1].Plugin == nil || !selection.Items[1].Plugin.Native ||
		selection.Items[1].Plugin.Generation != native.Generation || skills.ExternalSelectionItemRef(selection.Items[1]) != string(pin) {
		t.Fatalf("exact component binding=%+v found=%t err=%v", selection, found, err)
	}
	replay, err := application.SelectInstalledSkills(t.Context(), st, request)
	if err != nil || !replay.Replayed || replay.ExternalSelectionID != selected.ExternalSelectionID {
		t.Fatalf("native replay=%+v err=%v", replay, err)
	}
	current, err := st.GetRun(t.Context(), run.ID)
	if err != nil || current.Status != domain.RunCreated {
		t.Fatalf("candidate/selection started the Run: %+v %v", current, err)
	}
}

type taskCandidateStore struct {
	*store.SQLiteStore
	brokenID      string
	completeLimit bool
	duplicate     *plugins.Installation
	pin           *skills.CatalogPin
}

func (s taskCandidateStore) LoadPluginObject(ctx context.Context, id string) ([]byte, error) {
	if id == s.brokenID {
		return nil, errors.New("retained object fixture is missing")
	}
	return s.SQLiteStore.LoadPluginObject(ctx, id)
}

func (s taskCandidateStore) ListPluginInstallations(ctx context.Context, id string, limit int) ([]plugins.Installation, error) {
	if s.completeLimit {
		return make([]plugins.Installation, limit), nil
	}
	values, err := s.SQLiteStore.ListPluginInstallations(ctx, id, limit)
	if s.duplicate != nil {
		values = append(values, *s.duplicate)
	}
	return values, err
}

func (s taskCandidateStore) ActiveSkillCatalogPin(ctx context.Context, name string, surface domain.ExecutionSurface) (skills.CatalogPin, bool, error) {
	if s.pin != nil && s.pin.SkillName == name && s.pin.Surface == surface {
		return *s.pin, true, nil
	}
	return s.SQLiteStore.ActiveSkillCatalogPin(ctx, name, surface)
}

func TestTaskSkillCandidatesFilterModeCatalogLifecycleAndBadObjects(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "task-skill-eligibility.db"))
	defer st.Close()
	workspace := saveRunCreationWorkspace(t, st)
	available, _ := explicitPluginFixture(t, st, "available-task", false)
	broken, _ := explicitPluginFixture(t, st, "broken-task", false)
	disabled, _ := explicitPluginFixture(t, st, "disabled-task", false)
	reviewInstalledFixture(t, st, disabled, plugins.ReviewDisable)
	revoked, _ := explicitPluginFixture(t, st, "revoked-task", false)
	reviewInstalledFixture(t, st, revoked, plugins.ReviewRevoke)
	scope := candidateScope(workspace.ID)
	view, err := application.ListTaskSkillCandidates(t.Context(), taskCandidateStore{SQLiteStore: st, brokenID: broken.ID}, scope)
	if err != nil || !slices.Equal(candidateRefs(view), []string{"available-task@1.0.0"}) || view.UnavailableCount != 1 {
		t.Fatalf("partial object/lifecycle inventory=%+v err=%v", view, err)
	}
	for _, mismatch := range []string{"plan", "profile", "surface"} {
		request := scope
		switch mismatch {
		case "plan":
			request.Phase = domain.ExecutionPhasePlan
		case "profile":
			request.Profile = domain.ProfileReview
		case "surface":
			request.Surface, request.Profile = domain.ExecutionSurfaceCyber, domain.ProfileScript
		}
		view, err := application.ListTaskSkillCandidates(t.Context(), st, request)
		if err != nil || len(view.Items) != 0 {
			t.Fatalf("%s mismatch offered candidates=%+v err=%v", mismatch, view, err)
		}
	}
	for _, pin := range []skills.CatalogPin{
		{SkillName: "available-task", Surface: domain.ExecutionSurfaceCode, Version: "1.0.0", Enabled: false},
		{SkillName: "available-task", Surface: domain.ExecutionSurfaceCode, Version: "2.0.0", Enabled: true},
	} {
		wrapped := taskCandidateStore{SQLiteStore: st, pin: &pin, brokenID: broken.ID}
		view, err := application.ListTaskSkillCandidates(t.Context(), wrapped, scope)
		if err != nil || len(view.Items) != 0 {
			t.Fatalf("catalog-pinned candidate leaked=%+v err=%v", view, err)
		}
		run := createExplicitRun(t, st, "tool-loop")
		_, err = application.SelectInstalledSkills(t.Context(), wrapped, application.ProjectSkillSelectionRequest{
			RunID: run.ID, Refs: []string{"available-task@1.0.0"}, OperationKey: "denied-" + run.ID, RequestedBy: "operator", ConfirmUntrustedContext: true})
		if apperror.CodeOf(err) != apperror.CodePolicyDenied {
			t.Fatalf("catalog candidate/selection admission drift: %v", err)
		}
	}
	view, err = application.ListTaskSkillCandidates(t.Context(), taskCandidateStore{SQLiteStore: st, duplicate: &available, brokenID: broken.ID}, scope)
	if err != nil || len(view.Items) != 0 || view.UnavailableCount != 2 {
		t.Fatalf("ambiguous reference was substituted: %+v %v", view, err)
	}
	_, err = application.ListTaskSkillCandidates(t.Context(), taskCandidateStore{SQLiteStore: st, completeLimit: true}, scope)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("partial Plugin inventory accepted: %v", err)
	}
}

func TestTaskSkillSelectionRejectsNoncanonicalAndStaleNativePins(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "task-native-pins.db"))
	defer st.Close()
	root := filepath.Join(t.TempDir(), "native-pins")
	writeTaskNativeSkill(t, root, "native-pins")
	native := importPortableFixture(t, st, root, "native-pins-import")
	pin := installedReadPin(native)
	raw, _ := json.Marshal(pin)
	for _, ref := range []string{" " + string(raw), strings.Replace(string(raw), `"package_id":`, `"extra":true,"package_id":`, 1),
		strings.Replace(string(raw), `"revision":`, `"resource":"assets/file","revision":`, 1)} {
		run := createExplicitRun(t, st, "tool-loop")
		_, err := application.SelectInstalledSkills(t.Context(), st, application.ProjectSkillSelectionRequest{
			RunID: run.ID, Refs: []string{ref}, OperationKey: "bad-pin-" + run.ID, RequestedBy: "operator", ConfirmUntrustedContext: true})
		if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("noncanonical native pin accepted: %s err=%v", ref, err)
		}
	}
	native = reviewInstalledFixture(t, st, native, plugins.ReviewDisable)
	native = reviewInstalledFixture(t, st, native, plugins.ReviewEnable)
	run := createExplicitRun(t, st, "tool-loop")
	request := application.ProjectSkillSelectionRequest{RunID: run.ID, Refs: []string{string(raw)}, OperationKey: "stale-pin-selection", RequestedBy: "operator", ConfirmUntrustedContext: true}
	if _, err := application.SelectInstalledSkills(t.Context(), st, request); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("old installation generation was silently renewed: %v", err)
	}
	request.ConfirmUntrustedContext = false
	if _, err := application.SelectInstalledSkills(t.Context(), st, request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("native untrusted confirmation bypassed: %v", err)
	}
}
