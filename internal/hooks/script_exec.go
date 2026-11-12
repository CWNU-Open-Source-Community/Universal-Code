package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ProcessScriptExecutor executes operator script hooks as bounded child
// processes inside one held os.Root. Scripts receive their input as JSON on
// stdin, may print one bounded JSON value on stdout, run with an explicitly
// minimal non-inherited environment, and are killed at their declared timeout.
// Stderr is never captured or persisted. The hook directory root is opened per
// execution so a directory created after startup is still usable.
type ProcessScriptExecutor struct {
	hooksDir string
}

func NewProcessScriptExecutor(hooksDir string) (*ProcessScriptExecutor, error) {
	if filepath.Clean(hooksDir) != hooksDir || !filepath.IsAbs(hooksDir) {
		return nil, errors.New("script hook directory must be an absolute clean path")
	}
	return &ProcessScriptExecutor{hooksDir: filepath.Clean(hooksDir)}, nil
}

func (x *ProcessScriptExecutor) ExecuteScript(ctx context.Context,
	invocation ScriptInvocation,
) (ScriptOutput, error) {
	if x == nil || x.hooksDir == "" {
		return ScriptOutput{}, errors.New("script hook executor is not configured")
	}
	if invocation.Interpreter == "" || invocation.RelativePath == "" {
		return ScriptOutput{}, errors.New("script hook invocation is incomplete")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && invocation.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx,
			time.Duration(invocation.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	root, err := os.OpenRoot(x.hooksDir)
	if err != nil {
		return ScriptOutput{}, fmt.Errorf("%w: open script hook directory: %v", ErrScriptIntegrity, err)
	}
	defer root.Close()
	content, err := verifyScriptContent(root, invocation.RelativePath)
	if err != nil {
		return ScriptOutput{}, err
	}
	if scriptDigest(content) != invocation.SHA256 {
		return ScriptOutput{}, ErrScriptIntegrity
	}
	executable, prefix, err := resolveInterpreterFn(invocation.Interpreter)
	if err != nil {
		return ScriptOutput{}, err
	}
	stdin, err := invocation.stdinJSON()
	if err != nil {
		return ScriptOutput{}, err
	}

	scriptDir := filepath.Dir(filepath.Join(x.hooksDir, invocation.RelativePath))
	ext := filepath.Ext(invocation.RelativePath)
	snapshot, err := os.CreateTemp(scriptDir, ".operator-hook-*.tmp"+ext)
	if err != nil {
		return ScriptOutput{}, fmt.Errorf("create script snapshot: %w", err)
	}
	snapshotPath := snapshot.Name()
	defer func() { _ = os.Remove(snapshotPath) }()
	if _, err := snapshot.Write(content); err != nil {
		_ = snapshot.Close()
		return ScriptOutput{}, fmt.Errorf("write script snapshot: %w", err)
	}
	if err := snapshot.Close(); err != nil {
		return ScriptOutput{}, fmt.Errorf("close script snapshot: %w", err)
	}
	_ = os.Chmod(snapshotPath, 0500)

	argv := make([]string, 0, len(prefix)+2+len(invocation.Args))
	argv = append(argv, prefix...)
	argv = append(argv, snapshotPath)
	argv = append(argv, invocation.Args...)

	cmd := exec.CommandContext(ctx, executable, argv...)
	cmd.Dir = x.hooksDir
	cmd.Env = scriptEnvironmentVars()
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := &boundedBuffer{Limit: MaxOutputBytes}
	// Stderr is diagnostic only: truncate instead of failing the execution, so
	// a script that prints verbose warnings does not fail closed.
	stderr := &truncatingBuffer{Limit: MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if stdout.overflowed {
			return ScriptOutput{}, errors.New("script hook output exceeds its size limit")
		}
		if ctx.Err() != nil {
			return ScriptOutput{}, fmt.Errorf("script hook exceeded its timeout: %w", ctx.Err())
		}
		if detail := bytes.TrimSpace(stderr.Bytes()); len(detail) != 0 {
			return ScriptOutput{}, fmt.Errorf("script hook did not run successfully: %w: %s",
				err, detail)
		}
		return ScriptOutput{}, fmt.Errorf("script hook did not run successfully: %w", err)
	}
	if stdout.overflowed {
		return ScriptOutput{}, errors.New("script hook output exceeds its size limit")
	}
	return parseScriptOutput(stdout.Bytes())
}

func parseScriptOutput(raw []byte) (ScriptOutput, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ScriptOutput{}, errors.New("script hook printed no JSON output")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var output ScriptOutput
	if err := decoder.Decode(&output); err != nil {
		return ScriptOutput{}, fmt.Errorf("script hook output is not a valid script result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ScriptOutput{}, errors.New("script hook output contains trailing JSON")
	}
	if err := output.Validate(); err != nil {
		return ScriptOutput{}, err
	}
	return output, nil
}

// boundedBuffer caps captured stdout at the hook output limit and reports
// overflow instead of truncating silently.
type boundedBuffer struct {
	Limit      int
	buffer     bytes.Buffer
	overflowed bool
}

// truncatingBuffer captures at most Limit bytes and discards the rest while
// the child keeps writing, so oversized diagnostics never break the pipe.
type truncatingBuffer struct {
	Limit     int
	buffer    bytes.Buffer
	truncated bool
}

func (t *truncatingBuffer) Write(payload []byte) (int, error) {
	if room := t.Limit - t.buffer.Len(); room > 0 {
		if len(payload) > room {
			t.buffer.Write(payload[:room])
			t.truncated = true
		} else {
			t.buffer.Write(payload)
		}
	} else {
		t.truncated = true
	}
	return len(payload), nil
}

func (t *truncatingBuffer) Bytes() []byte { return t.buffer.Bytes() }

func (b *boundedBuffer) Write(payload []byte) (int, error) {
	if b.overflowed || b.buffer.Len()+len(payload) > b.Limit {
		b.overflowed = true
		return 0, errors.New("script hook output exceeds its size limit")
	}
	return b.buffer.Write(payload)
}

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }
