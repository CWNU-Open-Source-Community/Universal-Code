package webevidence

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestArtifactSignedRedirectScope(t *testing.T) {
	query := url.Values{"Expires": {"9999999999"}, "GoogleAccessId": {"gcs-urlsigner-prod@golang-modproxy.iam.gserviceaccount.com"}, "Signature": {base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 256))}}
	location := "https://storage.googleapis.com/proxy-golang-org-prod/fixture%3Av1.0.0.zip?Signature=" + url.QueryEscape(query.Get("Signature")) + "&Expires=" + query.Get("Expires") + "&GoogleAccessId=" + url.QueryEscape(query.Get("GoogleAccessId"))
	source := "https://proxy.golang.org/example.com/fixture/@v/v1.0.0.zip"
	for _, tc := range []struct {
		name, source, location string
		ordinary, wantOK       bool
	}{
		{"artifact", source, location, false, true},
		{"ordinary web", source, location, true, false},
		{"other source", "https://registry.npmjs.org/fixture.zip", location, false, false},
		{"module manifest", strings.TrimSuffix(source, ".zip") + ".mod", location, false, false},
		{"unrelated proxy route", "https://proxy.golang.org/fixture.zip", location, false, false},
		{"wrong bucket", source, strings.Replace(location, "proxy-golang-org-prod", "other-bucket", 1), false, false},
		{"wrong host", source, strings.Replace(location, "storage.googleapis.com", "other.example.com", 1), false, false},
		{"extra secret", source, location + "&token=private", false, false},
		{"duplicate signature", source, location + "&Signature=duplicate", false, false},
		{"malformed signature", source, strings.Replace(location, url.QueryEscape(query.Get("Signature")), "invalid", 1), false, false},
		{"non-HTTPS storage", source, strings.Replace(location, "https://", "http://", 1), false, false},
		{"fragment", source, location + "#fragment", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, authorizations, resolutions := 0, 0, 0
			client := &SafeHTTPClient{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				resolutions++
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			}), TransportFactory: func(string, []netip.Addr) http.RoundTripper {
				return roundTripFunc(func(request *http.Request) (*http.Response, error) {
					requests++
					if requests == 1 {
						return webResponse(http.StatusFound, http.Header{"Location": {tc.location}}, ""), nil
					}
					if request.URL.String() != tc.location {
						t.Fatal("signed path or query changed")
					}
					return webResponse(http.StatusOK, http.Header{}, "archive"), nil
				})
			}}
			authorize := func(string) error { authorizations++; return nil }
			var document HTTPDocument
			var err error
			if tc.ordinary {
				document, err = client.GetAuthorized(t.Context(), tc.source, 32, "text/plain", authorize)
			} else {
				document, err = client.GetArtifactAuthorized(t.Context(), tc.source, 32, authorize)
			}
			if tc.wantOK {
				if err != nil || requests != 2 || resolutions != 2 || authorizations != 2 || string(document.Body) != "archive" || document.FinalURL != tc.location {
					t.Fatalf("document=%+v requests=%d DNS=%d authority=%d err=%v", document, requests, resolutions, authorizations, err)
				}
			} else if err == nil || requests != 1 {
				t.Fatalf("denied redirect: requests=%d err=%v", requests, err)
			}
		})
	}
	t.Run("signed URL is not an initial request", func(t *testing.T) {
		requests := 0
		client := &SafeHTTPClient{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			requests++
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		})}
		if _, err := client.GetArtifactAuthorized(t.Context(), location, 32, func(string) error { return nil }); err == nil || requests != 0 {
			t.Fatalf("initial signed URL accepted: DNS=%d err=%v", requests, err)
		}
	})
}

func TestArtifactSignedRedirectRequestErrorOmitsURL(t *testing.T) {
	transportFailure := errors.New("transport failed")
	location := "https://storage.googleapis.com/proxy-golang-org-prod/fixture.zip?Expires=9999999999&GoogleAccessId=gcs-urlsigner-prod%40golang-modproxy.iam.gserviceaccount.com&Signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 256)))
	requests := 0
	client := &SafeHTTPClient{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}), TransportFactory: func(string, []netip.Addr) http.RoundTripper {
		return roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return webResponse(http.StatusFound, http.Header{"Location": {location}}, ""), nil
			}
			return nil, transportFailure
		})
	}}
	_, err := client.GetArtifactAuthorized(t.Context(), "https://proxy.golang.org/example.com/fixture/@v/v1.0.0.zip", 32, func(string) error { return nil })
	if !errors.Is(err, transportFailure) || requests != 2 || strings.Contains(err.Error(), "Signature") || strings.Contains(err.Error(), "Expires") || strings.Contains(err.Error(), "GoogleAccessId") {
		t.Fatalf("signed URL error: requests=%d err=%v", requests, err)
	}
}
