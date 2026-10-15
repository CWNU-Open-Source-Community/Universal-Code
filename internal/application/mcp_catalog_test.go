package application

import (
	"context"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
)

type catalogAuthorityClient struct {
	exactPermissionMCPClient
	check   func(context.Context) error
	revoked <-chan struct{}
}

func (c *catalogAuthorityClient) RefreshCapabilities(ctx context.Context, run, workspace string, check func(context.Context) error, revoked <-chan struct{}) (mcp.ScopedCapabilities, error) {
	c.check, c.revoked = check, revoked
	return c.capabilities, check(ctx)
}
func TestSupervisorCatalogUsesCurrentTaskAuthority(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
		t.Run(string(mode), func(t *testing.T) {
			state, run, _, lease, _ := newMCPApprovalModeRuntime(t, t.Context(), mode)
			permission, err := state.GetRunExecutionPermission(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			authority := domain.NewExecutionPermissionRuntimeAuthority()
			caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: authority}
			if mode == domain.RunExecutionPermissionFull {
				if _, err := authority.ActivateRunFullAccess(permission); err != nil {
					t.Fatal(err)
				}
			}
			client := &catalogAuthorityClient{}
			client.capabilities = mcp.ScopedCapabilities{ProtocolVersion: mcp.ClientProtocolVersion, Generation: strings.Repeat("a", 64)}
			runner := NewAgentRunner(state, nil, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithMCPClient(client)
			turn, err := state.BeginSupervisorTurn(t.Context(), lease, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.supervisorMCPCapabilities(t.Context(), turn, permission); err != nil {
				t.Fatal(err)
			}
			if client.check == nil || client.check(t.Context()) != nil {
				t.Fatal("catalog did not receive live task authority")
			}
			authority.RevokeRun(run.ID)
			select {
			case <-client.revoked:
			default:
				t.Fatal("revoked task retained catalog stream")
			}
			if client.check(t.Context()) == nil {
				t.Fatal("catalog worker retained revoked authority")
			}
		})
	}
}
