//go:build desktop

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/desktop"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type nativeChatResourceHost struct{}

func (nativeChatResourceHost) OpenURL(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return desktop.OpenNativeChatURL(target)
}

func (nativeChatResourceHost) FileAction(ctx context.Context, action string, resource desktop.ChatResource) (string, error) {
	name := filepath.Base(strings.ReplaceAll(resource.Name, "\\", "/"))
	if name == "" || name == "." || !filepath.IsLocal(name) || strings.ContainsAny(name, ":\x00\r\n") {
		name = "attachment"
	}
	switch action {
	case "copy_content":
		return "done", runtime.ClipboardSetText(ctx, string(resource.Data))
	case "copy_path":
		if resource.Path == "" {
			return "", fmt.Errorf("this attachment has no original local path; use Save as")
		}
		return "done", runtime.ClipboardSetText(ctx, resource.Path)
	case "save_as":
		path, err := runtime.SaveFileDialog(ctx, runtime.SaveDialogOptions{Title: "另存为", DefaultFilename: name})
		if err != nil {
			return "", err
		}
		if path == "" {
			return "cancelled", nil
		}
		return "done", os.WriteFile(path, resource.Data, 0600)
	case "reveal", "open", "open_with":
		if resource.Path != "" {
			return "done", desktop.OpenNativeChatFile(resource.Path, action)
		}
	}
	// Snapshot verified bytes into a per-content export. Repeated copies reuse
	// it and the file remains valid when another application pastes after exit.
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(resource.Data)
	dir := filepath.Join(cache, "Universal-Code", "chat-exports", fmt.Sprintf("%x", digest))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, resource.Data, 0600); err != nil {
		return "", err
	}
	if action == "copy_file" {
		return "done", desktop.CopyNativeChatFile(path)
	}
	return "done", desktop.OpenNativeChatFile(path, action)
}
