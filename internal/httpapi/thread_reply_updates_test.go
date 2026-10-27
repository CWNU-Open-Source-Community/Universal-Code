package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func TestThreadReplyUpdatesHTTPBaselinePublicIDsAndReadAuthorization(t *testing.T) {
	f := newAPIFixture(t)
	if _, err := f.store.SaveSessionMessage(t.Context(), session.NewMessage(f.run.SessionID, "assistant", "old reply")); err != nil {
		t.Fatal(err)
	}
	var baseline ThreadReplyUpdatesView
	decodeData(t, f.get(t, ThreadReplyUpdatesPath), &baseline)
	if baseline.Version != ThreadReplyUpdatesVersion || baseline.DataStoreID != f.store.DataStoreID() ||
		baseline.Cursor == "" || baseline.Items == nil || len(baseline.Items) != 0 || baseline.HasMore {
		t.Fatalf("baseline contract: %#v", baseline)
	}
	for _, message := range []session.Message{
		session.NewMessage(f.run.SessionID, "system", "HiddenReplyUpdateCanary"),
		session.NewMessage(f.run.SessionID, "assistant", "PUBLIC_BODY_NOT_IN_FEED"),
	} {
		if _, err := f.store.SaveSessionMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	path := ThreadReplyUpdatesPath + "?limit=1&cursor=" + url.QueryEscape(baseline.Cursor)
	var first ThreadReplyUpdatesView
	decodeData(t, f.get(t, path), &first)
	if len(first.Items) != 0 || !first.HasMore || first.Cursor == baseline.Cursor {
		t.Fatalf("empty source page: %#v", first)
	}
	var reply ThreadReplyUpdatesView
	response := f.get(t, ThreadReplyUpdatesPath+"?limit=1&cursor="+url.QueryEscape(first.Cursor))
	decodeData(t, response, &reply)
	if len(reply.Items) != 1 || reply.Items[0].ThreadID != domain.InitialThreadID(f.run.ID) ||
		reply.Items[0].MessageID == "" || reply.Items[0].RunOrdinal != 1 || reply.Items[0].Sequence <= 0 ||
		reply.Items[0].ThreadStatus != "active" || reply.HasMore {
		t.Fatalf("reply identity: %#v", reply)
	}
	for _, forbidden := range []string{"HiddenReplyUpdateCanary", "PUBLIC_BODY_NOT_IN_FEED", "payload_json", "content_sha256", "subject_id"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("feed exposed %s", forbidden)
		}
	}
	var replay ThreadReplyUpdatesView
	decodeData(t, f.get(t, ThreadReplyUpdatesPath+"?limit=1&cursor="+url.QueryEscape(first.Cursor)), &replay)
	if !reflect.DeepEqual(reply, replay) {
		t.Fatal("replay changed message identities")
	}
	for _, token := range []string{"", testControlToken, "wrong-token"} {
		assertAPIError(t, f.request(t, http.MethodGet, path, token, "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusUnauthorized, "POLICY_DENIED")
	}
	assertAPIError(t, f.request(t, http.MethodPost, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusMethodNotAllowed, "INVALID_ARGUMENT")
	assertAPIError(t, f.request(t, http.MethodGet, path, testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", strings.NewReader("body")), http.StatusBadRequest, "INVALID_ARGUMENT")
}

type unavailableReplyUpdateStore struct {
	Store
	identity string
}

func (s unavailableReplyUpdateStore) DataStoreID() string { return s.identity }
func (s unavailableReplyUpdateStore) ListThreadReplyUpdates(context.Context, string, int) ([]domain.ThreadSearchMatch, string, bool, error) {
	return []domain.ThreadSearchMatch{{MessageID: "partial-reply"}}, "partial-cursor", true, errors.New("reply reader failed")
}

func TestThreadReplyUpdatesHTTPValidationStoreIsolationAndFailure(t *testing.T) {
	f := newAPIFixture(t)
	var baseline ThreadReplyUpdatesView
	decodeData(t, f.get(t, ThreadReplyUpdatesPath), &baseline)
	for _, query := range []string{"?cursor=", "?cursor=invalid", "?cursor=a&cursor=b", "?limit=0", "?limit=101", "?limit=1&limit=2", "?unknown=yes"} {
		assertAPIError(t, f.get(t, ThreadReplyUpdatesPath+query), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	other := newAPIFixture(t)
	assertAPIError(t, other.get(t, ThreadReplyUpdatesPath+"?cursor="+url.QueryEscape(baseline.Cursor)), http.StatusBadRequest, "INVALID_ARGUMENT")
	original := f.api.store
	f.api.store = legacyHealthStore{Store: original}
	assertAPIError(t, f.get(t, ThreadReplyUpdatesPath), http.StatusServiceUnavailable, "UNAVAILABLE")
	f.api.store = unavailableReplyUpdateStore{Store: original, identity: f.store.DataStoreID()}
	response := f.get(t, ThreadReplyUpdatesPath+"?cursor="+url.QueryEscape(baseline.Cursor))
	if response.Code < 500 || strings.Contains(response.Body.String(), "partial-cursor") || strings.Contains(response.Body.String(), "partial-reply") {
		t.Fatalf("failed reader returned partial state: %d %s", response.Code, response.Body.String())
	}
}
