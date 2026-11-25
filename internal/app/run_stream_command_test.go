package app

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func streamUntilCancelled(t *testing.T, runID string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	var stderr strings.Builder
	done := make(chan int, 1)
	go func() {
		code := ExecuteContext(ctx, args, writer, &stderr)
		writer.Close()
		done <- code
	}()

	// Reading the first record establishes that the stream is open before
	// another CLI invocation cancels the run.
	output := bufio.NewReader(reader)
	first, err := output.ReadString('\n')
	if err != nil {
		t.Fatalf("stream ended before the first record: code=%d stderr=%s err=%v", <-done, &stderr, err)
	}
	var cancelErr strings.Builder
	if code := ExecuteContext(ctx, []string{"run", "cancel", runID}, io.Discard, &cancelErr); code != 0 {
		t.Errorf("run cancel failed: code=%d stderr=%s", code, &cancelErr)
		cancel()
	}
	rest, err := io.ReadAll(output)
	code := <-done
	if err != nil {
		t.Fatal(err)
	}
	return first + string(rest), stderr.String(), code
}

func requireRunStillCreated(t *testing.T, runID string) {
	t.Helper()
	stdout, stderr, code := executeTestCommand(t, "run", "show", runID)
	if code != 0 || !strings.Contains(stdout, "\nstatus: created\n") {
		t.Fatalf("expected created run: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
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

	stdout, stderr, code := streamUntilCancelled(t, runID, "run", "events", runID, "--follow")
	if code != 7 || !strings.Contains(stderr, "cancelled status") {
		t.Fatalf("follow exit drifted: code=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, "run "+runID+" ") {
		t.Fatalf("following an existing run printed a transition receipt: %q", stderr)
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

	stdout, stderr, code := streamUntilCancelled(t, runID, "run", "start", runID, "--stream")
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
	for _, flag := range []string{"--stream", "--stream=false"} {
		_, stderr, code = executeTestCommand(t, "run", "cancel", runID, flag)
		if code != 2 || !strings.Contains(stderr, "--stream is only valid for run start") {
			t.Fatalf("run cancel %s: stderr=%q code=%d", flag, stderr, code)
		}
	}
	requireRunStillCreated(t, runID)
}

func TestRunEventsFollowTimeoutEmitsDeadlineRecord(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "run events timeout")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	// The run is never driven, so the stream ends on the deadline alone —
	// deterministic without a concurrent driver.
	stdout, stderr, code := executeTestCommand(t, "run", "events", runID, "--follow", "--timeout", "300ms")
	if code != 9 || !strings.Contains(stderr, "deadline") {
		t.Fatalf("follow deadline exit drifted: code=%d stderr=%s", code, stderr)
	}
	records := decodeHeadlessCLIRecords(t, stdout)
	end := records[len(records)-1]
	if end.Kind != "stream.end" || end.Reason != "deadline_exceeded" || end.ExitCode != 9 || end.Terminal {
		t.Fatalf("deadline end record drifted: %#v", end)
	}
}

func TestRunStreamTimeoutValidation(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "run stream timeout validation")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)

	_, stderr, code = executeTestCommand(t, "run", "events", runID, "--timeout", "1s")
	if code == 0 || !strings.Contains(stderr, "timeout requires --follow") {
		t.Fatalf("timeout without follow must be rejected: stderr=%q code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "run", "events", runID, "--follow", "--timeout", "25h")
	if code == 0 || !strings.Contains(stderr, "between zero and 24h") {
		t.Fatalf("oversized timeout must be rejected: stderr=%q code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "run", "start", runID, "--timeout", "30m")
	if code == 0 || !strings.Contains(stderr, "timeout requires --stream") {
		t.Fatalf("timeout without stream must be rejected: stderr=%q code=%d", stderr, code)
	}
	// An explicit zero timeout on a non-start transition must be a usage
	// error, not a silent plain cancel.
	_, stderr, code = executeTestCommand(t, "run", "cancel", runID, "--timeout=0")
	if code != 2 || !strings.Contains(stderr, "flag provided but not defined") {
		t.Fatalf("--timeout on cancel must be a usage error with exit code 2: stderr=%q code=%d", stderr, code)
	}
	// A rejected timeout must never transition the run.
	_, stderr, code = executeTestCommand(t, "run", "events", runID)
	if code != 0 || !strings.Contains(stdoutOnce(t, runID), "created") {
		t.Fatalf("run status must stay created after rejections")
	}
}

func stdoutOnce(t *testing.T, runID string) string {
	t.Helper()
	stdout, _, _ := executeTestCommand(t, "run", "show", runID)
	return stdout
}
