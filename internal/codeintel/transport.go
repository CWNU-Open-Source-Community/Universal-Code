package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type notificationHandler func(method string, params json.RawMessage)

type transport struct {
	descriptor ServerDescriptor
	root       string
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderr     *boundedBuffer
	process    *ownedCommand
	onNotify   notificationHandler

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	done    chan struct{}
	err     error
	closed  bool
	fail    sync.Once
}

func newTransport(descriptor ServerDescriptor, root, runtimeHome string,
	onNotify notificationHandler,
) (*transport, error) {
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	command := exec.Command(descriptor.Executable, descriptor.Arguments...)
	command.Dir = root
	command.Env = minimalLSPEnvironment(runtimeHome)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open LSP stdin: %w", err)
	}
	// Own the read end ourselves: Cmd.Wait closes StdoutPipe before the
	// protocol reader necessarily consumes the server's final message.
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open LSP stdout: %w", err)
	}
	command.Stdout = stdoutWriter
	stderr := newBoundedBuffer(MaxLogBytes)
	command.Stderr = stderr
	process, err := startOwnedCommand(command)
	_ = stdoutWriter.Close()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start reviewed LSP server: %w", err)
	}
	postStartDigest, available, digestErr := executableDigest(descriptor.Executable)
	if digestErr != nil || !available || postStartDigest != descriptor.ExecutableSHA256 {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = process.Kill()
		waitCtx, cancel := context.WithTimeout(context.Background(),
			MaximumShutdownGracePeriod)
		_ = process.Wait(waitCtx)
		cancel()
		return nil, errors.New("reviewed LSP executable changed across process startup")
	}
	value := &transport{descriptor: descriptor, root: root, stdin: stdin,
		stdout: bufio.NewReaderSize(stdout, 64*1024), stderr: stderr, process: process,
		onNotify: onNotify, nextID: 1, pending: make(map[int64]chan rpcResponse),
		done: make(chan struct{})}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer stdout.Close()
		value.readLoop()
	}()
	go func() {
		<-process.done
		// Prefer a protocol failure already written to stdout over the generic
		// exit notification. Bound draining when a descendant holds stdout open.
		stopDrain := time.AfterFunc(MaximumShutdownGracePeriod, func() { _ = stdout.Close() })
		<-readDone
		stopDrain.Stop()
		value.setFailure(fmt.Errorf("language server exited: %v", process.Err()), false)
	}()
	return value, nil
}

func (t *transport) request(ctx context.Context, method string, params any,
	target any,
) error {
	if ctx == nil || method == "" {
		return errors.New("LSP request context and method are required")
	}
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.closed {
		err := t.err
		t.mu.Unlock()
		if err == nil {
			err = errors.New("LSP transport is closed")
		}
		return err
	}
	id := t.nextID
	t.nextID++
	ready := make(chan rpcResponse, 1)
	t.pending[id] = ready
	t.mu.Unlock()

	idRaw := json.RawMessage(strconv.FormatInt(id, 10))
	request := rpcEnvelope{JSONRPC: "2.0", ID: idRaw, Method: method, Params: paramsRaw}
	if err := t.write(ctx, request); err != nil {
		t.removePending(id)
		return err
	}
	select {
	case <-ctx.Done():
		t.removePending(id)
		cancelCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		_ = t.notify(cancelCtx, "$/cancelRequest", map[string]any{"id": id})
		cancel()
		return ctx.Err()
	case <-t.done:
		t.removePending(id)
		return t.failure()
	case response := <-ready:
		if response.err != nil {
			return response.err
		}
		if target == nil || bytes.Equal(response.result, []byte("null")) {
			return nil
		}
		if len(response.result) > MaxResultBytes {
			return errors.New("LSP response result exceeds the semantic result limit")
		}
		decoder := json.NewDecoder(bytes.NewReader(response.result))
		decoder.UseNumber()
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("decode LSP %s response: %w", method, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("LSP response contains trailing JSON")
		}
		return nil
	}
}

func (t *transport) notify(ctx context.Context, method string, params any) error {
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return t.write(ctx, rpcEnvelope{JSONRPC: "2.0", Method: method, Params: paramsRaw})
}

func (t *transport) write(ctx context.Context, envelope rpcEnvelope) error {
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(raw) > MaxMessageBytes {
		return errors.New("LSP request exceeds the transport limit")
	}
	message := make([]byte, 0, len(raw)+64)
	message = append(message, []byte("Content-Length: "+strconv.Itoa(len(raw))+"\r\n\r\n")...)
	message = append(message, raw...)
	ready := make(chan error, 1)
	go func() {
		t.writeMu.Lock()
		defer t.writeMu.Unlock()
		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			ready <- errors.New("LSP transport is closed")
			return
		}
		_, writeErr := t.stdin.Write(message)
		ready <- writeErr
	}()
	select {
	case <-ctx.Done():
		t.setFailure(ctx.Err(), true)
		return ctx.Err()
	case <-t.done:
		return t.failure()
	case err := <-ready:
		if err != nil {
			t.setFailure(fmt.Errorf("write LSP message: %w", err), true)
		}
		return err
	}
}

func (t *transport) readLoop() {
	for {
		raw, err := readLSPMessage(t.stdout)
		if err != nil {
			t.setFailure(fmt.Errorf("read LSP message: %w", err), true)
			return
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.JSONRPC != "2.0" {
			t.setFailure(errors.New("language server returned an invalid JSON-RPC envelope"), true)
			return
		}
		if len(envelope.ID) != 0 && envelope.Method != "" {
			t.handleServerRequest(envelope)
			continue
		}
		if envelope.Method != "" {
			if t.onNotify != nil {
				t.onNotify(envelope.Method, append(json.RawMessage(nil), envelope.Params...))
			}
			continue
		}
		id, err := numericRPCID(envelope.ID)
		if err != nil {
			t.setFailure(err, true)
			return
		}
		t.mu.Lock()
		ready := t.pending[id]
		delete(t.pending, id)
		t.mu.Unlock()
		if ready == nil {
			// A late response to an explicitly cancelled request is ignored.
			continue
		}
		if envelope.Error != nil {
			message, _ := sanitizeText(envelope.Error.Message, 2048, false)
			ready <- rpcResponse{err: fmt.Errorf("LSP error %d: %s", envelope.Error.Code, message)}
		} else {
			ready <- rpcResponse{result: append(json.RawMessage(nil), envelope.Result...)}
		}
	}
}

func (t *transport) handleServerRequest(request rpcEnvelope) {
	result := any(nil)
	var responseErr *rpcError
	switch request.Method {
	case "workspace/configuration":
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(request.Params, &params) == nil {
			result = make([]any, len(params.Items))
		} else {
			result = []any{}
		}
	case "workspace/workspaceFolders":
		uri, err := workspaceURI(t.root)
		if err == nil {
			result = []map[string]string{{"uri": uri, "name": filepath.Base(t.root)}}
		}
	case "client/registerCapability", "client/unregisterCapability",
		"window/workDoneProgress/create":
		result = nil
	case "workspace/applyEdit":
		result = map[string]any{"applied": false,
			"failureReason": "Universal Code code-intel runtime is read-only"}
	default:
		responseErr = &rpcError{Code: -32601, Message: "method not supported by read-only client"}
	}
	response := rpcEnvelope{JSONRPC: "2.0", ID: append(json.RawMessage(nil), request.ID...),
		Error: responseErr}
	if responseErr == nil {
		response.Result, _ = json.Marshal(result)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = t.write(ctx, response)
	cancel()
}

func (t *transport) removePending(id int64) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

func (t *transport) setFailure(err error, kill bool) {
	if err == nil {
		err = errors.New("LSP transport stopped")
	}
	t.fail.Do(func() {
		t.mu.Lock()
		t.err = err
		t.closed = true
		pending := t.pending
		t.pending = make(map[int64]chan rpcResponse)
		t.mu.Unlock()
		_ = t.stdin.Close()
		if kill && t.process != nil {
			_ = t.process.Kill()
			waitCtx, cancel := context.WithTimeout(context.Background(),
				MaximumShutdownGracePeriod)
			_ = t.process.Wait(waitCtx)
			cancel()
		}
		close(t.done)
		for _, ready := range pending {
			ready <- rpcResponse{err: err}
		}
	})
}

func (t *transport) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	return errors.New("LSP transport stopped")
}

func (t *transport) closeGracefully(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if !closed {
		shutdownCtx, cancel := context.WithTimeout(ctx, MaximumShutdownGracePeriod)
		_ = t.request(shutdownCtx, "shutdown", nil, nil)
		_ = t.notify(shutdownCtx, "exit", nil)
		cancel()
		_ = t.stdin.Close()
	} else {
		_ = t.process.Kill()
	}
	select {
	case <-t.process.done:
		t.setFailure(errors.New("LSP transport closed"), false)
		return nil
	case <-ctx.Done():
		_ = t.process.Kill()
		t.setFailure(ctx.Err(), true)
		return ctx.Err()
	case <-time.After(MaximumShutdownGracePeriod):
		_ = t.process.Kill()
		t.setFailure(errors.New("LSP shutdown exceeded its grace period"), true)
		return errors.New("LSP shutdown exceeded its grace period")
	}
}

func readLSPMessage(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	headerBytes := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		headerBytes += len(line)
		if headerBytes > 8192 {
			return nil, errors.New("LSP header exceeds 8192 bytes")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		if !found {
			return nil, errors.New("LSP header is malformed")
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if contentLength >= 0 {
				return nil, errors.New("LSP message repeats Content-Length")
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 2 || parsed > MaxMessageBytes {
				return nil, errors.New("LSP Content-Length is invalid")
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 {
		return nil, errors.New("LSP message lacks Content-Length")
	}
	raw := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, errors.New("LSP message body is not valid JSON")
	}
	return raw, nil
}

func numericRPCID(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("LSP response lacks an id")
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("LSP response id is invalid")
	}
	return value, nil
}

func minimalLSPEnvironment(runtimeHome string) []string {
	allowed := map[string]struct{}{
		"PATH": {}, "PATHEXT": {}, "SYSTEMROOT": {}, "WINDIR": {},
		"LANG": {}, "LC_ALL": {}, "GOROOT": {},
	}
	values := make([]string, 0, len(allowed)+12)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, found := allowed[strings.ToUpper(name)]; found {
			values = append(values, entry)
		}
	}
	if runtimeHome != "" {
		values = append(values, "HOME="+runtimeHome, "USERPROFILE="+runtimeHome,
			"TMP="+runtimeHome, "TEMP="+runtimeHome, "TMPDIR="+runtimeHome,
			"GOCACHE="+filepath.Join(runtimeHome, "go-build"),
			"GOMODCACHE="+filepath.Join(runtimeHome, "go-mod"))
	}
	values = append(values, "GOENV=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local",
		"NPM_CONFIG_OFFLINE=true", "NPM_CONFIG_AUDIT=false", "NPM_CONFIG_FUND=false")
	return values
}
