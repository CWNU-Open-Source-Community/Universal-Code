package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUserPromptSubmitEventRules(t *testing.T) {
	if !UserPromptSubmit.Valid() {
		t.Fatal("user_prompt_submit event is not valid")
	}
	declaration := Declaration{ProtocolVersion: ProtocolVersion, ID: "prompt-guard",
		Event: UserPromptSubmit, Action: ActionDeny, FailurePolicy: FailureDeny,
		TimeoutMillis: 100, Message: "prompt denied by operator hook"}
	if err := declaration.Validate(); err != nil {
		t.Fatalf("declarative deny on user_prompt_submit was rejected: %v", err)
	}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "prompt-tool",
		Event: UserPromptSubmit, Action: ActionDeny, FailurePolicy: FailureDeny,
		TimeoutMillis: 100, Message: "prompt denied", ToolNames: []string{"shell"}}).Validate(); err == nil {
		t.Fatal("user_prompt_submit declaration accepted tool filters")
	}
	spec := ScriptSpec{Interpreter: "python", Path: "check.py", SHA256: testHookDigest("script")}
	if err := (Declaration{ProtocolVersion: ProtocolVersion, ID: "prompt-script",
		Event: UserPromptSubmit, Action: ActionScript, FailurePolicy: FailureDeny,
		TimeoutMillis: 1_000, Script: &spec}).Validate(); err != nil {
		t.Fatalf("script hook on user_prompt_submit was rejected: %v", err)
	}
}

func TestBuildUserPromptSubmitPayloadRoundTripsAndClips(t *testing.T) {
	raw := BuildUserPromptSubmitPayload(UserPromptSubmitPayload{ThreadID: "thread-1",
		RunID: "run-1", Content: "check the prompt"})
	var payload UserPromptSubmitPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ThreadID != "thread-1" || payload.RunID != "run-1" ||
		payload.Content != "check the prompt" || payload.Truncated {
		t.Fatalf("unexpected payload: %#v", payload)
	}

	oversized := strings.Repeat("长", MaxPayloadBytes) // multibyte, far beyond the limit
	raw = BuildUserPromptSubmitPayload(UserPromptSubmitPayload{ThreadID: "thread-1",
		Content: oversized})
	var clipped UserPromptSubmitPayload
	if err := json.Unmarshal(raw, &clipped); err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxPayloadBytes {
		t.Fatalf("clipped payload exceeded the input limit: %d", len(raw))
	}
	if !clipped.Truncated {
		t.Fatal("clipped payload did not report truncation")
	}
	if !utf8Valid(clipped.Content) || clipped.Content == oversized {
		t.Fatalf("clipped content is not a proper UTF-8 prefix: %d bytes", len(clipped.Content))
	}
	if clipped.ThreadID != "thread-1" {
		t.Fatalf("identity was lost during clipping: %#v", clipped)
	}

	raw = BuildUserPromptSubmitPayload(UserPromptSubmitPayload{ThreadID: "thread-1"})
	var empty UserPromptSubmitPayload
	if err := json.Unmarshal(raw, &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Content != "" || empty.Truncated {
		t.Fatalf("empty prompt was not preserved: %#v", empty)
	}
}

func utf8Valid(value string) bool {
	for _, current := range value {
		if current == 0xFFFD {
			return false
		}
	}
	return true
}
