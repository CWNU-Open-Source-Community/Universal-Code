package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (r *AgentBrowserRuntime) initializeScopedNetwork(ctx context.Context) error {
	// Network's ordered allow patterns run before Chromium creates a CORS
	// preflight. Fetch alone does not see all browser-generated OPTIONS traffic.
	patterns := make([]map[string]any, 0, len(r.scope.Origins)+1)
	for _, origin := range r.scope.Origins {
		patterns = append(patterns, map[string]any{"urlPattern": origin.String() + "/*", "block": false})
	}
	patterns = append(patterns, map[string]any{"urlPattern": "*://*:*/*", "block": true})
	for _, command := range []struct {
		method string
		params any
	}{
		{"Network.enable", map[string]any{}},
		{"Network.setBlockedURLs", map[string]any{"urlPatterns": patterns}},
		{"Network.setBypassServiceWorker", map[string]any{"bypass": true}},
		{"Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}}}},
	} {
		if err := r.call(ctx, command.method, command.params, nil); err != nil {
			return err
		}
	}
	return nil
}

type agentBrowserPausedRequest struct {
	RequestID, FrameID, ResourceType string
	Request                          struct {
		URL, Method     string
		HasPostData     bool
		PostDataEntries []struct{ Bytes *string }
	}
}

func (r *AgentBrowserRuntime) handleScopedRequestLocked(event cdpWireMessage) {
	var paused agentBrowserPausedRequest
	if json.Unmarshal(event.Params, &paused) != nil || !validRestrictedCDPToken(paused.RequestID) {
		r.cancel()
		return
	}
	ctx, cancel := context.WithDeadline(r.life, time.Now().Add(2*time.Minute))
	id := agentBrowserToken()
	r.networkPending[id] = cancel
	request := AgentBrowserNetworkRequest{ID: id, RuntimeID: r.networkRuntimeID, RequestID: paused.RequestID,
		FrameID: paused.FrameID, DocumentURL: r.currentURL, DocumentEpoch: r.epoch, Authority: r.request.Authority,
		Method: paused.Request.Method, URL: paused.Request.URL, ExpiresAt: time.Now().Add(2 * time.Minute)}
	go r.reviewScopedRequest(ctx, cancel, request, paused)
}

func (r *AgentBrowserRuntime) reviewScopedRequest(ctx context.Context, cancel context.CancelFunc, request AgentBrowserNetworkRequest, paused agentBrowserPausedRequest) {
	defer cancel()
	defer func() { r.mu.Lock(); delete(r.networkPending, request.ID); r.mu.Unlock() }()
	continued := false
	defer func() {
		if !continued {
			abortCtx, stop := context.WithTimeout(r.life, time.Second)
			defer stop()
			_ = r.cdp.send(abortCtx, r.session, "Fetch.failRequest", map[string]any{"requestId": request.RequestID, "errorReason": "BlockedByClient"})
		}
	}()
	if err := r.scope.AuthorizeNavigation(ctx, request.URL); err != nil {
		if paused.ResourceType == "Document" {
			r.mu.Lock()
			r.networkDocumentError = err
			r.mu.Unlock()
		}
		return
	}
	dispatch := func() error {
		if err := r.request.CheckAuthority(ctx, r.request.Authority); err != nil {
			return err
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if ctx.Err() != nil || r.life.Err() != nil || r.epoch != request.DocumentEpoch {
			return ErrAgentBrowserStaleReference
		}
		if continued {
			return errors.New("browser network request already consumed")
		}
		if err := r.cdp.send(ctx, r.session, "Fetch.continueRequest", map[string]any{"requestId": request.RequestID}); err != nil {
			return err
		}
		continued = true
		return nil
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		_ = dispatch()
	default:
		// CDP omits bytes for files/blobs it cannot serialize. Never approve a
		// truncated textual postData representation as the complete request body.
		body := []byte{}
		if paused.Request.HasPostData && len(paused.Request.PostDataEntries) == 0 {
			return
		}
		for _, entry := range paused.Request.PostDataEntries {
			if entry.Bytes == nil {
				return
			}
			data, err := base64.StdEncoding.DecodeString(*entry.Bytes)
			if err != nil || len(body)+len(data) > 1024*1024 {
				return
			}
			body = append(body, data...)
		}
		digest := sha256.Sum256(body)
		request.BodySHA256, request.BodyBytes = hex.EncodeToString(digest[:]), len(body)
		if r.request.ReviewNetworkWrite != nil {
			_ = r.request.ReviewNetworkWrite(ctx, request, dispatch)
		}
	}
}
