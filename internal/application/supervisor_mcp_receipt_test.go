package application

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func TestMCPDiscoveryReceiptKeepsWholeSchemaOrOmitsPage(t *testing.T) {
	var page any
	if err := json.Unmarshal([]byte(`{"catalog_revision":"catalog-one","total_matches":4,"offset":0,"operations":[
		{"name":"scroll","input_schema":{"type":"object","required":["window_id","app_id","observation_id","dx","dy"],"properties":{"dy":{"enum":[-10,-5,0,5,10]},"nested":{"properties":{"one":{"properties":{"two":{"properties":{"three":{"type":"string"}}}}}}}}}},
		{"name":"click","input_schema":{"required":["window_id","app_id","observation_id","x","y"]}},
		{"name":"type","input_schema":{"required":["window_id","app_id","observation_id","text"]}},
		{"name":"key","input_schema":{"required":["window_id","app_id","observation_id","key"]}}
	]}`), &page); err != nil {
		t.Fatal(err)
	}
	call := boundaryBudgetCall(t, "discovery", "mcp_tool_search", page.(map[string]any))
	call.Round, call.Position, call.ModelAttempt = 1, 1, 1
	call.PayloadJSON, call.AuthorityJSON = `{"query":"computer"}`, `{}`
	call.CreatedAt = time.Now().UTC()
	call.CompletedAt = &call.CreatedAt
	original := call.ResultJSON
	rounds := []domain.SupervisorToolRound{{RunID: call.RunID, Turn: call.Turn, AttemptID: call.AttemptID,
		Round: call.Round, ModelAttempt: call.ModelAttempt, CreatedAt: call.CreatedAt,
		CompletedAt: call.CompletedAt, Calls: []domain.SupervisorToolCall{call}}}
	boundary, err := minimalSupervisorBoundaryReceipt([]domain.SupervisorToolCall{call}, "session-mcp", "attempt-next")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"boundary", "segment"} {
		for _, budget := range []int{20000, 800} {
			t.Run(fmt.Sprintf("%s/%d", kind, budget), func(t *testing.T) {
				var content string
				var result any
				var ref domain.HistoryReadRequest
				var omitted bool
				if kind == "boundary" {
					content, err = supervisorBoundaryReceiptContent(boundary, budget, nil, false)
					if err != nil {
						t.Fatal(err)
					}
					entry := boundaryBudgetEntries(t, content)[call.CallID]
					result = entry["result"]
					omitted, _ = entry["result_details_omitted"].(bool)
					encoded, _ := json.Marshal(entry["original_result"])
					if err := json.Unmarshal(encoded, &ref); err != nil {
						t.Fatal(err)
					}
					if supervisorBoundaryReceiptTokens(boundary, content) > budget {
						t.Fatal("boundary exceeded its capacity")
					}
				} else {
					plan, e := supervisorSegmentReceiptPlan(rounds, 1, budget, "attempt-next", "session-mcp")
					if e != nil {
						t.Fatal(e)
					}
					content = plan.ReceiptContent
					var entries []supervisorSegmentReceiptRound
					for _, line := range strings.Split(content, "\n") {
						if strings.HasPrefix(line, "{") {
							var entry supervisorSegmentReceiptRound
							if e := json.Unmarshal([]byte(line), &entry); e != nil {
								t.Fatal(e)
							}
							entries = append(entries, entry)
						}
					}
					if len(entries) != 1 || len(entries[0].Calls) != 1 {
						t.Fatal("missing segment receipt")
					}
					entry := entries[0].Calls[0]
					result, ref, omitted = entry.Result, entry.OriginalResult, entry.DetailsOmitted
					if plan.ReceiptTokens > budget {
						t.Fatal("segment exceeded its capacity")
					}
				}
				if budget == 20000 && (result == nil || omitted) {
					t.Fatal("a fitting search page must remain complete")
				}
				if result != nil && !reflect.DeepEqual(result, page) {
					t.Fatal("search page was partially rewritten: schemas and page membership must be atomic")
				}
				if result == nil && !omitted {
					t.Fatal("omitted search page was not marked for readback")
				}
				if ref.SourceID == "" || ref.ExpectedSHA256 != session.ContentSHA256(original) || call.ResultJSON != original {
					t.Fatal("durable discovery result or exact readback changed")
				}
			})
		}
	}
}
