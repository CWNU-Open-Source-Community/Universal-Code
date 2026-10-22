package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBundleLoadsImmutableSnapshotAndServesBoundedRoutes(t *testing.T) {
	directory := writeTestBundle(t)
	bundle, err := LoadDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AssetCount() != 6 || len(bundle.Digest()) != 64 || bundle.Source() == "" {
		t.Fatalf("unexpected bundle metadata: source=%q assets=%d digest=%q",
			bundle.Source(), bundle.AssetCount(), bundle.Digest())
	}
	assertAllowlistedRootAssetsServed(t, bundle)

	index := requestBundle(t, bundle, http.MethodGet, "/", "", "")
	if index.Code != http.StatusOK || index.Body.String() != testIndexBody ||
		index.Header().Get("Cache-Control") != "no-store" ||
		index.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		index.Header().Get("ETag") == "" {
		t.Fatalf("unexpected index response: status=%d headers=%#v body=%q",
			index.Code, index.Header(), index.Body.String())
	}

	assetPath := "/assets/index-AbCd1234.js"
	asset := requestBundle(t, bundle, http.MethodGet, assetPath, "", "")
	if asset.Code != http.StatusOK || asset.Body.String() != testScriptBody ||
		asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		asset.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("unexpected asset response: status=%d headers=%#v body=%q",
			asset.Code, asset.Header(), asset.Body.String())
	}
	head := requestBundle(t, bundle, http.MethodHead, assetPath, "", "")
	if head.Code != http.StatusOK || head.Body.Len() != 0 ||
		head.Header().Get("Content-Length") != asset.Header().Get("Content-Length") {
		t.Fatalf("unexpected HEAD response: status=%d headers=%#v body=%q",
			head.Code, head.Header(), head.Body.String())
	}
	notModified := requestBundle(t, bundle, http.MethodGet, assetPath, "", asset.Header().Get("ETag"))
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional asset response: status=%d body=%q", notModified.Code, notModified.Body.String())
	}

	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	unchanged := requestBundle(t, bundle, http.MethodGet, "/", "", "")
	if unchanged.Body.String() != testIndexBody {
		t.Fatalf("loaded bundle changed after disk mutation: %q", unchanged.Body.String())
	}
}

func TestBundleSPAFallbackAndUnknownAssetsFailClosed(t *testing.T) {
	bundle, err := LoadDirectory(writeTestBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		method string
		path   string
		accept string
		status int
	}{
		{name: "navigation", method: http.MethodGet, path: "/runs/run-1", accept: "text/html", status: http.StatusOK},
		{name: "legacy workbench", method: http.MethodGet, path: "/legacy", accept: "text/html", status: http.StatusOK},
		{name: "legacy thread", method: http.MethodGet, path: "/legacy/threads/thread-1", accept: "text/html", status: http.StatusOK},
		{name: "xhtml", method: http.MethodGet, path: "/sessions/one", accept: "application/xhtml+xml", status: http.StatusOK},
		{name: "zero quality", method: http.MethodGet, path: "/runs/run-1", accept: "text/html;q=0.0", status: http.StatusNotFound},
		{name: "no accept", method: http.MethodGet, path: "/runs/run-1", status: http.StatusNotFound},
		{name: "missing asset", method: http.MethodGet, path: "/assets/missing-AbCd1234.js", accept: "text/html", status: http.StatusNotFound},
		{name: "extension", method: http.MethodGet, path: "/secrets.txt", accept: "text/html", status: http.StatusNotFound},
		{name: "dot segment", method: http.MethodGet, path: "/.hidden", accept: "text/html", status: http.StatusNotFound},
		{name: "deep route", method: http.MethodGet, path: "/1/2/3/4/5/6/7/8/9", accept: "text/html", status: http.StatusNotFound},
		{name: "write", method: http.MethodPost, path: "/", status: http.StatusMethodNotAllowed},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			response := requestBundle(t, bundle, current.method, current.path, current.accept, "")
			if response.Code != current.status {
				t.Fatalf("status=%d want=%d body=%q", response.Code, current.status, response.Body.String())
			}
		})
	}
}

func TestBundleStyleNonceRendersIndexWithoutReusingCachedHTML(t *testing.T) {
	const indexBody = `<!doctype html><head><meta name="uc-style-nonce" nonce="__UC_STYLE_NONCE__" /></head>` +
		`<script type="module" src="/assets/index-AbCd1234.js"></script>`
	bundle, err := LoadEmbeddedFS(fstest.MapFS{
		"dist/index.html":               &fstest.MapFile{Data: []byte(indexBody)},
		"dist/assets/index-AbCd1234.js": &fstest.MapFile{Data: []byte(testScriptBody)},
	}, "dist")
	if err != nil {
		t.Fatal(err)
	}
	digest := bundle.Digest()
	static := requestBundle(t, bundle, http.MethodGet, "/", "", "")
	for _, requestPath := range []string{"/", "/legacy/threads/thread-1"} {
		for _, nonce := range []string{"first-request-nonce", "second-request-nonce"} {
			response := requestBundleWithStyleNonce(t, bundle, http.MethodGet, requestPath,
				"text/html", static.Header().Get("ETag"), nonce)
			wantBody := strings.ReplaceAll(indexBody, "__UC_STYLE_NONCE__", nonce)
			if response.Code != http.StatusOK || response.Body.String() != wantBody ||
				response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" ||
				response.Header().Get("Content-Length") != strconv.Itoa(len(wantBody)) {
				t.Fatalf("nonce index reused a cached document or malformed its body: status=%d headers=%#v body=%q",
					response.Code, response.Header(), response.Body.String())
			}
			head := requestBundleWithStyleNonce(t, bundle, http.MethodHead, requestPath,
				"text/html", static.Header().Get("ETag"), nonce)
			if head.Code != http.StatusOK || head.Body.Len() != 0 ||
				head.Header().Get("Content-Length") != response.Header().Get("Content-Length") ||
				head.Header().Get("Cache-Control") != "no-store" || head.Header().Get("ETag") != "" {
				t.Fatalf("unexpected nonce HEAD: status=%d headers=%#v body=%q", head.Code, head.Header(), head.Body.String())
			}
		}
	}
	unchanged := requestBundle(t, bundle, http.MethodGet, "/", "", "")
	if unchanged.Body.String() != indexBody || unchanged.Header().Get("ETag") != static.Header().Get("ETag") ||
		bundle.Digest() != digest {
		t.Fatal("per-request nonce mutated the frozen index or its digest")
	}
	conditional := requestBundle(t, bundle, http.MethodGet, "/", "", static.Header().Get("ETag"))
	if conditional.Code != http.StatusNotModified {
		t.Fatalf("plain index conditional response changed: status=%d", conditional.Code)
	}

	assetPath := "/assets/index-AbCd1234.js"
	asset := requestBundleWithStyleNonce(t, bundle, http.MethodGet, assetPath, "", "", "request-nonce")
	if asset.Code != http.StatusOK || asset.Body.String() != testScriptBody ||
		asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || asset.Header().Get("ETag") == "" {
		t.Fatalf("nonce changed immutable asset response: status=%d headers=%#v body=%q", asset.Code, asset.Header(), asset.Body.String())
	}
	notModified := requestBundleWithStyleNonce(t, bundle, http.MethodGet, assetPath, "",
		asset.Header().Get("ETag"), "another-request-nonce")
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("nonce changed conditional asset response: status=%d body=%q", notModified.Code, notModified.Body.String())
	}
}

func TestBundleStyleNonceSupportsIndexWithoutPlaceholder(t *testing.T) {
	bundle, err := LoadDirectory(writeTestBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	static := requestBundle(t, bundle, http.MethodGet, "/", "", "")
	response := requestBundleWithStyleNonce(t, bundle, http.MethodGet, "/", "", static.Header().Get("ETag"), "request-nonce")
	if response.Code != http.StatusOK || response.Body.String() != testIndexBody ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" ||
		response.Header().Get("Content-Length") != strconv.Itoa(len(testIndexBody)) {
		t.Fatalf("legacy nonce index changed or was cached: status=%d headers=%#v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestBundleRejectsUnsafeOrMalformedTrees(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, directory string)
	}{
		{name: "invalid index UTF-8", setup: func(t *testing.T, directory string) {
			writeBundleFile(t, directory, "index.html", []byte{0xff})
			writeBundleFile(t, directory, "assets/index-AbCd1234.js", []byte("ok"))
		}},
		{name: "unhashed asset", setup: func(t *testing.T, directory string) {
			writeBundleFile(t, directory, "index.html", []byte(testIndexBody))
			writeBundleFile(t, directory, "assets/index.js", []byte("ok"))
		}},
		{name: "unsupported source map", setup: func(t *testing.T, directory string) {
			writeBundleFile(t, directory, "index.html", []byte(testIndexBody))
			writeBundleFile(t, directory, "assets/index-AbCd1234.map", []byte("{}"))
		}},
		{name: "oversized index", setup: func(t *testing.T, directory string) {
			writeBundleFile(t, directory, "index.html", []byte("x"))
			if err := os.Truncate(filepath.Join(directory, "index.html"), MaxIndexBytes+1); err != nil {
				t.Fatal(err)
			}
			writeBundleFile(t, directory, "assets/index-AbCd1234.js", []byte("ok"))
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestRootAssets(t, directory)
			current.setup(t, directory)
			if bundle, err := LoadDirectory(directory); err == nil || bundle != nil {
				t.Fatalf("unsafe bundle loaded: %#v", bundle)
			}
		})
	}

	if bundle, err := LoadDirectory(""); err == nil || bundle != nil {
		t.Fatal("empty UI directory was accepted")
	}
}

func TestBundleRejectsAssetSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ordinary Windows test users may not have symbolic-link privilege")
	}
	directory := t.TempDir()
	writeTestRootAssets(t, directory)
	writeBundleFile(t, directory, "index.html", []byte(testIndexBody))
	writeBundleFile(t, directory, "real-AbCd1234.js", []byte("outside assets"))
	if err := os.MkdirAll(filepath.Join(directory, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "real-AbCd1234.js"),
		filepath.Join(directory, "assets", "index-AbCd1234.js")); err != nil {
		t.Fatal(err)
	}
	if bundle, err := LoadDirectory(directory); err == nil || bundle != nil {
		t.Fatal("Web UI asset symlink was accepted")
	}
}

func TestEmbeddedBundleLoadsOneImmutableBoundedSnapshot(t *testing.T) {
	source := fstest.MapFS{
		"dist/index.html":                    &fstest.MapFile{Data: []byte(testIndexBody)},
		"dist/apple-touch-icon.png":          &fstest.MapFile{Data: []byte(testAppleTouchIconBody)},
		"dist/traverse-board-favicon-32.png": &fstest.MapFile{Data: []byte(testFaviconBody)},
		"dist/THIRD-PARTY-NOTICES.txt":       &fstest.MapFile{Data: []byte(testNoticesBody)},
		"dist/licenses/HarmonyOS-Sans.txt":   &fstest.MapFile{Data: []byte(testFontLicenseBody)},
		"dist/licenses/secret.txt":           &fstest.MapFile{Data: []byte("not allowlisted")},
		"dist/unlisted-root-icon.png":        &fstest.MapFile{Data: []byte("not allowlisted")},
		"dist/assets/index-AbCd1234.js":      &fstest.MapFile{Data: []byte(testScriptBody)},
		"dist/assets/index-D0TcvGy-.css":     &fstest.MapFile{Data: []byte("body { color: black; }")},
	}
	bundle, err := LoadEmbeddedFS(source, "dist")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Source() != "embedded:dist" || bundle.AssetCount() != 6 || len(bundle.Digest()) != 64 {
		t.Fatalf("unexpected embedded bundle: source=%q count=%d digest=%q",
			bundle.Source(), bundle.AssetCount(), bundle.Digest())
	}
	assertAllowlistedRootAssetsServed(t, bundle)
	delete(source, "dist/assets/index-AbCd1234.js")
	response := requestBundle(t, bundle, http.MethodGet, "/assets/index-AbCd1234.js", "", "")
	if response.Code != http.StatusOK || response.Body.String() != testScriptBody {
		t.Fatalf("embedded snapshot changed with source: status=%d body=%q",
			response.Code, response.Body.String())
	}
}

func TestEmbeddedBundleRejectsMalformedOrExecutableTrees(t *testing.T) {
	valid := func() fstest.MapFS {
		return fstest.MapFS{
			"dist/index.html":                    &fstest.MapFile{Data: []byte(testIndexBody)},
			"dist/apple-touch-icon.png":          &fstest.MapFile{Data: []byte(testAppleTouchIconBody)},
			"dist/traverse-board-favicon-32.png": &fstest.MapFile{Data: []byte(testFaviconBody)},
			"dist/THIRD-PARTY-NOTICES.txt":       &fstest.MapFile{Data: []byte(testNoticesBody)},
			"dist/licenses/HarmonyOS-Sans.txt":   &fstest.MapFile{Data: []byte(testFontLicenseBody)},
			"dist/assets/index-AbCd1234.js":      &fstest.MapFile{Data: []byte(testScriptBody)},
		}
	}
	tests := []struct {
		name   string
		root   string
		source func() fs.FS
	}{
		{name: "nil source", root: "dist", source: func() fs.FS { return nil }},
		{name: "dot root", root: ".", source: func() fs.FS { return valid() }},
		{name: "traversal root", root: "../dist", source: func() fs.FS { return valid() }},
		{name: "invalid UTF-8", root: "dist", source: func() fs.FS {
			current := valid()
			current["dist/index.html"] = &fstest.MapFile{Data: []byte{0xff}}
			return current
		}},
		{name: "unhashed asset", root: "dist", source: func() fs.FS {
			current := valid()
			delete(current, "dist/assets/index-AbCd1234.js")
			current["dist/assets/index.js"] = &fstest.MapFile{Data: []byte("unsafe")}
			return current
		}},
		{name: "symlink asset", root: "dist", source: func() fs.FS {
			current := valid()
			current["dist/assets/link-AbCd1234.js"] = &fstest.MapFile{Mode: fs.ModeSymlink}
			return current
		}},
		{name: "oversized index", root: "dist", source: func() fs.FS {
			current := valid()
			current["dist/index.html"] = &fstest.MapFile{Data: make([]byte, MaxIndexBytes+1)}
			return current
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle, err := LoadEmbeddedFS(test.source(), test.root)
			if err == nil || bundle != nil {
				t.Fatalf("unsafe embedded bundle loaded: %#v", bundle)
			}
		})
	}
}

func TestBundleAppliesLargerLimitOnlyToTTFAssets(t *testing.T) {
	tests := []struct {
		name       string
		assetName  string
		size       int64
		wantLoaded bool
	}{
		{name: "TTF above generic limit", assetName: "font-AbCd1234.ttf", size: MaxAssetBytes + 1, wantLoaded: true},
		{name: "TTF above dedicated limit", assetName: "font-AbCd1234.ttf", size: MaxTTFAssetBytes + 1},
		{name: "JavaScript above generic limit", assetName: "chunk-AbCd1234.js", size: MaxAssetBytes + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := writeTestBundle(t)
			assetPath := filepath.Join(directory, "assets", test.assetName)
			if err := os.WriteFile(assetPath, []byte{0}, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(assetPath, test.size); err != nil {
				t.Fatal(err)
			}
			bundle, err := LoadDirectory(directory)
			if test.wantLoaded {
				if err != nil || bundle == nil {
					t.Fatalf("bounded TTF was rejected: bundle=%#v err=%v", bundle, err)
				}
				return
			}
			if err == nil || bundle != nil {
				t.Fatalf("oversized asset was accepted: bundle=%#v", bundle)
			}
		})
	}
}

func TestEmbeddedBundleAppliesLargerLimitOnlyToTTFAssets(t *testing.T) {
	tests := []struct {
		name       string
		assetName  string
		size       int
		wantLoaded bool
	}{
		{name: "TTF above generic limit", assetName: "font-AbCd1234.ttf", size: MaxAssetBytes + 1, wantLoaded: true},
		{name: "TTF above dedicated limit", assetName: "font-AbCd1234.ttf", size: MaxTTFAssetBytes + 1},
		{name: "JavaScript above generic limit", assetName: "chunk-AbCd1234.js", size: MaxAssetBytes + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fstest.MapFS{
				"dist/index.html":                    &fstest.MapFile{Data: []byte(testIndexBody)},
				"dist/apple-touch-icon.png":          &fstest.MapFile{Data: []byte(testAppleTouchIconBody)},
				"dist/traverse-board-favicon-32.png": &fstest.MapFile{Data: []byte(testFaviconBody)},
				"dist/THIRD-PARTY-NOTICES.txt":       &fstest.MapFile{Data: []byte(testNoticesBody)},
				"dist/licenses/HarmonyOS-Sans.txt":   &fstest.MapFile{Data: []byte(testFontLicenseBody)},
				"dist/assets/index-AbCd1234.js":      &fstest.MapFile{Data: []byte(testScriptBody)},
				"dist/assets/" + test.assetName:      &fstest.MapFile{Data: make([]byte, test.size)},
			}
			bundle, err := LoadEmbeddedFS(source, "dist")
			if test.wantLoaded {
				if err != nil || bundle == nil {
					t.Fatalf("bounded embedded TTF was rejected: bundle=%#v err=%v", bundle, err)
				}
				return
			}
			if err == nil || bundle != nil {
				t.Fatalf("oversized embedded asset was accepted: bundle=%#v", bundle)
			}
		})
	}
}

const testIndexBody = "<!doctype html><script type=\"module\" src=\"/assets/index-AbCd1234.js\"></script>"
const testScriptBody = "document.body.dataset.ready = 'yes';"
const testFaviconBody = "test Traverse Board favicon"
const testAppleTouchIconBody = "test Traverse Board Apple touch icon"
const testNoticesBody = "test third-party notices"
const testFontLicenseBody = "test HarmonyOS Sans license"

func writeTestBundle(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	writeBundleFile(t, directory, "index.html", []byte(testIndexBody))
	writeTestRootAssets(t, directory)
	writeBundleFile(t, directory, "unlisted-root-icon.png", []byte("not allowlisted"))
	writeBundleFile(t, directory, "assets/index-AbCd1234.js", []byte(testScriptBody))
	writeBundleFile(t, directory, "assets/index-D0TcvGy-.css", []byte("body { color: black; }"))
	return directory
}

func writeTestRootAssets(t *testing.T, directory string) {
	t.Helper()
	writeBundleFile(t, directory, "apple-touch-icon.png", []byte(testAppleTouchIconBody))
	writeBundleFile(t, directory, "traverse-board-favicon-32.png", []byte(testFaviconBody))
	writeBundleFile(t, directory, "THIRD-PARTY-NOTICES.txt", []byte(testNoticesBody))
	writeBundleFile(t, directory, "licenses/HarmonyOS-Sans.txt", []byte(testFontLicenseBody))
	writeBundleFile(t, directory, "licenses/secret.txt", []byte("not allowlisted"))
}

func assertAllowlistedRootAssetsServed(t *testing.T, bundle *Bundle) {
	t.Helper()
	for _, current := range []struct {
		path        string
		body        string
		contentType string
	}{
		{path: "/apple-touch-icon.png", body: testAppleTouchIconBody, contentType: "image/png"},
		{path: "/traverse-board-favicon-32.png", body: testFaviconBody, contentType: "image/png"},
		{path: "/THIRD-PARTY-NOTICES.txt", body: testNoticesBody, contentType: "text/plain; charset=utf-8"},
		{path: "/licenses/HarmonyOS-Sans.txt", body: testFontLicenseBody, contentType: "text/plain; charset=utf-8"},
	} {
		response := requestBundle(t, bundle, http.MethodGet, current.path, "", "")
		if response.Code != http.StatusOK || response.Body.String() != current.body ||
			response.Header().Get("Content-Type") != current.contentType ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected root brand asset response for %s: status=%d headers=%#v body=%q",
				current.path, response.Code, response.Header(), response.Body.String())
		}
		head := requestBundle(t, bundle, http.MethodHead, current.path, "", "")
		if head.Code != http.StatusOK || head.Body.Len() != 0 ||
			head.Header().Get("Content-Length") != response.Header().Get("Content-Length") {
			t.Fatalf("unexpected root brand asset HEAD response for %s: status=%d headers=%#v body=%q",
				current.path, head.Code, head.Header(), head.Body.String())
		}
	}

	unknown := requestBundle(t, bundle, http.MethodGet, "/unlisted-root-icon.png", "text/html", "")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unlisted root asset status=%d want=%d body=%q",
			unknown.Code, http.StatusNotFound, unknown.Body.String())
	}
	unknownLicense := requestBundle(t, bundle, http.MethodGet, "/licenses/secret.txt", "text/html", "")
	if unknownLicense.Code != http.StatusNotFound {
		t.Fatalf("unlisted license status=%d want=%d body=%q",
			unknownLicense.Code, http.StatusNotFound, unknownLicense.Body.String())
	}
}

func writeBundleFile(t *testing.T, directory string, name string, body []byte) {
	t.Helper()
	filePath := filepath.Join(directory, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requestBundle(t *testing.T, bundle *Bundle, method string, requestPath string,
	accept string, etag string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1"+requestPath, nil)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response := httptest.NewRecorder()
	bundle.ServeHTTP(response, request)
	return response
}

func requestBundleWithStyleNonce(t *testing.T, bundle *Bundle, method string, requestPath string,
	accept string, etag string, nonce string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1"+requestPath, nil)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response := httptest.NewRecorder()
	bundle.ServeHTTPWithStyleNonce(response, request, nonce)
	return response
}

func TestAssetNameDigestValidation(t *testing.T) {
	for _, name := range []string{"index-AbCd1234.js", "chunk.12345678.css", "logo-A_B_C_D1.png",
		"index-D0TcvGy-.css"} {
		if !assetNameHasDigest(name, strings.ToLower(filepath.Ext(name))) {
			t.Fatalf("valid hashed asset name rejected: %s", name)
		}
	}
	for _, name := range []string{"index.js", "index-short.js", "index-short-.js", "index-bad!.js"} {
		if assetNameHasDigest(name, strings.ToLower(filepath.Ext(name))) {
			t.Fatalf("invalid hashed asset name accepted: %s", name)
		}
	}
}
