package dependencycache

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func TestYarnMirrorUsesVerifiedCacheAndControlledConfiguration(t *testing.T) {
	body := []byte("locked Yarn archive")
	calls := 0
	b, root := brokerFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	plan := npmPlan(body)
	plan.Ecosystem, plan.Packages[0].Kind = "yarn", projectprep.KindYarnTarball
	plan.Packages[0].URL = "https://registry.npmjs.org/@example/lib/-/lib-1.0.0.tgz"
	authority := func(context.Context) error { return nil }
	if _, err := b.Prepare(t.Context(), plan, nil, root, nil, authority); err == nil || calls != 0 {
		t.Fatal("unapproved source dispatched", err)
	}
	stats, err := b.Prepare(t.Context(), plan, []string{"https://registry.npmjs.org"}, root, nil, authority)
	if err != nil || stats.FetchedPackages != 1 {
		t.Fatal(stats, err)
	}
	mirror, err := os.ReadFile(filepath.Join(root, projectprep.YarnMirrorDirectory(plan.Target), "@example-lib-1.0.0.tgz"))
	if err != nil || !bytes.Equal(mirror, body) {
		t.Fatal("archive was not staged intact", err)
	}
	rc, err := os.ReadFile(filepath.Join(root, projectprep.YarnRCPath(plan.Target)))
	if err != nil || string(rc) != yarnPreparationRC {
		t.Fatal("controlled configuration missing", err)
	}
	replayRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stats, err = b.Prepare(t.Context(), plan, nil, replayRoot, nil, authority)
	if err != nil || calls != 1 || stats.CachedPackages != 1 || stats.Bytes != 0 {
		t.Fatal("offline cache replay failed", stats, err)
	}
}
