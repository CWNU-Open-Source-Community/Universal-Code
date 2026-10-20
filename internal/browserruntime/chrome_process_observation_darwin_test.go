//go:build darwin && cgo

package browserruntime

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinPIDEnumerationErrorsPropagate(t *testing.T) {
	// Compile the same private C wrappers used by production, replacing only
	// libproc with a deterministic syscall-result injector in this probe binary.
	binary := filepath.Join(t.TempDir(), "pid-list-probe")
	if output, err := exec.CommandContext(t.Context(), "clang", "-Wall", "-Werror", "-I.", "testdata/darwin_pid_list_probe.c", "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("native C observation probe compile: %v\n%s", err, output)
	}
	if output, err := exec.CommandContext(t.Context(), binary).CombinedOutput(); err != nil {
		t.Fatalf("native C observation probe: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}
