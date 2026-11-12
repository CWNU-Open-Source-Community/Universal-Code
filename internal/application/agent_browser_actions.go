package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

type agentBrowserPointerRuntime interface {
	ClickPoint(context.Context, browserruntime.AgentBrowserPoint) (browserruntime.AgentBrowserInteraction, error)
	MovePointer(context.Context, browserruntime.AgentBrowserPoint) (browserruntime.AgentBrowserInteraction, error)
}

func (s *AgentBrowserService) ExecuteAgentBrowserAction(ctx context.Context, scope toolgateway.AgentBrowserExecutionScope, name toolgateway.ToolName, raw json.RawMessage) (toolgateway.BrowserActionExecutionResult, error) {
	a := scope.Authority
	if e := a.Validate(); e != nil {
		return toolgateway.BrowserActionExecutionResult{}, e
	}
	canonical, e := toolgateway.NormalizeAgentBrowserPayload(name, raw)
	if e != nil {
		return toolgateway.BrowserActionExecutionResult{}, e
	}
	var p toolgateway.AgentBrowserPayload
	_ = json.Unmarshal(canonical, &p)
	if e = s.check(ctx, a); e != nil {
		return toolgateway.BrowserActionExecutionResult{}, e
	}
	// Direct gateway calls also require the durable call, its started marker and
	// exact sensitive decision. A model cannot manufacture this scope.
	if e = s.checkDurableDispatch(ctx, scope, canonical); e != nil {
		return toolgateway.BrowserActionExecutionResult{}, e
	}
	s.mu.Lock()
	slot := s.slots[agentBrowserSlotKey(a)]
	s.mu.Unlock()
	if slot == nil {
		return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("browser session is unavailable")
	}
	slot.action.Lock()
	defer slot.action.Unlock()
	if e = s.check(ctx, a); e != nil {
		return toolgateway.BrowserActionExecutionResult{}, e
	}
	if e = s.checkDurableDispatch(ctx, scope, canonical); e != nil {
		return toolgateway.BrowserActionExecutionResult{}, e
	}
	callID := scope.Call.SupervisorToolCallID
	if scope.Call.SpecialistToolCallID != "" {
		callID = scope.Call.SpecialistToolCallID
	}
	consumedID := callID
	if scope.Call.SpecialistToolCallID != "" {
		consumedID = a.AgentAttemptID + "\x00" + callID
	}
	if slot.consumed[consumedID] {
		return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("outcome_unknown: this browser call was already dispatched; do not repeat")
	}
	slot.consumed[consumedID] = true
	var readiness *runner.CommandServiceReadiness
	if p.ServiceJobID != "" {
		if s.options.ServiceReadiness == nil {
			return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("service readiness is unavailable")
		}
		observed, err := s.options.ServiceReadiness.CheckAgentBrowserService(ctx, a, p.ServiceJobID, p.URL)
		if err != nil {
			return toolgateway.BrowserActionExecutionResult{}, err
		}
		if err = s.check(ctx, a); err != nil {
			return toolgateway.BrowserActionExecutionResult{}, err
		}
		readiness = &observed
		if !observed.Verified || observed.State != "ready" {
			encoded, err := json.Marshal(struct {
				Service   *runner.CommandServiceReadiness `json:"service"`
				Navigated bool                            `json:"navigated"`
			}{readiness, false})
			return toolgateway.BrowserActionExecutionResult{Content: string(encoded)}, err
		}
	}
	s.mu.Lock()
	if s.closed || slot.closed || s.slots[agentBrowserSlotKey(a)] != slot {
		s.mu.Unlock()
		return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("browser session retired before dispatch; original authority cannot be rebound")
	}
	r := slot.runtime
	shared := s.options.SharedPage != nil && a.Role == domain.AgentRoleRoot
	start := name == toolgateway.BrowserNavigateTool || (shared && name != toolgateway.BrowserStatusTool)
	if r == nil && start {
		slot.view.State = "starting"
		slot.launchAttempted = true
	}
	s.mu.Unlock()
	if r == nil && start {
		r, e = s.startRuntime(ctx, a, slot)
		if e == nil {
			s.mu.Lock()
			slot.runtime = r
			s.mu.Unlock()
			go s.observeRuntimeDone(slot, r)
			e = s.check(ctx, a)
		}
		if e != nil {
			if r != nil {
				r.Cancel()
			}
			s.mu.Lock()
			slot.closed = true
			slot.view.State = "failed"
			slot.view.FailureReason = e.Error()
			var launchFailure *browserruntime.AgentBrowserLaunchError
			if errors.As(e, &launchFailure) && launchFailure.Cleanup.SessionID == a.BrowserSessionID {
				receipt := launchFailure.Cleanup
				receipt.CleanupPending = !receipt.Complete()
				slot.view.Cleanup = &receipt
			}
			s.mu.Unlock()
			return toolgateway.BrowserActionExecutionResult{}, normalizeAgentBrowserActionError(e)
		}
	}
	if r == nil && name != toolgateway.BrowserStatusTool {
		return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("navigate first to open the Agent browser")
	}
	if p.Sensitive != nil {
		if r == nil || r.Status().DocumentEpoch != p.Sensitive.DocumentEpoch || r.Status().CanonicalURL != p.Sensitive.Target {
			return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("approved browser document changed; obtain a new snapshot and approval")
		}
	}
	if r != nil {
		stop := context.AfterFunc(ctx, r.Cancel)
		defer stop()
	}
	s.mu.Lock()
	slot.view.LastAction = string(name)
	slot.view.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
	var value any
	metadata := map[string]string{"untrusted_output": "true", "agent_browser_session_id": a.BrowserSessionID, "manager_boot_id": a.ManagerBootID}
	if p.Target != nil {
		resolver, ok := r.(agentBrowserSnapshotTargetResolver)
		if !ok {
			return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("this owned browser does not support exact snapshot targets")
		}
		p.ElementRef, e = resolver.ResolveSnapshotTarget(ctx, p.SnapshotID, p.Target.Name, p.Target.Role)
		if e != nil {
			return toolgateway.BrowserActionExecutionResult{}, normalizeAgentBrowserActionError(e)
		}
	}
	switch name {
	case toolgateway.BrowserStatusTool:
		s.mu.Lock()
		value = s.projectSlotLocked(slot)
		s.mu.Unlock()
	case toolgateway.BrowserNavigateTool:
		if p.ServiceJobID != "" {
			// Browser startup may take seconds. Recheck the owned service at the
			// actual navigation boundary rather than relying on its earlier probe.
			observed, err := s.options.ServiceReadiness.CheckAgentBrowserService(ctx, a, p.ServiceJobID, p.URL)
			if err != nil {
				return toolgateway.BrowserActionExecutionResult{}, err
			}
			readiness = &observed
			if !observed.Verified || observed.State != "ready" {
				encoded, err := json.Marshal(struct {
					Service   *runner.CommandServiceReadiness `json:"service"`
					Navigated bool                            `json:"navigated"`
				}{readiness, false})
				return toolgateway.BrowserActionExecutionResult{Content: string(encoded)}, err
			}
		}
		if p.Viewport == nil {
			value, e = r.Navigate(ctx, p.URL)
		} else if navigator, ok := r.(agentBrowserViewportNavigator); ok {
			value, e = navigator.NavigateWithViewport(ctx, p.URL, browserruntime.AgentBrowserViewport{Width: p.Viewport.Width, Height: p.Viewport.Height})
		} else {
			e = agentBrowserUnavailable("this owned browser does not support viewport navigation")
		}
		if e == nil && readiness != nil {
			navigation, ok := value.(browserruntime.AgentBrowserNavigation)
			requested, _ := url.Parse(p.URL)
			observed, parseErr := url.Parse(navigation.CanonicalURL)
			if !ok || parseErr != nil || observed.Scheme != requested.Scheme || observed.Hostname() != requested.Hostname() || agentBrowserOriginPort(observed) != agentBrowserOriginPort(requested) {
				e = agentBrowserUnavailable("service navigation left the verified origin")
			} else {
				value = struct {
					browserruntime.AgentBrowserNavigation
					Service   *runner.CommandServiceReadiness `json:"service"`
					Navigated bool                            `json:"navigated"`
				}{navigation, readiness, true}
			}
		}
	case toolgateway.BrowserSnapshotTool:
		var snapshot browserruntime.AgentBrowserSnapshot
		snapshot, e = r.Snapshot(ctx)
		value = snapshot
		if e == nil {
			s.mu.Lock()
			slot.view.Title = snapshot.Title
			slot.titleEpoch = snapshot.DocumentEpoch
			slot.titleURL = snapshot.CanonicalURL
			s.mu.Unlock()
		}
	case toolgateway.BrowserClickTool, toolgateway.BrowserMoveTool:
		if p.Point != nil {
			pointer, ok := r.(agentBrowserPointerRuntime)
			if !ok {
				return toolgateway.BrowserActionExecutionResult{}, agentBrowserUnavailable("browser coordinate input is unavailable")
			}
			point := browserruntime.AgentBrowserPoint{X: p.Point.X, Y: p.Point.Y, DocumentEpoch: p.Point.DocumentEpoch, Viewport: browserruntime.AgentBrowserViewport{Width: p.Point.Viewport.Width, Height: p.Point.Viewport.Height}}
			if name == toolgateway.BrowserMoveTool {
				value, e = pointer.MovePointer(ctx, point)
			} else {
				value, e = pointer.ClickPoint(ctx, point)
			}
		} else {
			value, e = r.Click(ctx, p.SnapshotID, p.ElementRef)
		}
	case toolgateway.BrowserTypeTool:
		value, e = r.Type(ctx, p.SnapshotID, p.ElementRef, p.Value, p.Mode == "replace")
	case toolgateway.BrowserUploadTool:
		var absolutePath string
		absolutePath, e = s.resolveAgentBrowserUpload(ctx, a.WorkspaceID, p.WorkspacePath)
		if e == nil {
			value, e = r.Upload(ctx, p.SnapshotID, p.ElementRef, absolutePath)
		}
	case toolgateway.BrowserScrollTool:
		value, e = r.Scroll(ctx, *p.DeltaX, *p.DeltaY)
	case toolgateway.BrowserKeyTool:
		value, e = r.Key(ctx, p.Key)
	case toolgateway.BrowserScreenshotTool:
		var capture browserruntime.AgentBrowserScreenshot
		capture, e = r.Screenshot(ctx)
		if e == nil {
			var locator string
			locator, e = s.persistAgentBrowserScreenshot(ctx, a.WorkspaceID, a.RunID, scope.Call.OperationKey, capture)
			if e == nil {
				value = struct {
					browserruntime.AgentBrowserScreenshot
					Version  string `json:"version"`
					Artifact string `json:"artifact_locator"`
				}{capture, "browser_screenshot_result.v2", locator}
				metadata["artifact_locator"] = locator
				metadata["artifact_sha256"] = capture.SHA256
				metadata["artifact_bytes"] = fmt.Sprint(capture.Bytes)
				metadata["document_epoch"] = fmt.Sprint(capture.DocumentEpoch)
				metadata["canonical_url"] = capture.CanonicalURL
				s.mu.Lock()
				slot.view.ArtifactLocator = locator
				slot.screenshotEpoch = capture.DocumentEpoch
				slot.screenshotURL = capture.CanonicalURL
				slot.view.ScreenshotSHA256 = capture.SHA256
				slot.view.ScreenshotBytes = capture.Bytes
				if slot.screenshotCalls == nil {
					slot.screenshotCalls = map[string]string{}
				}
				slot.screenshotCalls[locator] = callID
				s.mu.Unlock()
			}
		}
	default:
		e = agentBrowserUnavailable("unsupported Agent browser action")
	}
	if e != nil {
		s.mu.Lock()
		slot.view.FailureReason = normalizeAgentBrowserActionError(e).Error()
		slot.view.UpdatedAt = time.Now().UTC()
		s.mu.Unlock()
		return toolgateway.BrowserActionExecutionResult{}, normalizeAgentBrowserActionError(e)
	}
	if e = s.check(ctx, a); e != nil {
		if r != nil {
			r.Cancel()
		}
		return toolgateway.BrowserActionExecutionResult{}, e
	}
	if r != nil {
		status := r.Status()
		s.mu.Lock()
		slot.view.CanonicalURL = status.CanonicalURL
		slot.view.DocumentEpoch = status.DocumentEpoch
		slot.view.Product = status.Product
		slot.view.State = status.State
		slot.view.FailureReason = ""
		slot.view.UpdatedAt = time.Now().UTC()
		s.mu.Unlock()
	}
	encoded, e := json.Marshal(value)
	return toolgateway.BrowserActionExecutionResult{Content: string(encoded), Metadata: metadata}, e
}

func agentBrowserOriginPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
func normalizeAgentBrowserActionError(err error) error {
	var action *browserruntime.AgentBrowserActionError
	if errors.As(err, &action) && action.OutcomeUnknown {
		return apperror.Wrap(apperror.CodeFailedPrecondition, "outcome_unknown: browser input was dispatched; do not automatically repeat it", err)
	}
	var scopeErr *browserruntime.AgentBrowserScopeError
	if errors.As(err, &scopeErr) {
		// Scope refusals are named and deterministic: nothing was dispatched on
		// a pre-navigation refusal, and a landing escape names the page it left.
		return apperror.Wrap(apperror.CodeFailedPrecondition, scopeErr.Error(), err)
	}
	if errors.Is(err, browserruntime.ErrAgentBrowserStaleReference) {
		return apperror.Wrap(apperror.CodeFailedPrecondition, "stale_browser_reference: snapshot or input invalidated the old refs; obtain a new snapshot before the next input; never automatically repeat an outcome_unknown action", err)
	}
	return apperror.Wrap(apperror.CodeFailedPrecondition, "Agent browser action failed", err)
}

// AgentBrowserMaxUploadBytes bounds one upload capture; it matches the
// download quarantine budget so neither direction can move more bytes than the
// browser session budget allows.
const AgentBrowserMaxUploadBytes = 16 << 20

// resolveAgentBrowserUpload verifies the requested upload is a real, regular,
// size-bounded file inside the exact workspace root, following symlinks to
// their destination and rejecting any resolution that escapes the root. The
// returned absolute path is what the browser process itself will read. This is
// a check-then-act boundary: a concurrent workspace writer can still swap or
// grow the file between verification and the browser opening it, which is an
// accepted residual within the approved external_write effect. Same-volume
// hardlinks are not detected either: their resolved path always stays inside
// the root, so containing them relies on the same host sandbox assumption that
// already bounds this residual.
func (s *AgentBrowserService) resolveAgentBrowserUpload(ctx context.Context, workspaceID, relative string) (string, error) {
	st, ok := s.store.(fullCDPWorkspaceInfoStore)
	if !ok {
		return "", agentBrowserUnavailable("browser upload workspace unavailable")
	}
	workspace, e := st.GetWorkspaceInfo(ctx, workspaceID)
	if e != nil {
		return "", e
	}
	if workspace.ID != workspaceID || !filepath.IsAbs(workspace.RootPath) {
		return "", agentBrowserUnavailable("browser upload workspace mismatch")
	}
	resolvedRoot, err := filepath.EvalSymlinks(workspace.RootPath)
	if err != nil {
		return "", agentBrowserUnavailable("browser upload workspace is unavailable")
	}
	target := filepath.Join(resolvedRoot, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || resolved == resolvedRoot || !withinResolvedWorkspace(resolvedRoot, resolved) {
		return "", agentBrowserUnavailable("browser upload file is unavailable or escapes the workspace")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", agentBrowserUnavailable("browser upload file is unavailable")
	}
	if info.Size() <= 0 {
		return "", agentBrowserUnavailable("browser upload file is empty")
	}
	if info.Size() > AgentBrowserMaxUploadBytes {
		return "", agentBrowserUnavailable("browser upload file exceeds the size bound")
	}
	return resolved, nil
}

func withinResolvedWorkspace(resolvedRoot, resolved string) bool {
	separator := string(filepath.Separator)
	// A resolved drive root such as C:\ already ends in the separator; appending
	// another one would reject every file on that drive.
	root := resolvedRoot
	if !strings.HasSuffix(root, separator) {
		root += separator
	}
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(strings.ToLower(resolved), strings.ToLower(root))
	}
	return strings.HasPrefix(resolved, root)
}

func (s *AgentBrowserService) persistAgentBrowserScreenshot(ctx context.Context, workspaceID, runID, key string, c browserruntime.AgentBrowserScreenshot) (string, error) {
	actual, e := imageattachment.Validate(c.PNG, c.MediaType, "")
	if e != nil || actual.SHA256 != c.SHA256 || actual.ByteSize != c.Bytes || c.DocumentEpoch == 0 {
		return "", agentBrowserUnavailable("browser screenshot integrity mismatch")
	}
	st, ok := s.store.(fullCDPWorkspaceInfoStore)
	if !ok {
		return "", agentBrowserUnavailable("browser screenshot Workspace unavailable")
	}
	workspace, e := st.GetWorkspaceInfo(ctx, workspaceID)
	if e != nil {
		return "", e
	}
	if workspace.ID != workspaceID || !filepath.IsAbs(workspace.RootPath) {
		return "", agentBrowserUnavailable("browser screenshot Workspace mismatch")
	}
	root, e := os.OpenRoot(workspace.RootPath)
	if e != nil {
		return "", e
	}
	defer root.Close()
	relative := fullCDPScreenshotRelativePath(runID, key)
	if e = root.MkdirAll(filepath.Dir(relative), 0700); e != nil {
		return "", e
	}
	file, e := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(e, os.ErrExist) {
		old, x := readBoundedFullCDPArtifact(root, relative, browserruntime.MaxScreenshotBytes)
		if x != nil || !bytesEqual(old, c.PNG) {
			return "", agentBrowserUnavailable("browser artifact already contains different bytes")
		}
	} else if e != nil {
		return "", e
	} else if e = writeFullCDPArtifact(file, c.PNG); e != nil {
		return "", e
	}
	return "workspace:///" + filepath.ToSlash(relative), nil
}
