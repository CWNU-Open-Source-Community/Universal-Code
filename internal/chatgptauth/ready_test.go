package chatgptauth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// The browser fixture follows only local test URLs; production owns no browser process.
var openBrowser func(context.Context, string) error

func browserReady(t *testing.T, ctx context.Context) func(string) {
	t.Helper()
	opener := openBrowser
	return func(local string) {
		go func() {
			if err := opener(ctx, local); err != nil {
				t.Errorf("local browser fixture: %v", err)
			}
		}()
	}
}

func TestLocalStartURLKeepsOAuthBindingsPrivate(t *testing.T) {
	f := fixture(t)
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := f.client.Login(ctx, s, "", true, func(local string) {
		u, parseErr := url.Parse(local)
		if parseErr != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.RawQuery != "" {
			t.Fatal("invalid private start URL", parseErr)
		}
		q, err := localAuthorization(local)
		if err != nil {
			t.Fatal(err)
		}
		for _, binding := range []string{"state", "nonce", "code_challenge"} {
			if q.Get(binding) == "" || strings.Contains(local, q.Get(binding)) {
				t.Error("OAuth binding leaked through local start URL", binding)
			}
		}
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("login did not cancel", err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("cancellation exchanged a token")
	}
}
