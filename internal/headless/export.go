package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
)

const (
	ProtocolVersion     = "headless.v1"
	EventRecordKind     = "run.event"
	EndRecordKind       = "stream.end"
	DefaultMaxEvents    = 1000
	MaxEvents           = 10_000
	DefaultPollInterval = 250 * time.Millisecond
	MinPollInterval     = 50 * time.Millisecond
	MaxPollInterval     = 5 * time.Second
	MaxIdentityRunes    = 256
	MaxLabelRunes       = 256
	MaxPayloadBytes     = 1 << 20
	readBatchSize       = 100
)

type Store interface {
	GetRun(ctx context.Context, id string) (domain.Run, error)
	ListRunEventsAfterSequence(ctx context.Context, runID string,
		afterSequence int64, limit int) ([]events.Event, error)
	LatestRunEventSequence(ctx context.Context, runID string) (int64, error)
}

type Request struct {
	RunID         string
	AfterSequence int64
	MaxEvents     int
	Follow        bool
	PollInterval  time.Duration
	// Stop, when provided, ends Follow mode gracefully: the exporter drains
	// the remaining durable events, emits the end record with reason
	// "completed", and returns nil — instead of waiting for context
	// cancellation. The run may still be running when Stop closes; the
	// record's Status/Terminal/SuggestedResume fields describe it.
	Stop <-chan struct{}
}

func (r Request) Normalize() (Request, error) {
	r.RunID = strings.TrimSpace(r.RunID)
	if r.RunID == "" || !utf8.ValidString(r.RunID) || strings.ContainsRune(r.RunID, 0) ||
		utf8.RuneCountInString(r.RunID) > 256 {
		return Request{}, apperror.New(apperror.CodeInvalidArgument,
			"headless Run id is required and bounded")
	}
	if r.AfterSequence < 0 {
		return Request{}, apperror.New(apperror.CodeInvalidArgument,
			"headless after-sequence cannot be negative")
	}
	if r.MaxEvents == 0 {
		r.MaxEvents = DefaultMaxEvents
	}
	if r.MaxEvents < 1 || r.MaxEvents > MaxEvents {
		return Request{}, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("headless max-events must be between 1 and %d", MaxEvents))
	}
	if r.PollInterval == 0 {
		r.PollInterval = DefaultPollInterval
	}
	if r.PollInterval < MinPollInterval || r.PollInterval > MaxPollInterval {
		return Request{}, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("headless poll interval must be between %s and %s",
				MinPollInterval, MaxPollInterval))
	}
	return r, nil
}

type PersistedEvent struct {
	EventID   string          `json:"event_id"`
	Version   string          `json:"version"`
	MissionID string          `json:"mission_id"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	SubjectID string          `json:"subject_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type EventRecord struct {
	Version  string         `json:"version"`
	Kind     string         `json:"kind"`
	RunID    string         `json:"run_id"`
	Sequence int64          `json:"sequence"`
	Event    PersistedEvent `json:"event"`
}

type EndRecord struct {
	Version         string `json:"version"`
	Kind            string `json:"kind"`
	RunID           string `json:"run_id"`
	Status          string `json:"status"`
	Terminal        bool   `json:"terminal"`
	Reason          string `json:"reason"`
	AfterSequence   int64  `json:"after_sequence"`
	LastSequence    int64  `json:"last_sequence"`
	EventsEmitted   int    `json:"events_emitted"`
	HasMore         bool   `json:"has_more"`
	Truncated       bool   `json:"truncated"`
	SuggestedResume int64  `json:"suggested_resume_after"`
	ExitCode        int    `json:"exit_code"`
}

type Exporter struct {
	store Store
}

func NewExporter(store Store) *Exporter {
	return &Exporter{store: store}
}

func (e *Exporter) Export(ctx context.Context, writer io.Writer, request Request) error {
	if e == nil || e.store == nil {
		return apperror.New(apperror.CodeInternal, "headless event Store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if writer == nil {
		return apperror.New(apperror.CodeInvalidArgument, "headless output writer is required")
	}
	request, err := request.Normalize()
	if err != nil {
		return err
	}
	initialRun, err := e.store.GetRun(ctx, request.RunID)
	if err != nil {
		return err
	}
	missionID := initialRun.MissionID
	tail, err := e.store.LatestRunEventSequence(ctx, request.RunID)
	if err != nil {
		return err
	}
	if request.AfterSequence > tail {
		return apperror.New(apperror.CodeInvalidArgument,
			"headless after-sequence is beyond the durable event tail")
	}

	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	lastSequence := request.AfterSequence
	emitted := 0
	stopped := false
	var run domain.Run
	for {
		remaining := request.MaxEvents - emitted
		if remaining > 0 {
			limit := min(remaining, readBatchSize)
			batch, err := e.store.ListRunEventsAfterSequence(ctx, request.RunID,
				lastSequence, limit)
			if err != nil {
				return err
			}
			for _, event := range batch {
				if err := validateNextEvent(request.RunID, missionID,
					lastSequence, event); err != nil {
					return err
				}
				if err := encoder.Encode(eventRecord(event)); err != nil {
					return apperror.Wrap(apperror.CodeInternal,
						"write headless event record", err)
				}
				lastSequence = event.Sequence
				emitted++
			}
			if len(batch) == limit && emitted < request.MaxEvents {
				continue
			}
		}

		run, err = e.store.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if run.MissionID != missionID {
			return apperror.New(apperror.CodeConflict,
				"headless Run Mission projection changed unexpectedly")
		}
		next, err := e.store.ListRunEventsAfterSequence(ctx, request.RunID,
			lastSequence, 1)
		if err != nil {
			return err
		}
		if len(next) > 0 && emitted < request.MaxEvents {
			continue
		}
		if emitted == request.MaxEvents && (len(next) > 0 || request.Follow && !run.Terminal()) {
			return finish(encoder, request, run, lastSequence, emitted,
				"max_events", len(next) > 0, apperror.New(apperror.CodeResourceExhausted,
					"headless event limit was reached"))
		}
		if run.Terminal() {
			return finishTerminal(encoder, request, run, lastSequence, emitted)
		}
		if !request.Follow {
			return finish(encoder, request, run, lastSequence, emitted,
				"snapshot", false, nil)
		}
		if stopped {
			// The driver asked Follow mode to end; the durable tail above is
			// drained, so the record is complete for this invocation. The
			// run may still be running — Status/Terminal/SuggestedResume
			// describe it for a later re-attach.
			return finish(encoder, request, run, lastSequence, emitted,
				"completed", false, nil)
		}

		timer := time.NewTimer(request.PollInterval)
		select {
		case <-request.Stop:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			stopped = true
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return finishContext(encoder, request, run, lastSequence, emitted, ctx.Err())
		case <-timer.C:
		}
	}
}

func validateNextEvent(runID string, missionID string, previous int64,
	event events.Event,
) error {
	if err := event.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeConflict,
			"stored Run event is invalid", err)
	}
	if event.RunID != runID || event.MissionID != missionID ||
		event.Sequence != previous+1 ||
		!validRecordText(event.EventID, MaxIdentityRunes, false) ||
		!validRecordText(event.RunID, MaxIdentityRunes, false) ||
		!validRecordText(event.MissionID, MaxIdentityRunes, false) ||
		!validRecordText(event.Type, MaxLabelRunes, false) ||
		!validRecordText(event.Source, MaxLabelRunes, false) ||
		!validRecordText(event.SubjectID, MaxIdentityRunes, true) ||
		len([]byte(event.PayloadJSON)) > MaxPayloadBytes {
		return apperror.New(apperror.CodeConflict,
			"stored Run event projection is invalid or not contiguous")
	}
	return nil
}

func validRecordText(value string, maxRunes int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maxRunes &&
		len([]byte(value)) <= maxRunes*4
}

func eventRecord(event events.Event) EventRecord {
	return EventRecord{Version: ProtocolVersion, Kind: EventRecordKind,
		RunID: event.RunID, Sequence: event.Sequence,
		Event: PersistedEvent{EventID: event.EventID, Version: event.Version,
			MissionID: event.MissionID, Type: event.Type, Source: event.Source,
			SubjectID: event.SubjectID, Payload: json.RawMessage(event.PayloadJSON),
			CreatedAt: event.CreatedAt}}
}

func finishTerminal(encoder *json.Encoder, request Request, run domain.Run,
	lastSequence int64, emitted int,
) error {
	switch run.Status {
	case domain.RunCompleted:
		return finish(encoder, request, run, lastSequence, emitted,
			"terminal", false, nil)
	case domain.RunFailed:
		return finish(encoder, request, run, lastSequence, emitted,
			"terminal", false, apperror.New(apperror.CodeFailedPrecondition,
				"headless Run finished with failed status"))
	case domain.RunCancelled:
		return finish(encoder, request, run, lastSequence, emitted,
			"terminal", false, apperror.New(apperror.CodeCancelled,
				"headless Run finished with cancelled status"))
	default:
		return apperror.New(apperror.CodeConflict,
			"headless Run terminal projection is invalid")
	}
}

func finishContext(encoder *json.Encoder, request Request, run domain.Run,
	lastSequence int64, emitted int, cause error,
) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		return finish(encoder, request, run, lastSequence, emitted,
			"deadline_exceeded", false, apperror.Wrap(apperror.CodeDeadlineExceeded,
				"headless event follow deadline exceeded", cause))
	}
	return finish(encoder, request, run, lastSequence, emitted,
		"cancelled", false, apperror.Wrap(apperror.CodeCancelled,
			"headless event follow was cancelled", cause))
}

func finish(encoder *json.Encoder, request Request, run domain.Run,
	lastSequence int64, emitted int, reason string, hasMore bool, resultErr error,
) error {
	exitCode := 0
	if resultErr != nil {
		exitCode = apperror.ExitCode(resultErr)
	}
	record := EndRecord{Version: ProtocolVersion, Kind: EndRecordKind,
		RunID: run.ID, Status: string(run.Status), Terminal: run.Terminal(),
		Reason: reason, AfterSequence: request.AfterSequence,
		LastSequence: lastSequence, EventsEmitted: emitted, HasMore: hasMore,
		Truncated: hasMore, SuggestedResume: lastSequence, ExitCode: exitCode}
	if err := encoder.Encode(record); err != nil {
		return apperror.Wrap(apperror.CodeInternal, "write headless end record", err)
	}
	return resultErr
}
