package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

type BrowserModelScreenshot struct {
	RunID, SessionID, CallID, CanonicalURL string
	Image                                  llm.ImagePart
}

// The immutable call, rather than a model-provided path, determines which
// saved image may be read. This grants no new browser or execution authority.
type browserScreenshotStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
}

func (s *FullCDPProductionService) ReadModelScreenshot(ctx context.Context, checkpoint domain.SupervisorCheckpoint, callID string) (BrowserModelScreenshot, error) {
	return readBrowserModelScreenshot(ctx, s.store, checkpoint, callID, false)
}
func (s *AgentBrowserService) ReadModelScreenshot(ctx context.Context, checkpoint domain.SupervisorCheckpoint, callID string) (BrowserModelScreenshot, error) {
	return readBrowserModelScreenshot(ctx, s.store, checkpoint, callID, true)
}
func readBrowserModelScreenshot(ctx context.Context, st browserScreenshotStore, checkpoint domain.SupervisorCheckpoint, callID string, agent bool) (BrowserModelScreenshot, error) {
	fail := func() (BrowserModelScreenshot, error) {
		return BrowserModelScreenshot{}, apperror.New(apperror.CodeFailedPrecondition, "saved browser screenshot does not match the current tool evidence")
	}
	if st == nil || checkpoint.Validate() != nil || checkpoint.AttemptID == "" || callID == "" {
		return fail()
	}
	reader, ok := st.(interface {
		ListSupervisorToolRounds(context.Context, domain.SupervisorCheckpoint) ([]domain.SupervisorToolRound, error)
	})
	if !ok {
		return fail()
	}
	rounds, err := reader.ListSupervisorToolRounds(ctx, checkpoint)
	if err != nil {
		return BrowserModelScreenshot{}, err
	}
	var matched *domain.SupervisorToolCall
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.CallID == callID {
				if matched != nil {
					return fail()
				}
				copy := call
				matched = &copy
			}
		}
	}
	if matched == nil {
		return fail()
	}
	call := *matched
	if call.Validate() != nil || call.RunID != checkpoint.RunID || call.Turn != checkpoint.NextTurn || call.AttemptID != checkpoint.AttemptID || call.ToolName != string(toolgateway.BrowserScreenshotTool) || call.Status != domain.SupervisorToolCompleted || call.ErrorCode != "" {
		return fail()
	}
	return readCompletedBrowserScreenshot(ctx, st, call, agent)
}

func readCompletedBrowserScreenshot(ctx context.Context, st browserScreenshotStore, call domain.SupervisorToolCall, agent bool) (BrowserModelScreenshot, error) {
	fail := func() (BrowserModelScreenshot, error) {
		return BrowserModelScreenshot{}, agentBrowserUnavailable("saved browser screenshot does not match its completed tool evidence")
	}
	if call.Validate() != nil || call.ToolName != string(toolgateway.BrowserScreenshotTool) || call.Status != domain.SupervisorToolCompleted || call.ErrorCode != "" {
		return fail()
	}
	authority, err := toolgateway.DecodeBrowserActionCallAuthority(json.RawMessage(call.AuthorityJSON))
	var agentAuthority toolgateway.AgentBrowserCallAuthority
	if agent {
		agentAuthority, err = toolgateway.DecodeAgentBrowserAuthority(json.RawMessage(call.AuthorityJSON))
		authority.RunID = agentAuthority.RunID
		authority.RootAgentID = agentAuthority.RootAgentID
		authority.SessionID = agentAuthority.SessionID
		authority.MissionID = agentAuthority.MissionID
		authority.WorkspaceID = agentAuthority.WorkspaceID
	}
	if err != nil || authority.RunID != call.RunID || call.AgentID != authority.RootAgentID || call.AgentAttemptID != call.AttemptID {
		return fail()
	}
	run, err := st.GetRun(ctx, call.RunID)
	if err != nil {
		return BrowserModelScreenshot{}, err
	}
	mission, err := st.GetMission(ctx, run.MissionID)
	if err != nil {
		return BrowserModelScreenshot{}, err
	}
	if authority.MissionID != run.MissionID || authority.SessionID != run.SessionID || authority.WorkspaceID != mission.WorkspaceID {
		return fail()
	}
	var envelope supervisorToolResultEnvelope
	// Decode named wire fields explicitly; do not interpret arbitrary locators.
	var output struct {
		Version       string `json:"version"`
		SessionID     string `json:"session_id"`
		DocumentEpoch uint64 `json:"document_epoch"`
		CanonicalURL  string `json:"canonical_url"`
		MediaType     string `json:"media_type"`
		SHA256        string `json:"sha256"`
		Artifact      string `json:"artifact_locator"`
		Bytes         int    `json:"bytes"`
	}
	if json.Unmarshal([]byte(call.ResultJSON), &envelope) != nil || envelope.Version != supervisorToolResultVersion || envelope.Tool != call.ToolName || envelope.Status != "completed" || envelope.Code != "" || envelope.Truncated || json.Unmarshal([]byte(envelope.Stdout), &output) != nil {
		return fail()
	}
	pageURL, urlErr := url.Parse(output.CanonicalURL)
	commonValid := output.MediaType == "image/png" && output.Bytes > 0 && output.Bytes <= browserruntime.MaxScreenshotBytes && envelope.Metadata["artifact_locator"] == output.Artifact && envelope.Metadata["artifact_sha256"] == output.SHA256 && envelope.Metadata["artifact_bytes"] == strconv.Itoa(output.Bytes)
	if !commonValid {
		return fail()
	}
	if agent {
		if _, e := toolgateway.NormalizeAgentBrowserURL(output.CanonicalURL); e != nil || output.Version != "browser_screenshot_result.v2" || output.SessionID != agentAuthority.BrowserSessionID || output.DocumentEpoch == 0 || envelope.Metadata["agent_browser_session_id"] != output.SessionID || envelope.Metadata["manager_boot_id"] != agentAuthority.ManagerBootID || envelope.Metadata["canonical_url"] != output.CanonicalURL || envelope.Metadata["document_epoch"] != strconv.FormatUint(output.DocumentEpoch, 10) {
			return fail()
		}
	} else if urlErr != nil || pageURL.Scheme+"://"+pageURL.Host != authority.TargetOrigin || output.Version != "browser_screenshot_result.v1" || envelope.Metadata["full_cdp_session_id"] != authority.FullCDPSessionID || envelope.Metadata["target_origin"] != authority.TargetOrigin {
		return fail()
	}

	// Legacy completed calls used the turn/payload identity. New calls also
	// include CallID so a second screenshot after an interaction is distinct.
	key := supervisorBrowserScreenshotOperationKey(call)
	relative := fullCDPScreenshotRelativePath(call.RunID, key)
	if output.Artifact != "workspace:///"+filepath.ToSlash(relative) {
		if agent {
			return fail()
		}
		legacy := fullCDPScreenshotRelativePath(call.RunID, supervisorToolOperationKey(call.RunID, call.Turn, toolgateway.BrowserScreenshotTool, json.RawMessage(call.PayloadJSON)))
		if output.Artifact != "workspace:///"+filepath.ToSlash(legacy) {
			return fail()
		}
		relative = legacy
	}
	workspaceStore, ok := st.(fullCDPWorkspaceInfoStore)
	if !ok {
		return fail()
	}
	workspace, err := workspaceStore.GetWorkspaceInfo(ctx, authority.WorkspaceID)
	if err != nil {
		return BrowserModelScreenshot{}, err
	}
	if workspace.ID != authority.WorkspaceID || !filepath.IsAbs(workspace.RootPath) {
		return fail()
	}
	root, err := os.OpenRoot(workspace.RootPath)
	if err != nil {
		return BrowserModelScreenshot{}, apperror.Wrap(apperror.CodeUnavailable, "saved screenshot workspace is unavailable", err)
	}
	defer root.Close()
	content, err := readBoundedFullCDPArtifact(root, relative, browserruntime.MaxScreenshotBytes)
	if err != nil {
		return BrowserModelScreenshot{}, apperror.Wrap(apperror.CodeUnavailable, "saved screenshot bytes are unavailable", err)
	}
	actual, err := imageattachment.Validate(content, output.MediaType, "")
	if err != nil || actual.SHA256 != output.SHA256 || actual.ByteSize != output.Bytes {
		return fail()
	}
	return BrowserModelScreenshot{RunID: run.ID, SessionID: run.SessionID, CallID: call.CallID, CanonicalURL: output.CanonicalURL,
		Image: llm.ImagePart{MediaType: actual.MIMEType, Data: content, SHA256: actual.SHA256, Width: actual.Width, Height: actual.Height}}, nil
}

func fullCDPScreenshotRelativePath(runID, operationKey string) string {
	digest := sha256.Sum256([]byte(operationKey))
	return filepath.Join(fullCDPBrowserArtifactDirectory, runID, hex.EncodeToString(digest[:])+".png")
}

func supervisorBrowserScreenshotOperationKey(call domain.SupervisorToolCall) string {
	base := supervisorToolOperationKey(call.RunID, call.Turn, toolgateway.BrowserScreenshotTool, json.RawMessage(call.PayloadJSON))
	digest := sha256.Sum256([]byte(base + "\x00" + call.CallID))
	return hex.EncodeToString(digest[:])
}

// Enrich the existing user-role tool-result message. Native call/result pairs
// stay intact; pixels are non-authorizing evidence and enter the normal budget.
func (s *AgentRunner) supervisorBrowserImages(ctx context.Context, checkpoint domain.SupervisorCheckpoint, ref llm.ModelRef, request llm.ChatRequest, rounds []domain.SupervisorToolRound) (llm.ChatRequest, error) {
	calls := map[string]domain.SupervisorToolCall{}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.ToolName == string(toolgateway.BrowserScreenshotTool) && call.Status == domain.SupervisorToolCompleted {
				calls[call.CallID] = call
			}
		}
	}
	if len(calls) == 0 {
		return request, nil
	}
	totalBytes, err := supervisorImageBytes(request)
	if err != nil {
		return llm.ChatRequest{}, err
	}
	request.Messages = append([]llm.Message(nil), request.Messages...)
	vision := s.router.DescribeVision(ref)
	for index := len(request.Messages) - 1; index >= 0; index-- {
		message := request.Messages[index]
		if message.Role != "user" || len(message.ToolResults) == 0 {
			continue
		}
		for resultIndex := len(message.ToolResults) - 1; resultIndex >= 0; resultIndex-- {
			result := message.ToolResults[resultIndex]
			call, ok := calls[result.ToolCallID]
			if !ok {
				continue
			}
			if result.IsError || result.Content != call.ResultJSON {
				return llm.ChatRequest{}, apperror.New(apperror.CodeFailedPrecondition, "browser screenshot context lost its exact tool result")
			}
			note := map[string]any{"source_kind": "browser_screenshot", "source_ref": call.CallID, "run_id": call.RunID, "instruction_authorized": false, "untrusted_evidence": true, "vision_capability": vision.State, "pixels_supplied": false}
			if vision.State == llm.VisionSupported {
				var value BrowserModelScreenshot
				var err error
				if toolgateway.IsAgentBrowserPayload(json.RawMessage(call.PayloadJSON)) {
					if s.agentBrowser == nil {
						return llm.ChatRequest{}, agentBrowserUnavailable("Agent browser screenshot reader unavailable")
					}
					value, err = s.agentBrowser.ReadModelScreenshot(ctx, checkpoint, call.CallID)
				} else {
					if s.browserActions == nil {
						return llm.ChatRequest{}, agentBrowserUnavailable("legacy browser screenshot reader unavailable")
					}
					value, err = s.browserActions.ReadModelScreenshot(ctx, checkpoint, call.CallID)
				}
				if err != nil {
					return llm.ChatRequest{}, err
				}
				note["sha256"], note["canonical_url"] = value.Image.SHA256, value.CanonicalURL
				if len(message.Images) < llm.MaxMessageImages && len(value.Image.Data) <= llm.MaxRequestImageBytes-totalBytes {
					message.Images = append(append([]llm.ImagePart(nil), message.Images...), value.Image)
					totalBytes += len(value.Image.Data)
					note["pixels_supplied"] = true
				} else {
					note["limitation"] = "Image pixels were omitted to stay within the model request image bound. The saved screenshot and exact tool result remain available. Do not claim visual inspection of this image."
				}
			} else {
				note["limitation"] = "Image pixels were not supplied because this model's vision support is not established. Do not claim visual inspection; use the recorded page text or a supported model."
			}
			encoded, _ := json.Marshal(note)
			message.Content += "\n" + string(encoded)
		}
		if err := llm.ValidateMessageImages(message); err != nil {
			return llm.ChatRequest{}, apperror.Wrap(apperror.CodeResourceExhausted, "browser screenshot image input exceeds the model image bound", err)
		}
		request.Messages[index] = message
	}
	return request, nil
}
