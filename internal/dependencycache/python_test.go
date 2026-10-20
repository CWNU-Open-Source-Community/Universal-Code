package dependencycache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func TestPythonWheelBrokerSourceIntegrityAndPrivateStage(t *testing.T) {
	body := []byte("locked wheel fixture")
	sum := sha256.Sum256(body)
	calls := 0
	broker, root := brokerFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	lock := "lock-version='1.0'\ncreated-by='fixture'\n[[packages]]\nname='six'\nversion='1.17.0'\n[[packages.wheels]]\nurl='https://files.pythonhosted.org/six-1.17.0-py2.py3-none-any.whl'\nhashes={sha256='" + hex.EncodeToString(sum[:]) + "'}\n"
	if err := os.WriteFile(filepath.Join(root, "pylock.toml"), []byte(lock), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := projectprep.Detect(t.Context(), root, ".")
	if err != nil || !plan.Ready {
		t.Fatalf("plan %+v %v", plan, err)
	}
	current := func(context.Context) error { return nil }
	if _, err := broker.Prepare(t.Context(), plan, nil, root, nil, current); !errors.Is(err, ErrSourceDenied) || calls != 0 {
		t.Fatalf("unreviewed Python source used: calls=%d err=%v", calls, err)
	}
	stats, err := broker.Prepare(t.Context(), plan, plan.Sources, root, nil, current)
	if err != nil || calls != 1 || stats.FetchedPackages != 1 {
		t.Fatalf("reviewed Python download: stats=%+v calls=%d err=%v", stats, calls, err)
	}
	seed, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(projectprep.PythonPlanFile("."))))
	if err != nil || !bytes.Equal(seed, plan.PythonSeed) {
		t.Fatal("private seed differs from the reviewed plan", err)
	}
	stats, err = broker.Prepare(t.Context(), plan, nil, root, nil, current)
	if err != nil || calls != 1 || stats.CachedPackages != 1 || stats.FetchedPackages != 0 {
		t.Fatalf("offline Python reuse: stats=%+v calls=%d err=%v", stats, calls, err)
	}
	pkg := plan.Packages[0]
	if err := verifyArtifact(pkg, []byte("changed wheel")); !errors.Is(err, ErrIntegrity) {
		t.Fatal("changed wheel accepted", err)
	}
	sha512sum := sha512.Sum512(body)
	pkg.Integrity = "sha512-" + base64.StdEncoding.EncodeToString(sha512sum[:])
	if err := verifyArtifact(pkg, body); !errors.Is(err, ErrIntegrity) {
		t.Fatal("Python wheel did not require SHA-256", err)
	}
}
