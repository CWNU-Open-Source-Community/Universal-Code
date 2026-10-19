package dependencycache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func pnpmPlan(body []byte) projectprep.Plan {
	plan := npmPlan(body)
	plan.Ecosystem = "pnpm"
	plan.Packages[0].Kind = projectprep.KindPNPMTarball
	plan.PNPMSeedLock = []byte(fmt.Sprintf("lockfileVersion: '9.0'\nimporters:\n  .: {}\npackages:\n  example@1.0.0:\n    resolution: {integrity: %s, tarball: 'file:../pnpm-tarballs/%s'}\nsnapshots:\n  example@1.0.0: {}\n", plan.Packages[0].Integrity, projectprep.PNPMTarballFile(plan.Packages[0].ID)))
	return plan
}

func TestPNPMStagesVerifiedTarballsAndSeedWithCacheReplay(t *testing.T) {
	body := []byte("locked pnpm tarball")
	calls := 0
	b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	plan := pnpmPlan(body)
	seedBefore := bytes.Clone(plan.PNPMSeedLock)
	original := []byte("lockfileVersion: '9.0'\n# original project lock\n")
	if err := os.WriteFile(filepath.Join(workspace, "pnpm-lock.yaml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	authority := func(context.Context) error { return nil }
	stats, err := b.Prepare(t.Context(), plan, []string{"https://registry.npmjs.org"}, workspace, nil, authority)
	if err != nil || calls != 1 || stats.FetchedPackages != 1 || stats.Bytes != int64(len(body)) {
		t.Fatalf("download: %+v calls=%d err=%v", stats, calls, err)
	}
	seed := filepath.Join(workspace, filepath.FromSlash(projectprep.PNPMSeedDirectory(plan.Target)))
	for name, want := range map[string][]byte{"package.json": []byte(pnpmSeedManifest), "pnpm-workspace.yaml": []byte(pnpmSeedWorkspace), "pnpm-lock.yaml": seedBefore} {
		got, err := os.ReadFile(filepath.Join(seed, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("seed %s: %q err=%v", name, got, err)
		}
	}
	relative := filepath.Join(filepath.FromSlash(projectprep.StageDirectory(plan.Target)), "pnpm-tarballs", projectprep.PNPMTarballFile(plan.Packages[0].ID))
	stagedPath := filepath.Join(workspace, relative)
	if got, err := os.ReadFile(stagedPath); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("tarball was not staged: %q err=%v", got, err)
	}
	stats, err = b.Prepare(t.Context(), plan, nil, workspace, nil, authority)
	if err != nil || calls != 1 || stats.CachedPackages != 1 || stats.Bytes != 0 {
		t.Fatalf("cache replay: %+v calls=%d err=%v", stats, calls, err)
	}
	if count, err := b.CachedPackages(t.Context(), plan); err != nil || count != 1 {
		t.Fatalf("cache count=%d err=%v", count, err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "pnpm-lock.yaml")); err != nil || !bytes.Equal(got, original) || !bytes.Equal(plan.PNPMSeedLock, seedBefore) {
		t.Fatalf("project lock or reviewed seed was changed: %q err=%v", got, err)
	}
	pkg := plan.Packages[0]
	key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
	cachePath := filepath.Join(b.root, hex.EncodeToString(key[:])+".blob")
	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	stagedInfo, err := os.Lstat(stagedPath)
	if err != nil || !stagedInfo.Mode().IsRegular() || os.SameFile(cacheInfo, stagedInfo) {
		t.Fatalf("sandbox received linked shared cache content: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if count, err := b.CachedPackages(t.Context(), plan); err != nil || count != 0 {
		t.Fatalf("corrupt cache count=%d err=%v", count, err)
	}
	if _, err := b.Prepare(t.Context(), plan, nil, workspace, nil, authority); !errors.Is(err, ErrIntegrity) || calls != 1 {
		t.Fatalf("corrupt cache was reused without source authority: calls=%d err=%v", calls, err)
	}
}

func TestPNPMDoesNotWriteSeedForDeniedOrIncompleteDownloads(t *testing.T) {
	for _, mode := range []string{"unapproved", "redirect", "bad-integrity", "later-artifact"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("pnpm tarball")
			calls := 0
			b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				if mode == "redirect" {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://unapproved.example/pkg.tgz"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if mode == "later-artifact" && calls == 2 {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("corrupt second tarball"))}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			plan := pnpmPlan(body)
			sources := []string{"https://registry.npmjs.org"}
			if mode == "unapproved" {
				sources = nil
			}
			if mode == "bad-integrity" {
				plan = pnpmPlan([]byte("different tarball"))
			}
			if mode == "later-artifact" {
				second := pnpmPlan([]byte("second tarball")).Packages[0]
				second.ID = "second@1.0.0"
				second.URL = "https://registry.npmjs.org/second/-/second-1.0.0.tgz"
				plan.Packages = append(plan.Packages, second)
			}
			if _, err := b.Prepare(t.Context(), plan, sources, workspace, nil, func(context.Context) error { return nil }); err == nil {
				t.Fatal("denied or incomplete download accepted")
			}
			if mode == "unapproved" && calls != 0 || mode == "redirect" && calls != 1 || mode == "later-artifact" && calls != 2 {
				t.Fatalf("unexpected dispatched requests: %d", calls)
			}
			seed := filepath.Join(workspace, filepath.FromSlash(projectprep.PNPMSeedDirectory(plan.Target)))
			if _, err := os.Lstat(seed); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete preparation wrote a seed: %v", err)
			}
		})
	}
}

func TestPNPMSeedRequiresCurrentAuthorityForEachFile(t *testing.T) {
	for allowedFiles := 0; allowedFiles < 3; allowedFiles++ {
		t.Run(fmt.Sprintf("revoke-after-%d-seed-files", allowedFiles), func(t *testing.T) {
			body := []byte("pnpm tarball")
			b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			plan := pnpmPlan(body)
			seed := filepath.Join(workspace, filepath.FromSlash(projectprep.PNPMSeedDirectory(plan.Target)))
			files := []string{"package.json", "pnpm-workspace.yaml", "pnpm-lock.yaml"}
			downloaded := false
			authority := func(context.Context) error {
				if !downloaded {
					return nil
				}
				written := 0
				for _, name := range files {
					if _, err := os.Stat(filepath.Join(seed, name)); err == nil {
						written++
					}
				}
				if written >= allowedFiles {
					return context.Canceled
				}
				return nil
			}
			_, err := b.Prepare(t.Context(), plan, []string{"https://registry.npmjs.org"}, workspace, func(Progress) { downloaded = true }, authority)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("revoked seed write: %v", err)
			}
			for index, name := range files {
				_, err := os.Lstat(filepath.Join(seed, name))
				if index < allowedFiles && err != nil || index >= allowedFiles && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("seed file %s after revocation: %v", name, err)
				}
			}
		})
	}
}

func TestPNPMSeedValidationAndWorkspaceOnlyPlan(t *testing.T) {
	for _, mode := range []string{"empty", "whitespace", "oversized", "wrong-ecosystem", "mixed-artifacts", "workspace-only"} {
		t.Run(mode, func(t *testing.T) {
			b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
				t.Fatal("seed validation or workspace-only plan dispatched a request")
				return nil, errors.New("unexpected request")
			})
			plan := pnpmPlan([]byte("pnpm tarball"))
			switch mode {
			case "empty":
				plan.PNPMSeedLock = nil
			case "whitespace":
				plan.PNPMSeedLock = []byte(" \t\n")
			case "oversized":
				plan.PNPMSeedLock = bytes.Repeat([]byte("x"), projectprep.MaxFileBytes+1)
			case "wrong-ecosystem":
				plan.Ecosystem = "npm"
			case "mixed-artifacts":
				plan.Packages[0].Kind = projectprep.KindNPMTarball
			case "workspace-only":
				plan.Packages = nil
			}
			_, err := b.Prepare(t.Context(), plan, nil, workspace, nil, func(context.Context) error { return nil })
			if mode == "workspace-only" {
				if err != nil {
					t.Fatal(err)
				}
				seedBytes, err := pnpmSeedBytes(plan)
				if want := int64(len(plan.PNPMSeedLock) + len(pnpmSeedManifest) + len(pnpmSeedWorkspace)); err != nil || seedBytes != want {
					t.Fatalf("seed not reserved in total byte limit: got=%d want=%d err=%v", seedBytes, want, err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid seed accepted")
			}
			if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
				t.Fatalf("invalid seed mutated workspace: %v err=%v", entries, err)
			}
		})
	}
}

func TestPNPMRejectsLinkedSeedAndTarballDirectories(t *testing.T) {
	for _, subtree := range []string{"pnpm-seed", "pnpm-tarballs"} {
		t.Run(subtree, func(t *testing.T) {
			body := []byte("pnpm tarball")
			b, workspace := brokerFixture(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			plan := pnpmPlan(body)
			outside := t.TempDir()
			outsideName := "package.json"
			if subtree == "pnpm-tarballs" {
				outsideName = projectprep.PNPMTarballFile(plan.Packages[0].ID)
			}
			outsidePath := filepath.Join(outside, outsideName)
			if err := os.WriteFile(outsidePath, []byte("outside original"), 0600); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(workspace, filepath.FromSlash(projectprep.StageDirectory(plan.Target)))
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			linked := filepath.Join(stage, subtree)
			if runtime.GOOS == "windows" {
				command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/d", "/c", "mklink", "/J", linked, outside)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("create directory junction: %v %s", err, output)
				}
			} else if err := os.Symlink(outside, linked); err != nil {
				t.Skipf("directory symlink unavailable: %v", err)
			}
			if _, err := b.Prepare(t.Context(), plan, []string{"https://registry.npmjs.org"}, workspace, nil, func(context.Context) error { return nil }); err == nil {
				t.Fatal("linked pnpm staging directory accepted")
			}
			if got, err := os.ReadFile(outsidePath); err != nil || string(got) != "outside original" {
				t.Fatalf("wrote outside workspace: %q err=%v", got, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
				t.Fatalf("outside directory acquired staging files: %v err=%v", entries, err)
			}
		})
	}
}
