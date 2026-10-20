package projectprep_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func npmWorkspaceFiles() map[string]string {
	return map[string]string{
		"repo/package.json":                 `{"name":"root","private":true,"workspaces":["apps/*","packages/*"]}`,
		"repo/apps/web/package.json":        `{"name":"web","version":"1.0.0","scripts":{"test":"node test.cjs"},"dependencies":{"is-number":"7.0.0","shared":"1.0.0"}}`,
		"repo/packages/shared/package.json": `{"name":"shared","version":"1.0.0"}`,
		"repo/package-lock.json":            `{"lockfileVersion":3,"packages":{"":{"workspaces":["apps/*","packages/*"]},"apps/web":{"version":"1.0.0","dependencies":{"is-number":"7.0.0","shared":"1.0.0"}},"packages/shared":{"version":"1.0.0"},"node_modules/web":{"link":true,"resolved":"apps/web"},"node_modules/shared":{"link":true,"resolved":"packages/shared"},"node_modules/is-number":{"version":"7.0.0","resolved":"https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz","integrity":"` + integrity("sha512", 64) + `"}}}`,
	}
}

func TestNPMWorkspaceRootAndSelectedMemberBindAllManifests(t *testing.T) {
	root := fixture(t, npmWorkspaceFiles())
	for _, target := range []string{"repo", "repo/apps/web"} {
		plan, err := projectprep.Detect(t.Context(), root, target)
		if err != nil || !plan.Ready || plan.WorkspaceRoot != "repo" || plan.Target != target || len(plan.Packages) != 1 || len(plan.NPMWorkspaceLinks) != 2 {
			t.Fatalf("%s plan=%+v err=%v", target, plan, err)
		}
		if (target == "repo/apps/web") != (plan.Test.ID == "test.npm") {
			t.Fatalf("selected test: %+v", plan.Test)
		}
		paths := map[string]bool{plan.Manifest.Path: true}
		for _, file := range plan.Lockfiles {
			if paths[file.Path] {
				t.Fatalf("duplicate bound file %s", file.Path)
			}
			paths[file.Path] = true
		}
		for name := range npmWorkspaceFiles() {
			if !paths[name] {
				t.Fatalf("unbound file %s", name)
			}
		}
	}
	before, _ := projectprep.Detect(t.Context(), root, "repo/apps/web")
	if err := os.WriteFile(filepath.Join(root, "repo/packages/shared/package.json"), []byte(`{"name":"shared","version":"1.0.0","scripts":{"postinstall":"changed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, _ := projectprep.Detect(t.Context(), root, "repo/apps/web")
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("sibling manifest change retained consent")
	}
}

func TestNPMWorkspaceRejectsUnreviewedMembersLinksAndLockChanges(t *testing.T) {
	for _, tc := range []struct{ name, file, old, new, want string }{
		{"escaping pattern", "repo/package.json", "apps/*", "../outside", "patterns"},
		{"escaping link", "repo/package-lock.json", `"resolved":"packages/shared"`, `"resolved":"../outside"`, "link"},
		{"unknown link", "repo/package-lock.json", `"resolved":"packages/shared"`, `"resolved":"packages/unknown"`, "link"},
		{"member mismatch", "repo/apps/web/package.json", "7.0.0", "6.0.0", "differs"},
		{"root pattern mismatch", "repo/package-lock.json", "apps/*", "other/*", "workspaces differ"},
		{"duplicate name", "repo/packages/shared/package.json", `"name":"shared"`, `"name":"web"`, "unique"},
		{"unknown member", "repo/package-lock.json", `"packages/shared":{"version":"1.0.0"}`, `"packages/unknown":{"version":"1.0.0"}`, "workspace"},
		{"member lock", "repo/apps/web/package-lock.json", "", "{}", "root lockfile"},
		{"outside file dependency", "repo/apps/web/package.json", `"shared":"1.0.0"`, `"shared":"file:../../../outside"`, "declared npm workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := npmWorkspaceFiles()
			if tc.old == "" {
				files[tc.file] = tc.new
			} else {
				files[tc.file] = strings.Replace(files[tc.file], tc.old, tc.new, 1)
			}
			plan, err := projectprep.Detect(t.Context(), fixture(t, files), "repo/apps/web")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), tc.want) {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
		})
	}
}

func TestNPMWorkspaceSupportsMemberLocalRegistryPackagesAndLegacyPatterns(t *testing.T) {
	files := npmWorkspaceFiles()
	for _, name := range []string{"repo/package.json", "repo/package-lock.json"} {
		files[name] = strings.Replace(files[name], `"workspaces":["apps/*","packages/*"]`, `"workspaces":{"packages":["apps/*","packages/*"]}`, 1)
	}
	files["repo/package-lock.json"] = strings.Replace(files["repo/package-lock.json"], `"node_modules/is-number":`, `"apps/web/node_modules/is-number":`, 1)
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "repo/apps/web")
	if err != nil || !plan.Ready || len(plan.Packages) != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestNPMWorkspaceDiscoveryIsBoundedByRunRootAndCancellation(t *testing.T) {
	files := npmWorkspaceFiles()
	root := fixture(t, files)
	plan, err := projectprep.Detect(t.Context(), filepath.Join(root, "repo/apps/web"), ".")
	if err != nil || plan.Ready || plan.WorkspaceRoot != "" {
		t.Fatalf("crossed Run root: %+v %v", plan, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := projectprep.Detect(ctx, root, "repo"); err != context.Canceled {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "repo/apps/unlisted"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo/apps/unlisted/package.json"), []byte(`{"name":"unlisted","version":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err = projectprep.Detect(t.Context(), root, "repo/apps/web")
	if err != nil || plan.Ready {
		t.Fatalf("unlocked new member accepted %+v %v", plan, err)
	}
}
