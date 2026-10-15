package chatgptauth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestCredentialsRefreshPreservesRegistrationRevision(t *testing.T) {
	for _, withID := range []bool{true, false} {
		t.Run(map[bool]string{true: "verified_id_token", false: "no_id_token"}[withID], func(t *testing.T) {
			f := fixture(t)
			s := testStore(t)
			a := testAuth()
			a.Revision = 17
			a.ExpiresAt = time.Now().Add(-time.Minute)
			a.Models = []Model{{Slug: "cached-model", DisplayName: "Cached", Visibility: "list"}}
			saveTest(t, s, a)
			f.token = func(r *http.Request) any {
				if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != a.RefreshToken {
					t.Error("credentials did not execute the saved refresh grant")
				}
				tokens := f.tokens()
				if !withID {
					delete(tokens, "id_token")
					delete(tokens, "scope")
				}
				return tokens
			}
			rotated, err := f.client.Credentials(context.Background(), s, a.ClientID)
			if err != nil || rotated == nil || rotated.AccessToken != "access-new" ||
				rotated.RefreshToken != "refresh-new" || rotated.Revision != a.Revision ||
				rotated.PendingRefresh != nil || len(rotated.Models) != 1 {
				t.Fatalf("normal token renewal replaced the registration: %#v, %v", rotated, err)
			}
			stored, err := s.Selected(a.ClientID)
			if err != nil || stored.Revision != a.Revision || stored.AccessToken != rotated.AccessToken {
				t.Fatal("returned refresh revision was not retained in storage", err)
			}
			// Catalog work and a model runtime captured before renewal still
			// belong to this verified session, even though the bearer rotated.
			if err := s.SaveModels(context.Background(), &a,
				[]Model{{Slug: "discovered-model", DisplayName: "Discovered", Visibility: "list"}}); err != nil {
				t.Fatalf("refresh invalidated an existing session consumer: %v", err)
			}
			next, err := f.client.Credentials(context.Background(), s, a.ClientID)
			if err != nil || next == nil || next.Revision != a.Revision || f.calls.Load() != 1 {
				t.Fatalf("renewed session was not reusable: calls=%d, err=%v", f.calls.Load(), err)
			}
		})
	}
}
