package dependencycache

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
	"golang.org/x/mod/sumdb/dirhash"
)

type artifactResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f artifactResolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestPrepareGoSignedArchiveRedirect(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("example.com/fixture@v1.0.0/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "module example.com/fixture\n\ngo 1.22\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "module.zip")
	if err := os.WriteFile(zipPath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	checksum, err := dirhash.HashZip(zipPath, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"Expires": {"9999999999"}, "GoogleAccessId": {"gcs-urlsigner-prod@golang-modproxy.iam.gserviceaccount.com"}, "Signature": {base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 256))}}
	location := "https://storage.googleapis.com/proxy-golang-org-prod/fixture.zip?" + query.Encode()
	for _, mode := range []string{"approved", "unapproved-storage", "private-dns", "revoked", "checksum-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			revoked := false
			broker, workspace := brokerFixture(t, func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.Host == "proxy.golang.org" {
					revoked = mode == "revoked"
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if request.URL.String() != location || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
					t.Fatal("signed redirect changed or acquired credentials")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(archive.Bytes()))}, nil
			})
			if mode == "private-dns" {
				broker.client.Resolver = artifactResolverFunc(func(_ context.Context, _, host string) ([]netip.Addr, error) {
					if host == "storage.googleapis.com" {
						return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
					}
					return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
				})
			}
			plan := projectprep.Plan{Ready: true, Target: ".", Ecosystem: "go", Packages: []projectprep.Package{{ID: "example.com/fixture@v1.0.0.zip", Kind: projectprep.KindGoZip, ModulePath: "example.com/fixture", Version: "v1.0.0", URL: "https://proxy.golang.org/example.com/fixture/@v/v1.0.0.zip", Integrity: checksum}}}
			if mode == "checksum-mismatch" {
				plan.Packages[0].Integrity = "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32))
			}
			sources := []string{projectprep.GoProxyOrigin, projectprep.GoProxyArchiveOrigin}
			if mode == "unapproved-storage" {
				sources = sources[:1]
			}
			stats, err := broker.Prepare(t.Context(), plan, sources, workspace, nil, func(context.Context) error {
				if revoked {
					return context.Canceled
				}
				return nil
			})
			if mode == "approved" {
				if err != nil || requests != 2 || stats.FetchedPackages != 1 {
					t.Fatalf("approved signed archive: stats=%+v requests=%d err=%v", stats, requests, err)
				}
				return
			}
			wantRequests := 1
			if mode == "checksum-mismatch" {
				wantRequests = 2
			}
			if err == nil || requests != wantRequests || stats.FetchedPackages != 0 {
				t.Fatalf("denied archive: mode=%s stats=%+v requests=%d err=%v", mode, stats, requests, err)
			}
			if mode == "unapproved-storage" && !errors.Is(err, ErrSourceDenied) || mode == "revoked" && !errors.Is(err, context.Canceled) || mode == "checksum-mismatch" && !errors.Is(err, ErrIntegrity) {
				t.Fatalf("unexpected denial: %v", err)
			}
			if mode == "private-dns" && !strings.Contains(err.Error(), "non-public") {
				t.Fatalf("storage DNS policy was not checked: %v", err)
			}
			entries, err := os.ReadDir(broker.root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed archive entered cache: %v %v", entries, err)
			}
		})
	}
}
