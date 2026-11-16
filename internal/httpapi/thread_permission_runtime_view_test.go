package httpapi

import (
	"cyberagent-workbench/internal/domain"
	"testing"
	"time"
)

func TestThreadPermissionHistoryDoesNotProjectRuntimeAuthority(t *testing.T) {
	now := time.Now().UTC()
	base, err := domain.NewInitialThreadExecutionPermissionSnapshot("view-initial", domain.Thread{ID: "view-thread", MissionID: "view-mission"}, "operator", now)
	if err != nil {
		t.Fatal(err)
	}
	runtime := domain.NewExecutionPermissionRuntimeAuthority()
	caps := domain.ExecutionPermissionRuntimeCapabilities{WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: runtime}
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionConservative, domain.RunExecutionPermissionWorkspaceAccess, domain.RunExecutionPermissionApproval, domain.RunExecutionPermissionFullAccess, domain.RunExecutionPermissionDebug} {
		old, err := base.Next("view-retained", mode, mode != domain.RunExecutionPermissionConservative, "operator", "retained history", now)
		if err != nil {
			t.Fatal(err)
		}
		got := threadExecutionPermissionView(old, caps)
		if got.Mode != string(mode) || got.RuntimeGateAvailable || got.ExecutionAuthorized || got.CapabilityGrant {
			t.Fatalf("historical view gained authority: %+v", got)
		}
	}
	full, err := base.Next("view-full", domain.RunExecutionPermissionFull, true, "operator", "current Full", now)
	if err != nil {
		t.Fatal(err)
	}
	if got := threadExecutionPermissionView(full, caps); got.RuntimeGateAvailable {
		t.Fatal("cold Full projected live")
	}
	if _, err := runtime.ActivateThreadFullAccess(full, nil); err != nil {
		t.Fatal(err)
	}
	if got := threadExecutionPermissionView(full, caps); !got.RuntimeGateAvailable {
		t.Fatal("live Full projected unavailable")
	}
	runtime.RevokeThread(full.ThreadID)
	if got := threadExecutionPermissionView(full, caps); got.RuntimeGateAvailable {
		t.Fatal("revoked Full projected live")
	}
}
