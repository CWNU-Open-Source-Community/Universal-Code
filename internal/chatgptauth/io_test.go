// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package chatgptauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLogoutRevocationRetriesAndCancellation(t *testing.T) {
	for _, scenario := range []string{"discovery", "revoke", "permanent", "cancel", "signed-out"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture(t)
			s := testStore(t)
			a := testAuth()
			if scenario == "signed-out" {
				a.clear()
			}
			saveTest(t, s, a)
			baseTransport := f.client.http.Transport
			attempts := 0
			ctx := context.Background()
			var cancel context.CancelFunc
			if scenario == "cancel" {
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			f.client.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
				if scenario == "signed-out" {
					t.Error("signed-out revocation called")
				}
				if (scenario == "discovery" && strings.Contains(r.URL.Path, "openid")) || (scenario != "discovery" && r.URL.Path == "/revoke") {
					attempts++
					if scenario == "permanent" {
						return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_client"}`)), Header: make(http.Header)}, nil
					}
					if attempts < 2 || scenario == "cancel" {
						return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":"server_error"}`)), Header: make(http.Header)}, nil
					}
				}
				return baseTransport.RoundTrip(r)
			})
			err := f.client.Logout(ctx, s, "")
			if scenario == "cancel" || scenario == "permanent" {
				if err == nil {
					t.Fatal("unconfirmed revocation ignored")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			stored, _ := s.Selected("")
			if stored.AccessToken != "" || stored.IDToken != "" {
				t.Fatal("tokens retained")
			}
			if scenario == "discovery" || scenario == "revoke" {
				if attempts != 2 {
					t.Fatal(attempts)
				}
			}
		})
	}
}
func TestOAuthNoRedirects(t *testing.T) {
	c := NewClient()
	r := &http.Request{}
	if err := c.http.CheckRedirect(r, nil); err != http.ErrUseLastResponse {
		t.Fatal(err)
	}
}
