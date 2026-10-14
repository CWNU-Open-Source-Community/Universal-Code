package dependencycache

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/webevidence"
	"golang.org/x/mod/sumdb/dirhash"
)

type publicResolver struct{ address string }

func (r publicResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr(r.address)}, nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type interruptedBody struct {
	prefix []byte
	closed bool
}

func (b *interruptedBody) Read(buffer []byte) (int, error) {
	n := copy(buffer, b.prefix)
	b.prefix = b.prefix[n:]
	if len(b.prefix) == 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}

func (b *interruptedBody) Close() error { b.closed = true; return nil }

func npmPlan(body []byte) projectprep.Plan {
	sum := sha512.Sum512(body)
	return projectprep.Plan{Ready: true, Target: ".", Ecosystem: "npm", Packages: []projectprep.Package{{ID: "example@1.0.0", Kind: projectprep.KindNPMTarball, URL: "https://registry.npmjs.org/example/-/example-1.0.0.tgz", Integrity: "sha512-" + base64.StdEncoding.EncodeToString(sum[:])}}}
}

func brokerFixture(t *testing.T, handler roundTrip) (*Broker, string) {
	t.Helper()
	b, err := New(filepath.Join(t.TempDir(), "private-cache"))
	if err != nil {
		t.Fatal(err)
	}
	b.client = &webevidence.SafeHTTPClient{Resolver: publicResolver{"8.8.8.8"}, TransportFactory: func(string, []netip.Addr) http.RoundTripper { return handler }}
	return b, t.TempDir()
}

func TestDownloadsVerifyAndReplayCacheOnly(t *testing.T) {
	body := []byte("locked tarball content")
	calls := 0
	b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	plan := npmPlan(body)
	authority := func(context.Context) error { return nil }
	var progress Progress
	stats, err := b.Prepare(t.Context(), plan, []string{"https://registry.npmjs.org"}, workspace, func(p Progress) { progress = p }, authority)
	if err != nil || calls != 1 || stats.FetchedPackages != 1 || progress.Bytes != int64(len(body)) {
		t.Fatalf("download: %+v %+v calls=%d err=%v", stats, progress, calls, err)
	}
	relative, err := artifactStagePath(plan.Target, plan.Packages[0])
	if err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(relative)))
	if err != nil || !bytes.Equal(body, staged) {
		t.Fatal("verified dependency was not staged", err)
	}
	stats, err = b.Prepare(t.Context(), plan, nil, workspace, nil, authority)
	if err != nil || calls != 1 || stats.CachedPackages != 1 || stats.Bytes != 0 {
		t.Fatalf("cache-only: %+v calls=%d err=%v", stats, calls, err)
	}
	if count, err := b.CachedPackages(t.Context(), plan); err != nil || count != 1 {
		t.Fatalf("cache count=%d %v", count, err)
	}
	pkg := plan.Packages[0]
	key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
	if err := os.WriteFile(filepath.Join(b.root, hex.EncodeToString(key[:])+".blob"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if count, err := b.CachedPackages(t.Context(), plan); err != nil || count != 0 {
		t.Fatalf("corrupt bytes counted as ready: count=%d %v", count, err)
	}
	if _, err = b.Prepare(t.Context(), plan, nil, workspace, nil, authority); !errors.Is(err, ErrIntegrity) || calls != 1 {
		t.Fatalf("corrupt cache-only attempt: calls=%d %v", calls, err)
	}
	stats, err = b.Prepare(t.Context(), plan, []string{"https://registry.npmjs.org"}, workspace, nil, authority)
	if err != nil || calls != 2 || stats.FetchedPackages != 1 || stats.CachedPackages != 0 || stats.Bytes != int64(len(body)) {
		t.Fatalf("authorized repair: %+v calls=%d %v", stats, calls, err)
	}
	if count, err := b.CachedPackages(t.Context(), plan); err != nil || count != 1 {
		t.Fatalf("repaired cache unavailable: count=%d %v", count, err)
	}
}

func TestSourceRedirectPrivateDNSAndAuthorityDenials(t *testing.T) {
	for _, mode := range []string{"unapproved", "redirect", "private-dns", "revoked", "bad-integrity"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("tarball")
			calls := 0
			b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				if mode == "redirect" {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://unapproved.example/pkg.tgz"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			plan := npmPlan(body)
			sources := []string{"https://registry.npmjs.org"}
			if mode == "unapproved" {
				sources = nil
			}
			if mode == "private-dns" {
				b.client.Resolver = publicResolver{"127.0.0.1"}
			}
			if mode == "bad-integrity" {
				plan = npmPlan([]byte("different"))
			}
			authority := func(context.Context) error {
				if mode == "revoked" {
					return errors.New("authorization revoked")
				}
				return nil
			}
			if _, err := b.Prepare(t.Context(), plan, sources, workspace, nil, authority); err == nil {
				t.Fatal("unsafe dependency preparation accepted")
			}
			if (mode == "unapproved" || mode == "private-dns" || mode == "revoked") && calls != 0 {
				t.Fatalf("denied request was dispatched: %d", calls)
			}
			if mode == "redirect" && calls != 1 {
				t.Fatalf("redirect escaped exact origin authority: %d", calls)
			}
			entries, err := os.ReadDir(b.root)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed download committed cache data", err)
			}
		})
	}
}

func TestArtifactInterruptedResponseRetriesOnceAndReauthorizes(t *testing.T) {
	for _, forever := range []bool{false, true} {
		name := "interrupted-then-success"
		if forever {
			name = "continuous-eof"
		}
		t.Run(name, func(t *testing.T) {
			body := []byte("verified tarball")
			calls := 0
			checkedAfterInterruption := false
			interrupted := &interruptedBody{prefix: []byte("unverified partial content")}
			b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				if forever {
					return nil, io.EOF
				}
				if calls == 1 {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: interrupted}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			stats, err := b.Prepare(t.Context(), npmPlan(body), []string{"https://registry.npmjs.org"}, workspace, nil, func(context.Context) error {
				if calls == 1 {
					checkedAfterInterruption = true
				}
				return nil
			})
			if calls != 2 {
				t.Fatalf("artifact made %d attempts, want 2", calls)
			}
			if forever {
				if !errors.Is(err, io.EOF) || stats.FetchedPackages != 0 {
					t.Fatalf("continuous EOF manufactured receipt: %+v %v", stats, err)
				}
				return
			}
			if err != nil || stats.FetchedPackages != 1 || stats.Bytes != int64(len(body)) || !checkedAfterInterruption || !interrupted.closed {
				t.Fatalf("bounded retry: %+v reauthorized=%t partialClosed=%t err=%v", stats, checkedAfterInterruption, interrupted.closed, err)
			}
		})
	}
}

func TestFinalizeNPMEmptyProjectAndInvalidModulePaths(t *testing.T) {
	t.Run("empty project has no bins", func(t *testing.T) {
		root := t.TempDir()
		for _, guest := range []bool{false, true} {
			if err := FinalizeNPM(t.Context(), root, ".", guest); err != nil {
				t.Fatalf("empty installation: %v", err)
			}
		}
		if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
			t.Fatalf("finalization fabricated files: %v %v", entries, err)
		}
	})
	t.Run("missing project remains invalid", func(t *testing.T) {
		if err := FinalizeNPM(t.Context(), t.TempDir(), "missing", true); err == nil {
			t.Fatal("missing project accepted as an empty installation")
		}
	})
	t.Run("modules file remains invalid", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "node_modules"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := FinalizeNPM(t.Context(), root, ".", true); err == nil {
			t.Fatal("ordinary file accepted as installed node_modules")
		}
	})
}

func TestGoModuleHashesAndGeneratedOfflineMetadata(t *testing.T) {
	modulePath, version := "example.com/fixture", "v1.0.0"
	mod := []byte("module example.com/fixture\n\ngo 1.22\n")
	modHash, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(mod)), nil })
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, body := range map[string][]byte{"go.mod": mod, "fixture.go": []byte("package fixture\n")} {
		f, err := writer.Create(modulePath + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "module.zip")
	if err := os.WriteFile(zipPath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	zipHash, err := dirhash.HashZip(zipPath, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	responses := map[string][]byte{".mod": mod, ".zip": archive.Bytes()}
	b, workspace := brokerFixture(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(responses[filepath.Ext(request.URL.Path)]))}, nil
	})
	plan := projectprep.Plan{Ready: true, Target: ".", Ecosystem: "go", Packages: []projectprep.Package{{ID: "fixture.mod", Kind: projectprep.KindGoMod, ModulePath: modulePath, Version: version, URL: "https://proxy.golang.org/example.com/fixture/@v/v1.0.0.mod", Integrity: modHash}, {ID: "fixture.zip", Kind: projectprep.KindGoZip, ModulePath: modulePath, Version: version, URL: "https://proxy.golang.org/example.com/fixture/@v/v1.0.0.zip", Integrity: zipHash}}}
	if _, err := b.Prepare(t.Context(), plan, []string{"https://proxy.golang.org"}, workspace, nil, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(workspace, filepath.FromSlash(projectprep.GoModuleCacheDirectory(".")), "cache", "download", "example.com", "fixture", "@v", "v1.0.0")
	if body, err := os.ReadFile(base + ".ziphash"); err != nil || string(body) != zipHash {
		t.Fatal("Go checksum cache not staged", err)
	}
	if body, err := os.ReadFile(base + ".info"); err != nil || !bytes.Contains(body, []byte(`"Version":"v1.0.0"`)) {
		t.Fatal("Go version metadata not generated", err)
	}
	plan.Packages[0].Integrity = "h1:invalid"
	if err := verifyArtifact(plan.Packages[0], mod); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Go manifest mismatch accepted: %v", err)
	}
}

func TestFinalizeNPMCreatesRegularBinsAndRejectsEscape(t *testing.T) {
	for _, escaped := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "escape"}[escaped], func(t *testing.T) {
			root := t.TempDir()
			packageDir := filepath.Join(root, "node_modules", "fixture")
			if err := os.MkdirAll(packageDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(packageDir, "cli.js"), []byte("#!/usr/bin/env node\n"), 0600); err != nil {
				t.Fatal(err)
			}
			bin := "cli.js"
			if escaped {
				bin = "../../outside.js"
			}
			if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{"name":"fixture","bin":{"fixture":"`+bin+`"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			err := FinalizeNPM(t.Context(), root, ".", true)
			if escaped {
				if err == nil {
					t.Fatal("escaping command metadata accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(filepath.Join(root, "node_modules", ".bin", "fixture"))
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				t.Fatal("generated command is not a regular file", err)
			}
			if err := FinalizeNPM(t.Context(), root, ".", false); err != nil {
				t.Fatal(err)
			}
			if body, err := os.ReadFile(filepath.Join(root, "node_modules", ".bin", "fixture.cmd")); err != nil || !bytes.Contains(body, []byte("node")) {
				t.Fatal("Windows command wrapper missing", err)
			}
		})
	}
}
