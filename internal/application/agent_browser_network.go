package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

type browserNetworkRequestStore interface {
	CreateBrowserNetworkRequest(context.Context, domain.BrowserNetworkRequest) error
	GetBrowserNetworkRequest(context.Context, string) (domain.BrowserNetworkRequest, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
	ConsumeBrowserNetworkRequest(context.Context, string, string) error
	FinishBrowserNetworkRequest(context.Context, string, bool) error
	InvalidateBrowserNetworkRequest(context.Context, string) error
	InvalidateOldBrowserNetworkRequests(context.Context, string) error
}

func (s *AgentBrowserService) reviewNetworkWrite(a toolgateway.AgentBrowserCallAuthority) func(context.Context, browserruntime.AgentBrowserNetworkRequest, func() error) error {
	return func(ctx context.Context, request browserruntime.AgentBrowserNetworkRequest, dispatch func() error) (result error) {
		st, ok := s.store.(browserNetworkRequestStore)
		if !ok {
			return agentBrowserUnavailable("browser request approval store unavailable")
		}
		if request.Authority.RunID != a.RunID || request.Authority.SessionID != a.BrowserSessionID || request.Authority.Generation != a.SessionGeneration || request.Authority.ManagerBootID != a.ManagerBootID || request.Authority.PermissionRevision != a.PermissionRevision || request.Authority.RunAuthorizationFence != a.RunAuthorizationFence || request.Authority.PermissionSnapshotID != a.PermissionSnapshotID || request.Authority.PermissionMode != string(a.PermissionMode) || (len(a.DeclaredOrigins) == 0 && a.BrowserNetworkAccess != domain.BrowserNetworkInternet) {
			return agentBrowserUnavailable("browser request authority changed")
		}
		if err := s.checkResource(ctx, a); err != nil {
			return err
		}
		sourceBytes, _ := json.Marshal(struct {
			Request   browserruntime.AgentBrowserNetworkRequest
			Authority toolgateway.AgentBrowserCallAuthority
		}{request, a})
		digest := sha256.Sum256(sourceBytes)
		authority, _ := json.Marshal(a)
		display, err := url.Parse(request.URL)
		if err != nil {
			return err
		}
		display.RawQuery, display.Fragment, display.User = "", "", nil
		v := domain.BrowserNetworkRequest{ID: domain.BrowserNetworkRequestPrefix + request.ID, RunID: a.RunID, SessionID: a.SessionID, WorkspaceID: a.WorkspaceID, ManagerBootID: a.ManagerBootID, RuntimeID: request.RuntimeID, Fingerprint: hex.EncodeToString(digest[:]), AuthorityJSON: string(authority), Method: request.Method, DisplayURL: display.String(), BodySHA256: request.BodySHA256, BodyBytes: request.BodyBytes, ExpiresAt: request.ExpiresAt}
		if err = st.CreateBrowserNetworkRequest(ctx, v); err != nil {
			return err
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			result = errors.Join(result, st.InvalidateBrowserNetworkRequest(cleanup, v.ID))
		}()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			if !v.ExpiresAt.After(time.Now()) {
				return agentBrowserUnavailable("browser network request expired")
			}
			if err = s.checkResource(ctx, a); err != nil {
				return err
			}
			record, err := st.GetApprovalByProposal(ctx, v.ID)
			if err != nil {
				return err
			}
			if record.RequestFingerprint != v.Fingerprint || record.GrantID != "" || record.ToolName != toolgateway.AgentBrowserApprovalTool {
				return agentBrowserUnavailable("browser network approval changed")
			}
			switch record.Status {
			case approval.StatusDenied:
				return agentBrowserUnavailable("operator denied browser network request")
			case approval.StatusApproved:
				if err = st.ConsumeBrowserNetworkRequest(ctx, v.ID, v.Fingerprint); err != nil {
					return err
				}
				dispatchErr := dispatch()
				finishCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				finishErr := st.FinishBrowserNetworkRequest(finishCtx, v.ID, dispatchErr == nil)
				stop()
				return errors.Join(dispatchErr, finishErr)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}
}

func recheckBrowserNetworkApproval(ctx context.Context, base any, record approval.Record) error {
	st, ok := base.(browserNetworkRequestStore)
	if !ok {
		return agentBrowserUnavailable("browser network approval source unavailable")
	}
	v, err := st.GetBrowserNetworkRequest(ctx, record.ProposalID)
	if err != nil {
		return err
	}
	if v.Status != "paused" || !v.ExpiresAt.After(time.Now()) || v.RunID != record.RunID || v.SessionID != record.SessionID || v.WorkspaceID != record.WorkspaceID || v.Fingerprint != record.RequestFingerprint || record.Mode != "per_call" || record.GrantID != "" || !strings.HasPrefix(v.ID, domain.BrowserNetworkRequestPrefix) {
		return agentBrowserUnavailable("browser network request is no longer live")
	}
	return nil
}
