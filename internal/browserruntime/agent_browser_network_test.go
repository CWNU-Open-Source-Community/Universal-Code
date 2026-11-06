package browserruntime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentBrowserRealScopedNetworkOrigins(t *testing.T) {
	var undeclaredHits, declaredReads atomic.Int32
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
}
