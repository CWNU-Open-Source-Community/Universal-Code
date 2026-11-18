package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/threadtranscript"
)

type imageLifecycleProvider struct{ lifecycleProvider }

func (*imageLifecycleProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: llm.VisionSupported, Source: "test_exact_model"}
}

type imageBoundaryProvider struct{ boundaryJourneyProvider }

func (*imageBoundaryProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: llm.VisionSupported, Source: "test_exact_model"}
}

func TestPureImageInputSurvivesApprovalAndToolBoundary(t *testing.T) {
	for _, approval := range []bool{true, false} {
		t.Run(fmt.Sprintf("approval_%t", approval), func(t *testing.T) {
			st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
			pixels := threadImagePNG(t, 96)
			image, err := st.SaveWorkspaceImage(t.Context(), "ws-tool-boundary", "pure-image-boundary-upload", "image/png", "input.png", pixels)
			if err != nil {
				t.Fatal(err)
			}
			input.Content = ""
			input.Images = []domain.ImageReference{{ID: image.ID, SHA256: image.SHA256}}
			p := &imageBoundaryProvider{}
			p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if !approval && index >= domain.MaxSupervisorToolRounds+2 {
					assertBoundaryDeliveredImage(t, request, pixels, image.SHA256, "")
				} else {
					assertRequestImage(t, request, pixels, image.SHA256, "")
				}
				if approval {
					switch index {
					case 1:
						return boundaryPropose("pure-image-proposal"), nil
					case 2:
						return textResponse(rootActionResponse(domain.RootActionWait, "Await review", "", "operator review required")), nil
					case 3:
						return textResponse(rootActionResponse(domain.RootActionFinish, "Review decision recorded", "done", "")), nil
					}
				} else {
					switch {
					case index <= domain.MaxSupervisorToolRounds:
						return boundaryRead(fmt.Sprintf("pure-image-read-%d", index), 1), nil
					case index == domain.MaxSupervisorToolRounds+1:
						assertBoundaryPrompt(t, request)
						return textResponse(rootActionResponse(domain.RootActionContinue, "Continue the bounded read", "", "")), nil
					case index == domain.MaxSupervisorToolRounds+2:
						return boundaryRead("pure-image-read-next", 1), nil
					case index == domain.MaxSupervisorToolRounds+3:
						return textResponse(rootActionResponse(domain.RootActionFinish, "Read complete", "done", "")), nil
					}
				}
				return nil, fmt.Errorf("unexpected request %d", index)
			}
			turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: approval})
			result, err := turns.Execute(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if approval {
				edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
				if err != nil || len(edits) != 1 || edits[0].Status != fileedit.StatusProposed {
					t.Fatalf("edits %v err=%v", edits, err)
				}
				if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edits[0].ID, Action: application.FileEditDeny}); err != nil {
					t.Fatal(err)
				}
				resume := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edits[0].ID})
				if resume.State != "completed" || !resume.ModelCalled || resume.ToolCalled || len(p.Requests()) != 3 {
					t.Fatalf("pure-image review did not resume exactly once: %#v", resume)
				}
			} else if len(p.Requests()) != domain.MaxSupervisorToolRounds+3 {
				t.Fatalf("boundary requests=%d", len(p.Requests()))
			}
			message, err := st.GetOperatorSteering(t.Context(), result.Submission.Message.ID)
			if err != nil || message.Content != "" || message.ImageCount != 1 || message.Status != domain.OperatorSteeringCommitted {
				t.Fatalf("original image input changed: %#v %v", message, err)
			}
			cp, found, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
			if err != nil || !found || cp.HasPendingInput() || cp.PendingImageCount != 0 {
				t.Fatalf("image input was not settled: %#v %v", cp, err)
			}
		})
	}
}

func TestThreadTurnImage1080pDefaultWindowAndOversizedImageFailure(t *testing.T) {
	for _, size := range []image.Point{{1920, 1080}, {8192, 2048}} {
		t.Run(fmt.Sprintf("%dx%d", size.X, size.Y), func(t *testing.T) {
			provider := &imageLifecycleProvider{lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionWait, "The image was received", "", "waiting")}}}
			st, turns, request, _, _ := threadFilesFixture(t, provider)
			request.Files = nil
			request.Content = "Inspect the screenshot without changing files"
			if size.X == 8192 {
				request.Content = ""
			}
			var out bytes.Buffer
			if err := png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))); err != nil {
				t.Fatal(err)
			}
			image, err := st.SaveWorkspaceImage(t.Context(), "ws-thread-files", "window-image-operation", "image/png", "screen.png", out.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			request.Images = []domain.ImageReference{{ID: image.ID, SHA256: image.SHA256}}
			result, err := turns.Execute(t.Context(), request)
			if size.X == 1920 {
				if err != nil || len(provider.requests) != 1 {
					t.Fatalf("ordinary 1080p image was not sent under default window: %v", err)
				}
				actual := provider.requests[0]
				if actual.Metadata["context_window_tokens"] != "32768" || actual.Metadata["context_history_omitted"] != "0" || len(actual.Tools) == 0 {
					t.Fatalf("fixture did not use ordinary tool/window contract: %v", actual.Metadata)
				}
				assertRequestImage(t, actual, out.Bytes(), image.SHA256, request.Content)
			} else {
				if err == nil || len(provider.requests) != 0 {
					t.Fatalf("oversized image reached model: calls=%d err=%v", len(provider.requests), err)
				}
				if result.Submission.Message.ID == "" {
					t.Fatal("failed image intent was not retained")
				}
				cp, found, err := st.GetSupervisorCheckpoint(t.Context(), result.Submission.Run.ID)
				if err != nil || !found || cp.PendingImageCount != 0 || cp.HasPendingInput() {
					t.Fatalf("failed pure image input was not sealed: %#v %v", cp, err)
				}
			}
		})
	}
}

type imagePressureProvider struct{ contextPressureToolProvider }

func (*imagePressureProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: llm.VisionSupported, Source: "test_exact_model"}
}

func TestThreadTurnImageSurvivesNativeToolPressureWithoutReplay(t *testing.T) {
	provider := &imagePressureProvider{contextPressureToolProvider{scriptedToolProvider: &scriptedToolProvider{responses: []*llm.ChatResponse{{Model: "model", ToolCalls: []llm.ToolCall{{ID: "image-read-once", Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"first.txt","start_line":1,"end_line":20}`)}}}, textResponse(rootActionResponse(domain.RootActionWait, "Read done", "", "waiting"))}}}}
	st, _, request, _, _ := threadFilesFixture(t, provider)
	request.Files = nil
	request.Content = "Read first.txt once and compare it with this screenshot"
	_, codeRun, createErr := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: request.Content, Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: "ws-thread-files", ModelRoute: provider.Name() + "/model", Interactive: true, NetworkMode: "disabled", Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 2}})
	if createErr != nil {
		t.Fatal(createErr)
	}
	request.ThreadID = domain.InitialThreadID(codeRun.ID)
	thread, err := st.GetThread(t.Context(), request.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(t.Context(), thread.ActiveRunID)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 6; index++ {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", fmt.Sprintf("Prior context %d ", index)+strings.Repeat("Historical image review observation. ", 180))); err != nil {
			t.Fatal(err)
		}
	}
	pixels := threadImagePNG(t, 77)
	image, err := st.SaveWorkspaceImage(t.Context(), "ws-thread-files", "image-pressure-upload-key", "image/png", "pressure.png", pixels)
	if err != nil {
		t.Fatal(err)
	}
	request.Images = []domain.ImageReference{{ID: image.ID, SHA256: image.SHA256}}
	ref := llm.ModelRef{Provider: provider.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(provider)
	provider.afterResponse = func(ctx context.Context, index int, req llm.ChatRequest) error {
		if index == 1 {
			estimate, err := strconv.Atoi(req.Metadata["context_input_estimate"])
			if err != nil {
				return err
			}
			return router.SetContextWindow(ref, llm.ContextWindow{ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: estimate - 1024 + 512 + 128, SafetyMarginTokens: 128, DefaultOutputTokens: 512, MaxOutputTokens: 512, Source: "image_pressure_test"})
		}
		return nil
	}
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(false))
	_, err = turns.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("unexpected model calls=%d", len(requests))
	}
	for _, req := range requests {
		assertRequestImage(t, req, pixels, image.SHA256, request.Content)
	}
	if summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || !found || summary.ID <= 0 {
		t.Fatalf("native image fixture did not compact under pressure: %v", err)
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 || rounds[0].Calls[0].Status != domain.SupervisorToolCompleted {
		t.Fatalf("actual read did not execute exactly once: %#v %v", rounds, err)
	}
	callID := rounds[0].Calls[0].CallID
	var calls, results int
	for _, message := range requests[1].Messages {
		for _, call := range message.ToolCalls {
			if call.ID == callID {
				calls++
			}
		}
		for _, result := range message.ToolResults {
			if result.ToolCallID == callID {
				results++
				if !strings.Contains(result.Content, "fixture first.txt") {
					t.Fatal("actual tool observation was lost")
				}
			}
		}
	}
	if calls != 1 || results != 1 || requests[1].Metadata["context_history_omitted"] != "0" {
		t.Fatalf("native pair lost/replayed: call=%d result=%d", calls, results)
	}
}

func threadImagePNG(t *testing.T, red uint8) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	im.Set(1, 1, color.NRGBA{R: red, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestThreadTurnImagesOriginalBytesPureImageReplayAndHistory(t *testing.T) {
	for _, content := range []string{"Describe this screenshot", ""} {
		t.Run(map[bool]string{true: "image-only", false: "text-and-image"}[content == ""], func(t *testing.T) {
			provider := &imageLifecycleProvider{lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionWait, "Image received", "", "waiting for your next message"), rootActionResponse(domain.RootActionWait, "Follow-up received", "", "waiting")}}}
			st, turns, request, _, path := threadFilesFixture(t, provider)
			request.Files = nil
			request.Content = content
			request.RequestedBy = "http_thread_operator"
			pixels := threadImagePNG(t, 212)
			image, err := st.SaveWorkspaceImage(t.Context(), "ws-thread-files", "image-upload-original", "image/png", "截图.png", pixels)
			if err != nil {
				t.Fatal(err)
			}
			request.Images = []domain.ImageReference{{ID: image.ID, SHA256: image.SHA256}}
			result, err := turns.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Submission.Message.Content != content || result.Submission.Message.ImageCount != 1 || result.Submission.Message.Status != domain.OperatorSteeringCommitted {
				t.Fatalf("unexpected original message: %#v", result.Submission.Message)
			}
			assertRequestImage(t, provider.requests[0], pixels, image.SHA256, content)
			history, err := st.ListSessionMessages(t.Context(), result.Submission.Session.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			users := 0
			evidence := 0
			for _, msg := range history {
				if msg.Role == "user" {
					users++
					if msg.Content != content {
						t.Fatalf("synthetic user content=%q", msg.Content)
					}
				}
				if msg.Provenance.SourceKind == session.SourceWorkspaceImage {
					evidence++
					if msg.Provenance.InstructionAuthorized || !strings.Contains(msg.Content, image.SHA256) {
						t.Fatal("image descriptor lost immutable non-authorizing evidence")
					}
				}
			}
			if users != 1 || evidence != 1 {
				t.Fatalf("users=%d evidence=%d", users, evidence)
			}
			source, err := st.ListThreadTranscriptSourceBefore(t.Context(), request.ThreadID, 0, 0, 101)
			if err != nil {
				t.Fatal(err)
			}
			items, err := threadtranscript.Build(request.ThreadID, source)
			if err != nil {
				t.Fatal(err)
			}
			projected := 0
			for _, item := range items {
				if item.SourceRef == result.Submission.Message.ID {
					projected++
				}
			}
			if projected != 1 {
				t.Fatalf("original transcript input count=%d", projected)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			beforeObservation, err := reopened.ExportThread(t.Context(), request.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			readAPI, err := httpapi.New(reopened, httpapi.Config{AccessToken: "image-observation-read-token-0001"})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				lookup := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/v1/threads/"+request.ThreadID+"/turn-request", nil)
				lookup.RemoteAddr = "127.0.0.1:45000"
				lookup.Header.Set("Authorization", "Bearer image-observation-read-token-0001")
				lookup.Header.Set("Idempotency-Key", request.OperationKey)
				observed := httptest.NewRecorder()
				readAPI.ServeHTTP(observed, lookup)
				var envelope struct {
					Data httpapi.ThreadRequestObservationView `json:"data"`
				}
				if err := json.Unmarshal(observed.Body.Bytes(), &envelope); err != nil || observed.Code != http.StatusOK || envelope.Data.State != "completed" || !envelope.Data.Settled || envelope.Data.MessageID != result.Submission.Message.ID || envelope.Data.ThreadID != request.ThreadID {
					t.Fatalf("reopened original image request GET failed: %d %s %v", observed.Code, observed.Body.String(), err)
				}
			}
			afterObservation, err := reopened.ExportThread(t.Context(), request.ThreadID)
			afterObservation.ExportedAt = beforeObservation.ExportedAt
			if err != nil || !reflect.DeepEqual(beforeObservation, afterObservation) || len(provider.requests) != 1 {
				t.Fatalf("image recovery GET mutated or executed: %v", err)
			}
			turns = newThreadFilesService(reopened, provider)
			replayed, err := turns.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !replayed.Replayed || len(provider.requests) != 1 {
				t.Fatalf("original request reexecuted: replay=%t calls=%d", replayed.Replayed, len(provider.requests))
			}
			other, err := reopened.SaveWorkspaceImage(t.Context(), "ws-thread-files", "image-other-identity-key", "image/png", "same-bytes.png", pixels)
			if err != nil {
				t.Fatal(err)
			}
			changed := request
			changed.Images = []domain.ImageReference{{ID: other.ID, SHA256: other.SHA256}}
			if _, err := turns.Execute(t.Context(), changed); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict || len(provider.requests) != 1 {
				t.Fatalf("same turn key accepted different image identity: %v", err)
			}
			request.OperationKey = "image-followup-operation"
			request.Content = "Continue using the preceding screenshot"
			request.Images = nil
			if _, err := turns.Execute(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			assertRequestImage(t, provider.requests[1], pixels, image.SHA256, content)
		})
	}
}

func assertBoundaryDeliveredImage(t *testing.T, request llm.ChatRequest, pixels []byte, sha, accepted string) {
	t.Helper()
	for _, message := range request.Messages {
		if len(message.Images) == 0 {
			continue
		}
		prefix, encoded, found := strings.Cut(message.Content, "\n")
		var delivery struct {
			Version       string  `json:"version"`
			Delivery      string  `json:"delivery"`
			AcceptedInput *string `json:"accepted_input"`
		}
		if !found || !strings.HasPrefix(prefix, "Harness input delivery:") ||
			json.Unmarshal([]byte(encoded), &delivery) != nil ||
			delivery.Version != "supervisor_input_delivery.v1" || delivery.Delivery != "tool_boundary_continuation" ||
			delivery.AcceptedInput == nil || *delivery.AcceptedInput != accepted {
			t.Fatalf("boundary image lost its exact accepted input: %q", message.Content)
		}
		assertRequestImage(t, request, pixels, sha, message.Content)
		return
	}
	t.Fatal("boundary request lost the original image")
}

func assertRequestImage(t *testing.T, request llm.ChatRequest, pixels []byte, sha, content string) {
	t.Helper()
	count := 0
	for _, message := range request.Messages {
		for _, part := range message.Images {
			count++
			if !bytes.Equal(part.Data, pixels) || part.SHA256 != sha || message.Role != "user" || message.Content != content {
				t.Fatalf("image source/pixels changed: %#v content=%q", part, message.Content)
			}
		}
	}
	if count != 1 {
		t.Fatalf("model image count=%d", count)
	}
	raw, _ := json.Marshal(request)
	if bytes.Contains(raw, []byte("data:image")) {
		t.Fatal("image leaked into diagnostic JSON")
	}
}

func TestThreadTurnImagesRejectDifferentHashForeignWorkspaceAndUnknownVision(t *testing.T) {
	provider := &lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionWait, "unexpected", "", "wait")}}
	st, turns, request, _, _ := threadFilesFixture(t, provider)
	request.Files = nil
	pixels := threadImagePNG(t, 34)
	image, err := st.SaveWorkspaceImage(t.Context(), "ws-thread-files", "image-upload-validation", "image/png", "screenshot.png", pixels)
	if err != nil {
		t.Fatal(err)
	}
	request.Images = []domain.ImageReference{{ID: image.ID, SHA256: image.SHA256}}
	if _, err := turns.Execute(t.Context(), request); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeFailedPrecondition {
		t.Fatalf("vision gate err=%v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatal("unsupported model was called")
	}
	if _, err := st.SaveWorkspaceImage(t.Context(), "ws-thread-files", "image-upload-validation", "image/png", "screenshot.png", threadImagePNG(t, 35)); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("upload key accepted different bytes: %v", err)
	}
	request.OperationKey = "image-bad-hash-operation"
	request.Images[0].SHA256 = strings.Repeat("f", 64)
	if _, err := turns.Execute(t.Context(), request); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("wrong image hash accepted: %v", err)
	}
	if _, _, err := st.GetWorkspaceImage(t.Context(), "other-workspace", image.ID); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
		t.Fatalf("foreign workspace read accepted: %v", err)
	}
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "ws-other-images", Name: "other", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	foreign, err := st.SaveWorkspaceImage(t.Context(), "ws-other-images", "foreign-image-operation", "image/png", "foreign.png", pixels)
	if err != nil {
		t.Fatal(err)
	}
	request.OperationKey = "foreign-image-turn-operation"
	request.Images = []domain.ImageReference{{ID: foreign.ID, SHA256: foreign.SHA256}}
	if _, err := turns.Execute(t.Context(), request); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound || len(provider.requests) != 0 {
		t.Fatalf("foreign image was accepted into Thread: %v", err)
	}
}

func TestThreadImageHistoryBeyond64PreservesBindingsAndExplicitCoverage(t *testing.T) {
	provider := &imageLifecycleProvider{}
	for range 18 {
		provider.responses = append(provider.responses, rootActionResponse(domain.RootActionWait, "Image input received", "", "waiting"))
	}
	st, _, request, _, _ := threadFilesFixture(t, provider)
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "Review saved screenshots", Profile: "review", WorkspaceID: "ws-thread-files", ModelRoute: provider.Name() + "/model", Interactive: true, Budget: domain.Budget{MaxTurns: 64}})
	if err != nil {
		t.Fatal(err)
	}
	request.ThreadID = domain.InitialThreadID(run.ID)
	request.Files = nil
	for index := range 4 {
		image, err := st.SaveWorkspaceImage(t.Context(), "ws-thread-files", fmt.Sprintf("long-history-image-%d", index), "image/png", fmt.Sprintf("image-%d.png", index), threadImagePNG(t, uint8(index+1)))
		if err != nil {
			t.Fatal(err)
		}
		request.Images = append(request.Images, domain.ImageReference{ID: image.ID, SHA256: image.SHA256})
	}
	// Keep this image-history fixture on the extractive path: its finite script
	// supplies ordinary replies, not generated-summary responses.
	turns := toolBoundaryService(st, st, provider)
	var messageIDs []string
	for index := range 17 {
		request.OperationKey = fmt.Sprintf("long-image-turn-%d", index)
		request.Content = fmt.Sprintf("Review screenshot group %d", index)
		result, err := turns.Execute(t.Context(), request)
		if err != nil {
			t.Fatalf("image turn %d: %v", index, err)
		}
		messageIDs = append(messageIDs, result.Submission.Message.ID)
	}
	// Recent text history causes the remaining image-bearing inputs to compact;
	// no raw image row or message binding is removed by that projection.
	for index := range 22 {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", fmt.Sprintf("Later text observation %d: %s", index, compactionFixtureObservation()))); err != nil {
			t.Fatal(err)
		}
	}
	request.OperationKey = "long-image-text-followup"
	request.Content = "Summarize available observations and explicitly state missing visual context"
	request.Images = nil
	if _, err := turns.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 18 {
		t.Fatalf("unexpected model requests=%d", len(provider.requests))
	}
	final := provider.requests[17]
	pixels, descriptors, omitted := 0, 0, 0
	for _, message := range final.Messages {
		pixels += len(message.Images)
		if strings.HasPrefix(message.Content, "Earlier image descriptor only:") {
			descriptors++
			if !strings.Contains(message.Content, `"pixels_included":false`) || !strings.Contains(message.Content, `"instruction_authorized":false`) {
				t.Fatal("old image claimed visual or instruction authority")
			}
		}
		if strings.HasPrefix(message.Content, "Image history coverage:") {
			if _, err := fmt.Sscanf(message.Content, "Image history coverage: %d older image references", &omitted); err != nil {
				t.Fatal(err)
			}
		}
	}
	if pixels != 0 || descriptors != 32 || omitted != 36 || pixels+descriptors+omitted != 68 || final.Metadata["context_history_omitted"] != "0" {
		t.Fatalf("wrong bounded image coverage: pixels=%d descriptors=%d omitted=%d metadata=%v", pixels, descriptors, omitted, final.Metadata)
	}
	for _, id := range messageIDs {
		images, err := st.ListOperatorMessageImages(t.Context(), run.ID, id)
		if err != nil || len(images) != 4 {
			t.Fatalf("raw image bindings lost: %s len=%d err=%v", id, len(images), err)
		}
	}
	if summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || !found || summary.ID <= 0 {
		t.Fatalf("no real compaction occurred: %v", err)
	}
}
