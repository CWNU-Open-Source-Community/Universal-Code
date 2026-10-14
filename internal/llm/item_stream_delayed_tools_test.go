package llm

import "testing"

func TestItemStreamDelayedToolNamesKeepDistinctCallIdentities(t *testing.T) {
	for _, names := range []string{
		`[{"index":0,"function":{"name":"read_file","arguments":"{}"}},{"index":1,"function":{"name":"list_files","arguments":"{}"}}]`,
		`[{"index":1,"function":{"name":"list_files","arguments":"{}"}},{"index":0,"function":{"name":"read_file","arguments":"{}"}}]`,
	} {
		s := &openAIStreamState{provider: "compatible", model: "model",
			events: newProviderStreamEvents("compatible", "model", "wire-response", StreamGranularityDelta)}
		a, err := NewItemStreamAccumulator("attempt", "compatible", "model")
		if err != nil {
			t.Fatal(err)
		}
		for _, payload := range []string{
			`{"model":"model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function"},{"index":1,"id":"call_b","type":"function"}]}}]}`,
			`{"model":"model","choices":[{"index":0,"delta":{"tool_calls":` + names + `}}]}`,
			`{"model":"model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		} {
			chunk, err := s.consume([]byte(payload))
			if err != nil {
				t.Fatal(err)
			}
			if chunk != nil {
				if _, _, err := a.Consume(*chunk); err != nil {
					t.Fatal(err)
				}
			}
		}
		final, err := s.finalChunk()
		if err != nil {
			t.Fatal(err)
		}
		final, _, err = a.Consume(final)
		if err != nil {
			t.Fatal(err)
		}
		if len(final.ToolCalls) != 2 || final.ToolCalls[0].StreamCallID == final.ToolCalls[1].StreamCallID {
			t.Fatalf("tool identities collide: %+v", final.ToolCalls)
		}
		if _, _, err := CompleteOutputItems(a.ResponseID(), "", final.ToolCalls, a.Items()); err != nil {
			t.Fatalf("persist completed parallel calls: %v", err)
		}
	}
}
