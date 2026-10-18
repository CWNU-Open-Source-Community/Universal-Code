package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGenerationTimingMeasuresFromFirstDeltaAndStaysPrivate(t *testing.T) {
	stream := newProviderStreamEvents("provider", "model", "response", StreamGranularityDelta)
	stream.start()
	if terminal := stream.terminalEvent(OutcomeSuccess, &Usage{}); terminal.DecodeMillis != nil {
		t.Fatal("a stream without generation acquired a duration")
	}
	stream = newProviderStreamEvents("provider", "model", "response", StreamGranularityDelta)
	stream.start()
	stream.emit(StreamEvent{Type: StreamTextDelta, TextDelta: "first"})
	if stream.firstGeneration.IsZero() {
		t.Fatal("the first text delta did not start timing")
	}
	stream.firstGeneration = time.Now().Add(-2 * time.Second)
	first := stream.firstGeneration
	stream.emit(StreamEvent{Type: StreamToolArgumentDelta, ArgumentDelta: "{}"})
	if !stream.firstGeneration.Equal(first) {
		t.Fatal("a later delta reset the first generation time")
	}
	terminal := stream.terminalEvent(OutcomeSuccess, &Usage{})
	if terminal.DecodeMillis == nil || *terminal.DecodeMillis < 2000 || *terminal.DecodeMillis > 2500 {
		t.Fatalf("generation duration=%v", terminal.DecodeMillis)
	}
	encoded, err := json.Marshal(terminal)
	if err != nil || strings.Contains(string(encoded), "decode") || strings.Contains(string(encoded), "2000") {
		t.Fatalf("adapter timing escaped the private in-memory field: %s %v", encoded, err)
	}
	complete := newProviderStreamEvents("provider", "model", "response", StreamGranularityComplete)
	complete.emit(StreamEvent{Type: StreamTextDelta, TextDelta: "buffered response"})
	if complete.terminalEvent(OutcomeSuccess, &Usage{}).DecodeMillis != nil {
		t.Fatal("a buffered response was reported as measured streaming generation")
	}
}

func TestPrivateReasoningStartsGenerationTimingWithoutPublicText(t *testing.T) {
	t.Run("Chat Completions", func(t *testing.T) {
		state := openAIStreamState{provider: "provider", model: "model",
			events: newProviderStreamEvents("provider", "model", "response", StreamGranularityDelta)}
		chunk, err := state.consume([]byte(`{"model":"model","choices":[{"index":0,"delta":{"reasoning_content":"private reasoning"}}]}`))
		if err != nil || state.events.firstGeneration.IsZero() || (chunk != nil && chunk.Text != "") {
			t.Fatalf("private reasoning timing/public text: %+v %v", chunk, err)
		}
	})
	t.Run("Responses", func(t *testing.T) {
		state := responsesStreamState{provider: "provider", started: true,
			items:  map[string]*responsesStreamItem{"reasoning": {private: true}},
			events: newProviderStreamEvents("provider", "model", "response", StreamGranularityDelta)}
		chunk, _, err := state.consume([]byte(`{"type":"response.reasoning_text.delta","item_id":"reasoning","delta":"private reasoning"}`))
		if err != nil || state.events.firstGeneration.IsZero() || chunk != nil {
			t.Fatalf("private reasoning timing/public text: %+v %v", chunk, err)
		}
	})
	t.Run("Anthropic", func(t *testing.T) {
		state := anthropicStreamState{model: "model",
			events: newProviderStreamEvents("provider", "model", "response", StreamGranularityDelta)}
		for _, raw := range []string{
			`{"type":"message_start","message":{"id":"response","model":"model","usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private reasoning"}}`,
		} {
			chunk, _, err := state.consume([]byte(raw), "provider")
			if err != nil || (chunk != nil && chunk.Text != "") {
				t.Fatalf("private reasoning became public: %+v %v", chunk, err)
			}
		}
		if state.events.firstGeneration.IsZero() {
			t.Fatal("private thinking did not start timing")
		}
	})
}
