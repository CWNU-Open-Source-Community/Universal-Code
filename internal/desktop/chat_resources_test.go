package desktop

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

type chatResourceFixture struct {
	resource  ChatResource
	requested ChatResourceRequest
	actions   []string
	url       string
}

func (f *chatResourceFixture) ReadChatResource(_ context.Context, r ChatResourceRequest) (ChatResource, error) {
	f.requested = r
	return f.resource, nil
}
func (f *chatResourceFixture) OpenURL(_ context.Context, url string) error { f.url = url; return nil }
func (f *chatResourceFixture) FileAction(_ context.Context, action string, _ ChatResource) (string, error) {
	f.actions = append(f.actions, action)
	return "done", nil
}

func TestChatResourcesReadAndActionsKeepExactReference(t *testing.T) {
	f := &chatResourceFixture{resource: ChatResource{Name: "report.md", Path: "report.md", Data: []byte("# 完成\n")}}
	b := &DesktopBridge{contextProvider: func() context.Context { return context.Background() }, resourceReader: f, resourceHost: f}
	r := ChatResourceRequest{Version: ChatResourceVersion, WorkspaceID: "workspace", RunID: "historical-run", Path: "report.md"}
	view, err := b.ReadChatResource(r)
	if err != nil || view.DataBase64 != base64.StdEncoding.EncodeToString(f.resource.Data) || f.requested != r {
		t.Fatalf("read: %#v %v", view, err)
	}
	for _, action := range []string{"copy_file", "copy_path", "copy_content", "save_as", "open", "open_with", "reveal"} {
		r.Action = action
		if status, err := b.ChatResourceAction(r); err != nil || status != "done" {
			t.Fatalf("%s: %s %v", action, status, err)
		}
	}
	r.Action = "execute"
	if _, err := b.ChatResourceAction(r); err == nil || len(f.actions) != 7 {
		t.Fatal("unknown action reached native host")
	}
	r.Action = "copy_content"
	f.resource.Data = []byte{0, 255}
	if _, err := b.ChatResourceAction(r); err == nil {
		t.Fatal("binary content was copied as text")
	}
	r.AttachmentID = "attachment"
	if _, err := b.ReadChatResource(r); err == nil {
		t.Fatal("ambiguous reference accepted")
	}
}

func TestChatResourcePathsAreWorkspaceBoundAndByteExact(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "项目说明.md")
	content := []byte("# title\r\n中文\n")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"项目说明.md", file} {
		value, err := readWorkspaceChatResource(root, path)
		if err != nil || string(value.Data) != string(content) || value.RelativePath != "项目说明.md" || value.Path != file {
			t.Fatalf("%q: %#v %v", path, value, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret.txt", outside, "file.txt:secret", ".", "bad\x00.txt"} {
		if _, err := readWorkspaceChatResource(root, path); err == nil {
			t.Fatalf("accepted invalid path %q", path)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err == nil {
		if _, err := readWorkspaceChatResource(root, "link.txt"); err == nil {
			t.Fatal("symlink escaped workspace")
		}
	}
	large := filepath.Join(root, "large.dat")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(MaxClipboardFileBytes + 1)
	_ = f.Close()
	if _, err := readWorkspaceChatResource(root, "large.dat"); err == nil {
		t.Fatal("unbounded file read")
	}
}

func TestChatExternalLinksUseBrowserWithoutOpeningHostProtocols(t *testing.T) {
	host := &chatResourceFixture{}
	b := &DesktopBridge{contextProvider: func() context.Context { return context.Background() }, resourceHost: host}
	for _, target := range []string{"https://github.com/org/repo/pull/4", "http://localhost:5173/path?q=hello", "https://example.com/?q=a&b=c#part"} {
		if err := b.OpenExternalURL(target); err != nil || host.url != target {
			t.Fatalf("%s: %v", target, err)
		}
	}
	for _, target := range []string{"javascript:alert(1)", "file:///C:/test.exe", "https://user:secret@example.com/", "https://example.com/\n", "https://example.com\\@localhost", "ms-settings:privacy", "cmd.exe"} {
		host.url = ""
		if err := b.OpenExternalURL(target); err == nil || host.url != "" {
			t.Fatalf("opened invalid target %q", target)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.contextProvider = func() context.Context { return ctx }
	if err := b.OpenExternalURL("https://example.com/"); err == nil {
		t.Fatal("closed bridge opened browser")
	}
}

func TestChatResourcesReadStoredAttachmentAndGeneratedFile(t *testing.T) {
	b, input := newClipboardBridgeFixture(t)
	state := b.clipboardImages.(*store.SQLiteStore)
	plane := &ControlPlane{stateStore: state}
	b.resourceReader = plane
	input.files = []ClipboardFile{{Name: "用户文件.md", Data: []byte("原始文件\r\n")}}
	paste, err := b.PasteClipboardFiles(clipboardRequest())
	if err != nil || len(paste.Attachments) != 1 {
		t.Fatalf("paste: %#v %v", paste, err)
	}
	ref := ChatResourceRequest{Version: ChatResourceVersion, WorkspaceID: "workspace-clipboard", AttachmentID: paste.Attachments[0].ID}
	read, err := b.ReadChatResource(ref)
	if err != nil || read.DataBase64 != base64.StdEncoding.EncodeToString(input.files[0].Data) {
		t.Fatalf("read attachment: %#v %v", read, err)
	}
	ref.WorkspaceID = "other-workspace"
	if _, err := b.ReadChatResource(ref); err == nil {
		t.Fatal("cross-workspace attachment read")
	}
	workspace, err := plane.ResolveWorkspace(t.Context(), "workspace-clipboard")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.RootPath, "generated.md"), []byte("generated result"), 0600); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(t.Context(), application.CreateRunRequest{
		Goal: "create a report", Profile: "code", Surface: "code", WorkspaceID: workspace.ID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	ref = ChatResourceRequest{Version: ChatResourceVersion, WorkspaceID: workspace.ID, RunID: run.ID, Path: "generated.md"}
	read, err = b.ReadChatResource(ref)
	if err != nil || read.RelativePath != "generated.md" || read.DataBase64 != base64.StdEncoding.EncodeToString([]byte("generated result")) {
		t.Fatalf("read generated file: %#v %v", read, err)
	}
	// Moving the conversation changes its current workspace, while old replies
	// must continue reading the original run's file even if names overlap.
	// Match the canonical roots returned by the project picker. TempDir can
	// contain /var symlinks on macOS or short directory names on Windows.
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"move-target", "unrelated-project"} {
		if err := state.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: id, Name: id, RootPath: target, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "generated.md"), []byte("different project file"), 0600); err != nil {
		t.Fatal(err)
	}
	thread, err := state.GetThreadByRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := application.NewThreadProjectMoveService(state, nil).Move(t.Context(), domain.ThreadProjectMoveRequest{
		Version: domain.ThreadProjectMoveVersion, ThreadID: thread.ID, ProjectWorkspaceID: "move-target",
		ExpectedVersion: thread.ProjectAssignmentVersion, OperationKey: "chat-resource-move-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	ref.WorkspaceID = moved.Thread.WorkspaceID
	read, err = b.ReadChatResource(ref)
	if err != nil || read.DataBase64 != base64.StdEncoding.EncodeToString([]byte("generated result")) {
		t.Fatalf("historical file after project move: %#v %v", read, err)
	}
	ref.WorkspaceID = "unrelated-project"
	if _, err := b.ReadChatResource(ref); err == nil {
		t.Fatal("unrelated workspace accepted for historical run")
	}
}
