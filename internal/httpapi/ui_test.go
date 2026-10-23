package httpapi

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"cyberagent-workbench/internal/webui"
)

func TestWebUIReusesLoopbackBoundaryWithoutWeakeningAPIAuthorization(t *testing.T) {
	fixture := newAPIFixture(t)
	var uiCalls atomic.Int64
	var lastUIPath string
	ui := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		uiCalls.Add(1)
		lastUIPath = request.URL.Path
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		if request.Method == http.MethodGet {
			_, _ = io.WriteString(writer, "<!doctype html><title>CyberAgent</title>")
		}
	})
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, UIHandler: ui})
	if err != nil {
		t.Fatal(err)
	}

	root := performRequest(t, api, http.MethodGet, "/", "",
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "CyberAgent") || uiCalls.Load() != 1 {
		t.Fatalf("anonymous loopback UI failed: status=%d calls=%d body=%q",
			root.Code, uiCalls.Load(), root.Body.String())
	}
	assertUISecurityHeaders(t, root, "")
	if root.Header().Get("X-CyberAgent-API-Version") != "" {
		t.Fatal("UI response was mislabeled as an API response")
	}
	legacy := performRequest(t, api, http.MethodGet, "/legacy/threads/thread-1", "",
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if legacy.Code != http.StatusOK || lastUIPath != "/legacy/threads/thread-1" || uiCalls.Load() != 2 {
		t.Fatalf("legacy SPA route did not reach UI handler: status=%d path=%q calls=%d body=%q",
			legacy.Code, lastUIPath, uiCalls.Load(), legacy.Body.String())
	}

	unauthorizedAPI := performRequest(t, api, http.MethodGet, "/api/v1/health", "",
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, unauthorizedAPI, http.StatusUnauthorized, "POLICY_DENIED")
	authorizedAPI := performRequest(t, api, http.MethodGet, "/api/v1/health", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if authorizedAPI.Code != http.StatusOK || uiCalls.Load() != 2 {
		t.Fatalf("authenticated API was diverted to UI: status=%d calls=%d", authorizedAPI.Code, uiCalls.Load())
	}
	reservedTypo := performRequest(t, api, http.MethodGet, "/api/v10", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, reservedTypo, http.StatusNotFound, "NOT_FOUND")
}

func TestWebUIRejectsUnsafeRequestsBeforeStaticHandler(t *testing.T) {
	fixture := newAPIFixture(t)
	var uiCalls atomic.Int64
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, UIHandler: styleNonceUIHandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request, _ string) {
			uiCalls.Add(1)
			writer.WriteHeader(http.StatusNoContent)
		})})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		method string
		path   string
		token  string
		host   string
		remote string
		body   io.Reader
		status int
	}{
		{name: "external Host", method: http.MethodGet, path: "/", host: "agent.example:8765",
			remote: "127.0.0.1:45000", status: http.StatusForbidden},
		{name: "external client", method: http.MethodGet, path: "/", host: "127.0.0.1:8765",
			remote: "192.0.2.10:45000", status: http.StatusForbidden},
		{name: "write", method: http.MethodPost, path: "/", host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", status: http.StatusMethodNotAllowed},
		{name: "query", method: http.MethodGet, path: "/?token=no", host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", status: http.StatusBadRequest},
		{name: "old legacy query", method: http.MethodGet, path: "/?legacy=1", host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", status: http.StatusBadRequest},
		{name: "body", method: http.MethodGet, path: "/", host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", body: strings.NewReader("unexpected"), status: http.StatusBadRequest},
		{name: "authorization", method: http.MethodGet, path: "/", token: testAccessToken,
			host: "127.0.0.1:8765", remote: "127.0.0.1:45000", status: http.StatusBadRequest},
		{name: "noncanonical", method: http.MethodGet, path: "/ui//route", host: "127.0.0.1:8765",
			remote: "127.0.0.1:45000", status: http.StatusBadRequest},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			response := performRequest(t, api, current.method, current.path, current.token,
				current.host, current.remote, current.body)
			if response.Code != current.status || response.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("status=%d want=%d headers=%#v body=%q",
					response.Code, current.status, response.Header(), response.Body.String())
			}
			assertUISecurityHeaders(t, response, "")
		})
	}
	if uiCalls.Load() != 0 {
		t.Fatalf("unsafe requests reached static handler %d times", uiCalls.Load())
	}
}

func TestWebUIPanicIsContainedWithoutInternalDetails(t *testing.T) {
	fixture := newAPIFixture(t)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, UIHandler: http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { panic("private static failure") })})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(t, api, http.MethodGet, "/", "",
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if response.Code != http.StatusInternalServerError ||
		strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "panic") {
		t.Fatalf("UI panic leaked or returned wrong status: status=%d body=%q", response.Code, response.Body.String())
	}
	assertUISecurityHeaders(t, response, "")
}

func TestWebUIStyleNonceMatchesIndexAndChangesPerResponse(t *testing.T) {
	fixture := newAPIFixture(t)
	bundle, err := webui.LoadEmbeddedFS(fstest.MapFS{
		"dist/index.html": &fstest.MapFile{Data: []byte(`<!doctype html><head>` +
			`<meta name="uc-style-nonce" nonce="__UC_STYLE_NONCE__" /></head>` +
			`<script type="module" src="/assets/index-AbCd1234.js"></script>`)},
		"dist/assets/index-AbCd1234.js": &fstest.MapFile{Data: []byte("document.body.dataset.ready = 'yes';")},
	}, "dist")
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, UIHandler: bundle})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, requestPath := range []string{"/", "/legacy/threads/thread-1", "/"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765"+requestPath, nil)
		request.RemoteAddr = "127.0.0.1:45000"
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
			response.Header().Get("ETag") != "" {
			t.Fatalf("unexpected index response: status=%d headers=%#v", response.Code, response.Header())
		}
		_, suffix, found := strings.Cut(response.Body.String(), `<meta name="uc-style-nonce" nonce="`)
		nonce, _, closed := strings.Cut(suffix, `"`)
		raw, err := base64.StdEncoding.DecodeString(nonce)
		if !found || !closed || err != nil || len(raw) != 32 || seen[nonce] {
			t.Fatalf("index nonce is missing, malformed, or reused: %q", nonce)
		}
		seen[nonce] = true
		assertUISecurityHeaders(t, response, nonce)
	}

	for _, token := range []string{"", testAccessToken} {
		response := performRequest(t, api, http.MethodGet, "/api/v1/health", token,
			"127.0.0.1:8765", "127.0.0.1:45000", nil)
		wantStatus := http.StatusOK
		if token == "" {
			wantStatus = http.StatusUnauthorized
		}
		if response.Code != wantStatus ||
			response.Header().Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" ||
			response.Header().Get("X-CyberAgent-UI-Version") != "" {
			t.Fatalf("UI nonce changed API authorization or policy: status=%d headers=%#v", response.Code, response.Header())
		}
	}
}

type styleNonceUIHandlerFunc func(http.ResponseWriter, *http.Request, string)

func (handler styleNonceUIHandlerFunc) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	handler(writer, request, "")
}

func (handler styleNonceUIHandlerFunc) ServeHTTPWithStyleNonce(writer http.ResponseWriter, request *http.Request, nonce string) {
	handler(writer, request, nonce)
}

func assertUISecurityHeaders(t *testing.T, response *httptest.ResponseRecorder, nonce string) {
	t.Helper()
	header := response.Header()
	csp := header.Get("Content-Security-Policy")
	directives := make(map[string]string)
	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if _, duplicate := directives[fields[0]]; duplicate {
			t.Fatalf("duplicate CSP directive in %q", csp)
		}
		directives[fields[0]] = strings.Join(fields[1:], " ")
	}
	styleSource := "'self'"
	if nonce != "" {
		styleSource += " 'nonce-" + nonce + "'"
	}
	wantDirectives := map[string]string{
		"default-src": "'none'", "base-uri": "'none'", "connect-src": "'self'",
		"font-src": "'self'", "form-action": "'none'", "frame-ancestors": "'none'",
		"img-src": "'self' data: blob:", "manifest-src": "'self'", "media-src": "data:",
		"object-src": "'none'", "script-src": "'self'", "style-src": styleSource,
		"style-src-attr": "'unsafe-inline'", "worker-src": "'self'",
	}
	if len(directives) != len(wantDirectives) {
		t.Fatalf("unexpected CSP directives: %q", csp)
	}
	for name, want := range wantDirectives {
		if directives[name] != want {
			t.Fatalf("CSP %s=%q want=%q", name, directives[name], want)
		}
	}
	if header.Get("X-Frame-Options") != "DENY" || header.Get("X-Content-Type-Options") != "nosniff" ||
		header.Get("Referrer-Policy") != "no-referrer" ||
		header.Get("Cross-Origin-Opener-Policy") != "same-origin" ||
		header.Get("Cross-Origin-Resource-Policy") != "same-origin" ||
		header.Get("X-CyberAgent-UI-Version") != "web-ui.v1" || header.Get("X-Request-ID") == "" {
		t.Fatalf("missing or unsafe UI security headers: %#v", header)
	}
}
