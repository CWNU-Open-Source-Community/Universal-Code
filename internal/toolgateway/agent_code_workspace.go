package toolgateway

import (
	"context"
	"errors"
	"os"
	"strings"

	"cyberagent-workbench/internal/rootfs"
)

// AgentCodeWorkspaceResolver resolves one Run's filesystem target without
// changing the source workspace used by unrelated control or process tools.
type AgentCodeWorkspaceResolver func(context.Context, string, string) (workspaceID, root string, err error)

func (g *Gateway) WithAgentCodeWorkspaceResolver(resolver AgentCodeWorkspaceResolver) *Gateway {
	if g != nil {
		g.agentCodeWorkspaceResolver = resolver
	}
	return g
}

func (g *Gateway) bindAgentCodeWorkspace(ctx context.Context, call ToolCall) (string, string, error) {
	if g.agentCodeWorkspaceResolver == nil {
		root, err := g.bindWorkspaceRoot(ctx, call.WorkspaceID, call.WorkspaceRoot)
		return call.WorkspaceID, root, err
	}
	workspaceID, root, err := g.agentCodeWorkspaceResolver(ctx, call.RunID, call.WorkspaceID)
	if err != nil {
		return "", "", err
	}
	if !validAgentCodeIdentity(workspaceID) || strings.TrimSpace(root) == "" {
		return "", "", errors.New("Run file workspace resolver returned an invalid target")
	}
	root, err = rootfs.ExactAbsolutePath(root)
	if err != nil {
		return "", "", errors.New("Run file workspace resolver returned an invalid path")
	}
	if call.WorkspaceRoot != "" {
		provided, err := rootfs.ExactAbsolutePath(call.WorkspaceRoot)
		if err != nil {
			return "", "", errors.New("provided file root is invalid")
		}
		expectedInfo, expectedErr := os.Stat(root)
		providedInfo, providedErr := os.Stat(provided)
		if expectedErr != nil || providedErr != nil || !expectedInfo.IsDir() || !providedInfo.IsDir() ||
			!os.SameFile(expectedInfo, providedInfo) {
			return "", "", errors.New("provided file root does not match this Run's workspace target")
		}
	}
	return workspaceID, root, nil
}
