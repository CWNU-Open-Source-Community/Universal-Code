package desktop

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const ChatResourceVersion = "desktop_chat_resource.v1"

// A reference selects a registered workspace or a stored attachment, never an
// arbitrary host file. RunID retains the physical directory of historical turns.
type ChatResourceRequest struct {
	Version      string `json:"version"`
	WorkspaceID  string `json:"workspace_id"`
	RunID        string `json:"run_id,omitempty"`
	Path         string `json:"path,omitempty"`
	AttachmentID string `json:"attachment_id,omitempty"`
	ImageID      string `json:"image_id,omitempty"`
	Action       string `json:"action,omitempty"`
}

type ChatResource struct {
	Name         string
	Path         string
	RelativePath string
	MIMEType     string
	Data         []byte
}

type ChatResourceView struct {
	Version      string `json:"version"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	MIMEType     string `json:"mime_type"`
	DataBase64   string `json:"data_base64"`
}

type ChatResourceReader interface {
	ReadChatResource(context.Context, ChatResourceRequest) (ChatResource, error)
}
type ChatResourceHost interface {
	OpenURL(context.Context, string) error
	FileAction(context.Context, string, ChatResource) (string, error)
}

func validChatResourceRequest(r ChatResourceRequest) bool {
	if r.Version != ChatResourceVersion || !validWorkspaceIdentity(r.WorkspaceID) ||
		(r.RunID != "" && !validWorkspaceIdentity(r.RunID)) {
		return false
	}
	count := 0
	for _, value := range []string{r.Path, r.AttachmentID, r.ImageID} {
		if value != "" {
			count++
		}
	}
	return count == 1 && (r.AttachmentID == "" || validWorkspaceIdentity(r.AttachmentID)) &&
		(r.ImageID == "" || validWorkspaceIdentity(r.ImageID))
}

// OpenExternalURL opens a web address in the default browser, outside the
// privileged renderer. OS handler protocols and shell arguments are not accepted.
func (b *DesktopBridge) OpenExternalURL(raw string) error {
	if b == nil || b.resourceHost == nil {
		return apperror.New(apperror.CodeUnavailable, "Browser opening is unavailable")
	}
	target, err := externalWebURL(raw)
	if err != nil {
		return err
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return err
	}
	return b.resourceHost.OpenURL(ctx, target)
}

func externalWebURL(raw string) (string, error) {
	if len(raw) > 8192 || raw != strings.TrimSpace(raw) || strings.IndexFunc(raw, unicode.IsControl) >= 0 || strings.Contains(raw, "\\") {
		return "", apperror.New(apperror.CodeInvalidArgument, "Invalid web address")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", apperror.New(apperror.CodeInvalidArgument, "Only HTTP and HTTPS web links can open in the browser")
	}
	return u.String(), nil
}

func OpenNativeChatURL(raw string) error {
	target, err := externalWebURL(raw)
	if err != nil {
		return err
	}
	return launchChatGPTBrowser(target)
}

func (b *DesktopBridge) ReadChatResource(r ChatResourceRequest) (ChatResourceView, error) {
	resource, err := b.readChatResource(r)
	if err != nil {
		return ChatResourceView{}, err
	}
	return ChatResourceView{Version: ChatResourceVersion, Name: resource.Name, Path: resource.Path,
		RelativePath: resource.RelativePath, MIMEType: resource.MIMEType, DataBase64: base64.StdEncoding.EncodeToString(resource.Data)}, nil
}

func (b *DesktopBridge) ChatResourceAction(r ChatResourceRequest) (string, error) {
	switch r.Action {
	case "copy_file", "copy_path", "copy_content", "save_as", "open", "open_with", "reveal":
	default:
		return "", apperror.New(apperror.CodeInvalidArgument, "Unknown file action")
	}
	if b == nil || b.resourceHost == nil {
		return "", apperror.New(apperror.CodeUnavailable, "Native file actions are unavailable")
	}
	resource, err := b.readChatResource(r)
	if err != nil {
		return "", err
	}
	if r.Action == "copy_content" && (!utf8.Valid(resource.Data) || strings.ContainsRune(string(resource.Data), 0)) {
		return "", apperror.New(apperror.CodeInvalidArgument, "This binary file cannot be copied as text; use Copy file")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return "", err
	}
	return b.resourceHost.FileAction(ctx, r.Action, resource)
}

func (b *DesktopBridge) readChatResource(r ChatResourceRequest) (ChatResource, error) {
	if b == nil || b.resourceReader == nil {
		return ChatResource{}, apperror.New(apperror.CodeUnavailable, "File actions are unavailable")
	}
	if !validChatResourceRequest(r) {
		return ChatResource{}, apperror.New(apperror.CodeInvalidArgument, "Invalid file reference")
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return ChatResource{}, err
	}
	return b.resourceReader.ReadChatResource(ctx, r)
}

func (c *ControlPlane) ReadChatResource(ctx context.Context, r ChatResourceRequest) (ChatResource, error) {
	if c == nil || c.stateStore == nil || !validChatResourceRequest(r) {
		return ChatResource{}, apperror.New(apperror.CodeInvalidArgument, "Invalid file reference")
	}
	if r.AttachmentID != "" {
		meta, data, err := c.stateStore.GetWorkspaceFileAttachment(ctx, r.WorkspaceID, r.AttachmentID)
		return ChatResource{Name: meta.Name, MIMEType: meta.MIMEType, Data: data}, err
	}
	if r.ImageID != "" {
		meta, data, err := c.stateStore.GetWorkspaceImage(ctx, r.WorkspaceID, r.ImageID)
		return ChatResource{Name: meta.Name, MIMEType: meta.MIMEType, Data: data}, err
	}
	workspace, err := c.ResolveWorkspace(ctx, r.WorkspaceID)
	if err != nil {
		return ChatResource{}, err
	}
	if r.RunID != "" {
		run, err := c.stateStore.GetRun(ctx, r.RunID)
		if err != nil {
			return ChatResource{}, err
		}
		mission, err := c.stateStore.GetMission(ctx, run.MissionID)
		if err != nil {
			return ChatResource{}, err
		}
		files, err := application.ResolveRunFileWorkspace(ctx, c.stateStore, run, mission, c.standardCodeDrydocks)
		if err != nil {
			return ChatResource{}, err
		}
		if files.Source.ID != r.WorkspaceID && files.Workspace.ID != r.WorkspaceID {
			return ChatResource{}, apperror.New(apperror.CodeConflict, "File reference does not belong to this run")
		}
		workspace.RootPath = files.Workspace.RootPath
	}
	return readWorkspaceChatResource(workspace.RootPath, r.Path)
}

func readWorkspaceChatResource(rootPath, requested string) (ChatResource, error) {
	if len(requested) > 4096 || !utf8.ValidString(requested) || strings.IndexFunc(requested, unicode.IsControl) >= 0 {
		return ChatResource{}, apperror.New(apperror.CodeInvalidArgument, "Invalid file path")
	}
	// Absolute paths in model output are accepted only inside this exact workspace.
	path := filepath.FromSlash(requested)
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(rootPath, path)
		if err != nil {
			return ChatResource{}, err
		}
	}
	if !filepath.IsLocal(path) || strings.Contains(path, ":") {
		return ChatResource{}, apperror.New(apperror.CodePolicyDenied, "File must be inside this workspace")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return ChatResource{}, err
	}
	defer root.Close()
	f, err := root.Open(path)
	if err != nil {
		return ChatResource{}, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return ChatResource{}, apperror.New(apperror.CodeInvalidArgument, "Select a regular file")
	}
	if before.Size() > MaxClipboardFileBytes {
		return ChatResource{}, apperror.New(apperror.CodeResourceExhausted, "File actions support files up to 5 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxClipboardFileBytes+1))
	if err != nil {
		return ChatResource{}, err
	}
	after, err := f.Stat()
	if err != nil || int64(len(data)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return ChatResource{}, apperror.New(apperror.CodeConflict, "File changed while being read; try again")
	}
	media := mime.TypeByExtension(filepath.Ext(path))
	if media == "" {
		media = http.DetectContentType(data)
	}
	return ChatResource{Name: filepath.Base(path), Path: filepath.Join(rootPath, path), RelativePath: filepath.ToSlash(filepath.Clean(path)), MIMEType: media, Data: data}, nil
}
