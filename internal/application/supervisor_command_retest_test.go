package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

func TestSupervisorCommandRerunsSamePayloadAfterEdit(t *testing.T) {
	f := newCommandApprovalFixture(t, domain.RunExecutionPermissionAuto, false)
	if f.service.checkpoints == nil {
		t.Fatal("retest fixture requires the durable workspace checkpoint boundary")
	}
	input := commandApprovalNativeInput(t, false)
	input.Commands[0].Arguments[1] = `const fs=require('fs');fs.appendFileSync('count.txt','1');const value=fs.readFileSync('source.txt','utf8');process.stdout.write(value);process.exit(value==='fixed'?0:1);`
	for round, source := range []string{"broken", "fixed"} {
		if err := os.WriteFile(filepath.Join(f.root, "source.txt"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		f.record(t, input, round+1)
		if waiting, err := f.resume(t); err != nil || !waiting {
			t.Fatalf("round %d review: waiting=%t err=%v", round+1, waiting, err)
		}
		f.decide(t, ApprovalControlApproveOnce)
		if waiting, err := f.resume(t); err != nil || waiting {
			t.Fatalf("round %d execution: waiting=%t err=%v", round+1, waiting, err)
		}
		call, _, err := f.st.GetSupervisorApprovalCall(t.Context(), f.call.RunID, f.call.CallID)
		if err != nil || call.Status != domain.SupervisorToolCompleted || !strings.Contains(call.ResultJSON, source) {
			t.Fatalf("round %d did not execute the current file: %+v err=%v", round+1, call, err)
		}
		if waiting, err := f.resume(t); err != nil || waiting {
			t.Fatalf("round %d replay: waiting=%t err=%v", round+1, waiting, err)
		}
	}
	count, err := os.ReadFile(filepath.Join(f.root, "count.txt"))
	if err != nil || string(count) != "11" {
		t.Fatalf("logical calls did not each execute exactly once: %q err=%v", count, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.call.RunID, Limit: 10})
	if err != nil || len(jobs) != 2 || jobs[0].ID == jobs[1].ID {
		t.Fatalf("retest did not get its own durable job: %+v err=%v", jobs, err)
	}
	failed, completed := 0, 0
	for _, job := range jobs {
		if job.State == runner.CommandRuntimeJobFailed {
			failed++
		}
		if job.State == runner.CommandRuntimeJobCompleted {
			completed++
		}
	}
	if failed != 1 || completed != 1 {
		t.Fatalf("test did not observe the repair: failed=%d completed=%d", failed, completed)
	}
	boundaries, err := f.st.ListWorkspaceCheckpointTransactions(t.Context(), f.call.RunID, 10)
	if err != nil || len(boundaries) != 2 {
		t.Fatalf("logical calls did not each own a checkpoint boundary: %+v err=%v", boundaries, err)
	}
}
