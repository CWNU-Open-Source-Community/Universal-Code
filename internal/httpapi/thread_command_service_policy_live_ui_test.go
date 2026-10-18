package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in browser acceptance uses the built application, real API, SQLite, and
// owned native process. The model response only selects this fixed fixture.
func TestThreadCommandServiceSettingsBrowserFixture(t *testing.T) {
	output := os.Getenv("UC_SERVICE_SETTINGS_UI")
	if output == "" {
		t.Skip("set UC_SERVICE_SETTINGS_UI to a local fixture metadata path")
	}
	dist, err := filepath.Abs(filepath.Join("..", "..", "web", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatalf("build the frontend first: %v", err)
	}
	f := newThreadApplicationServicesLiveFixture(t)
	job := f.start(t, "shared-ui")
	f.waitDetail(t, job, func(d threadApplicationServicesLiveDetail) bool { return len(d.CandidateURLs) > 0 })
	mux := http.NewServeMux()
	mux.Handle("/api/", f.api)
	mux.Handle("/", http.FileServer(http.Dir(dist)))
	server := httptest.NewUnstartedServer(mux)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	metadata, err := json.Marshal(map[string]string{
		"url":       server.URL + "/#/threads/" + job.threadID,
		"thread_id": job.threadID, "run_id": job.runID, "job_id": job.jobID,
		"read_token": testAccessToken, "control_token": testControlToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("real service settings fixture ready at %s; write %s.stop when finished", server.URL, output)
	deadline := time.NewTimer(25 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("browser fixture timed out before explicit cleanup")
		case <-tick.C:
			if _, err := os.Stat(output + ".stop"); err == nil {
				return
			}
		}
	}
}
