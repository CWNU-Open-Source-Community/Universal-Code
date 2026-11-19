package app

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// cancelRunLater drives the run to a terminal state from a second process
// handle while the stream under test is polling, mirroring how a scheduler
// or another terminal would drive a run externally. The delay must exceed
// the main goroutine's `run start` commit on slow CI.
func cancelRunLater(runID string, delay time.Duration, done chan<- string) {
	time.Sleep(delay)
	var out, errOut strings.Builder
	code := Execute([]string{"run", "cancel", runID}, &out, &errOut)
	done <- fmt.Sprintf("%s|code=%d", errOut.String(), code)
}

func TestRunEventsFollowStreamsUntilTerminal(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "run events follow")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("run id missing: %s", created)
	}

	done := make(chan string, 1)
	go cancelRunLater(runID, 700*time.Millisecond, done)
	stdout, stderr, code := executeTestCommand(t, "run", "events", runID, "--follow")
	if cancelReceipt := <-done; !strings.Contains(cancelReceipt, "code=0") {
		t.Fatalf("concurrent cancel failed: %s", cancelReceipt)
	}
	if code != 7 || !strings.Contains(stderr, "cancelled status") {
		t.Fatalf("follow exit drifted: code=%d stderr=%s", code, stderr)
	}
	records := decodeHeadlessCLIRecords(t, stdout)
	if len(records) < 2 {
		t.Fatalf("follow stream omitted records: %#v", records)
	}
	end := records[len(records)-1]
	if end.Kind != "stream.end" || end.Status != "cancelled" || !end.Terminal || end.ExitCode != 7 {
		t.Fatalf("follow end record drifted: %#v", end)
	}
	for index, record := range records[:len(records)-1] {
		if record.Kind != "run.event" || record.Sequence != int64(index+1) {
			t.Fatalf("follow event records must be contiguous: %#v", record)
		}
	}
}

func TestRunStartStreamRoutesReceiptToStderrAndStreams(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "run start stream")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("run id missing: %s", created)
	}

	done := make(chan string, 1)
	go cancelRunLater(runID, 1500*time.Millisecond, done)
	stdout, stderr, code := executeTestCommand(t, "run", "start", runID, "--stream")
	if cancelReceipt := <-done; !strings.Contains(cancelReceipt, "code=0") {
		t.Fatalf("concurrent cancel failed: %s", cancelReceipt)
	}
	if code != 7 {
		t.Fatalf("stream exit drifted: code=%d stderr=%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "{\"version\":\"headless.v1\"") {
		t.Fatalf("stdout must be a clean NDJSON stream, got %.80q", stdout)
	}
	if !strings.Contains(stderr, "run "+runID+" running") ||
		!strings.Contains(stderr, "run step") {
		t.Fatalf("stream mode must route the transition receipt to stderr: %q", stderr)
	}
	records := decodeHeadlessCLIRecords(t, stdout)
	end := records[len(records)-1]
	if end.Kind != "stream.end" || end.Status != "cancelled" || !end.Terminal {
		t.Fatalf("stream end record drifted: %#v", end)
	}
}

func TestRunEventsOneShotOutputUnchanged(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "run events one-shot")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	stdout, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || stderr != "" {
		t.Fatalf("one-shot events failed: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "#1\t") {
		t.Fatalf("one-shot events must keep the TSV shape: %q", stdout)
	}
	if strings.Contains(stdout, "headless.v1") {
		t.Fatalf("one-shot events must not switch to NDJSON: %q", stdout)
	}
}

func TestRunStreamFlagValidation(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "run stream validation")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)

	_, stderr, code = executeTestCommand(t, "run", "events")
	if code == 0 || !strings.Contains(stderr, "usage: cyberagent run events <run-id> [--follow]") {
		t.Fatalf("events usage must mention --follow: stderr=%q code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "run", "cancel", runID, "--stream")
	if code == 0 || !strings.Contains(stderr, "--stream is only valid for run start") {
		t.Fatalf("--stream on cancel must be rejected: stderr=%q code=%d", stderr, code)
	}
	// The stream flag must be accepted after the positional too; the
	// concurrent cancel gives the stream a terminal state to end on.
	done := make(chan string, 1)
	go cancelRunLater(runID, 1500*time.Millisecond, done)
	_, _, code = executeTestCommand(t, "run", "start", runID, "--stream")
	if cancelReceipt := <-done; !strings.Contains(cancelReceipt, "code=0") {
		t.Fatalf("concurrent cancel failed: %s", cancelReceipt)
	}
	if code != 7 {
		t.Fatalf("positional-then-flag stream parsing drifted: code=%d", code)
	}
}
