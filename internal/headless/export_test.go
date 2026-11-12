package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

func TestExporterWritesOrderedSnapshotNDJSON(t *testing.T) {
	store := newFakeStore(t, domain.RunRunning, 1, 2)
	var output bytes.Buffer
	err := NewExporter(store).Export(context.Background(), &output, Request{
		RunID: store.run.ID, MaxEvents: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	records := decodeRecords(t, output.Bytes())
	if bytes.Count(output.Bytes(), []byte{'\n'}) != len(records) {
		t.Fatalf("headless output is not one JSON record per line: %q", output.String())
	}
	if len(records) != 3 || records[0]["kind"] != EventRecordKind ||
		records[0]["sequence"] != float64(1) || records[1]["sequence"] != float64(2) {
		t.Fatalf("event records drifted: %#v", records)
	}
	end := records[2]
	if end["kind"] != EndRecordKind || end["status"] != string(domain.RunRunning) ||
		end["reason"] != "snapshot" || end["terminal"] != false ||
		end["events_emitted"] != float64(2) || end["exit_code"] != float64(0) {
		t.Fatalf("snapshot end record drifted: %#v", end)
	}
	if !strings.Contains(output.String(), `\u003csafe\u003e`) {
		t.Fatalf("NDJSON did not preserve the persisted JSON payload: %s", output.String())
	}
}

func TestExporterResumesAfterSequence(t *testing.T) {
	store := newFakeStore(t, domain.RunRunning, 1, 2, 3)
	var output bytes.Buffer
	if err := NewExporter(store).Export(context.Background(), &output, Request{
		RunID: store.run.ID, AfterSequence: 2, MaxEvents: 10,
	}); err != nil {
		t.Fatal(err)
	}
	records := decodeRecords(t, output.Bytes())
	if len(records) != 2 || records[0]["sequence"] != float64(3) ||
		records[1]["after_sequence"] != float64(2) ||
		records[1]["suggested_resume_after"] != float64(3) {
		t.Fatalf("resume output drifted: %#v", records)
	}

	output.Reset()
	err := NewExporter(store).Export(context.Background(), &output, Request{
		RunID: store.run.ID, AfterSequence: 4, MaxEvents: 10,
	})
	if apperror.CodeOf(err) != apperror.CodeInvalidArgument || output.Len() != 0 {
		t.Fatalf("future cursor was not rejected before output: code=%s output=%s err=%v",
			apperror.CodeOf(err), output.String(), err)
	}
}

func TestExporterReportsTruncationAndStableResume(t *testing.T) {
	store := newFakeStore(t, domain.RunRunning, 1, 2)
	var output bytes.Buffer
	err := NewExporter(store).Export(context.Background(), &output, Request{
		RunID: store.run.ID, MaxEvents: 1,
	})
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted ||
		apperror.ExitCode(err) != 8 {
		t.Fatalf("event truncation code drifted: code=%s exit=%d err=%v",
			apperror.CodeOf(err), apperror.ExitCode(err), err)
	}
	records := decodeRecords(t, output.Bytes())
	end := records[len(records)-1]
	if len(records) != 2 || end["reason"] != "max_events" ||
		end["has_more"] != true || end["truncated"] != true ||
		end["suggested_resume_after"] != float64(1) || end["exit_code"] != float64(8) {
		t.Fatalf("truncation end record drifted: %#v", records)
	}
}

func TestExporterMapsTerminalRunStatus(t *testing.T) {
	tests := []struct {
		status domain.RunStatus
		code   apperror.Code
		exit   int
	}{
		{status: domain.RunCompleted, exit: 0},
		{status: domain.RunFailed, code: apperror.CodeFailedPrecondition, exit: 4},
		{status: domain.RunCancelled, code: apperror.CodeCancelled, exit: 7},
	}
	for _, test := range tests {
		t.Run(string(test.status), func(t *testing.T) {
			store := newFakeStore(t, test.status)
			var output bytes.Buffer
			err := NewExporter(store).Export(context.Background(), &output, Request{
				RunID: store.run.ID, MaxEvents: 10,
			})
			actualExit := 0
			if err != nil {
				actualExit = apperror.ExitCode(err)
			}
			if apperror.CodeOf(err) != test.code || actualExit != test.exit {
				t.Fatalf("terminal status mapping drifted: code=%s exit=%d err=%v",
					apperror.CodeOf(err), actualExit, err)
			}
			records := decodeRecords(t, output.Bytes())
			end := records[len(records)-1]
			if end["status"] != string(test.status) || end["terminal"] != true ||
				end["reason"] != "terminal" || end["exit_code"] != float64(test.exit) {
				t.Fatalf("terminal end record drifted: %#v", end)
			}
		})
	}
}

func TestExporterFollowDeadlineIsFramed(t *testing.T) {
	store := newFakeStore(t, domain.RunRunning)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	err := NewExporter(store).Export(ctx, &output, Request{
		RunID: store.run.ID, MaxEvents: 10, Follow: true,
		PollInterval: MinPollInterval,
	})
	if apperror.CodeOf(err) != apperror.CodeDeadlineExceeded || apperror.ExitCode(err) != 9 {
		t.Fatalf("follow deadline mapping drifted: code=%s exit=%d err=%v",
			apperror.CodeOf(err), apperror.ExitCode(err), err)
	}
	records := decodeRecords(t, output.Bytes())
	end := records[len(records)-1]
	if end["reason"] != "deadline_exceeded" || end["exit_code"] != float64(9) ||
		end["terminal"] != false {
		t.Fatalf("deadline end record drifted: %#v", end)
	}
}

func TestExporterFollowDrainsTerminalTail(t *testing.T) {
	store := newFakeStore(t, domain.RunRunning, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		time.Sleep(60 * time.Millisecond)
		store.completeWithEvent(t, 2)
	}()
	var output bytes.Buffer
	if err := NewExporter(store).Export(ctx, &output, Request{
		RunID: store.run.ID, MaxEvents: 10, Follow: true,
		PollInterval: MinPollInterval,
	}); err != nil {
		t.Fatal(err)
	}
	records := decodeRecords(t, output.Bytes())
	end := records[len(records)-1]
	if len(records) != 3 || records[1]["sequence"] != float64(2) ||
		end["status"] != string(domain.RunCompleted) || end["reason"] != "terminal" ||
		end["last_sequence"] != float64(2) || end["exit_code"] != float64(0) {
		t.Fatalf("terminal follow tail drifted: %#v", records)
	}
}

func TestExporterFollowStopsGracefullyOnStopSignal(t *testing.T) {
	store := newFakeStore(t, domain.RunRunning, 1)
	// The run stays running (the one-shot caller does not close it), so the
	// only end path is the driver's Stop signal.
	stop := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		time.Sleep(60 * time.Millisecond)
		close(stop)
	}()
	var output bytes.Buffer
	if err := NewExporter(store).Export(ctx, &output, Request{
		RunID: store.run.ID, MaxEvents: 10, Follow: true,
		PollInterval: MinPollInterval, Stop: stop,
	}); err != nil {
		t.Fatalf("a graceful stop must return nil: %v", err)
	}
	records := decodeRecords(t, output.Bytes())
	end := records[len(records)-1]
	if len(records) != 2 || records[0]["sequence"] != float64(1) ||
		end["reason"] != "completed" || end["has_more"] != false ||
		end["status"] != string(domain.RunRunning) || end["exit_code"] != float64(0) ||
		end["last_sequence"] != float64(1) {
		t.Fatalf("graceful stop record drifted: %#v", records)
	}
}

func TestExporterFailsClosedOnSequenceOrWriterDrift(t *testing.T) {
	store := newFakeStore(t, domain.RunRunning, 1, 3)
	var output bytes.Buffer
	err := NewExporter(store).Export(context.Background(), &output, Request{
		RunID: store.run.ID, MaxEvents: 10,
	})
	if apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("sequence gap was accepted: code=%s err=%v", apperror.CodeOf(err), err)
	}
	store = newFakeStore(t, domain.RunRunning, 1)
	store.events[0].MissionID = "mission-forged"
	output.Reset()
	err = NewExporter(store).Export(context.Background(), &output, Request{
		RunID: store.run.ID, MaxEvents: 10,
	})
	if apperror.CodeOf(err) != apperror.CodeConflict || output.Len() != 0 {
		t.Fatalf("cross-Mission event was projected: code=%s output=%s err=%v",
			apperror.CodeOf(err), output.String(), err)
	}
	store = newFakeStore(t, domain.RunRunning, 1)
	store.events[0].Type = strings.Repeat("x", MaxLabelRunes+1)
	output.Reset()
	err = NewExporter(store).Export(context.Background(), &output, Request{
		RunID: store.run.ID, MaxEvents: 10,
	})
	if apperror.CodeOf(err) != apperror.CodeConflict || output.Len() != 0 {
		t.Fatalf("oversized event metadata was projected: code=%s output=%s err=%v",
			apperror.CodeOf(err), output.String(), err)
	}

	store = newFakeStore(t, domain.RunRunning, 1)
	err = NewExporter(store).Export(context.Background(), failingWriter{}, Request{
		RunID: store.run.ID, MaxEvents: 10,
	})
	if apperror.CodeOf(err) != apperror.CodeInternal {
		t.Fatalf("writer failure mapping drifted: code=%s err=%v", apperror.CodeOf(err), err)
	}
}

type fakeStore struct {
	mu     sync.RWMutex
	run    domain.Run
	events []events.Event
}

func newFakeStore(t *testing.T, status domain.RunStatus, sequences ...int64) *fakeStore {
	t.Helper()
	now := time.Now().UTC()
	run := domain.Run{ID: "run-headless", MissionID: "mission-headless", Status: status,
		Config: domain.RunConfig{ModelRoute: "mock/default"},
		Budget: domain.Budget{MaxTurns: 1}, CreatedAt: now, UpdatedAt: now}
	if status != domain.RunCreated && status != domain.RunPreparing {
		run.StartedAt = &now
	}
	if run.Terminal() {
		run.FinishedAt = &now
	}
	items := make([]events.Event, 0, len(sequences))
	for _, sequence := range sequences {
		event, err := events.New(run.ID, run.MissionID, events.RunStatusChangedEvent,
			"headless_test", run.ID, map[string]any{"label": "<safe>"})
		if err != nil {
			t.Fatal(err)
		}
		event.Sequence = sequence
		event.CreatedAt = now.Add(time.Duration(sequence) * time.Millisecond)
		items = append(items, event)
	}
	return &fakeStore{run: run, events: items}
}

func (s *fakeStore) GetRun(context.Context, string) (domain.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.run, nil
}

func (s *fakeStore) ListRunEventsAfterSequence(_ context.Context, _ string,
	afterSequence int64, limit int,
) ([]events.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]events.Event, 0, limit)
	for _, event := range s.events {
		if event.Sequence > afterSequence {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (s *fakeStore) LatestRunEventSequence(context.Context, string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest int64
	for _, event := range s.events {
		latest = max(latest, event.Sequence)
	}
	return latest, nil
}

func (s *fakeStore) completeWithEvent(t *testing.T, sequence int64) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	event, err := events.New(s.run.ID, s.run.MissionID, events.RunStatusChangedEvent,
		"headless_test", s.run.ID, map[string]any{"to": domain.RunCompleted})
	if err != nil {
		t.Error(err)
		return
	}
	event.Sequence = sequence
	event.CreatedAt = now
	s.events = append(s.events, event)
	s.run.Status = domain.RunCompleted
	s.run.UpdatedAt = now
	s.run.FinishedAt = &now
}

func decodeRecords(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var records []map[string]any
	for {
		var record map[string]any
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode NDJSON: %v\n%s", err, data)
		}
		records = append(records, record)
	}
	return records
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
