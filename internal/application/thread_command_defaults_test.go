package application_test

import (
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type commandDefaultsInventory struct{}

func (commandDefaultsInventory) InstalledCommandRuntimeAdapters() []commandruntimeadapter.Identity {
	return []commandruntimeadapter.Identity{commandruntimeadapter.HostUnsandboxed("default-command-fixture")}
}

func commandDefaultsTurns(st *store.SQLiteStore, provider *scriptedToolProvider) *application.ThreadTurnService {
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	execution := application.NewRunExecutionHandoffWithRuntime(st, router, policy.NewDefaultChecker(), application.RunRuntimeDependencies{ExecutionCapabilities: capabilities}).WithGeneratedContextCompaction(false)
	return application.NewThreadTurnServiceWithExecutionCapabilities(st, application.NewRunLifecycleControlService(st), execution, capabilities)
}

func TestDesktopCommandDefaultsRespectExplicitSelections(t *testing.T) {
	for _, selection := range []string{"default", "preview", "docker", "sbx", "server_preview", "runtime_missing"} {
		t.Run(selection, func(t *testing.T) {
			st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			if selection == "preview" || selection == "docker" || selection == "sbx" {
				if _, err := application.NewRunExecutionProfileService(st).Change(t.Context(), application.ChangeRunExecutionProfileRequest{
					RunID: run.ID, Profile: selection, OperationKey: "explicit-command-environment-" + selection,
					RequestedBy: "test_operator", Reason: "preserve the operator's advanced environment choice",
				}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := st.GetRunExecutionProfile(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
				textResponse(rootActionResponse(domain.RootActionFinish, "reply", "done", "")),
				textResponse(rootActionResponse(domain.RootActionFinish, "second reply", "done", "")),
			}}
			turns := commandDefaultsTurns(st, provider)
			if selection == "runtime_missing" {
				turns.WithDefaultCommandRuntime(nil)
			} else if selection != "server_preview" {
				turns.WithDefaultCommandRuntime(commandDefaultsInventory{})
			}
			_, err = turns.Execute(t.Context(), input)
			after, getErr := st.GetRunExecutionProfile(t.Context(), run.ID)
			if selection == "docker" || selection == "sbx" || selection == "runtime_missing" {
				if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "environment is unavailable") ||
					len(provider.Requests()) != 0 || getErr != nil || after.ID != before.ID {
					t.Fatalf("missing backend was hidden or replaced by host: before=%+v after=%+v calls=%d err=%v/%v", before, after, len(provider.Requests()), err, getErr)
				}
				return
			}
			want := before.Profile
			if selection == "default" {
				want = domain.RunExecutionProfileLocal
			}
			if err != nil || getErr != nil || after.Profile != want || len(provider.Requests()) != 1 {
				t.Fatalf("wrong effective command profile: %+v want=%s calls=%d err=%v/%v", after, want, len(provider.Requests()), err, getErr)
			}
			if selection == "preview" {
				if after.IsDefaultPreview() {
					t.Fatal("explicit Preview lost its provenance")
				}
				if _, err := application.NewThreadExecutionPermissionService(st, domain.ExecutionPermissionRuntimeCapabilities{}).Change(t.Context(), application.ChangeThreadExecutionPermissionRequest{
					ThreadID: input.ThreadID, Mode: "auto", OperationKey: "explicit-preview-next-policy", RequestedBy: "test_operator", Reason: "create the next permission epoch",
				}); err != nil {
					t.Fatal(err)
				}
				input.OperationKey += "-next-preview"
				result, err := turns.Execute(t.Context(), input)
				continued, getErr := st.GetRunExecutionProfile(t.Context(), result.Submission.Run.ID)
				if err != nil || getErr != nil || result.Submission.Run.ID == run.ID || continued.Profile != domain.RunExecutionProfilePreview || continued.IsDefaultPreview() {
					t.Fatalf("successor lost explicit Preview intent: %+v err=%v/%v", continued, err, getErr)
				}
			}
		})
	}
}

func TestDesktopCommandDefaultsAdvanceLegacyPreviewOnlyAtQuiescentInput(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "active_lease"}[busy], func(t *testing.T) {
			st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
				textResponse(rootActionResponse(domain.RootActionFinish, "historical reply", "done", "")),
				textResponse(rootActionResponse(domain.RootActionFinish, "local reply", "done", "")),
			}}
			turns := commandDefaultsTurns(st, provider)
			if _, err := turns.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetRunExecutionProfile(t.Context(), run.ID)
			if err != nil || !before.IsDefaultPreview() {
				t.Fatal("legacy fixture is not an initial Preview", err)
			}
			turns.WithDefaultCommandRuntime(commandDefaultsInventory{})
			input.OperationKey += "-local-next"
			if busy {
				lease, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "legacy-preview-owner", TTL: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := turns.Execute(t.Context(), input); err == nil || len(provider.Requests()) != 1 {
					t.Fatalf("default migration displaced an active execution: calls=%d err=%v", len(provider.Requests()), err)
				}
				thread, err := st.GetThread(t.Context(), input.ThreadID)
				if err != nil || thread.ActiveRunID != run.ID {
					t.Fatal("blocked migration partially retired the Run", err)
				}
				if _, _, err := st.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
					t.Fatal(err)
				}
			}
			result, err := turns.Execute(t.Context(), input)
			after, getErr := st.GetRunExecutionProfile(t.Context(), result.Submission.Run.ID)
			old, oldErr := st.GetRunExecutionProfile(t.Context(), run.ID)
			if err != nil || getErr != nil || oldErr != nil || result.Submission.Run.ID == run.ID || after.Profile != domain.RunExecutionProfileLocal || old.ID != before.ID || len(provider.Requests()) != 2 {
				t.Fatalf("legacy contract was rewritten or local successor failed: next=%+v old=%+v calls=%d err=%v/%v/%v", after, old, len(provider.Requests()), err, getErr, oldErr)
			}
		})
	}
}
