package browserruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

// These are trusted, fixed observation functions evaluated in an isolated
// world. There is deliberately no API accepting model-supplied JavaScript.
const agentBrowserInspectFunction = `function(){
 if(!this.isConnected || this.ownerDocument!==document) return {connected:false};
 const e=this, s=getComputedStyle(e), b=e.getBoundingClientRect();
 const name=(e.getAttribute('aria-label')||e.getAttribute('title')||((e.tagName==='INPUT'||e.tagName==='TEXTAREA')?'':e.innerText)||'').trim().slice(0,512);
 const x=Math.max(0,b.left)+Math.max(0,Math.min(innerWidth,b.right)-Math.max(0,b.left))/2;
 const y=Math.max(0,b.top)+Math.max(0,Math.min(innerHeight,b.bottom)-Math.max(0,b.top))/2;
 const hit=document.elementFromPoint(x,y);
 const tag=e.tagName.toLowerCase(), type=(e.getAttribute('type')||'').toLowerCase();
 const signature=JSON.stringify([tag,type,name,e.href||e.getAttribute('href'),e.formAction||e.getAttribute('formaction'),e.form&&e.form.action,e.form&&e.form.method,e.getAttribute('target'),e.getAttribute('download'),e.getAttribute('role'),e.getAttribute('name'),e.getAttribute('aria-label')]);
 if(signature.length>16384) return {connected:false};
 return {connected:true,tag,type,name,disabled:!!e.disabled||e.getAttribute('aria-disabled')==='true',readOnly:!!e.readOnly,
 editable:tag==='textarea'||(tag==='input'&&['','text','search','email','url','tel','password','number'].includes(type))||e.isContentEditable,
 visible:s.visibility==='visible'&&s.display!=='none'&&Number(s.opacity)!==0&&b.width>0&&b.height>0,
 hit:!!hit&&(hit===e||e.contains(hit)),focused:document.activeElement===e||e.contains(document.activeElement),x,y,
 signature};
}`

type agentBrowserNode struct {
	Connected, Disabled, ReadOnly, Editable, Visible, Hit, Focused bool
	Tag, Type, Name, Signature                                     string
	X, Y                                                           float64
}
type agentBrowserPage struct {
	Title, Text, Ready string
	Truncated          bool
	Layout             *AgentBrowserLayout
}

func (r *AgentBrowserRuntime) world(ctx context.Context) (int64, error) {
	r.mu.Lock()
	frame := r.frame
	r.mu.Unlock()
	var result struct {
		ID int64 `json:"executionContextId"`
	}
	err := r.call(ctx, "Page.createIsolatedWorld", map[string]any{"frameId": frame, "worldName": "traverse-agent-observer", "grantUniveralAccess": false}, &result)
	return result.ID, err
}
func (r *AgentBrowserRuntime) observe(ctx context.Context, backend, world int64, function string, out any) error {
	var resolved struct {
		Object struct {
			ID string `json:"objectId"`
		}
	}
	if err := r.call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": backend, "executionContextId": world, "objectGroup": "agent-observation"}, &resolved); err != nil {
		return err
	}
	if resolved.Object.ID == "" {
		return ErrAgentBrowserTargetChanged
	}
	var called struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		}
		ExceptionDetails json.RawMessage
	}
	if err := r.call(ctx, "Runtime.callFunctionOn", map[string]any{"objectId": resolved.Object.ID, "functionDeclaration": function, "returnByValue": true, "silent": true}, &called); err != nil {
		return err
	}
	if len(called.ExceptionDetails) > 0 || len(called.Result.Value) == 0 {
		return ErrAgentBrowserTargetChanged
	}
	return json.Unmarshal(called.Result.Value, out)
}
func (r *AgentBrowserRuntime) releaseObjects(ctx context.Context) {
	_ = r.call(ctx, "Runtime.releaseObjectGroup", map[string]any{"objectGroup": "agent-observation"}, nil)
}

func (r *AgentBrowserRuntime) Navigate(ctx context.Context, rawURL string) (AgentBrowserNavigation, error) {
	return r.navigate(ctx, rawURL, nil)
}

func (r *AgentBrowserRuntime) NavigateWithViewport(ctx context.Context, rawURL string, viewport AgentBrowserViewport) (AgentBrowserNavigation, error) {
	if r.attached != nil {
		return AgentBrowserNavigation{}, errors.New("shared native page uses the user-visible viewport; resize the browser pane instead")
	}
	if err := viewport.validate(); err != nil {
		return AgentBrowserNavigation{}, err
	}
	return r.navigate(ctx, rawURL, &viewport)
}

func (r *AgentBrowserRuntime) navigate(ctx context.Context, rawURL string, viewport *AgentBrowserViewport) (AgentBrowserNavigation, error) {
	canonical, err := agentBrowserURL(rawURL)
	if err != nil {
		return AgentBrowserNavigation{}, err
	}
	if err := r.authorizeSessionNavigation(ctx, canonical); err != nil {
		return AgentBrowserNavigation{}, err
	}
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserNavigation{}, err
	}
	defer end()
	defer r.releaseObjects(op)
	r.mu.Lock()
	r.invalidateLocked()
	r.networkDocumentError = nil
	r.invalidateNetworkLocked()
	r.mu.Unlock()
	if viewport != nil {
		if err := r.call(op, "Emulation.setDeviceMetricsOverride", map[string]any{"width": viewport.Width, "height": viewport.Height, "deviceScaleFactor": 1, "mobile": false}, nil); err != nil {
			return AgentBrowserNavigation{}, r.unknown(err)
		}
	}
	var result struct {
		FrameID, LoaderID, ErrorText string
		IsDownload                   bool
	}
	if err := r.call(op, "Page.navigate", map[string]any{"url": canonical}, &result); err != nil {
		return AgentBrowserNavigation{}, r.unknown(err)
	}
	if result.ErrorText != "" || result.IsDownload {
		r.mu.Lock()
		scopeErr := r.networkDocumentError
		r.mu.Unlock()
		if scopeErr != nil {
			return AgentBrowserNavigation{}, scopeErr
		}
		return AgentBrowserNavigation{}, errors.New("browser navigation failed or attempted a download")
	}
	// Waiting observes the one dispatched navigation; it never dispatches it again.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		loaded := r.loaded
		currentLoader := r.loader
		scopeErr := r.networkDocumentError
		r.mu.Unlock()
		if scopeErr != nil {
			return AgentBrowserNavigation{}, scopeErr
		}
		if result.LoaderID != "" && (loaded != result.LoaderID || currentLoader != result.LoaderID) {
			select {
			case <-op.Done():
				return AgentBrowserNavigation{}, r.unknown(op.Err())
			case <-ticker.C:
				continue
			}
		}
		doc, err := r.document(op)
		if err != nil {
			return AgentBrowserNavigation{}, r.unknown(err)
		}
		world, err := r.world(op)
		if err != nil {
			return AgentBrowserNavigation{}, r.unknown(err)
		}
		var page agentBrowserPage
		if err = r.observe(op, doc.backend, world, agentBrowserDocumentFunction, &page); err != nil {
			return AgentBrowserNavigation{}, r.unknown(err)
		}
		r.mu.Lock()
		loader := r.loader
		r.mu.Unlock()
		if (result.LoaderID == "" || loader == result.LoaderID) && page.Ready != "loading" {
			if err = r.check(op, doc.epoch); err != nil {
				return AgentBrowserNavigation{}, r.unknown(err)
			}
			var observed *AgentBrowserViewport
			if page.Layout != nil {
				v := page.Layout.Viewport
				observed = &v
			}
			if err := r.authorizeSessionNavigation(ctx, doc.url); err != nil {
				var scopeErr *AgentBrowserScopeError
				if errors.As(err, &scopeErr) && scopeErr.Code == AgentBrowserScopeOriginNotDeclared {
					return AgentBrowserNavigation{}, &AgentBrowserScopeError{Code: AgentBrowserScopeLeftOrigins, Origin: doc.url}
				}
				return AgentBrowserNavigation{}, err
			}
			return AgentBrowserNavigation{SessionID: r.request.Authority.SessionID, CanonicalURL: doc.url, DocumentEpoch: doc.epoch, CompletedAt: time.Now().UTC(), Viewport: observed}, nil
		}
		select {
		case <-op.Done():
			return AgentBrowserNavigation{}, r.unknown(op.Err())
		case <-ticker.C:
		}
	}
}

func (r *AgentBrowserRuntime) Snapshot(ctx context.Context) (AgentBrowserSnapshot, error) {
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserSnapshot{}, err
	}
	defer end()
	defer r.releaseObjects(op)
	doc, err := r.document(op)
	if err != nil {
		return AgentBrowserSnapshot{}, err
	}
	world, err := r.world(op)
	if err != nil {
		return AgentBrowserSnapshot{}, err
	}
	var page agentBrowserPage
	if err = r.observe(op, doc.backend, world, agentBrowserDocumentFunction, &page); err != nil {
		return AgentBrowserSnapshot{}, err
	}
	var tree struct {
		Nodes []struct {
			Ignored bool
			Backend int64 `json:"backendDOMNodeId"`
			Role    struct{ Value string }
			Name    struct{ Value string }
		}
	}
	r.mu.Lock()
	frame := r.frame
	r.mu.Unlock()
	if err = r.call(op, "Accessibility.getFullAXTree", map[string]any{"frameId": frame}, &tree); err != nil {
		return AgentBrowserSnapshot{}, err
	}
	snapshot := AgentBrowserSnapshot{Version: AgentBrowserProtocolVersion, SessionID: r.request.Authority.SessionID, SnapshotID: agentBrowserToken(), CanonicalURL: doc.url, Title: page.Title, DocumentEpoch: doc.epoch, Text: page.Text, Truncated: page.Truncated, Layout: page.Layout, UntrustedEvidence: true, FramesSupported: false, Elements: []AgentBrowserElement{}}
	refs := map[string]agentBrowserRef{}
	for _, node := range tree.Nodes {
		if node.Ignored || node.Backend == 0 || !agentBrowserInteractiveRole(node.Role.Value) {
			continue
		}
		if len(refs) >= 128 {
			snapshot.Truncated = true
			break
		}
		var inspected agentBrowserNode
		if err = r.observe(op, node.Backend, world, agentBrowserInspectFunction, &inspected); err != nil {
			return AgentBrowserSnapshot{}, err
		}
		if !inspected.Connected || !inspected.Visible {
			continue
		}
		ref := agentBrowserToken()
		name := node.Name.Value
		if len(name) > 512 {
			name = string([]rune(name)[:min(len([]rune(name)), 512)])
		}
		refs[ref] = agentBrowserRef{backend: node.Backend, epoch: doc.epoch, snapshot: snapshot.SnapshotID, signature: inspected.Signature, name: name, role: node.Role.Value}
		snapshot.Elements = append(snapshot.Elements, AgentBrowserElement{Ref: ref, Role: node.Role.Value, Name: name, Tag: inspected.Tag, Type: inspected.Type, Disabled: inspected.Disabled})
	}
	if err = boundAgentBrowserSnapshot(&snapshot, refs); err != nil {
		return AgentBrowserSnapshot{}, err
	}
	if err = r.check(op, doc.epoch); err != nil {
		return AgentBrowserSnapshot{}, err
	}
	r.mu.Lock()
	if r.epoch != doc.epoch {
		r.mu.Unlock()
		return AgentBrowserSnapshot{}, ErrAgentBrowserStaleReference
	}
	r.refs = refs
	r.mu.Unlock()
	snapshot.CompletedAt = time.Now().UTC()
	return snapshot, nil
}
func agentBrowserInteractiveRole(role string) bool {
	switch role {
	case "button", "link", "textbox", "searchbox", "combobox", "checkbox", "radio", "switch", "menuitem", "tab", "option", "spinbutton", "slider":
		return true
	}
	return false
}

// Resolve only the installed, bounded snapshot's exposed AX names. Click/Type
// subsequently perform the normal live signature, hit-test and authority checks.
func (r *AgentBrowserRuntime) ResolveSnapshotTarget(ctx context.Context, snapshot, name, role string) (string, error) {
	// Gateway redaction can make different raw AX labels look identical. Never
	// resolve such labels back to one raw node; use an existing current ref.
	if redact.String(name) != name || strings.Contains(name, "[REDACTED") {
		return "", ErrAgentBrowserTargetChanged
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var matched string
	current := false
	for ref, stored := range r.refs {
		if stored.epoch != r.epoch || stored.snapshot != snapshot {
			continue
		}
		current = true
		if stored.name == name && stored.role == role {
			if matched != "" {
				return "", ErrAgentBrowserTargetChanged
			}
			matched = ref
		}
	}
	if !current {
		return "", ErrAgentBrowserStaleReference
	}
	if matched == "" {
		return "", ErrAgentBrowserTargetChanged
	}
	return matched, nil
}

func (r *AgentBrowserRuntime) reference(ctx context.Context, snapshot, ref string) (agentBrowserRef, agentBrowserNode, error) {
	r.mu.Lock()
	stored, ok := r.refs[ref]
	epoch := r.epoch
	r.mu.Unlock()
	if !ok || stored.epoch != epoch || stored.snapshot != snapshot {
		return stored, agentBrowserNode{}, ErrAgentBrowserStaleReference
	}
	world, err := r.world(ctx)
	if err != nil {
		return stored, agentBrowserNode{}, err
	}
	var node agentBrowserNode
	if err = r.observe(ctx, stored.backend, world, agentBrowserInspectFunction, &node); err != nil {
		return stored, node, ErrAgentBrowserTargetChanged
	}
	if !node.Connected || !node.Visible || node.Disabled || node.Signature != stored.signature {
		return stored, node, ErrAgentBrowserTargetChanged
	}
	if err = r.call(ctx, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": stored.backend}, nil); err != nil {
		return stored, node, ErrAgentBrowserTargetChanged
	}
	if err = r.observe(ctx, stored.backend, world, agentBrowserInspectFunction, &node); err != nil {
		return stored, node, ErrAgentBrowserTargetChanged
	}
	if !node.Connected || !node.Visible || !node.Hit || node.Disabled || node.Signature != stored.signature {
		return stored, node, ErrAgentBrowserTargetChanged
	}
	return stored, node, r.check(ctx, epoch)
}
func (r *AgentBrowserRuntime) interaction(action string) AgentBrowserInteraction {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refs = map[string]agentBrowserRef{}
	return AgentBrowserInteraction{r.request.Authority.SessionID, r.currentURL, r.epoch, action, true, time.Now().UTC()}
}
func (r *AgentBrowserRuntime) input(ctx context.Context, method string, params any) error {
	r.mu.Lock()
	r.refs = map[string]agentBrowserRef{}
	r.mu.Unlock()
	if err := r.call(ctx, method, params, nil); err != nil {
		return r.unknown(err)
	}
	if err := r.request.CheckAuthority(ctx, r.request.Authority); err != nil {
		r.Cancel()
		return r.unknown(err)
	}
	return nil
}

// An uncertain partial key/mouse sequence must not leave held input available
// to later operations. Invalidate the complete slot and reap its owned tree.
func (r *AgentBrowserRuntime) unknown(err error) error {
	r.Cancel()
	return &AgentBrowserActionError{Err: err, Dispatched: true, OutcomeUnknown: true}
}
func (r *AgentBrowserRuntime) Click(ctx context.Context, snapshot, ref string) (AgentBrowserInteraction, error) {
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	defer end()
	defer r.releaseObjects(op)
	stored, node, err := r.reference(op, snapshot, ref)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	r.mu.Lock()
	r.refs = map[string]agentBrowserRef{}
	r.mu.Unlock()
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		if kind == "mouseReleased" {
			if err = r.check(op, stored.epoch); err != nil {
				return AgentBrowserInteraction{}, r.unknown(err)
			}
			world, worldErr := r.world(op)
			var after agentBrowserNode
			if worldErr != nil {
				return AgentBrowserInteraction{}, r.unknown(worldErr)
			}
			if err = r.observe(op, stored.backend, world, agentBrowserInspectFunction, &after); err != nil || !after.Connected || !after.Hit || after.Signature != stored.signature || math.Abs(after.X-node.X) > 1 || math.Abs(after.Y-node.Y) > 1 {
				return AgentBrowserInteraction{}, r.unknown(ErrAgentBrowserTargetChanged)
			}
		}
		if err = r.input(op, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": node.X, "y": node.Y, "button": "left", "clickCount": 1}); err != nil {
			return AgentBrowserInteraction{}, err
		}
	}
	return r.interaction("click"), nil
}
func (r *AgentBrowserRuntime) Type(ctx context.Context, snapshot, ref, text string, replace bool) (AgentBrowserInteraction, error) {
	if len(text) > 16384 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return AgentBrowserInteraction{}, ErrBrowserRuntimeBoundary
	}
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	defer end()
	defer r.releaseObjects(op)
	stored, node, err := r.reference(op, snapshot, ref)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	if !node.Editable || node.ReadOnly {
		return AgentBrowserInteraction{}, ErrAgentBrowserTargetChanged
	}
	if err = r.call(op, "DOM.focus", map[string]any{"backendNodeId": stored.backend}, nil); err != nil {
		return AgentBrowserInteraction{}, r.unknown(err)
	}
	if err = r.focused(op, stored); err != nil {
		return AgentBrowserInteraction{}, r.unknown(err)
	}
	r.mu.Lock()
	r.refs = map[string]agentBrowserRef{}
	r.mu.Unlock()
	if replace {
		for _, kind := range []string{"keyDown", "keyUp"} {
			if err = r.input(op, "Input.dispatchKeyEvent", map[string]any{"type": kind, "key": "a", "code": "KeyA", "windowsVirtualKeyCode": 65, "modifiers": 2}); err != nil {
				return AgentBrowserInteraction{}, err
			}
			if err = r.focused(op, stored); err != nil {
				return AgentBrowserInteraction{}, r.unknown(err)
			}
		}
	}
	if err = r.input(op, "Input.insertText", map[string]any{"text": text}); err != nil {
		return AgentBrowserInteraction{}, err
	}
	return r.interaction("type"), nil
}

// Upload attaches one operator-validated workspace file to a snapshot-resolved
// file input. The runtime re-checks the element type from the observed
// document; the absolute path itself was proven to live inside the workspace
// by the application layer before dispatch.
func (r *AgentBrowserRuntime) Upload(ctx context.Context, snapshot, ref, filePath string) (AgentBrowserInteraction, error) {
	if !utf8.ValidString(filePath) || strings.ContainsRune(filePath, 0) || len(filePath) > 4096 || !filepath.IsAbs(filePath) {
		return AgentBrowserInteraction{}, ErrBrowserRuntimeBoundary
	}
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	defer end()
	defer r.releaseObjects(op)
	stored, node, err := r.reference(op, snapshot, ref)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	if node.Tag != "input" || node.Type != "file" || node.Disabled {
		return AgentBrowserInteraction{}, ErrAgentBrowserTargetChanged
	}
	if err = r.call(op, "DOM.setFileInputFiles", map[string]any{"files": []string{filePath}, "backendNodeId": stored.backend}, nil); err != nil {
		return AgentBrowserInteraction{}, r.unknown(err)
	}
	return r.interaction("upload"), nil
}

func (r *AgentBrowserRuntime) focused(ctx context.Context, ref agentBrowserRef) error {
	if err := r.check(ctx, ref.epoch); err != nil {
		return err
	}
	world, err := r.world(ctx)
	if err != nil {
		return err
	}
	var node agentBrowserNode
	if err = r.observe(ctx, ref.backend, world, agentBrowserInspectFunction, &node); err != nil {
		return err
	}
	if !node.Connected || !node.Focused || !node.Editable || node.ReadOnly || node.Disabled || node.Signature != ref.signature {
		return ErrAgentBrowserTargetChanged
	}
	return r.check(ctx, ref.epoch)
}
func (r *AgentBrowserRuntime) Scroll(ctx context.Context, deltaX, deltaY float64) (AgentBrowserInteraction, error) {
	if math.IsNaN(deltaX) || math.IsNaN(deltaY) || math.IsInf(deltaX, 0) || math.IsInf(deltaY, 0) || math.Abs(deltaX) > 10000 || math.Abs(deltaY) > 10000 {
		return AgentBrowserInteraction{}, ErrBrowserRuntimeBoundary
	}
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	defer end()
	viewport, err := r.currentViewport(op)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	if err = r.input(op, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": float64(viewport.Width) / 2, "y": float64(viewport.Height) / 2, "deltaX": deltaX, "deltaY": deltaY}); err != nil {
		return AgentBrowserInteraction{}, err
	}
	return r.interaction("scroll"), nil
}
func (r *AgentBrowserRuntime) Key(ctx context.Context, key string) (AgentBrowserInteraction, error) {
	keys := map[string]int{"Enter": 13, "Tab": 9, "Escape": 27, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Backspace": 8, "Delete": 46, "Home": 36, "End": 35, "PageUp": 33, "PageDown": 34}
	code, ok := keys[key]
	if !ok {
		return AgentBrowserInteraction{}, ErrBrowserRuntimeBoundary
	}
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	defer end()
	for _, kind := range []string{"keyDown", "keyUp"} {
		params := map[string]any{"type": kind, "key": key, "code": key, "windowsVirtualKeyCode": code}
		if key == "Enter" && kind == "keyDown" {
			params["text"] = "\r"
		}
		if err = r.input(op, "Input.dispatchKeyEvent", params); err != nil {
			return AgentBrowserInteraction{}, err
		}
	}
	return r.interaction("key"), nil
}
func (r *AgentBrowserRuntime) Screenshot(ctx context.Context) (AgentBrowserScreenshot, error) {
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserScreenshot{}, err
	}
	defer end()
	doc, err := r.document(op)
	if err != nil {
		return AgentBrowserScreenshot{}, err
	}
	viewport, err := r.currentViewport(op)
	if err != nil {
		return AgentBrowserScreenshot{}, err
	}
	var capture struct{ Data string }
	if err = r.call(op, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false, "fromSurface": true}, &capture); err != nil {
		return AgentBrowserScreenshot{}, err
	}
	data, err := base64.StdEncoding.DecodeString(capture.Data)
	if err != nil || len(data) == 0 || len(data) > 8*1024*1024 {
		return AgentBrowserScreenshot{}, errors.New("invalid browser screenshot")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 {
		return AgentBrowserScreenshot{}, errors.New("invalid browser screenshot dimensions")
	}
	if err = r.check(op, doc.epoch); err != nil {
		return AgentBrowserScreenshot{}, err
	}
	sum := sha256.Sum256(data)
	after, err := r.currentViewport(op)
	if err != nil {
		return AgentBrowserScreenshot{}, err
	}
	if after != viewport {
		return AgentBrowserScreenshot{}, ErrAgentBrowserStaleReference
	}
	if err = r.check(op, doc.epoch); err != nil {
		return AgentBrowserScreenshot{}, err
	}
	return AgentBrowserScreenshot{SessionID: r.request.Authority.SessionID, CanonicalURL: doc.url, DocumentEpoch: doc.epoch, MediaType: "image/png", SHA256: hex.EncodeToString(sum[:]), Bytes: len(data), PNG: data, CompletedAt: time.Now().UTC(), Viewport: &viewport, ImageWidth: config.Width, ImageHeight: config.Height}, nil
}
