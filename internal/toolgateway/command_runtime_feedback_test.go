package toolgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
)

type commandFeedbackExecutor struct{ commandRuntimeExecutorStub }

func (e *commandFeedbackExecutor) ExecuteCommandRuntime(ctx context.Context, scope CommandRuntimeContext, input CommandRuntimeInput) (CommandRuntimeExecutionResult, error) {
	result, err := e.commandRuntimeExecutorStub.ExecuteCommandRuntime(ctx, scope, input)
	result.Pages[0].Frames[0].Text = "\x1b[32mhttp://localhost:3000/\x1b[0m http://127.0.0.1:3000/ https://example.com/ http://localhost:9000/?token=secret-value-1234567890 http://user:password@localhost:8080/"
	return result, err
}

func TestCommandRuntimeReturnsSanitizedUnverifiedServiceCandidates(t *testing.T) {
	state := &commandRuntimePolicyStore{trackedStructuredStore: newTrackedStructuredStore()}
	gateway := New(state, policy.NewDefaultChecker()).WithCommandRuntimeExecutor(&commandFeedbackExecutor{})
	outcome, err := gateway.Invoke(t.Context(), commandRuntimeToolCall(commandRuntimeValidPayload("Write-Output ok")))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		CandidateURLs []commandRuntimeURLCandidate `json:"candidate_urls"`
	}
	if err := json.Unmarshal([]byte(outcome.Result.Stdout), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.CandidateURLs) != 1 || response.CandidateURLs[0].URL != "http://127.0.0.1:3000/" || response.CandidateURLs[0].JobID != "command-job-1" || response.CandidateURLs[0].Source != "command_output" || response.CandidateURLs[0].Verified {
		t.Fatalf("candidate projection=%s", outcome.Result.Stdout)
	}
	if strings.Contains(outcome.Result.Stdout, "secret-value-1234567890") {
		t.Fatal("secret survived output sanitization")
	}
}

func TestCommandRuntimeCandidatesKeepPageJobIdentityAndBoundResults(t *testing.T) {
	result := CommandRuntimeExecutionResult{Jobs: []runner.CommandRuntimeJobSnapshot{{ID: "job-1"}, {ID: "job-2"}}, Pages: []runner.CommandRuntimeOutputPage{
		{JobID: "job-1", Frames: []runner.CommandRuntimeFrame{{Text: "http://localhost:3000/"}}},
		{JobID: "job-2", Frames: []runner.CommandRuntimeFrame{{Text: "http://localhost:3000/"}}},
		{JobID: "job-1", Frames: []runner.CommandRuntimeFrame{{Text: "http://localhost:3000/ http://[::1]:4000/"}}},
		{JobID: "other-job", Frames: []runner.CommandRuntimeFrame{{Text: "http://localhost:5000/"}}},
	}}
	got := commandRuntimeCandidateURLs(result)
	if len(got) != 3 || got[0].JobID != "job-1" || got[1].JobID != "job-2" || got[2].URL != "http://[::1]:4000/" {
		t.Fatalf("wrong page binding or deduplication: %+v", got)
	}
	result.Pages = append(result.Pages, runner.CommandRuntimeOutputPage{JobID: "job-1", Frames: []runner.CommandRuntimeFrame{{Text: strings.Repeat("http://localhost:1/ ", 50)}}})
	if got := commandRuntimeCandidateURLs(result); len(got) != 4 {
		t.Fatalf("duplicate candidates: %+v", got)
	}
	for port := 1000; port < 1020; port++ {
		result.Pages = append(result.Pages, runner.CommandRuntimeOutputPage{JobID: "job-2", Frames: []runner.CommandRuntimeFrame{{Text: fmt.Sprintf("http://localhost:%d/", port)}}})
	}
	if got := commandRuntimeCandidateURLs(result); len(got) != runner.MaxCommandServiceURLs {
		t.Fatalf("unbounded candidates: %d", len(got))
	}
}
