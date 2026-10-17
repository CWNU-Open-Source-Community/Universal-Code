package toolgateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSpecialistResultReadUsesChildEnvelopeOnly(t *testing.T) {
	if SpecialistResultReadTool.Valid() {
		t.Fatal("child result reader became a root dispatch tool")
	}
	definition := SpecialistResultReadDefinition()
	if definition.Name != SpecialistResultReadTool || !json.Valid(definition.InputSchema) {
		t.Fatal("child schema missing")
	}
	payload := `{"call_id":"completed","part":"result","expected_sha256":"` + strings.Repeat("a", 64) + `","offset":0,"limit":1024}`
	envelope, _ := json.Marshal(SpecialistToolPayload{Tool: string(SpecialistResultReadTool), Arguments: json.RawMessage(payload)})
	_, _, err := NormalizeSpecialistToolPayload(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(payload, `"offset":0`, `"offset":-1`, 1),
		strings.Replace(payload, `"limit":1024`, `"limit":8193`, 1),
		strings.Replace(payload, `"part":"result"`, `"part":"parent_history"`, 1),
		strings.TrimSuffix(payload, "}") + `,"agent_id":"parent"}`,
		payload + payload,
	} {
		if _, _, err := NormalizeSpecialistResultRead(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid request accepted: %s", raw)
		}
	}
}
