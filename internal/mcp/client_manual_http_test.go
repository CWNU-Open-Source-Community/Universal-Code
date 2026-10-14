package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestManualConfigLocalHTTPTransmitsOnlyConfiguredHeaders(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		posts.Add(1)
		if r.Header.Get("X-Project") != "fixture" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("manual fixed header projection changed or added authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var envelope Envelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(envelope.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(clientFixtureResponse(envelope))
	}))
	defer server.Close()
	d := clientTransportDescriptor(TransportStreamableHTTP, server.URL, nil)
	d.ProtocolVersion, d.Headers = ConfigClientProtocolVersion, map[string]string{"x-project": "fixture"}
	transport, err := newRemoteClientTransport(d, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := newClient(transport, d)
	defer client.Close()
	if _, err := client.Discover(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(t.Context(), "lookup", json.RawMessage(`{"query":"fixture"}`), 4096); err != nil {
		t.Fatal(err)
	}
	if posts.Load() < 4 {
		t.Fatal("local HTTP did not initialize/discover/call")
	}
}
