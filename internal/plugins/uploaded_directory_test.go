package plugins

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/toolcontract"
)

func uploadedDirectoryFixture() UploadedDirectory {
	return UploadedDirectory{RootName: "uploaded-skill", Files: []UploadedDirectoryFile{
		{Path: "SKILL.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\nname: uploaded-skill\ndescription: Read retained source bytes.\n---\nUse the provided resources.\n"))},
		{Path: "assets/data.bin", ContentBase64: base64.StdEncoding.EncodeToString([]byte{0, 255, 1, 128})},
		{Path: "assets/empty.txt", ContentBase64: ""},
	}}
}

func TestCaptureUploadedDirectoryFreezesDeterministicNativeBytes(t *testing.T) {
	upload := uploadedDirectoryFixture()
	marker := filepath.Join(t.TempDir(), "not-executed")
	script := []byte("Set-Content -LiteralPath '" + strings.ReplaceAll(marker, "'", "''") + "' -Value executed\n")
	upload.Files = append(upload.Files, UploadedDirectoryFile{Path: "scripts/install.ps1", ContentBase64: base64.StdEncoding.EncodeToString(script)})
	scratch := t.TempDir()
	source := toolcontract.SourceRef{URI: "upload-directory:uploaded-skill"}
	first, err := CaptureUploadedDirectory(t.Context(), upload, "stable-package", source, scratch)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(upload.Files)
	second, err := CaptureUploadedDirectory(t.Context(), upload, "stable-package", source, scratch)
	if err != nil || !bytes.Equal(first.Archive(), second.Archive()) || !reflect.DeepEqual(first.Snapshot, second.Snapshot) {
		t.Fatalf("file order changed acquired identity: %v", err)
	}
	if first.Snapshot.Name != "uploaded-skill" || len(first.Snapshot.Skills) != 1 || first.Snapshot.Skills[0].Description != "Read retained source bytes." {
		t.Fatalf("native metadata was not parsed: %+v", first.Snapshot)
	}
	for _, entry := range first.Snapshot.Inventory {
		want := uint32(0o600)
		if entry.Kind == "directory" {
			want = 0o700
		}
		if entry.Mode != want {
			t.Fatalf("noncanonical mode: %+v", entry)
		}
	}
	reader, err := OpenPortableSnapshot(t.Context(), first.Snapshot, first.Archive(), scratch)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		want []byte
	}{{"assets/data.bin", []byte{0, 255, 1, 128}}, {"assets/empty.txt", []byte{}}, {"scripts/install.ps1", script}} {
		got, _, err := reader.Read(t.Context(), first.Snapshot.Skills[0].Instructions.Component, test.path, MaxUncompressedBytes)
		if err != nil || !bytes.Equal(got, test.want) {
			t.Fatalf("retained %s: %x %v", test.path, got, err)
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("uploaded script ran: %v", err)
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("temporary parsing directories remain: %v %v", entries, err)
	}
}

func TestCaptureUploadedDirectoryRejectsPathsAliasesAndEncodings(t *testing.T) {
	tests := map[string]func(*UploadedDirectory){
		"absolute root": func(u *UploadedDirectory) { u.RootName = "C:/host" },
		"nested root":   func(u *UploadedDirectory) { u.RootName = "parent/skill" },
		"long root":     func(u *UploadedDirectory) { u.RootName = strings.Repeat("x", MaxUploadedDirectoryRootBytes+1) },
		"root reserved": func(u *UploadedDirectory) { u.RootName = "NUL" },
		"missing files": func(u *UploadedDirectory) { u.Files = nil },
		"duplicate":     func(u *UploadedDirectory) { u.Files = append(u.Files, u.Files[0]) },
		"case file": func(u *UploadedDirectory) {
			u.Files = append(u.Files, UploadedDirectoryFile{Path: "skill.md"})
		},
		"case directory": func(u *UploadedDirectory) {
			u.Files = append(u.Files, UploadedDirectoryFile{Path: "Assets/other.txt"})
		},
		"unicode case": func(u *UploadedDirectory) {
			u.Files = append(u.Files, UploadedDirectoryFile{Path: "assets/S.txt"}, UploadedDirectoryFile{Path: "assets/ſ.txt"})
		},
		"file as parent": func(u *UploadedDirectory) {
			u.Files = append(u.Files, UploadedDirectoryFile{Path: "a"}, UploadedDirectoryFile{Path: "a/b"})
		},
		"parent as file": func(u *UploadedDirectory) {
			u.Files = append(u.Files, UploadedDirectoryFile{Path: "a/b"}, UploadedDirectoryFile{Path: "a"})
		},
		"invalid base64": func(u *UploadedDirectory) { u.Files[1].ContentBase64 = "not!base64" },
		"padding bits":   func(u *UploadedDirectory) { u.Files[1].ContentBase64 = "AB==" },
		"whitespace":     func(u *UploadedDirectory) { u.Files[1].ContentBase64 = "YQ==\n" },
		"invalid native metadata": func(u *UploadedDirectory) {
			u.Files[0].ContentBase64 = base64.StdEncoding.EncodeToString([]byte("name supplied without native frontmatter"))
		},
	}
	for _, name := range []string{"", ".", "..", "../outside", "/absolute", "C:/host/file", `C:\host\file`, "a//b", "a/./b", "a/../b", "a\x00b", "a\nb", "NUL.txt", "assets/LPT¹.log", "assets/trailing.", ".git/config", ".GIT/config", strings.Repeat("x", 256), strings.Repeat("a/", MaxUploadedDirectoryPathBytes/2) + "x", string([]byte{0xff})} {
		name := name
		tests[fmt.Sprintf("path %q", name)] = func(u *UploadedDirectory) { u.Files[1].Path = name }
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			upload := uploadedDirectoryFixture()
			mutate(&upload)
			_, err := CaptureUploadedDirectory(t.Context(), upload, "package", toolcontract.SourceRef{URI: "test:upload"}, t.TempDir())
			if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
				t.Fatalf("invalid upload was not rejected: %v", err)
			}
		})
	}
}

func TestCaptureUploadedDirectoryEnforcesAcquisitionLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*UploadedDirectory)
	}{
		{"file count", func(u *UploadedDirectory) { u.Files = make([]UploadedDirectoryFile, MaxSnapshotEntries+1) }},
		{"implied directories", func(u *UploadedDirectory) {
			for i := 1; i < MaxSnapshotEntries; i++ {
				u.Files = append(u.Files, UploadedDirectoryFile{Path: fmt.Sprintf("d%d/file", i)})
			}
			u.Files = u.Files[:MaxSnapshotEntries]
		}},
		{"decoded bytes", func(u *UploadedDirectory) {
			u.Files[1].ContentBase64 = strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxUncompressedBytes)+4)
		}},
		{"archive bytes", func(u *UploadedDirectory) {
			value := make([]byte, MaxArchiveBytes+128*1024)
			if _, err := rand.Read(value); err != nil {
				t.Fatal(err)
			}
			u.Files[1].ContentBase64 = base64.StdEncoding.EncodeToString(value)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			upload := uploadedDirectoryFixture()
			test.mutate(&upload)
			_, err := CaptureUploadedDirectory(t.Context(), upload, "package", toolcontract.SourceRef{URI: "test:limits"}, t.TempDir())
			if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
				t.Fatalf("acquisition limit not enforced: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CaptureUploadedDirectory(ctx, uploadedDirectoryFixture(), "package", toolcontract.SourceRef{URI: "test:cancel"}, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition: %v", err)
	}
}
