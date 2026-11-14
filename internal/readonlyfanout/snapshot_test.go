package readonlyfanout

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/rootfs"
)

func TestBuildPlanCreatesDeterministicSixWayReadOnlySnapshot(t *testing.T) {
	root := t.TempDir()
	for index, name := range []string{
		"api/a.go", "api/b.go", "cmd/main.go", "docs/design.md",
		"internal/a.go", "internal/b.go", "web/app.ts", "web/view.tsx",
	} {
		writeSnapshotTestFile(t, root, name, strings.Repeat(name+"\n", index+1))
	}
	writeSnapshotTestFile(t, root, ".env", "TOKEN=must-not-enter-manifest")
	writeSnapshotTestFile(t, root, "node_modules/pkg/index.js", "ignored dependency")
	writeSnapshotTestFile(t, root, "image.bin", "\x00binary")
	now := time.Now().UTC()
	request := BuildPlanRequest{
		PlanID: "fanout-plan-1", RunID: "run-1", WorkspaceID: "ws-1",
		WorkspaceRoot: root, ScopePath: ".", Goal: "audit source modules",
		Tier: domain.ReadOnlyFanoutSix, RequestedBy: "operator", CreatedAt: now,
	}
	first, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.EffectiveParallelism != 6 || first.ShardCount != 6 || first.FileCount != 8 ||
		first.ExcludedCount != 3 || first.SnapshotDigest != second.SnapshotDigest ||
		!slices.Equal(first.Files, second.Files) || !slices.Equal(first.Shards, second.Shards) {
		t.Fatalf("unexpected deterministic snapshot: first=%#v second=%#v", first, second)
	}
	for _, file := range first.Files {
		if strings.Contains(file.RelativePath, ".env") ||
			strings.Contains(file.RelativePath, "node_modules") || file.ShardOrdinal > 6 {
			t.Fatalf("unsafe or invalid file entered snapshot: %#v", file)
		}
	}
}

func TestBuildPlanRejectsScopeEscapeAndDetectsSnapshotDrift(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTestFile(t, root, "src/main.go", "package main\n")
	request := BuildPlanRequest{
		PlanID: "fanout-plan-2", RunID: "run-2", WorkspaceID: "ws-2",
		WorkspaceRoot: root, ScopePath: ".", Goal: "review source",
		Tier: domain.ReadOnlyFanoutAuto, RequestedBy: "operator",
	}
	first, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, root, "src/main.go", "package main\n// changed\n")
	second, err := BuildPlan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotDigest == second.SnapshotDigest {
		t.Fatal("workspace content change did not change snapshot digest")
	}
	request.ScopePath = "../outside"
	if _, err := BuildPlan(context.Background(), request); err == nil {
		t.Fatal("workspace scope escape was accepted")
	}
	if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "linked-src")); err == nil {
		request.ScopePath = "linked-src"
		if _, err := BuildPlan(context.Background(), request); err == nil {
			t.Fatal("symlink scope was accepted")
		}
	}
}

func TestLoadVerifiedSnapshotReturnsRedactedManifestBytes(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTestFile(t, root, "src/main.go",
		"package main\n// API_KEY=super-secret-value\n")
	plan, err := BuildPlan(context.Background(), BuildPlanRequest{
		PlanID: "fanout-plan-verified", RunID: "run-verified",
		WorkspaceID: "ws-verified", WorkspaceRoot: root, ScopePath: ".",
		Goal: "review source", Tier: domain.ReadOnlyFanoutOne,
		RequestedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := LoadVerifiedSnapshot(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SnapshotDigest != plan.SnapshotDigest || len(snapshot.Shards) != 1 ||
		len(snapshot.Shards[0].Files) != 1 ||
		strings.Contains(snapshot.Shards[0].Files[0].Content, "super-secret-value") ||
		snapshot.Shards[0].Files[0].Redactions != 1 {
		t.Fatalf("unexpected verified snapshot: %#v", snapshot)
	}
}

func TestLoadVerifiedSnapshotRejectsAnyManifestDrift(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTestFile(t, root, "src/main.go", "package main\n")
	plan, err := BuildPlan(context.Background(), BuildPlanRequest{
		PlanID: "fanout-plan-drift", RunID: "run-drift", WorkspaceID: "ws-drift",
		WorkspaceRoot: root, ScopePath: ".", Goal: "review source",
		Tier: domain.ReadOnlyFanoutOne, RequestedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, root, "src/new.go", "package src\n")
	if _, err := LoadVerifiedSnapshot(context.Background(), root, plan); err == nil ||
		!strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("new file drift was accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "src", "new.go")); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, root, "src/main.go", "package main\n// changed\n")
	if _, err := LoadVerifiedSnapshot(context.Background(), root, plan); err == nil {
		t.Fatal("content drift was accepted")
	}
}

func TestVerifiedSnapshotPreservesWhitespaceInScopeAndFiles(t *testing.T) {
	root := t.TempDir()
	const scope = "src "
	const relative = scope + "/nested /entry.go "
	const content = "package exact\n"
	dir, err := rootfs.ExactAbsolutePath(filepath.Join(root, scope))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	writeSnapshotTestFile(t, root, relative, content)
	writeSnapshotTestFile(t, root, scope+"/nested /entry.go", "package ordinary\n")
	writeSnapshotTestFile(t, root, scope+"/.env ", "API_KEY=excluded-value\n")
	writeSnapshotTestFile(t, root, "src/nested/entry.go", "package neighbor\n")

	plan, err := BuildPlan(t.Context(), BuildPlanRequest{
		PlanID: "fanout-plan-whitespace", RunID: "run-whitespace",
		WorkspaceID: "ws-whitespace", WorkspaceRoot: root, ScopePath: scope,
		Goal: "review source", Tier: domain.ReadOnlyFanoutOne, RequestedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScopePath != scope || len(plan.Files) != 2 ||
		plan.Files[0].RelativePath != strings.TrimSuffix(relative, " ") ||
		plan.Files[1].RelativePath != relative || plan.ExcludedCount != 1 {
		t.Fatalf("unexpected snapshot for %q: files=%#v excluded=%d", plan.ScopePath, plan.Files, plan.ExcludedCount)
	}

	writeSnapshotTestFile(t, root, "src/nested/entry.go", "package updatedNeighbor\n")
	snapshot, err := LoadVerifiedSnapshot(t.Context(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	files := snapshot.Shards[0].Files
	if len(files) != 2 || files[0].Content != "package ordinary\n" ||
		files[1].RelativePath != relative || files[1].Content != content {
		t.Fatalf("unexpected verified files: %#v", files)
	}
	writeSnapshotTestFile(t, root, relative, "package change\n")
	if _, err := LoadVerifiedSnapshot(t.Context(), root, plan); err == nil {
		t.Fatal("change to exact path did not invalidate the snapshot")
	}
}

func writeSnapshotTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path, err := rootfs.ExactAbsolutePath(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
