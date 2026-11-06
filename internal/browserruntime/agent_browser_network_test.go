package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentBrowserScopedNetworkRequiresOrderedAllowRules(t *testing.T) {
	for _, raw := range []string{`{"domains":[]}`, `{"domains":[{"domain":"Network","commands":[{"name":"setBlockedURLs","parameters":[{"name":"urls"}]}]}]}`} {
		if err := validateAgentBrowserNetworkProtocol([]byte(raw)); err == nil {
			t.Fatal("legacy network rules silently accepted")
		}
	}
	if err := validateAgentBrowserNetworkProtocol([]byte(`{"domains":[{"domain":"Network","commands":[{"name":"setBlockedURLs","parameters":[{"name":"urlPatterns"}]}],"types":[{"id":"BlockPattern","properties":[{"name":"block"}]}]}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestAgentBrowserNetworkContinuationWaitsForProtocolError(t *testing.T) {
	upgrade := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var command cdpWireMessage
		if conn.ReadJSON(&command) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": command.ID, "sessionId": command.SessionID, "error": map[string]any{"code": -32602, "message": "Invalid InterceptionId"}})
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cdp := newAgentBrowserCDP(conn, func(cdpWireMessage) {})
	defer cdp.close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	await, err := cdp.send(ctx, "session", "Fetch.continueRequest", map[string]any{"requestId": "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err = await(); err == nil {
		t.Fatal("protocol rejection treated as a successful dispatch")
	}
}

func TestAgentBrowserRealScopedNetworkOrigins(t *testing.T) {
	if os.Getenv("TRAVERSE_TEST_AGENT_BROWSER") != "1" {
		t.Skip("opt-in real installed browser")
	}
	var undeclaredHits, declaredReads, workerReads atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		undeclaredHits.Add(1)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,X-Test")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.WriteHeader(204)
	}))
	defer other.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/worker.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprintf(w, `fetch('/worker-read');fetch(%q,{method:'POST',mode:'no-cors',body:'worker-write'}).catch(()=>{});`, other.URL+"/worker")
		case "/worker-read":
			workerReads.Add(1)
			w.WriteHeader(204)
		case "/redirect":
			http.Redirect(w, r, other.URL+"/redirect-destination", 302)
		case "/read":
			declaredReads.Add(1)
			fmt.Fprint(w, "declared read works")
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!doctype html><title>Scoped page</title><h1>Declared site</h1><img src="%s/image"><script>
    fetch('/read').then(r=>r.text()).then(t=>document.body.append(t));
    fetch(%q,{method:'POST',mode:'no-cors',body:'unapproved-write'}).catch(()=>{});
    fetch(%q,{method:'POST',headers:{'Content-Type':'application/json','X-Test':'preflight'},body:'{}'}).catch(()=>{});
    fetch('/redirect').catch(()=>{});
				new Worker('/worker.js');
				</script>`, other.URL, other.URL+"/write", other.URL+"/preflight")
		}
	}))
	defer site.Close()
	for _, mode := range []string{"ask", "auto"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := agentBrowserTestRuntimeWithCheck(t, nil, func(req *AgentBrowserStartRequest) {
				req.Authority.PermissionMode = mode
				req.ScopedOrigins = []string{site.URL}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if _, err := r.Navigate(ctx, site.URL); err != nil {
				t.Fatal(err)
			}
			s := agentBrowserWaitSnapshot(t, r, func(s AgentBrowserSnapshot) bool { return strings.Contains(s.Text, "declared read works") })
			if !strings.Contains(s.Text, "Declared site") {
				t.Fatal(s.Text)
			}
			time.Sleep(300 * time.Millisecond)
			if n := undeclaredHits.Load(); n != 0 {
				t.Fatalf("undeclared origin received %d requests, including possible OPTIONS", n)
			}
			if _, err := r.Navigate(ctx, site.URL+"/redirect"); err == nil {
				t.Fatal("redirect to undeclared origin accepted")
			}
			if n := undeclaredHits.Load(); n != 0 {
				t.Fatalf("redirect reached undeclared origin: %d", n)
			}
			t.Logf("%s: declared site and fetch work; undeclared image, POST, CORS OPTIONS and redirect received zero requests", mode)
		})
	}
	if declaredReads.Load() != 2 {
		t.Fatalf("declared reads=%d", declaredReads.Load())
	}
	if workerReads.Load() != 2 {
		t.Fatalf("declared worker reads=%d", workerReads.Load())
	}
}

func TestAgentBrowserRealScopedWriteExactApproval(t *testing.T) {
	const body = "exact approved page body"
	expected := sha256.Sum256([]byte(body))
	for _, outcome := range []string{"approve", "deny", "navigate", "close"} {
		t.Run(outcome, func(t *testing.T) {
			received := make(chan string, 4)
			proposals := make(chan AgentBrowserNetworkRequest, 4)
			decide := make(chan bool, 1)
			completed := make(chan error, 1)
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					data, _ := io.ReadAll(r.Body)
					received <- string(data)
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				if r.URL.Path == "/blank" {
					fmt.Fprint(w, `<!doctype html><h1>Other document</h1>`)
					return
				}
				fmt.Fprint(w, `<!doctype html><title>Write approval</title><h1>Read works</h1><button onclick="fetch('/write',{method:'POST',body:'`+body+`'}).then(()=>document.body.append('sent'))">Send</button>`)
			}))
			defer site.Close()
			r, _ := agentBrowserTestRuntimeWithCheck(t, nil, func(req *AgentBrowserStartRequest) {
				req.Authority.PermissionMode = "auto"
				req.ScopedOrigins = []string{site.URL}
				req.ReviewNetworkWrite = func(ctx context.Context, v AgentBrowserNetworkRequest, dispatch func() error) error {
					proposals <- v
					select {
					case approved := <-decide:
						if approved {
							err := dispatch()
							completed <- err
							return err
						}
						completed <- fmt.Errorf("denied")
						return fmt.Errorf("denied")
					case <-ctx.Done():
						completed <- ctx.Err()
						return ctx.Err()
					}
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if _, err := r.Navigate(ctx, site.URL); err != nil {
				t.Fatal(err)
			}
			s := agentBrowserWaitSnapshot(t, r, func(s AgentBrowserSnapshot) bool { return strings.Contains(s.Text, "Read works") })
			ref := agentBrowserElementByName(t, s, "Send")
			if _, err := r.Click(ctx, s.SnapshotID, ref.Ref); err != nil {
				t.Fatal(err)
			}
			var proposal AgentBrowserNetworkRequest
			select {
			case proposal = <-proposals:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if proposal.Method != "POST" || proposal.URL != site.URL+"/write" || proposal.BodyBytes != len(body) || proposal.BodySHA256 != hex.EncodeToString(expected[:]) {
				t.Fatalf("request binding: %+v", proposal)
			}
			select {
			case got := <-received:
				t.Fatalf("unapproved request sent: %s", got)
			case <-time.After(100 * time.Millisecond):
			}
			switch outcome {
			case "approve":
				decide <- true
			case "deny":
				decide <- false
			case "navigate":
				if _, err := r.Navigate(ctx, site.URL+"/blank"); err != nil {
					t.Fatal(err)
				}
				decide <- true
			case "close":
				r.Cancel()
				decide <- true
			}
			select {
			case err := <-completed:
				if outcome == "approve" && err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if outcome == "approve" {
				select {
				case got := <-received:
					if got != body {
						t.Fatal(got)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			select {
			case got := <-received:
				t.Fatalf("unexpected or repeated write: %s", got)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}
