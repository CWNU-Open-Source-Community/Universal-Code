package app

import (
	"bufio"
	"context"
	"io"
	"strconv"
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

func TestRunStreamTimeoutEmitsResumableDeadlineRecord(t *testing.T) {
	for _, tc := range []struct{ action, flag, timeout string }{
		{"events", "--follow", "300ms"},
		{"start", "--stream", "1ns"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			t.Setenv("CYBERAGENT_HOME", t.TempDir())
			created, stderr, code := executeTestCommand(t, "run", "create", "run stream timeout")
			if code != 0 {
				t.Fatal(stderr)
			}
			runID := runIDPattern.FindString(created)
			stdout, stderr, code := executeTestCommand(t, "run", tc.action, runID, tc.flag, "--timeout", tc.timeout)
			if code != 9 || !strings.Contains(stderr, "deadline") {
				t.Fatalf("stream deadline: code=%d stderr=%s", code, stderr)
			}
			records := decodeHeadlessCLIRecords(t, stdout)
			if len(records) == 0 {
				t.Fatal("timeout omitted the end record")
			}
			end := records[len(records)-1]
			if end.Kind != "stream.end" || end.Reason != "deadline_exceeded" || end.RunID != runID ||
				end.ExitCode != 9 || end.Terminal || end.EventsEmitted != len(records)-1 ||
				end.LastSequence != int64(end.EventsEmitted) || end.SuggestedResume != end.LastSequence {
				t.Fatalf("wrong deadline end record: %#v", end)
			}

			if _, stderr, code := executeTestCommand(t, "run", "cancel", runID); code != 0 {
				t.Fatal(stderr)
			}
			stdout, stderr, code = executeTestCommand(t, "headless", "events", runID, "--after-sequence",
				strconv.FormatInt(end.SuggestedResume, 10))
			resumed := decodeHeadlessCLIRecords(t, stdout)
			if code != 7 || len(resumed) < 2 || resumed[0].Sequence != end.LastSequence+1 ||
				resumed[len(resumed)-1].Status != "cancelled" {
				t.Fatalf("resume after %d: code=%d stderr=%q records=%#v", end.LastSequence, code, stderr, resumed)
			}
		})
	}
}

func TestRunStreamTimeoutValidation(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "run stream timeout validation")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)

	for _, tc := range []struct {
		action  string
		flags   []string
		message string
	}{
		{"events", []string{"--timeout", "1s"}, "timeout requires --follow"},
		{"events", []string{"--timeout=0"}, "timeout requires --follow"},
		{"events", []string{"--follow", "--timeout", "25h"}, "between zero and 24h"},
		{"start", []string{"--timeout", "30m"}, "timeout requires --stream"},
		{"start", []string{"--timeout=0"}, "timeout requires --stream"},
		{"cancel", []string{"--timeout=0"}, "flag provided but not defined"},
	} {
		args := append([]string{"run", tc.action, runID}, tc.flags...)
		_, stderr, code := executeTestCommand(t, args...)
		if code != 2 || !strings.Contains(stderr, tc.message) {
			t.Errorf("%v: expected usage error %q, got code=%d stderr=%q", args, tc.message, code, stderr)
		}
	}
	requireRunStillCreated(t, runID)
}
