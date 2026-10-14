package releasegate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectEXESigningPrepareNeedsOnlyPayloadAndMetadata(t *testing.T) {
	shell, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Fatal("Windows Desktop release checks require PowerShell 7: ", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.MkdirTemp(root, ".direct-exe-prepare-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(fixture); err != nil {
			t.Error(err)
		}
	})
	// Preparation hashes the unsigned payload; signing and PE validation happen later.
	payload := []byte("unsigned signing-request fixture")
	payloadHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	metadata, err := json.Marshal(map[string]any{
		"protocol_version": "portable_release_metadata.v1", "app_version": "v1.0.0",
		"revision": strings.Repeat("a", 40), "binary_name": "TraverseBoard.exe",
		"sha256": payloadHash, "source_date_epoch": time.Now().Add(-time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"TraverseBoard.exe": payload, "release-metadata.json": metadata} {
		if err := os.WriteFile(filepath.Join(fixture, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-NoProfile", "-NonInteractive", "-File", filepath.Join(root, "scripts", "stage-direct-exe.ps1"),
		"-OutputDirectory", filepath.Base(fixture), "-Channel", "stable",
		"-ExpectedSignerSubject", "CN=Fixture", "-ExpectedSignerThumbprint", strings.Repeat("A", 40)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, shell, append(append([]string{}, args...), "-PrepareSigningRequest")...).CombinedOutput()
	if err != nil {
		t.Fatalf("prepare with clean output failed: %v\n%s", err, output)
	}
	requestRaw, err := os.ReadFile(filepath.Join(fixture, "direct-exe-signing-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]string
	if err := json.Unmarshal(requestRaw, &request); err != nil ||
		request["protocol_version"] != "direct_exe_signing_request.v1" ||
		request["payload_sha256"] != payloadHash || request["required_signer_subject"] != "CN=Fixture" {
		t.Fatalf("prepared request = %s, err = %v", requestRaw, err)
	}
	output, err = exec.CommandContext(ctx, shell, args...).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Direct EXE staging input is missing:") ||
		!strings.Contains(string(output), "sbom.json") {
		t.Fatalf("final staging accepted missing distribution inputs: %v\n%s", err, output)
	}
}
