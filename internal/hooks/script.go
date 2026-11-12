package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Operator-owned script hooks extend the declarative runtime with one bounded,
// host-executed action. A script is untrusted: it can deny or annotate through
// a single bounded JSON value and can never widen a call, modify tool
// arguments, grant authority, or recurse. Repository content, project
// instructions, skills, plugins, and tool output never contribute script hook
// declarations; operators install them under their own home hooks directory.
const (
	MaxScriptTimeoutMillis = 10_000
	MaxScriptArgs          = 8
	MaxScriptArgRunes      = 128
	MaxScriptPathBytes     = 256
	MaxScriptFileBytes     = 1024 * 1024
)

// ErrScriptIntegrity signals that a script file does not match its pinned SHA256 digest.
// Integrity failures must fail closed regardless of the hook's FailurePolicy.
var ErrScriptIntegrity = errors.New("script hook content does not match its pinned digest")

// Interpreter names accepted in ScriptSpec. Availability is resolved by the
// executor on every execution and an unavailable interpreter fails closed.
const (
	InterpreterPowerShell Interpreter = "powershell"
	InterpreterPwsh       Interpreter = "pwsh"
	InterpreterSh         Interpreter = "sh"
	InterpreterPython     Interpreter = "python"
	InterpreterNode       Interpreter = "node"
)

// Interpreter is a fixed host-resolved program used to run a script file. No
// shell is involved: the executor builds one argument vector per interpreter.
type Interpreter string

func (i Interpreter) Valid() bool {
	switch i {
	case InterpreterPowerShell, InterpreterPwsh, InterpreterSh, InterpreterPython, InterpreterNode:
		return true
	default:
		return false
	}
}

// ScriptSpec declares one operator-owned script. Path is relative to the
// operator hooks directory (never absolute, never escaping it) and SHA256 pins
// the exact executed content; the executor re-verifies both on every fire.
type ScriptSpec struct {
	Interpreter string   `json:"interpreter"`
	Path        string   `json:"path"`
	Args        []string `json:"args,omitempty"`
	SHA256      string   `json:"sha256"`
}

func (s ScriptSpec) Validate() error {
	if !Interpreter(s.Interpreter).Valid() {
		return errors.New("hook script interpreter is not supported")
	}
	if !validScriptPath(s.Path) {
		return errors.New("hook script path is invalid")
	}
	if len(s.Args) > MaxScriptArgs {
		return fmt.Errorf("hook script declares more than %d arguments", MaxScriptArgs)
	}
	for _, argument := range s.Args {
		if !validScriptArgument(argument) {
			return errors.New("hook script argument is invalid")
		}
	}
	if !validDigest(s.SHA256) {
		return errors.New("hook script content digest is invalid")
	}
	return nil
}

func validScriptPath(value string) bool {
	if value == "" || len([]byte(value)) > MaxScriptPathBytes ||
		strings.ContainsFunc(value, func(current rune) bool {
			return current < 0x20 || current == ':' || current == '\\'
		}) {
		return false
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "..") {
		return false
	}
	slash := filepath.ToSlash(filepath.FromSlash(value))
	return slash == value && !strings.HasPrefix(slash, "./") && !strings.HasSuffix(slash, "/")
}

func validScriptArgument(value string) bool {
	if value == "" || len([]rune(value)) > MaxScriptArgRunes {
		return false
	}
	for _, current := range value {
		switch {
		case current >= 'a' && current <= 'z', current >= 'A' && current <= 'Z',
			current >= '0' && current <= '9':
		case strings.ContainsRune("._:@=+-", current):
		default:
			return false
		}
	}
	return true
}

// ScriptInvocation is the host-owned request handed to a ScriptExecutor. The
// engine derives it from a validated declaration; executors must re-verify the
// pinned file inside their held root before spawning anything.
type ScriptInvocation struct {
	HookID        string          `json:"hook_id"`
	Event         Event           `json:"event"`
	RunID         string          `json:"run_id,omitempty"`
	WorkspaceID   string          `json:"workspace_id,omitempty"`
	ToolName      string          `json:"tool_name,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Dir           string          `json:"-"`
	RelativePath  string          `json:"-"`
	Interpreter   string          `json:"-"`
	Args          []string        `json:"-"`
	SHA256        string          `json:"-"`
	TimeoutMillis int             `json:"-"`
}

func (i ScriptInvocation) stdinJSON() ([]byte, error) {
	encoded, err := json.Marshal(struct {
		Event       Event           `json:"event"`
		RunID       string          `json:"run_id,omitempty"`
		WorkspaceID string          `json:"workspace_id,omitempty"`
		ToolName    string          `json:"tool_name,omitempty"`
		Payload     json.RawMessage `json:"payload,omitempty"`
	}{Event: i.Event, RunID: i.RunID, WorkspaceID: i.WorkspaceID, ToolName: i.ToolName,
		Payload: i.Payload})
	if err != nil {
		return nil, fmt.Errorf("encode script hook input: %w", err)
	}
	return encoded, nil
}

// ScriptAnnotation is one bounded untrusted annotation produced by a script.
type ScriptAnnotation struct {
	Message string `json:"message"`
}

// ScriptOutput is the single bounded JSON value a script may print on stdout.
type ScriptOutput struct {
	ProtocolVersion string             `json:"protocol_version"`
	Denied          bool               `json:"denied"`
	Reason          string             `json:"reason,omitempty"`
	Annotations     []ScriptAnnotation `json:"annotations,omitempty"`
}

func (o ScriptOutput) Validate() error {
	if o.ProtocolVersion != ProtocolVersion {
		return errors.New("script hook output protocol version is invalid")
	}
	if !validText(o.Reason, MaxAnnotationBytes, true) {
		return errors.New("script hook output reason is invalid or oversized")
	}
	if len(o.Annotations) > MaxAnnotations {
		return fmt.Errorf("script hook output exceeds %d annotations", MaxAnnotations)
	}
	for _, annotation := range o.Annotations {
		if !validText(annotation.Message, MaxAnnotationBytes, false) {
			return errors.New("script hook annotation is invalid or oversized")
		}
	}
	return nil
}

// ScriptExecutor runs one declared script with host-owned bounds. Implementations
// must not inherit the ambient environment, must not route through a shell, and
// must treat script output as untrusted until it validates.
type ScriptExecutor interface {
	ExecuteScript(ctx context.Context, invocation ScriptInvocation) (ScriptOutput, error)
}

var (
	resolveInterpreterFn  = resolveInterpreter
	lookupEnvFn           = os.LookupEnv
	scriptEnvironmentVars = scriptEnvironmentEntries
)

// resolveInterpreter maps an interpreter name to an executable path and its
// fixed argument prefix. Platform-locked interpreters fail closed off-platform.
func resolveInterpreter(name string) (string, []string, error) {
	switch Interpreter(name) {
	case InterpreterPowerShell:
		if runtime.GOOS != "windows" {
			return "", nil, errors.New("powershell interpreter is unavailable on this platform")
		}
		executable, err := exec.LookPath("powershell")
		if err != nil {
			return "", nil, fmt.Errorf("hook script interpreter %q is unavailable: %w", name, err)
		}
		return executable, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}, nil
	case InterpreterPwsh:
		executable, err := exec.LookPath("pwsh")
		if err != nil {
			return "", nil, fmt.Errorf("hook script interpreter %q is unavailable: %w", name, err)
		}
		return executable, []string{"-NoProfile", "-NonInteractive", "-File"}, nil
	case InterpreterSh:
		if runtime.GOOS == "windows" {
			return "", nil, errors.New("sh interpreter is unavailable on this platform")
		}
		executable, err := exec.LookPath("sh")
		if err != nil {
			return "", nil, fmt.Errorf("hook script interpreter %q is unavailable: %w", name, err)
		}
		return executable, nil, nil
	case InterpreterPython:
		executable, err := exec.LookPath("python")
		if err != nil {
			return "", nil, fmt.Errorf("hook script interpreter %q is unavailable: %w", name, err)
		}
		return executable, []string{"-I"}, nil
	case InterpreterNode:
		executable, err := exec.LookPath("node")
		if err != nil {
			return "", nil, fmt.Errorf("hook script interpreter %q is unavailable: %w", name, err)
		}
		return executable, nil, nil
	default:
		return "", nil, errors.New("hook script interpreter is not supported")
	}
}

// scriptEnvironmentEntries is the explicit, non-inherited child environment:
// only OS-operational variables that keep child runtimes functional. Nothing
// else from the parent environment (credentials, tokens, provider keys) passes
// to a script, and scripts never see the model's provider configuration.
func scriptEnvironmentEntries() []string {
	keys := []string{"PATH", "TMPDIR"}
	if runtime.GOOS == "windows" {
		keys = []string{"SystemRoot", "SystemDrive", "windir", "COMSPEC", "PATHEXT", "TEMP", "TMP"}
	}
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := lookupEnvFn(key); ok {
			entries = append(entries, key+"="+value)
		}
	}
	slices.Sort(entries)
	return entries
}

// verifyScriptContent re-reads the pinned script through the held root and
// confirms it is a regular, non-link file whose exact bytes still match the
// declared digest. Any drift, replacement, missing file, or link substitution fails closed.
func verifyScriptContent(root *os.Root, relative string) ([]byte, error) {
	info, err := root.Lstat(relative)
	if err != nil {
		return nil, fmt.Errorf("%w: script hook file is not accessible: %v", ErrScriptIntegrity, err)
	}
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: script hook path is not a regular file", ErrScriptIntegrity)
	}
	if info.Size() > MaxScriptFileBytes {
		return nil, fmt.Errorf("%w: script hook file exceeds %d bytes", ErrScriptIntegrity, MaxScriptFileBytes)
	}
	content, err := root.ReadFile(relative)
	if err != nil {
		return nil, fmt.Errorf("%w: script hook file is not readable: %v", ErrScriptIntegrity, err)
	}
	digest := sha256.Sum256(content)
	reRead, err := root.ReadFile(relative)
	if err != nil || sha256.Sum256(reRead) != digest {
		return nil, fmt.Errorf("%w: script hook file changed while reading", ErrScriptIntegrity)
	}
	return content, nil
}

func scriptDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
