package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/webevidence"
)

type boundaryWebFetchBackend struct {
	body  string
	calls int
}

func (f *boundaryWebFetchBackend) Fetch(_ context.Context, target string, _ webevidence.NetworkAuthority, _ webevidence.RobotsPolicy) (webevidence.FetchedContent, error) {
	f.calls++
	return webevidence.FetchedContent{RequestedURL: target, FinalURL: target, HTTPStatus: http.StatusOK,
		RawDigest: webevidence.DigestBytes([]byte(f.body)), Robots: "allowed",
		Parsed: webevidence.ParsedDocument{Title: "Original multilingual evidence", Body: f.body, MIME: "text/plain", Charset: "utf-8"}}, nil
}

type boundaryWebPage struct {
	SourceID   string `json:"source_id"`
	SnapshotID string `json:"snapshot_id"`
	Body       string `json:"body"`
	BodyOffset int    `json:"body_offset"`
	NextOffset *int   `json:"next_offset"`
}

func latestBoundaryWebPage(t *testing.T, request llm.ChatRequest) (boundaryWebPage, string) {
	t.Helper()
	for i := len(request.Messages) - 1; i >= 0; i-- {
		for _, result := range request.Messages[i].ToolResults {
			var envelope struct{ Tool, Stdout string }
			var output struct{ Snapshot boundaryWebPage }
			if json.Unmarshal([]byte(result.Content), &envelope) == nil && envelope.Tool == "web_fetch" &&
				json.Unmarshal([]byte(envelope.Stdout), &output) == nil && output.Snapshot.SnapshotID != "" {
				return output.Snapshot, result.ToolCallID
			}
		}
	}
	t.Fatal("missing real model-visible page")
	return boundaryWebPage{}, ""
}

func TestThreadWebBoundaryRetainsActualMultilingualPageCursorAndRawHash(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "web-boundary-cursor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// At offset 1738 the original 2048-character page ends at 3786, while
	// the same conservative token policy exposes exactly 889 runes, to 2627.
	body := strings.Repeat("p", 1738) + strings.Repeat("中", 663) + strings.Repeat("a", 226) + "界" + strings.Repeat("中文🙂e\u0301正文。", 600)
	backend := &boundaryWebFetchBackend{body: body}
	search := &searchContextBackend{}
	provider := &boundaryJourneyProvider{}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Read a specified source section across an internal segment", Profile: "review", Interactive: true,
		ModelRoute: provider.Name() + "/model", NetworkMode: "allowlist", AllowedTargets: []string{"docs.example.com"},
		Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var preceding boundaryWebPage
	var precedingCallID string
	pageCall := func(id string, page boundaryWebPage, offset, limit int) *llm.ChatResponse {
		return toolResponse(id, "web_fetch", fmt.Sprintf(`{"version":"web_fetch.v1","source_id":%q,"snapshot_id":%q,"offset":%d,"limit":%d}`, page.SourceID, page.SnapshotID, offset, limit))
	}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index == 1:
			return toolResponse("boundary-search", "web_search", `{"version":"web_search.v1","query":"specified-source-section","limit":1}`), nil
		case index == 2:
			return toolResponse("boundary-fetch", "web_fetch", `{"version":"web_fetch.v1","url":"https://docs.example.com/actual-paper"}`), nil
		case index >= 3 && index < domain.MaxSupervisorToolRounds:
			page, _ := latestBoundaryWebPage(t, request)
			return pageCall(fmt.Sprintf("boundary-short-page-%d", index), page, 1024, 256), nil
		case index == domain.MaxSupervisorToolRounds:
			page, _ := latestBoundaryWebPage(t, request)
			return pageCall("boundary-multilingual-page", page, 1738, 2048), nil
		case index == domain.MaxSupervisorToolRounds+1:
			assertBoundaryPrompt(t, request)
			preceding, precedingCallID = latestBoundaryWebPage(t, request)
			if preceding.BodyOffset != 1738 || utf8.RuneCountInString(preceding.Body) != 889 || preceding.NextOffset == nil || *preceding.NextOffset != 2627 {
				return nil, fmt.Errorf("fixture did not expose the exact model cursor: offset=%d runes=%d next=%v", preceding.BodyOffset, utf8.RuneCountInString(preceding.Body), preceding.NextOffset)
			}
			return textResponse(rootActionResponse(domain.RootActionContinue, "Continue the specified section.", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+2:
			content := boundaryContextText(t, request)
			found := false
			for _, line := range strings.Split(content, "\n") {
				var entry struct {
					CallID         string `json:"call_id"`
					ResultSHA      string `json:"result_sha256"`
					Metadata       map[string]string
					OriginalResult domain.HistoryReadRequest `json:"original_result"`
					Result         struct{ Snapshot boundaryWebPage }
				}
				if json.Unmarshal([]byte(line), &entry) != nil || entry.CallID != precedingCallID {
					continue
				}
				found = true
				page := entry.Result.Snapshot
				if page.NextOffset == nil || *page.NextOffset != *preceding.NextOffset || page.BodyOffset != preceding.BodyOffset ||
					page.SourceID != preceding.SourceID || page.SnapshotID != preceding.SnapshotID ||
					(entry.Metadata["next_offset"] != "" && entry.Metadata["next_offset"] != strconv.Itoa(*preceding.NextOffset)) {
					return nil, fmt.Errorf("boundary replaced model cursor %d with raw cursor: %+v", *preceding.NextOffset, page)
				}
				rounds, err := st.ListRunSupervisorToolRoundsPage(ctx, run.ID, 0, domain.MaxSupervisorToolRounds+10)
				if err != nil {
					return nil, err
				}
				matched := false
				for _, round := range rounds {
					for _, call := range round.Calls {
						if call.CallID == precedingCallID {
							var rawEnvelope struct{ Stdout string }
							var rawOutput struct{ Snapshot boundaryWebPage }
							if json.Unmarshal([]byte(call.ResultJSON), &rawEnvelope) != nil || json.Unmarshal([]byte(rawEnvelope.Stdout), &rawOutput) != nil ||
								rawOutput.Snapshot.NextOffset == nil || *rawOutput.Snapshot.NextOffset != 3786 || entry.ResultSHA != session.ContentSHA256(call.ResultJSON) {
								return nil, fmt.Errorf("raw result/hash was rewritten instead of model projection")
							}
							matched = true
							read, err := st.ReadThreadHistory(ctx, run.ID, entry.OriginalResult)
							if err != nil || read.Record.CallID != call.CallID || read.Record.ResultSHA256 != entry.ResultSHA ||
								read.ContentSHA256 != entry.ResultSHA || !strings.HasPrefix(call.ResultJSON, read.Content) {
								return nil, fmt.Errorf("boundary original result reference cannot read its exact sealed bytes: %v", err)
							}
						}
					}
				}
				if !matched {
					return nil, fmt.Errorf("boundary identity does not reference a real stored call")
				}
			}
			if !found {
				return nil, fmt.Errorf("boundary omitted the current page cursor")
			}
			return pageCall("boundary-continuation", preceding, *preceding.NextOffset, 2048), nil
		case index == domain.MaxSupervisorToolRounds+3:
			page, _ := latestBoundaryWebPage(t, request)
			end := page.BodyOffset + utf8.RuneCountInString(page.Body)
			if page.BodyOffset != *preceding.NextOffset || preceding.Body+page.Body != string([]rune(body)[preceding.BodyOffset:end]) {
				return nil, fmt.Errorf("cross-segment read skipped or duplicated Unicode text")
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "The requested section was read.", "reply complete", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
		application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(false).
			WithWebEvidence(webevidence.NewService(st, search, backend)))
	input := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID),
		Content: "Read the specified multilingual section and preserve its place.", OperationKey: "web-boundary-one-input", RequestedBy: "test_operator"}
	result, err := turns.Execute(t.Context(), input)
	if err != nil || result.Submission.Message.Status != domain.OperatorSteeringCommitted || len(provider.Requests()) != domain.MaxSupervisorToolRounds+3 || search.calls != 1 || backend.calls != 1 {
		t.Fatalf("boundary journey: models=%d searches=%d HTTP-fetches=%d err=%v", len(provider.Requests()), search.calls, backend.calls, err)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
	if _, err := turns.Execute(t.Context(), input); err != nil || len(provider.Requests()) != domain.MaxSupervisorToolRounds+3 || search.calls != 1 || backend.calls != 1 {
		t.Fatal("same key replay executed again", err)
	}
}
