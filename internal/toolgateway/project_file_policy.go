package toolgateway

import (
	"context"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/tools"
)

func (g *Gateway) projectWorkspaceFS(ctx context.Context, call ToolCall) (tools.WorkspaceFS, error) {
	fs := tools.NewWorkspaceFS(call.WorkspaceRoot)
	var run domain.Run
	var err error
	if call.RunID != "" {
		reader, ok := g.store.(interface {
			GetRun(context.Context, string) (domain.Run, error)
		})
		if !ok {
			return fs, apperror.New(apperror.CodeFailedPrecondition, "task configuration reader is unavailable")
		}
		run, err = reader.GetRun(ctx, call.RunID)
	} else if call.SessionID != "" {
		if reader, ok := g.store.(interface {
			GetRunBySession(context.Context, string) (domain.Run, bool, error)
		}); ok {
			run, _, err = reader.GetRunBySession(ctx, call.SessionID)
		}
	}
	if err != nil {
		return fs, apperror.Normalize(err)
	}
	if run.ID == "" {
		return fs, nil
	}
	if call.SessionID != "" && call.SessionID != run.SessionID {
		return fs, apperror.New(apperror.CodePolicyDenied, "workspace read task and session do not match")
	}
	fs.PathPolicy, err = projectconfig.PathPolicy(run.Config)
	return fs, err
}
