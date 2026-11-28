package tools

import (
	"context"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadFileToolScopesToWorkspace(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("scoped notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadFileTool(root)
	result, err := tool.Run(context.Background(), Call{Args: map[string]string{"path": "notes.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "scoped notes\n" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}

	result, err = tool.Run(context.Background(), Call{Args: map[string]string{"path": "../outside.txt"}})
	if err == nil {
		t.Fatal("expected workspace escape to fail")
	}
	if !strings.Contains(result.Stderr, "escapes workspace") {
		t.Fatalf("unexpected stderr: %q", result.Stderr)
	}

	result, err = tool.Run(context.Background(), Call{Args: map[string]string{"path": filepath.Join(root, "notes.txt")}})
	if err == nil {
		t.Fatal("expected absolute path to fail")
	}
	if !strings.Contains(result.Stderr, "relative") {
		t.Fatalf("unexpected stderr: %q", result.Stderr)
	}
}

func TestListWorkspaceToolScopesAndLimitsDepth(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "example.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "nested", "deep.py"), []byte("print('deep')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewListWorkspaceTool(root)
	result, err := tool.Run(context.Background(), Call{Args: map[string]string{"path": ".", "max_depth": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"README.md", "scripts", "example.py"} {
		if !strings.Contains(result.Stdout, want) {
			t.Fatalf("tree missing %q:\n%s", want, result.Stdout)
		}
	}
	if strings.Contains(result.Stdout, "deep.py") {
		t.Fatalf("tree exceeded requested depth:\n%s", result.Stdout)
	}
}

func TestReadFileToolTruncatesText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileTool(root)
	result, err := tool.Run(context.Background(), Call{Args: map[string]string{"path": "long.txt", "max_bytes": "3"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "abc") || !strings.Contains(result.Stdout, "truncated at 3 bytes") {
		t.Fatalf("unexpected truncated output: %q", result.Stdout)
	}
}

func TestReadFileToolRejectsOversizedReadLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "short.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileTool(root)
	result, err := tool.Run(context.Background(), Call{Args: map[string]string{
		"path": "short.txt", "max_bytes": "999999999999999999999999",
	}})
	if err == nil || !strings.Contains(result.Stderr, "between 1 and") {
		t.Fatalf("oversized read limit was not rejected: result=%#v err=%v", result, err)
	}
}

func TestReadFileToolTruncatesAtUTF8Boundary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "utf8.txt"), []byte("界面"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadFileTool(root).Run(context.Background(), Call{
		Args: map[string]string{"path": "utf8.txt", "max_bytes": "4"},
	})
	if err != nil || !result.Truncated || !utf8.ValidString(result.Stdout) || !strings.Contains(result.Stdout, "界") {
		t.Fatalf("UTF-8 truncation failed: %#v err=%v", result, err)
	}
}

func TestReadFileToolRejectsInvalidUTF8BeforeTruncation(t *testing.T) {
	root := t.TempDir()
	data := append([]byte{0xff}, []byte(strings.Repeat("a", 16))...)
	if err := os.WriteFile(filepath.Join(root, "binary.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadFileTool(root).Run(context.Background(), Call{
		Args: map[string]string{"path": "binary.txt", "max_bytes": "4"},
	})
	if err == nil || !strings.Contains(result.Stderr, "not valid UTF-8") {
		t.Fatalf("invalid UTF-8 prefix was not rejected: %#v err=%v", result, err)
	}
}

func TestReadFileToolRejectsInvalidUTF8AtTruncationBoundary(t *testing.T) {
	root := t.TempDir()
	data := append([]byte("abc"), 0xff)
	data = append(data, []byte("more text")...)
	if err := os.WriteFile(filepath.Join(root, "boundary.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := NewReadFileTool(root).Run(context.Background(), Call{
		Args: map[string]string{"path": "boundary.bin", "max_bytes": "4"},
	})
	if err == nil || !strings.Contains(result.Stderr, "not valid UTF-8") {
		t.Fatalf("invalid UTF-8 boundary was not rejected: %#v err=%v", result, err)
	}
}

func TestReadFileToolRedactsSecrets(t *testing.T) {
	root := t.TempDir()
	mimoToken := "t" + "p-" + strings.Repeat("a", 40)
	raw := "MIMO_API_KEY=" + mimoToken + "\n"
	if err := os.WriteFile(filepath.Join(root, "env.txt"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileTool(root)
	result, err := tool.Run(context.Background(), Call{Args: map[string]string{"path": "env.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Stdout, mimoToken[:11]) {
		t.Fatalf("secret was not redacted: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "[REDACTED:secret]") {
		t.Fatalf("expected redaction marker, got %q", result.Stdout)
	}
}

func TestWorkspaceFSResolveForWriteScopesExistingAndNewFiles(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := NewWorkspaceFS(root)
	existing, err := fs.ResolveForWrite("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if existing != filepath.Join(root, "notes.txt") {
		t.Fatalf("unexpected existing path: %q", existing)
	}
	created, err := fs.ResolveForWrite(filepath.Join("scripts", "new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if created != filepath.Join(root, "scripts", "new.go") {
		t.Fatalf("unexpected new path: %q", created)
	}
	if _, err := fs.ResolveForWrite("../outside.txt"); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	if _, err := fs.ResolveForWrite(filepath.Join("missing", "new.go")); err == nil || !strings.Contains(err.Error(), "parent directory") {
		t.Fatalf("expected missing parent rejection, got %v", err)
	}
}

func TestWorkspaceFSResolveForWriteRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := NewWorkspaceFS(root).ResolveForWrite("link.txt"); err == nil ||
		!strings.Contains(err.Error(), "symbolic links or reparse points") {
		t.Fatalf("expected symlink escape rejection, got %v", err)
	}
}

func TestWorkspaceToolsHonorPreCancelledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	readResult, readErr := NewReadFileTool(root).Run(ctx, Call{Args: map[string]string{"path": "notes.txt"}})
	if !errors.Is(readErr, context.Canceled) || readResult.ExitCode != 130 {
		t.Fatalf("read did not honor cancellation: result=%#v err=%v", readResult, readErr)
	}
	listResult, listErr := NewListWorkspaceTool(root).Run(ctx, Call{Args: map[string]string{"path": "."}})
	if !errors.Is(listErr, context.Canceled) || listResult.ExitCode != 130 {
		t.Fatalf("list did not honor cancellation: result=%#v err=%v", listResult, listErr)
	}
}

func TestReadFileToolRejectsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	result, err := NewReadFileTool(root).Run(context.Background(), Call{
		Args: map[string]string{"path": "."},
	})
	if err == nil || !strings.Contains(result.Stderr, "regular file") {
		t.Fatalf("directory was not rejected as a non-regular file: result=%#v err=%v", result, err)
	}
}

// A failed lookup must name the workspace-relative path plainly: the raw
// stat text carries the host's absolute path and Windows syscall details,
// which reach the model context through tool results and stay in the
// durable session history that later turns feed back to the model.
func TestReadFileToolMissingFileNamesPathCleanly(t *testing.T) {
	root := t.TempDir()
	tool := NewReadFileTool(root)
	result, err := tool.Run(context.Background(), Call{Args: map[string]string{"path": "nope.txt"}})
	if err == nil {
		t.Fatal("expected missing file to fail")
	}
	if result.Stderr != "file does not exist in this workspace: nope.txt" {
		t.Fatalf("stderr must name the workspace-relative path: %q", result.Stderr)
	}
	if err.Error() != result.Stderr {
		t.Fatalf("returned error must match the workspace-relative message: %v", err)
	}
	if !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("missing-file cause must stay unwrappable: %v", err)
	}
}

func TestListWorkspaceToolMissingDirNamesPathCleanly(t *testing.T) {
	root := t.TempDir()
	tool := NewListWorkspaceTool(root)
	result, err := tool.Run(context.Background(), Call{Args: map[string]string{"path": "nope-dir"}})
	if err == nil {
		t.Fatal("expected missing directory to fail")
	}
	if result.Stderr != "directory does not exist in this workspace: nope-dir" {
		t.Fatalf("stderr must name the workspace-relative path: %q", result.Stderr)
	}
	if err.Error() != result.Stderr {
		t.Fatalf("returned error must match the workspace-relative message: %v", err)
	}
	if !errors.Is(err, iofs.ErrNotExist) {
		t.Fatalf("missing-directory cause must stay unwrappable: %v", err)
	}
}
