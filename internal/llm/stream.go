package llm

import "errors"

const (
	MaxModelDeltaEvents = 32
	MaxModelOutputBytes = 64 * 1024
	// Anthropic-compatible streams admit up to four times the tool-call limit
	// in ordered content blocks. A tool item has four persisted boundaries;
	// keep one complete provider response representable in a single flush.
	MaxStreamBoundariesPerDelta = MaxProviderOutputItems*4 + 4
)

type ModelDelta struct {
	Sequence       int
	ChunkCount     int
	ByteCount      int
	TotalBytes     int
	Done           bool
	ResponseID     string
	StreamSequence int
	Boundaries     []StreamBoundary
}

func (d ModelDelta) Validate(maxEvents int, maxBytes int) error {
	if d.Sequence <= 0 || d.Sequence > maxEvents {
		return errors.New("model delta sequence is outside its event limit")
	}
	if d.ChunkCount < 0 || d.ByteCount < 0 || d.TotalBytes < 0 || d.ByteCount > d.TotalBytes {
		return errors.New("model delta counters cannot be negative or inconsistent")
	}
	if maxBytes > 0 && d.TotalBytes > maxBytes {
		return errors.New("model delta total exceeds its byte limit")
	}
	if len(d.Boundaries) > MaxStreamBoundariesPerDelta {
		return errors.New("model delta contains too many item boundaries")
	}
	itemStream := d.ResponseID != "" || d.StreamSequence != 0 || len(d.Boundaries) != 0
	if itemStream {
		if err := validateStreamIdentity(d.ResponseID, "model delta response"); err != nil {
			return err
		}
		if d.StreamSequence <= 0 || d.StreamSequence > MaxItemStreamEvents {
			return errors.New("model delta item stream sequence is invalid")
		}
		previous := 0
		terminal := StreamEventType("")
		for index, boundary := range d.Boundaries {
			if err := boundary.Validate(d.ResponseID); err != nil {
				return err
			}
			if boundary.Sequence <= previous || boundary.Sequence > d.StreamSequence {
				return errors.New("model delta item boundaries are unordered")
			}
			if boundary.Type == StreamResponseCompleted || boundary.Type == StreamResponseFailed ||
				boundary.Type == StreamResponseCancelled {
				if terminal != "" || index != len(d.Boundaries)-1 ||
					boundary.Sequence != d.StreamSequence {
					return errors.New("model delta response terminal boundary is not final")
				}
				terminal = boundary.Type
			}
			previous = boundary.Sequence
		}
		if d.Done && terminal != StreamResponseCompleted {
			return errors.New("completed model delta requires a response completion boundary")
		}
		if !d.Done && terminal == StreamResponseCompleted {
			return errors.New("non-final model delta cannot complete its response")
		}
	}
	if !d.Done && (d.ChunkCount == 0 || d.ByteCount == 0) && len(d.Boundaries) == 0 {
		return errors.New("non-final model delta must contain byte or item progress")
	}
	return nil
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 {
		return errors.New("model usage counters cannot be negative")
	}
	maxInt := int(^uint(0) >> 1)
	if u.InputTokens > maxInt-u.OutputTokens {
		return errors.New("model usage counters overflow")
	}
	if u.CacheReadTokens < 0 || u.CacheCreationTokens < 0 ||
		u.CacheCreation5mTokens < 0 || u.CacheCreation1hTokens < 0 {
		return errors.New("model cache usage counters cannot be negative")
	}
	if u.CacheReadTokens > u.InputTokens || u.CacheCreationTokens > u.InputTokens-u.CacheReadTokens {
		return errors.New("model cache usage exceeds total input")
	}
	if u.CacheCreation5mTokens > u.CacheCreationTokens ||
		u.CacheCreation1hTokens > u.CacheCreationTokens-u.CacheCreation5mTokens {
		return errors.New("model cache creation breakdown exceeds total creation")
	}
	if u.CacheCreationBreakdownKnown && u.CacheCreation5mTokens+u.CacheCreation1hTokens != u.CacheCreationTokens {
		return errors.New("model cache creation breakdown does not match total creation")
	}
	return nil
}
