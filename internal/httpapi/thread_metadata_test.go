package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestThreadMetadataRequiresControlAndRetainsThreadIdentity(t *testing.T) {
	f := newAPIFixture(t)
	thread, err := f.store.GetThreadByRun(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"name", "pin"} {
		response := performSessionMessageRequest(t, api, http.MethodPut, "/api/v1/threads/"+thread.ID+"/"+action, testAccessToken, "", "application/json", strings.NewReader(`{}`))
		if response.Code != http.StatusUnauthorized {
			t.Fatal("read token changed metadata", response.Code)
		}
	}
	var view ThreadView
	for _, body := range []string{`{"title":"  Conversation result  "}`, `{"title":"Conversation result"}`} {
		decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodPut, "/api/v1/threads/"+thread.ID+"/name", testControlToken, "", "application/json", strings.NewReader(body)), http.StatusOK, &view)
	}
	if view.ID != thread.ID || view.Title != "Conversation result" || view.LastRunID != thread.LastRunID {
		t.Fatal(view)
	}
	decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodPut, "/api/v1/threads/"+thread.ID+"/pin", testControlToken, "", "application/json", strings.NewReader(`{"pinned":true}`)), http.StatusOK, &view)
	if !view.Pinned {
		t.Fatal(view)
	}
	items, _ := readThreadList(t, api, "/api/v1/threads?pinned=true")
	if len(items) != 1 || !items[0].Pinned || items[0].Title != view.Title {
		t.Fatal(items)
	}
	for _, request := range []struct{ action, body string }{
		{"name", `{"title":" "}`}, {"name", `{"title":"bad\nname"}`}, {"name", `{"title":"valid","extra":true}`},
		{"pin", `{}`}, {"pin", `{"pinned":null}`}, {"pin", `{"pinned":"true"}`},
	} {
		response := performSessionMessageRequest(t, api, http.MethodPut, "/api/v1/threads/"+thread.ID+"/"+request.action, testControlToken, "", "application/json", strings.NewReader(request.body))
		if response.Code != http.StatusBadRequest {
			t.Fatal(request, response.Code)
		}
	}
}
