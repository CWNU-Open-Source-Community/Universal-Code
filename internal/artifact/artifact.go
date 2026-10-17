package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	KindToolOutput   = "tool_output"
	EncodingUTF8     = "utf-8"
	MaxContentBytes  = 4 * 1024 * 1024
	MaxIdentityRunes = 256
	MaxMIMEBytes     = 256
	MaxListLimit     = 500
)

type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

func (s Stream) Valid() bool {
	return s == StreamStdout || s == StreamStderr
}

type Output struct {
	Stream   Stream
	MIME     string
	Content  string
	Redacted bool
}

type CaptureRequest struct {
	RunID       string
	SessionID   string
	WorkspaceID string
	SourceID    string
	ToolName    string
	Outputs     []Output
	// Child outputs retain the exact in-flight host-issued invocation, even
	// when SourceID identifies an owned background command Job.
	AgentID              string
	AgentAttemptID       string
	SpecialistToolCallID string
	InvocationID         string
}

func NormalizeCaptureRequest(request CaptureRequest) (CaptureRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.SourceID = strings.TrimSpace(request.SourceID)
	request.ToolName = strings.TrimSpace(request.ToolName)
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.AgentAttemptID = strings.TrimSpace(request.AgentAttemptID)
	request.SpecialistToolCallID = strings.TrimSpace(request.SpecialistToolCallID)
	request.InvocationID = strings.TrimSpace(request.InvocationID)
	for label, value := range map[string]string{
		"run id": request.RunID, "session id": request.SessionID, "workspace id": request.WorkspaceID,
		"source id": request.SourceID, "tool name": request.ToolName,
		"agent id": request.AgentID, "agent attempt id": request.AgentAttemptID,
		"specialist tool call id": request.SpecialistToolCallID, "invocation id": request.InvocationID,
	} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) != value || len([]rune(value)) > MaxIdentityRunes {
			return CaptureRequest{}, fmt.Errorf("artifact %s must be normalized and bounded UTF-8", label)
		}
	}
	if request.RunID == "" || request.SessionID == "" || request.SourceID == "" || request.ToolName == "" {
		return CaptureRequest{}, errors.New("artifact Run, Session, source, and tool identities are required")
	}
	if request.AgentID != "" || request.AgentAttemptID != "" || request.SpecialistToolCallID != "" || request.InvocationID != "" {
		if request.AgentID == "" || request.AgentAttemptID == "" || request.SpecialistToolCallID == "" || request.InvocationID == "" {
			return CaptureRequest{}, errors.New("child artifact capture requires its complete invocation identity")
		}
	}
	if len(request.Outputs) == 0 || len(request.Outputs) > 2 {
		return CaptureRequest{}, errors.New("artifact capture requires one or two output streams")
	}
	seen := map[Stream]bool{}
	outputs := make([]Output, len(request.Outputs))
	for index, output := range request.Outputs {
		if !output.Stream.Valid() || seen[output.Stream] {
			return CaptureRequest{}, fmt.Errorf("artifact output stream %q is invalid or duplicated", output.Stream)
		}
		seen[output.Stream] = true
		output.MIME = strings.TrimSpace(output.MIME)
		if output.MIME == "" || len([]byte(output.MIME)) > MaxMIMEBytes {
			return CaptureRequest{}, errors.New("artifact output MIME is required and bounded")
		}
		if _, _, err := mime.ParseMediaType(output.MIME); err != nil {
			return CaptureRequest{}, fmt.Errorf("invalid artifact MIME %q: %w", output.MIME, err)
		}
		if output.Content == "" {
			return CaptureRequest{}, errors.New("artifact output content is required")
		}
		if !utf8.ValidString(output.Content) || len([]byte(output.Content)) > MaxContentBytes {
			return CaptureRequest{}, fmt.Errorf("artifact output must be valid UTF-8 and at most %d bytes", MaxContentBytes)
		}
		redacted := redact.String(output.Content)
		output.Redacted = output.Redacted || redacted != output.Content || strings.Contains(redacted, "[REDACTED:")
		output.Content = redacted
		if len([]byte(output.Content)) > MaxContentBytes {
			return CaptureRequest{}, fmt.Errorf("redacted artifact output exceeds %d bytes", MaxContentBytes)
		}
		outputs[index] = output
	}
	request.Outputs = outputs
	return request, nil
}

type Descriptor struct {
	ID          string
	RunID       string
	SessionID   string
	WorkspaceID string
	SourceID    string
	ToolName    string
	Stream      Stream
	Kind        string
	MIME        string
	Encoding    string
	SHA256      string
	SizeBytes   int64
	Redacted    bool
	CreatedAt   time.Time
}

func (d Descriptor) Validate() error {
	for label, value := range map[string]string{
		"id": d.ID, "run id": d.RunID, "session id": d.SessionID, "workspace id": d.WorkspaceID,
		"source id": d.SourceID, "tool name": d.ToolName,
	} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) != value || len([]rune(value)) > MaxIdentityRunes {
			return fmt.Errorf("artifact %s must be normalized and bounded UTF-8", label)
		}
	}
	if d.ID == "" || d.RunID == "" || d.SessionID == "" || d.SourceID == "" || d.ToolName == "" {
		return errors.New("artifact identity, Run, Session, source, and tool are required")
	}
	if !d.Stream.Valid() || d.Kind != KindToolOutput || d.Encoding != EncodingUTF8 {
		return errors.New("artifact stream, kind, or encoding is invalid")
	}
	if d.MIME == "" || len([]byte(d.MIME)) > MaxMIMEBytes {
		return errors.New("artifact MIME is required and bounded")
	}
	if _, _, err := mime.ParseMediaType(d.MIME); err != nil {
		return fmt.Errorf("invalid artifact MIME %q: %w", d.MIME, err)
	}
	if !validDigest(d.SHA256) {
		return errors.New("artifact SHA-256 must be a lowercase hex digest")
	}
	if d.SizeBytes <= 0 || d.SizeBytes > MaxContentBytes || d.CreatedAt.IsZero() {
		return errors.New("artifact size and creation time are invalid")
	}
	return nil
}

type Blob struct {
	Descriptor
	Content string
}

func (b Blob) Validate() error {
	if err := b.Descriptor.Validate(); err != nil {
		return err
	}
	if !utf8.ValidString(b.Content) || int64(len([]byte(b.Content))) != b.SizeBytes {
		return errors.New("artifact content size or UTF-8 encoding is invalid")
	}
	if Hash(b.Content) != b.SHA256 {
		return errors.New("artifact content hash does not match its descriptor")
	}
	return nil
}

type ListFilter struct {
	RunID    string
	SourceID string
	Stream   Stream
	Limit    int
	Offset   int
}

type Store interface {
	CaptureToolOutput(ctx context.Context, request CaptureRequest) ([]Descriptor, error)
	GetRunArtifact(ctx context.Context, id string) (Blob, error)
	ListRunArtifacts(ctx context.Context, filter ListFilter) ([]Descriptor, error)
}

type Manager struct {
	store Store
}

func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) Capture(ctx context.Context, request CaptureRequest) ([]Descriptor, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("artifact store is required")
	}
	normalized, err := NormalizeCaptureRequest(request)
	if err != nil {
		return nil, err
	}
	return m.store.CaptureToolOutput(ctx, normalized)
}

func (m *Manager) Get(ctx context.Context, id string) (Blob, error) {
	if m == nil || m.store == nil {
		return Blob{}, errors.New("artifact store is required")
	}
	id = strings.TrimSpace(id)
	if id == "" || !utf8.ValidString(id) || len([]rune(id)) > MaxIdentityRunes {
		return Blob{}, errors.New("artifact id is required and bounded")
	}
	return m.store.GetRunArtifact(ctx, id)
}

func (m *Manager) List(ctx context.Context, filter ListFilter) ([]Descriptor, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("artifact store is required")
	}
	return m.store.ListRunArtifacts(ctx, filter)
}

func Hash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
