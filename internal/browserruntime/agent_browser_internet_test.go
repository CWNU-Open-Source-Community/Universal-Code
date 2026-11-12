package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentBrowserIndependentNetworkModeAdmission(t *testing.T) {
	base := AgentBrowserStartRequest{RuntimeDeadline: time.Now().Add(time.Minute),
		Authority: AgentBrowserAuthority{RunID: "run", ManagerBootID: "boot", SessionID: "session", Generation: 1,
			PermissionSnapshotID: "permissions", PermissionRevision: 1, PermissionActivation: 1, RunAuthorizationFence: 1},
		CheckAuthority:     func(context.Context, AgentBrowserAuthority) error { return nil },
		ReviewNetworkWrite: func(context.Context, AgentBrowserNetworkRequest, func() error) error { return errors.New("denied") }}
	for _, permission := range []string{"ask", "auto", "full"} {
		for _, mode := range []AgentBrowserNetworkMode{AgentBrowserNetworkInternet, AgentBrowserNetworkRestricted} {
			t.Run(permission+"/"+string(mode), func(t *testing.T) {
				request := base
				request.Authority.PermissionMode, request.NetworkMode = permission, mode
				if mode == AgentBrowserNetworkRestricted {
					request.ScopedOrigins = []string{"http://127.0.0.1:3000"}
				}
				if err := validateAgentBrowserRequest(request); err != nil {
					t.Fatal(err)
				}
				request.ReviewNetworkWrite = nil
				if err := validateAgentBrowserRequest(request); err == nil {
					t.Fatal("network access without write review accepted")
				}
			})
		}
	}
	for _, mode := range []AgentBrowserNetworkMode{AgentBrowserNetworkOff, "unknown", AgentBrowserNetworkRestricted} {
		request := base
		request.Authority.PermissionMode, request.NetworkMode = "full", mode
		if err := validateAgentBrowserRequest(request); err == nil {
			t.Fatalf("invalid/off request accepted: %q", mode)
		}
	}
	request := base
	request.Authority.PermissionMode, request.NetworkMode = "ask", AgentBrowserNetworkInternet
	request.ScopedOrigins = []string{"http://127.0.0.1:3000"}
	if err := validateAgentBrowserRequest(request); err == nil {
		t.Fatal("internet with a conflicting restricted origin set accepted")
	}
}

func TestAgentBrowserInternetDestinationPolicy(t *testing.T) {
	r := &AgentBrowserRuntime{request: AgentBrowserStartRequest{NetworkMode: AgentBrowserNetworkInternet}}
	for _, raw := range []string{"https://example.com/docs#section", "http://127.0.0.1:3000/preview", "http://192.168.1.25:8080/"} {
		if err := r.authorizeSessionNavigation(t.Context(), raw); err != nil {
			t.Fatalf("network-accessible HTTP(S) URL rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"file:///private.txt", "javascript:alert(1)", "https://user:password@example.com/"} {
		if err := r.authorizeSessionNavigation(t.Context(), raw); err == nil {
			t.Fatalf("structurally forbidden URL accepted: %s", raw)
		}
	}
}

func TestAgentBrowserInternetRequestReviewFence(t *testing.T) {
	for _, outcome := range []string{"approve", "deny", "document_changed", "revoked", "cancelled", "missing_body"} {
		t.Run(outcome, func(t *testing.T) {
			conn := newScreencastTestConn()
			life, stop := context.WithCancel(t.Context())
			defer stop()
			cdp := newAgentBrowserCDP(conn, func(cdpWireMessage) {})
			defer cdp.close()
			r := &AgentBrowserRuntime{request: AgentBrowserStartRequest{NetworkMode: AgentBrowserNetworkInternet},
				life: life, cancel: stop, cdp: cdp, networkEpoch: 3, networkPending: map[string]context.CancelFunc{}}
			r.request.CheckAuthority = func(context.Context, AgentBrowserAuthority) error {
				if outcome == "revoked" {
					return errors.New("authority revoked")
				}
				return nil
			}
			var reviewed bool
			r.request.ReviewNetworkWrite = func(ctx context.Context, request AgentBrowserNetworkRequest, dispatch func() error) error {
				reviewed = true
				digest := sha256.Sum256([]byte("body"))
				if request.BodyBytes != 4 || request.BodySHA256 != hex.EncodeToString(digest[:]) {
					t.Errorf("request body binding: %+v", request)
				}
				switch outcome {
				case "deny":
					return errors.New("denied")
				case "document_changed":
					r.mu.Lock()
					r.invalidateNetworkLocked()
					r.mu.Unlock()
				case "cancelled":
					stop()
				}
				err := dispatch()
				if outcome == "approve" {
					if err != nil {
						t.Error(err)
					}
					if again := dispatch(); again == nil {
						t.Error("request dispatched twice")
					}
				} else if err == nil {
					t.Error("invalidated request dispatched")
				}
				return err
			}
			ctx, cancel := context.WithCancel(life)
			defer cancel()
			paused := agentBrowserPausedRequest{}
			if err := json.Unmarshal([]byte(`{"request":{"hasPostData":true,"postDataEntries":[{"bytes":"Ym9keQ=="}]}}`), &paused); err != nil {
				t.Fatal(err)
			}
			if outcome == "missing_body" {
				paused.Request.PostDataEntries = nil
			}
			r.reviewNetworkRequest(ctx, cancel, AgentBrowserNetworkRequest{ID: "request", RequestID: "interception", Method: "POST",
				URL: "http://127.0.0.1:3000/write", DocumentEpoch: 3}, paused)
			if reviewed != (outcome != "missing_body") {
				t.Fatalf("review invoked=%t", reviewed)
			}
			var continuations int
			for _, command := range conn.wrote() {
				if command.Method == "Fetch.continueRequest" {
					continuations++
				}
			}
			want := 0
			if outcome == "approve" {
				want = 1
			}
			if continuations != want {
				t.Fatalf("continued=%d want=%d", continuations, want)
			}
			if outcome == "revoked" && life.Err() == nil {
				t.Fatal("revoked network authority did not cancel the runtime")
			}
		})
	}
}

func TestAgentBrowserRealInternetCrossOriginResourcesAndRedirect(t *testing.T) {
	for _, permission := range []string{"ask", "auto"} {
		t.Run(permission, func(t *testing.T) {
			var reads, images, workers, preflights, writes atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type,X-Test")
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
				if r.Method == "OPTIONS" {
					preflights.Add(1)
					w.WriteHeader(204)
					return
				}
				if r.Method == "POST" {
					writes.Add(1)
					data, _ := io.ReadAll(r.Body)
					fmt.Fprint(w, string(data))
					return
				}
				switch r.URL.Path {
				case "/image":
					images.Add(1)
					w.WriteHeader(204)
				case "/worker-read":
					workers.Add(1)
					fmt.Fprint(w, "worker remote read")
				case "/landing":
					fmt.Fprint(w, "<!doctype html><title>Landing</title><h1>Remote redirect destination</h1>")
				default:
					reads.Add(1)
					fmt.Fprint(w, "remote resource loaded")
				}
			}))
			defer remote.Close()
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/redirect":
					http.Redirect(w, r, remote.URL+"/landing", 302)
				case "/worker.js":
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprintf(w, `fetch(%q);fetch(%q,{method:'POST',body:'worker write'}).catch(()=>{});`, remote.URL+"/worker-read", remote.URL+"/worker-write")
				default:
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, `<!doctype html><title>Internet</title><h1>Local page</h1><img src="%s/image"><script>
fetch(%q).then(r=>r.text()).then(t=>document.body.append(t));
fetch(%q,{method:'POST',headers:{'Content-Type':'application/json','X-Test':'preflight'},body:'{}'}).catch(()=>{});
new Worker('/worker.js');
</script>`, remote.URL, remote.URL+"/read", remote.URL+"/write")
				}
			}))
			defer local.Close()
			proposals := make(chan AgentBrowserNetworkRequest, 8)
			r, _ := agentBrowserTestRuntimeWithCheck(t, nil, func(request *AgentBrowserStartRequest) {
				request.Authority.PermissionMode = permission
				request.NetworkMode = AgentBrowserNetworkInternet
				request.ReviewNetworkWrite = func(ctx context.Context, request AgentBrowserNetworkRequest, dispatch func() error) error {
					proposals <- request
					return errors.New("denied")
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if _, err := r.Navigate(ctx, local.URL); err != nil {
				t.Fatal(err)
			}
			agentBrowserWaitSnapshot(t, r, func(s AgentBrowserSnapshot) bool { return strings.Contains(s.Text, "remote resource loaded") })
			for range 2 {
				select {
				case request := <-proposals:
					if request.Method != "POST" || !strings.HasPrefix(request.URL, remote.URL+"/") {
						t.Fatalf("unexpected write proposal: %+v", request)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if reads.Load() == 0 || images.Load() == 0 || workers.Load() == 0 || preflights.Load() == 0 || writes.Load() != 0 {
				t.Fatalf("cross-origin requests: reads=%d images=%d workers=%d preflights=%d unapproved writes=%d", reads.Load(), images.Load(), workers.Load(), preflights.Load(), writes.Load())
			}
			navigation, err := r.Navigate(ctx, local.URL+"/redirect")
			if err != nil || navigation.CanonicalURL != remote.URL+"/landing" {
				t.Fatalf("cross-origin redirect: %+v %v", navigation, err)
			}
			t.Logf("%s: cross-origin fetch, image, worker and OPTIONS loaded; page/worker POSTs reviewed and denied; redirect landed on another origin", permission)
		})
	}
}
