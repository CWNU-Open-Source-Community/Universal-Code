package application

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
)

const (
	modelDeltaFlushBytes           = 2 * 1024
	modelDeltaFlushInterval        = 250 * time.Millisecond
	publicPreviewScanIntervalBytes = 64
)

type modelStreamResult struct {
	Response      *llm.ChatResponse
	Events        int
	Bytes         int
	Usage         *llm.Usage
	ToolCallCount int
	DecodeMillis  *int64
}

type modelStreamAggregator struct {
	supervisor        *AgentRunner
	checkpoint        domain.SupervisorCheckpoint
	attempt           llm.ModelAttempt
	ref               llm.ModelRef
	live              *activeCallLease
	rootPreview       *rootMessagePreviewer
	commentaryPreview *publicCommentaryPreviewer

	output            bytes.Buffer
	previewBytes      int
	pendingChunks     int
	pendingBytes      int
	events            int
	durableBytes      int
	stream            *llm.ItemStreamAccumulator
	streamSequence    int
	pendingBoundaries []llm.StreamBoundary
	observedUsage     *llm.Usage
	observedToolCalls int
	decodeMillis      *int64
}

func (s *AgentRunner) streamModel(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, ref llm.ModelRef, request llm.ChatRequest, live *activeCallLease) (modelStreamResult, error) {
	chunks, err := s.router.StreamChatModelRef(ctx, ref, request)
	if err != nil {
		return modelStreamResult{}, err
	}
	aggregator := &modelStreamAggregator{
		supervisor: s, checkpoint: checkpoint, attempt: attempt, ref: ref, live: live,
		rootPreview:       newRootMessagePreviewer(s.checker),
		commentaryPreview: newPublicCommentaryPreviewer(s.checker),
	}
	responseID := llm.StableStreamID("response", checkpoint.RunID, checkpoint.AttemptID,
		strconv.Itoa(attempt.Number))
	aggregator.stream, err = llm.NewItemStreamAccumulator(responseID, ref.Provider, ref.Model)
	if err != nil {
		return modelStreamResult{}, llm.NewProviderError(llm.OutcomeInvalidResponse,
			ref.Provider, "could not initialize item stream", err)
	}
	return aggregator.consume(ctx, chunks)
}

func (a *modelStreamAggregator) consume(ctx context.Context, chunks <-chan llm.ChatChunk) (result modelStreamResult, failure error) {
	defer func() {
		if failure == nil {
			return
		}
		// Stop must not wait for a provider that ignores cancellation. It may,
		// however, have already delivered a terminal receipt behind a rejected
		// text chunk. Drain only immediately available chunks, with a bound, and
		// retain their accounting without publishing text or accepting tools.
		for i := 0; i < 128; i++ {
			select {
			case chunk, open := <-chunks:
				if !open {
					result.Usage, result.ToolCallCount = a.observedUsage, a.observedToolCalls
					result.DecodeMillis = a.decodeMillis
					return
				}
				a.observeUsage(chunk)
				if chunk.Done || chunk.Err != nil {
					result.Usage, result.ToolCallCount = a.observedUsage, a.observedToolCalls
					result.DecodeMillis = a.decodeMillis
					return
				}
			default:
				result.Usage, result.ToolCallCount = a.observedUsage, a.observedToolCalls
				result.DecodeMillis = a.decodeMillis
				return
			}
		}
		result.Usage, result.ToolCallCount = a.observedUsage, a.observedToolCalls
		result.DecodeMillis = a.decodeMillis
	}()
	if chunks == nil {
		return a.result(nil), llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider, "returned a nil stream", nil)
	}
	if a.stream == nil {
		responseID := llm.StableStreamID("response", a.checkpoint.RunID, a.checkpoint.AttemptID,
			strconv.Itoa(a.attempt.Number))
		stream, err := llm.NewItemStreamAccumulator(responseID, a.ref.Provider, a.ref.Model)
		if err != nil {
			return a.result(nil), err
		}
		a.stream = stream
	}
	ticker := time.NewTicker(modelDeltaFlushInterval)
	defer ticker.Stop()
	for {
		var chunk llm.ChatChunk
		var ok bool
		select {
		case chunk, ok = <-chunks:
		default:
			select {
			case chunk, ok = <-chunks:
			case <-ctx.Done():
				_ = a.acceptStreamEvents(a.stream.Abort(llm.OutcomeCancelled))
				flushErr := a.flush(false)
				return a.result(nil), modelStreamFailure(ctx.Err(), flushErr)
			case <-ticker.C:
				if err := a.flush(false); err != nil {
					return a.result(nil), err
				}
				continue
			}
		}
		if !ok {
			_ = a.acceptStreamEvents(a.stream.Abort(llm.OutcomeInvalidResponse))
			flushErr := a.flush(false)
			if ctx.Err() != nil {
				return a.result(nil), modelStreamFailure(ctx.Err(), flushErr)
			}
			streamErr := llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider, "stream closed before a final chunk", nil)
			return a.result(nil), modelStreamFailure(streamErr, flushErr)
		}
		// Preserve received accounting independently of public output. Stop or
		// an invalid item/tool can reject that output after usage has arrived.
		a.observeUsage(chunk)
		normalized, itemEvents, itemErr := a.stream.Consume(chunk)
		if itemErr != nil {
			_ = a.acceptStreamEvents(a.stream.Abort(llm.OutcomeInvalidResponse))
			flushErr := a.flush(false)
			streamErr := llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider,
				"returned an invalid item stream", itemErr)
			return a.result(nil), modelStreamFailure(streamErr, flushErr)
		}
		chunk = normalized
		a.observeUsage(chunk)
		if usage := validatedFailedStreamUsage(chunk); usage != nil {
			a.observedUsage, a.observedToolCalls = usage, len(chunk.ToolCalls)
		}
		if err := a.acceptStreamEvents(itemEvents); err != nil {
			return a.result(nil), err
		}
		if chunk.Err != nil {
			flushErr := a.flush(false)
			if ctx.Err() != nil {
				return a.result(nil), modelStreamFailure(ctx.Err(), flushErr)
			}
			return a.result(nil), modelStreamFailure(llm.NormalizeProviderError(a.ref.Provider, chunk.Err), flushErr)
		}
		if err := a.appendText(chunk.Text); err != nil {
			flushErr := a.flush(false)
			return a.result(nil), modelStreamFailure(err, flushErr)
		}
		if !chunk.Done && a.pendingBytes >= modelDeltaFlushBytes {
			if err := a.flush(false); err != nil {
				return a.result(nil), err
			}
		}
		if !chunk.Done {
			continue
		}
		if chunk.Usage == nil {
			flushErr := a.flush(false)
			streamErr := llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider, "final stream chunk omitted usage", nil)
			return a.result(nil), modelStreamFailure(streamErr, flushErr)
		}
		if err := chunk.Usage.Validate(); err != nil {
			flushErr := a.flush(false)
			streamErr := llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider, "returned invalid token usage", err)
			return a.result(nil), modelStreamFailure(streamErr, flushErr)
		}
		if !utf8.Valid(a.output.Bytes()) {
			flushErr := a.flush(false)
			streamErr := llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider, "stream returned invalid UTF-8", nil)
			return a.result(nil), modelStreamFailure(streamErr, flushErr)
		}
		provider := strings.TrimSpace(chunk.Provider)
		if provider == "" {
			provider = a.ref.Provider
		}
		model := strings.TrimSpace(chunk.Model)
		if model == "" {
			model = a.ref.Model
		}
		toolCalls, err := llm.NormalizeToolCalls(chunk.ToolCalls)
		if err != nil {
			streamErr := llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider,
				"final stream chunk returned invalid tool calls", err)
			return a.result(nil), streamErr
		}
		if len(toolCalls) > 0 {
			if err := a.publishCommentaryPreview(true); err != nil {
				return a.result(nil), err
			}
		} else if err := a.publishPublicPreview(true); err != nil {
			return a.result(nil), err
		}
		if err := a.flush(true); err != nil {
			return a.result(nil), err
		}
		response := &llm.ChatResponse{
			ResponseID: a.stream.ResponseID(), Text: a.output.String(), ToolCalls: toolCalls,
			Items: a.stream.Items(), Usage: *chunk.Usage, Provider: provider, Model: model,
			FinishReason: chunk.FinishReason, Replay: chunk.Replay.Clone(),
		}
		return a.result(response), nil
	}
}

func (a *modelStreamAggregator) acceptStreamEvents(items []llm.StreamEvent) error {
	for _, item := range items {
		if item.Sequence <= a.streamSequence {
			return llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider,
				"item stream sequence moved backwards", nil)
		}
		a.streamSequence = item.Sequence
		if boundary, ok := llm.BoundaryForEvent(item); ok {
			a.pendingBoundaries = append(a.pendingBoundaries, boundary)
		}
		if a.live != nil {
			if err := a.live.PublishItemEvent(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *modelStreamAggregator) appendText(text string) error {
	if text == "" {
		return nil
	}
	if len(text) > llm.MaxModelOutputBytes-a.output.Len() {
		return llm.NewProviderError(llm.OutcomeInvalidResponse, a.ref.Provider, "stream output exceeds 65536 bytes", nil)
	}
	_, _ = a.output.WriteString(text)
	a.pendingChunks++
	a.pendingBytes += len(text)
	if a.live != nil {
		if err := a.live.PublishProgress(len(text), a.output.Len()); err != nil {
			return err
		}
		return a.publishPublicPreview(false)
	}
	return nil
}

func (a *modelStreamAggregator) publishPublicPreview(force bool) error {
	if a.live == nil {
		return nil
	}
	if !force && a.output.Len()-a.previewBytes < publicPreviewScanIntervalBytes {
		return nil
	}
	a.previewBytes = a.output.Len()
	if a.rootPreview != nil {
		preview, complete, changed := a.rootPreview.Update(a.output.String())
		if preview != "" || complete {
			if !changed {
				return nil
			}
			return a.live.PublishPublicPreview(PublicModelStreamRootMessage, preview, complete)
		}
	}
	return nil
}

func (a *modelStreamAggregator) publishCommentaryPreview(complete bool) error {
	if a.commentaryPreview == nil || a.live == nil {
		return nil
	}
	preview, ready, changed := a.commentaryPreview.Update(a.output.String(), complete)
	if !changed {
		return nil
	}
	return a.live.PublishPublicPreview(PublicModelStreamToolCommentary, preview, ready)
}

func (a *modelStreamAggregator) flush(done bool) error {
	if a.pendingBytes == 0 && len(a.pendingBoundaries) == 0 && !done {
		return nil
	}
	if !done && a.events >= llm.MaxModelDeltaEvents-1 {
		return nil
	}
	if a.events >= llm.MaxModelDeltaEvents {
		return apperror.New(apperror.CodeResourceExhausted, "model delta event limit was exhausted")
	}
	delta := llm.ModelDelta{
		Sequence: a.events + 1, ChunkCount: a.pendingChunks, ByteCount: a.pendingBytes,
		TotalBytes: a.durableBytes + a.pendingBytes, Done: done,
		ResponseID: a.stream.ResponseID(), StreamSequence: a.streamSequence,
		Boundaries: append([]llm.StreamBoundary(nil), a.pendingBoundaries...),
	}
	eventCtx, eventCancel := supervisorModelEventContext(context.Background())
	inserted, err := a.supervisor.store.RecordSupervisorModelDelta(eventCtx, a.checkpoint, a.attempt, delta)
	eventCancel()
	if err != nil {
		return err
	}
	if !inserted {
		return apperror.New(apperror.CodeConflict, "model delta event already exists")
	}
	a.events++
	a.durableBytes = delta.TotalBytes
	a.pendingChunks = 0
	a.pendingBytes = 0
	a.pendingBoundaries = nil
	return nil
}

func (a *modelStreamAggregator) observeUsage(chunk llm.ChatChunk) {
	for _, event := range chunk.Events {
		if event.Type == llm.StreamResponseCompleted && event.DecodeMillis != nil && *event.DecodeMillis >= 0 {
			millis := *event.DecodeMillis
			a.decodeMillis = &millis
		}
	}
	terminal := chunk.Done || (chunk.Err != nil && llm.CompletionError(a.ref.Provider, chunk.FinishReason) != nil)
	if terminal && chunk.Usage != nil && chunk.Usage.Validate() == nil {
		usage := *chunk.Usage
		a.observedUsage = &usage
		a.observedToolCalls = len(chunk.ToolCalls)
	}
}

func (a *modelStreamAggregator) result(response *llm.ChatResponse) modelStreamResult {
	return modelStreamResult{Response: response, Events: a.events, Bytes: a.durableBytes,
		Usage: a.observedUsage, ToolCallCount: a.observedToolCalls, DecodeMillis: a.decodeMillis}
}

// Call only after ItemStreamAccumulator.Consume has validated the terminal.
// A provider's response.failed receipt can have known usage without a content
// finish reason, unlike a transport error that never returned a receipt.
func validatedFailedStreamUsage(chunk llm.ChatChunk) *llm.Usage {
	if chunk.Err == nil || chunk.Usage == nil || chunk.Usage.Validate() != nil {
		return nil
	}
	for _, event := range chunk.Events {
		if event.Type == llm.StreamResponseFailed {
			usage := *chunk.Usage
			return &usage
		}
	}
	return nil
}

func modelStreamFailure(primary error, persistence error) error {
	if persistence != nil {
		return persistence
	}
	return primary
}
