package workspacecheckpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadCaptureIndexBoundsMemoryButHashesTheWholeFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "index")
	// The oversized fixture exceeds the stored limit; the reader must hash
	// the complete file while keeping only the limited prefix in memory.
	oversized := bytes.Repeat([]byte("index-row-\n"), (MaxStoredIndexBytes/11)+5)
	if err := os.WriteFile(path, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	raw, digest, err := readCaptureIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != MaxStoredIndexBytes+1 {
		t.Fatalf("captured prefix length: %d", len(raw))
	}
	if !bytes.Equal(raw, oversized[:MaxStoredIndexBytes+1]) {
		t.Fatal("captured prefix diverged from the file head")
	}
	full := sha256.Sum256(oversized)
	if hex.EncodeToString(digest[:]) != hex.EncodeToString(full[:]) {
		t.Fatal("digest did not cover the complete index")
	}

	small := []byte("compact index\n")
	if err := os.WriteFile(path, small, 0600); err != nil {
		t.Fatal(err)
	}
	raw, digest, err = readCaptureIndex(path)
	if err != nil || len(raw) != len(small) || !bytes.Equal(raw, small) {
		t.Fatalf("small index capture: len=%d err=%v", len(raw), err)
	}
	smallDigest := sha256.Sum256(small)
	if hex.EncodeToString(digest[:]) != hex.EncodeToString(smallDigest[:]) {
		t.Fatal("small index digest diverged")
	}

	if _, _, err = readCaptureIndex(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing index error: %v", err)
	}

	// The exact-limit boundary: a file of exactly MaxStoredIndexBytes bytes
	// stays fully capturable (blob branch), and the +1 byte tips it into the
	// oversized classification.
	exact := bytes.Repeat([]byte("x"), MaxStoredIndexBytes)
	if err := os.WriteFile(path, exact, 0600); err != nil {
		t.Fatal(err)
	}
	raw, digest, err = readCaptureIndex(path)
	if err != nil || len(raw) != MaxStoredIndexBytes || !bytes.Equal(raw, exact) {
		t.Fatalf("exact-limit capture: len=%d err=%v", len(raw), err)
	}
	exactDigest := sha256.Sum256(exact)
	if hex.EncodeToString(digest[:]) != hex.EncodeToString(exactDigest[:]) {
		t.Fatal("exact-limit digest diverged")
	}
	over := append(append([]byte{}, exact...), 'y')
	if err := os.WriteFile(path, over, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _, err = readCaptureIndex(path)
	if err != nil || len(raw) != MaxStoredIndexBytes+1 {
		t.Fatalf("one-byte-over capture: len=%d err=%v", len(raw), err)
	}
}
