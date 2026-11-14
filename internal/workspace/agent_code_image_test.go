package workspace

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/imageattachment"
)

func TestAgentCodeImageEnforcesWorkspaceReadPolicy(t *testing.T) {
	root := t.TempDir()
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"valid.png", "excluded.png", ".hidden.png", "ignored.png"} {
		if err := os.WriteFile(filepath.Join(root, name), pixels.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.png\n"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := AgentCodePathPolicy{ExcludePaths: []string{"excluded.png"}}
	meta, data, err := AgentCodeReadImage(root, "valid.png", policy)
	if err != nil || meta.Width != 2 || meta.Height != 3 || !bytes.Equal(data, pixels.Bytes()) {
		t.Fatalf("meta=%+v err=%v", meta, err)
	}
	for _, path := range []string{"../valid.png", filepath.Join(root, "valid.png"), ".hidden.png", "ignored.png", "excluded.png", "."} {
		if _, _, err := AgentCodeReadImage(root, path, policy); err == nil {
			t.Fatalf("accepted blocked path %q", path)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("<svg></svg>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AgentCodeReadImage(root, "fake.png", policy); err == nil {
		t.Fatal("accepted non-raster image")
	}
	if err := os.WriteFile(filepath.Join(root, "huge.png"), make([]byte, imageattachment.MaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AgentCodeReadImage(root, "huge.png", policy); err == nil {
		t.Fatal("accepted oversized image")
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, pixels.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.png")); err == nil {
		if _, _, err := AgentCodeReadImage(root, "link.png", policy); err == nil {
			t.Fatal("accepted escaping symlink")
		}
	} else {
		t.Log("symlink creation unavailable; other path refusals exercised")
	}
}
