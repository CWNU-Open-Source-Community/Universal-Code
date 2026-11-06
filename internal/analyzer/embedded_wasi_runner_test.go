package analyzer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestEmbeddedWASIExecutesPinnedFixtureAndValidatesResult(t *testing.T) {
	raw := testRequestJSON(t)
	expected, expectedExit := Evaluate(raw)
	if expectedExit != ExitSuccess {
		t.Fatalf("reference evaluator failed with exit %d", expectedExit)
	}

	result, code := ExecuteEmbeddedWASI(context.Background(), raw)
	if code != "" {
		t.Fatalf("embedded WASI execution failed: %s (%#v)", code, result.Execution)
	}
	if !bytes.Equal(result.RawResult, expected) {
		t.Fatalf("guest result diverged from deterministic reference\nwant=%s\n got=%s",
			expected, result.RawResult)
	}
	candidate, candidateCode := BuildInvocationCandidate(raw)
	if candidateCode != "" {
		t.Fatal(candidateCode)
	}
	if code := ValidateAnalyzerEmbeddedWASIExecution(result.Execution, candidate); code != "" {
		t.Fatalf("execution receipt failed validation: %s", code)
	}
	if !result.Execution.RuntimeClosed || !result.Execution.GuestInstantiated ||
		!result.Execution.GuestExecuted || result.Execution.FilesystemMounted ||
		result.Execution.EnvironmentInherited || result.Execution.NetworkEnabled ||
		result.Execution.SubprocessEnabled || result.Execution.RawRequestIncluded ||
		result.Execution.RawResultIncluded {
		t.Fatalf("embedded execution widened its host boundary: %#v", result.Execution)
	}
}

func TestEmbeddedWASIModuleDigestIsPinned(t *testing.T) {
	digest := sha256.Sum256(embeddedAnalyzerFixtureWASM)
	const expected = "394785b47eebf248740036000da5afdd075d64396df0ccfcde7e4bac5a86a3c5"
	if actual := hex.EncodeToString(digest[:]); actual != expected {
		t.Fatalf("embedded analyzer fixture changed without provenance review: %s", actual)
	}
}

func TestEmbeddedWASIClosesInfiniteGuestOnDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	result, code := executeEmbeddedWASIModule(ctx, testRequestJSON(t), infiniteWASIModule())
	if code != CodeDeadlineExceeded {
		t.Fatalf("deadline returned %q with receipt %#v", code, result.Execution)
	}
	if result.Execution.Status != InvocationTimedOut || !result.Execution.RuntimeClosed ||
		!result.Execution.GuestExecuted || len(result.RawResult) != 0 {
		t.Fatalf("deadline did not fail closed: %#v", result)
	}
}

func TestEmbeddedWASIRejectsCancelledContextBeforeExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, code := ExecuteEmbeddedWASI(ctx, testRequestJSON(t))
	if code != CodeProcessFailed || result.Execution.Status != InvocationCancelled ||
		result.Execution.GuestExecuted || len(result.RawResult) != 0 {
		t.Fatalf("cancelled context did not fail closed: code=%q result=%#v", code, result)
	}
}

func TestBoundedAnalyzerWriterNeverRetainsOverflow(t *testing.T) {
	writer := newBoundedAnalyzerWriter(4)
	if written, err := writer.Write([]byte("abcdef")); written != 4 || err == nil ||
		!writer.overflow || string(writer.bytes()) != "abcd" {
		t.Fatalf("bounded writer failed closed: written=%d err=%v overflow=%t bytes=%q",
			written, err, writer.overflow, writer.bytes())
	}
}

// (module
//
//	(func (export "_start") (loop br 0))
//	(memory (export "memory") 1))
func infiniteWASIModule() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x05, 0x03, 0x01, 0x00, 0x01,
		0x07, 0x13, 0x02,
		0x06, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, 0x00, 0x00,
		0x06, 0x6d, 0x65, 0x6d, 0x6f, 0x72, 0x79, 0x02, 0x00,
		0x0a, 0x09, 0x01, 0x07, 0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x0b,
	}
}
