package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

func TestUsageStatisticsReadAPIQueryAndAuthorization(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token string) *httptest.ResponseRecorder {
		return performRequest(t, api, method, path, token, "127.0.0.1", "127.0.0.1:12345", nil)
	}
	for _, token := range []string{"", testControlToken} {
		response := request(http.MethodGet, "/api/v1/usage", token)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("usage exposed without read authorization: %+v", response)
		}
	}
	response := performRequest(t, api, http.MethodGet, "/api/v1/usage", testAccessToken, "127.0.0.1", "127.0.0.1:12345", nil)
	var result domain.UsageStatistics
	decodeData(t, response, &result)
	if result.Period != "30d" || result.UTCOffsetMinutes != 0 || result.Cost.Currency != "USD" || result.Models == nil || result.RecentSessions == nil {
		t.Fatalf("wrong defaults/empty collections: %+v", result)
	}
	response = performRequest(t, api, http.MethodGet, "/api/v1/usage?period=all&utc_offset_minutes=480", testAccessToken, "127.0.0.1", "127.0.0.1:12345", nil)
	decodeData(t, response, &result)
	if result.Period != "all" || result.UTCOffsetMinutes != 480 {
		t.Fatalf("usage query not forwarded: %+v", result)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "usage scope", Profile: "review"})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	response = request(http.MethodGet, "/api/v1/usage?period=all&utc_offset_minutes=480&thread_id="+threadID, testAccessToken)
	decodeDataStatus(t, response, http.StatusOK, &result)
	if result.Period != "all" || result.UTCOffsetMinutes != 480 || result.Totals.Calls != 0 {
		t.Fatalf("scoped usage query not forwarded: %+v", result)
	}
	if response := request(http.MethodGet, "/api/v1/usage?thread_id=missing-thread", testAccessToken); response.Code != http.StatusNotFound {
		t.Fatalf("unknown Thread must not become empty global usage: %d %s", response.Code, response.Body.String())
	}
	for _, query := range []string{"period=week", "period=", "period=today&period=all", "utc_offset_minutes=", "utc_offset_minutes=841", "utc_offset_minutes=-841", "utc_offset_minutes=1.5", "utc_offset_minutes=0&utc_offset_minutes=1", "thread_id=", "thread_id=a&thread_id=b", "thread_id=%20a", "thread_id=a%2Fb", "thread_id=a%0Ab", "unknown=all"} {
		response := request(http.MethodGet, "/api/v1/usage?"+query, testAccessToken)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid query accepted: %s: %+v", query, response)
		}
	}
	if response := request(http.MethodPost, "/api/v1/usage", testAccessToken); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("usage accepted a mutation: %+v", response)
	}
	withBody := performRequest(t, api, http.MethodGet, "/api/v1/usage", testAccessToken, "127.0.0.1", "127.0.0.1:12345", strings.NewReader("{}"))
	if withBody.Code != http.StatusBadRequest {
		t.Fatalf("usage accepted a GET body: %d %s", withBody.Code, withBody.Body.String())
	}
}

func TestUsageStatisticsOpenAPIContract(t *testing.T) {
	raw, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var spec openAPIDocument
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	operation := spec.Paths["/api/v1/usage"].Get
	if operation == nil || !operation.ReadOnly || operation.OperationID != "getUsageStatistics" || len(operation.Parameters) != 3 {
		t.Fatalf("usage contract missing: %+v", operation)
	}
	thread := operation.Parameters[2]
	if thread.Name != "thread_id" || thread.In != "query" || thread.Required || thread.Schema["minLength"] != float64(1) || thread.Schema["maxLength"] != float64(256) || operation.Responses["404"] == nil {
		t.Fatalf("optional Thread scope contract missing: %+v", thread)
	}
	for _, name := range []string{"UsageStatistics", "UsageTotals", "UsageCost", "UsageModel", "UsageSession", "UsageDay", "UsageActivityDay"} {
		if spec.Components.Schemas[name] == nil {
			t.Fatalf("usage response type %s absent from OpenAPI", name)
		}
	}
}
