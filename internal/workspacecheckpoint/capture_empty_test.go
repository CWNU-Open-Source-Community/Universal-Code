package workspacecheckpoint

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCapturePersistsEmptyFileContentWithoutTreatingItAsMissing(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%t", git), func(t *testing.T) {
			root := t.TempDir()
			if git {
				root = newCheckpointRepository(t)
			}
			const name = ".npm-cache/_update-notifier-last-checked"
			mustCheckpointWrite(t, filepath.Join(root, filepath.FromSlash(name)), []byte{})
			if git {
				runCheckpointGit(t, root, "add", name)
			}
			snapshot, err := Capture(t.Context(), validCaptureRequest(root, time.Now().UTC()))
			if err != nil {
				t.Fatal(err)
			}
			entry := checkpointEntriesByPath(snapshot.Entries)[name]
			emptySHA := fmt.Sprintf("%x", sha256.Sum256(nil))
			if entry.State != StatePresent || entry.Size != 0 || !entry.Recoverable ||
				entry.StoragePolicy != StorageStored || entry.BlobSHA256 != emptySHA {
				t.Fatalf("empty file lost: %#v", entry)
			}
			found := false
			for _, blob := range snapshot.Blobs {
				if blob.SHA256 == emptySHA {
					found = true
					if blob.Content == nil || len(blob.Content) != 0 {
						t.Fatalf("empty content not preserved: %#v", blob)
					}
				}
			}
			if !found {
				t.Fatal("empty blob absent")
			}
			if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))); err != nil || len(data) != 0 {
				t.Fatalf("capture changed source: %q %v", data, err)
			}
			rootHandle, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer rootHandle.Close()
			_, omitted, err := hashCaptureFile(t.Context(), rootHandle, name, false)
			if err != nil || omitted != nil {
				t.Fatalf("hash-only capture retained content: %v %v", omitted, err)
			}
		})
	}
}
