//go:build workbenchjourney

package main

import (
	"encoding/json"
	"testing"
)

func TestReconnectMarkerBelongsToLatestUserTurn(t *testing.T) {
	for _, current := range []string{"JOURNEY_RECONNECT: new-turn", "JOURNEY_READ: Continue normally"} {
		t.Run(current, func(t *testing.T) {
			var request modelRequest
			body, err := json.Marshal(map[string]any{
				"messages": []map[string]string{
					{"role": "user", "content": "JOURNEY_RECONNECT: old-turn"},
					{"role": "assistant", "content": "Finished old turn"},
					{"role": "user", "content": current},
				},
				"tools": []map[string]string{{"name": "workspace_read"}},
			})
			if err != nil || json.Unmarshal(body, &request) != nil {
				t.Fatal("invalid model fixture")
			}
			block, err := nextResponse(request)
			if err != nil {
				t.Fatal(err)
			}
			if current == "JOURNEY_READ: Continue normally" {
				if block.Type == "journey_reconnect_hold" {
					t.Fatalf("ordinary follow-up reused an old hold: %+v", block)
				}
			} else if block.Type != "journey_reconnect_hold" || block.Text != "new-turn" {
				t.Fatalf("new turn reused an old marker: %+v", block)
			}
		})
	}
}
