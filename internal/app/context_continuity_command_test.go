package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/projectconfig"
)

func TestContextInstructionsAndExplicitMemoryCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if stdout, stderr, code := executeTestCommand(t, "workspace", "init", "context-demo"); code != 0 || stderr != "" || !strings.Contains(stdout, "initialized") {
		t.Fatalf("workspace init output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	root := filepath.Join(home, "workspaces", "context-demo")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("Use deterministic tests.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := executeTestCommand(t, "context", "instructions",
		"--workspace", "context-demo", "--target", "internal/parser.go", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("instruction output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var instructions struct {
		ProtocolVersion string `json:"protocol_version"`
		Fingerprint     string `json:"fingerprint"`
		Sources         []struct {
			Path         string `json:"path"`
			WhyEffective string `json:"why_effective"`
			Authority    struct {
				ToolGrant    bool `json:"tool_grant"`
				NetworkGrant bool `json:"network_grant"`
			} `json:"authority"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(stdout), &instructions); err != nil {
		t.Fatal(err)
	}
	if instructions.ProtocolVersion != projectconfig.InstructionSnapshotProtocolVersion ||
		instructions.Fingerprint == "" || len(instructions.Sources) != 1 ||
		instructions.Sources[0].Path == "" || instructions.Sources[0].WhyEffective == "" ||
		instructions.Sources[0].Authority.ToolGrant || instructions.Sources[0].Authority.NetworkGrant {
		t.Fatalf("instruction snapshot is not explainable/non-authorizing: %#v", instructions)
	}
	created, stderr, code := executeTestCommand(t, "run", "create", "inspect instructions",
		"--workspace", "context-demo", "--max-turns", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("run identity missing: %s", created)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("Use deterministic tests, then the full suite.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = executeTestCommand(t, "context", "instructions",
		"--run", runID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("Run instruction inspect output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var state application.ProjectInstructionState
	if err := json.Unmarshal([]byte(stdout), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Stale || !state.Diff.RequiresConfirmation ||
		state.Pinned.Snapshot.Fingerprint == state.Live.Fingerprint {
		t.Fatalf("Run instruction drift was not explained: %#v", state)
	}
	stdout, stderr, code = executeTestCommand(t, "context", "instructions",
		"--run", runID, "--confirm", "--expected-fingerprint",
		state.Pinned.Snapshot.Fingerprint, "--expected-live-fingerprint",
		state.Live.Fingerprint, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("Run instruction refresh output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	if err := json.Unmarshal([]byte(stdout), &state); err != nil {
		t.Fatal(err)
	}
	if state.Stale || !state.RefreshConfirmed || state.Pinned.Revision != 2 {
		t.Fatalf("Run instruction refresh was not confirmed: %#v", state)
	}

	stdout, stderr, code = executeTestCommand(t, "context", "memory", "create",
		"--scope", "project", "--workspace", "context-demo",
		"--title", "Test convention", "--content", "Run deterministic tests")
	if code != 0 || stderr != "" {
		t.Fatalf("memory create output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var memory contextmgr.Memory
	if err := json.Unmarshal([]byte(stdout), &memory); err != nil {
		t.Fatal(err)
	}
	if memory.Scope != contextmgr.MemoryScopeProject || memory.Version != 1 ||
		memory.SourceKind != "operator_explicit" {
		t.Fatalf("unexpected explicit memory: %#v", memory)
	}
	if _, stderr, code = executeTestCommand(t, "context", "memory", "create",
		"--title", "credential", "--content", "token=ghp_abcdefghijklmnopqrstuvwxyz1234567890"); code == 0 || !strings.Contains(stderr, "sensitive") {
		t.Fatalf("Secret-like memory was not rejected: stderr=%q code=%d", stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "context", "memory", "edit",
		memory.ID, "--version", "1", "--reference", "docs/testing.md")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"docs/testing.md"`) {
		t.Fatalf("memory reference edit output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "context", "memory", "disable",
		memory.ID, "--version", "2")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"status": "disabled"`) {
		t.Fatalf("memory disable output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "context", "memory", "export",
		"--scope", "project", "--workspace", "context-demo", "--all")
	if code != 0 || stderr != "" || !strings.Contains(stdout, memory.ID) ||
		!strings.Contains(stdout, `"capability_grant": false`) {
		t.Fatalf("memory export output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "context", "memory", "delete",
		memory.ID, "--version", "3")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "recoverable: false") {
		t.Fatalf("memory delete output=%q stderr=%q code=%d", stdout, stderr, code)
	}
}

func TestSessionContinuityCLI_CheckpointTreeAndFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "branch-demo"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	created, stderr, code := executeTestCommand(t, "run", "create",
		"branch-safe work", "--workspace", "branch-demo", "--max-turns", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	sessionID := sessionIDPattern.FindString(created)
	if runID == "" || sessionID == "" {
		t.Fatalf("run/session identities missing: %s", created)
	}
	stdout, stderr, code := executeTestCommand(t, "session", "checkpoint", runID,
		"--title", "safe branch point", "--summary", "No authority is inherited", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("checkpoint output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var checkpoint contextmgr.ContinuityNode
	if err := json.Unmarshal([]byte(stdout), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Kind != contextmgr.ContinuityNodeCheckpoint ||
		checkpoint.Snapshot.Authority != (contextmgr.ContinuityAuthority{}) {
		t.Fatalf("checkpoint widened authority: %#v", checkpoint)
	}
	stdout, stderr, code = executeTestCommand(t, "session", "tree", sessionID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("tree output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var tree contextmgr.SessionTree
	if err := json.Unmarshal([]byte(stdout), &tree); err != nil {
		t.Fatal(err)
	}
	if tree.CapabilityGrant || len(tree.Nodes) < 2 {
		t.Fatalf("unexpected continuity tree: %#v", tree)
	}
	stdout, stderr, code = executeTestCommand(t, "session", "fork", checkpoint.ID,
		"--goal", "alternate safe branch", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("fork output=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var fork application.ContinuityBranchResult
	if err := json.Unmarshal([]byte(stdout), &fork); err != nil {
		t.Fatal(err)
	}
	if fork.Run.ID == runID || fork.Run.SessionID == sessionID ||
		fork.Node.Kind != contextmgr.ContinuityNodeFork || len(fork.NotInherited) < 8 {
		t.Fatalf("fork did not create a fresh non-authorizing branch: %#v", fork)
	}
}

// The usage line and the scope guard must agree: --confirm belongs to the
// --run scope (the pinned snapshot it mutates is run state), and the guard
// errors are usage-classified so scripts can tell argument mistakes from
// real failures.
func TestContextInstructionsConfirmScopeIsDocumentedAndClassified(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	_, stderr, code := executeTestCommand(t, "context", "instructions",
		"--workspace", "no-such-ws", "--confirm",
		"--expected-fingerprint", "deadbeef", "--expected-live-fingerprint", "deadbeef")
	if code != 2 || !strings.Contains(stderr, "requires --run <run-id>") ||
		!strings.Contains(stderr, "usage:") {
		t.Fatalf("workspace-scope confirm must be a usage error naming --run: stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "confirm-scope-ws"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	created, stderr, code := executeTestCommand(t, "run", "create", "confirm scope probe",
		"--workspace", "confirm-scope-ws")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	_, stderr, code = executeTestCommand(t, "context", "instructions", "--run", runID, "--confirm")
	if code != 2 || !strings.Contains(stderr, "pinned and live fingerprints") {
		t.Fatalf("confirm without fingerprints must be a usage error: stderr=%q code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "context", "instructions", "--run", runID,
		"--expected-fingerprint", "abc")
	if code != 2 || !strings.Contains(stderr, "only with --confirm") {
		t.Fatalf("fingerprints without confirm must be a usage error: stderr=%q code=%d", stderr, code)
	}
}
