package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

func validateAgentBrowserNetworkProtocol(raw []byte) error {
	var protocol struct {
		Domains []struct {
			Domain   string
			Commands []struct {
				Name       string
				Parameters []struct{ Name string }
			}
			Types []struct {
				ID         string
				Properties []struct{ Name string }
			}
		}
	}
	if err := json.Unmarshal(raw, &protocol); err != nil {
		return err
	}
	parameter, block := false, false
	for _, d := range protocol.Domains {
		if d.Domain != "Network" {
			continue
		}
		for _, c := range d.Commands {
			if c.Name == "setBlockedURLs" {
				for _, p := range c.Parameters {
					if p.Name == "urlPatterns" {
						parameter = true
					}
				}
			}
		}
		for _, typ := range d.Types {
			if typ.ID == "BlockPattern" {
				for _, p := range typ.Properties {
					if p.Name == "block" {
						block = true
					}
				}
			}
		}
	}
	if !parameter || !block {
		return errors.New("browser_scoped_network_unsupported: this browser lacks ordered network origin rules")
	}
	return nil
}

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
	} {
		if err := r.cdp.call(ctx, r.session, command.method, command.params, nil); err != nil {
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
		FrameID: paused.FrameID, DocumentURL: r.currentURL, DocumentEpoch: r.networkEpoch, Authority: r.request.Authority,
		Method: paused.Request.Method, URL: paused.Request.URL, ExpiresAt: time.Now().Add(2 * time.Minute)}
	go r.reviewScopedRequest(ctx, cancel, request, paused)
}

func (r *AgentBrowserRuntime) reviewScopedRequest(ctx context.Context, cancel context.CancelFunc, request AgentBrowserNetworkRequest, paused agentBrowserPausedRequest) {
	defer cancel()
	defer func() {
		r.mu.Lock()
		delete(r.networkPending, request.ID)
		r.mu.Unlock()
	}()
	continued := false
	defer func() {
		if !continued {
			abortCtx, stop := context.WithTimeout(r.life, time.Second)
			defer stop()
			// Navigation and Network's origin rule can already have ended the
			// interception. The write's source is invalidated independently.
			_ = r.cdp.call(abortCtx, "", "Fetch.failRequest", map[string]any{"requestId": request.RequestID, "errorReason": "BlockedByClient"}, nil)
		}
	}()
	if err := r.scope.AuthorizeNavigation(ctx, request.URL); err != nil {
		if paused.ResourceType == "Document" {
			r.mu.Lock()
			if request.FrameID == r.frame {
				r.networkDocumentError = err
			}
			r.mu.Unlock()
		}
		return
	}
	dispatch := func() error {
		if err := r.request.CheckAuthority(ctx, r.request.Authority); err != nil {
			return err
		}
		r.mu.Lock()
		if ctx.Err() != nil || r.life.Err() != nil || r.networkEpoch != request.DocumentEpoch {
			r.mu.Unlock()
			return ErrAgentBrowserStaleReference
		}
		if continued {
			r.mu.Unlock()
			return errors.New("browser network request already consumed")
		}
		await, err := r.cdp.send(ctx, "", "Fetch.continueRequest", map[string]any{"requestId": request.RequestID})
		r.mu.Unlock()
		if err != nil {
			return err
		}
		err = await()
		continued = err == nil
		return err
	}
	switch request.Method {
	case "GET", "HEAD", "OPTIONS":
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
