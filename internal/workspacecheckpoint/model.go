package workspacecheckpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ProtocolVersion = "workspace-checkpoint.v1"

	MaxEntries             = 20_000
	MaxPathRunes           = 1_024
	MaxStoredFileBytes     = 4 * 1024 * 1024
	MaxStoredIndexBytes    = 32 * 1024 * 1024
	MaxCheckpointBlobBytes = 64 * 1024 * 1024
	MaxPreviewChanges      = 2_000
	MaxConflictItems       = 256
	MaxStoreBlobBytes      = 2 * 1024 * 1024 * 1024
	MaxStoreCheckpoints    = 10_000
	MaxStoreEntries        = 2_000_000
	MaxStoreTransactions   = 20_000
)

type RecoveryLevel string

const (
	RecoveryComplete    RecoveryLevel = "complete"
	RecoveryPartial     RecoveryLevel = "partial"
	RecoveryUnavailable RecoveryLevel = "unavailable"
)

func (l RecoveryLevel) Valid() bool {
	return l == RecoveryComplete || l == RecoveryPartial || l == RecoveryUnavailable
}

type TriggerKind string

const (
	TriggerManual          TriggerKind = "manual"
	TriggerFileTool        TriggerKind = "file_tool"
	TriggerCommandBatch    TriggerKind = "command_batch"
	TriggerGitMutation     TriggerKind = "git_mutation"
	TriggerAgentMerge      TriggerKind = "agent_merge"
	TriggerRewindPreflight TriggerKind = "rewind_preflight"
	TriggerRewindResult    TriggerKind = "rewind_result"
	TriggerFork            TriggerKind = "fork"
)

func (k TriggerKind) Valid() bool {
	switch k {
	case TriggerManual, TriggerFileTool, TriggerCommandBatch, TriggerGitMutation,
		TriggerAgentMerge, TriggerRewindPreflight, TriggerRewindResult, TriggerFork:
		return true
	default:
		return false
	}
}

type Phase string

const (
	PhaseStandalone Phase = "standalone"
	PhaseBefore     Phase = "before"
	PhaseAfter      Phase = "after"
	PhasePreflight  Phase = "preflight"
)

func (p Phase) Valid() bool {
	return p == PhaseStandalone || p == PhaseBefore || p == PhaseAfter || p == PhasePreflight
}

type EntryKind string

const (
	EntryFile      EntryKind = "file"
	EntryDirectory EntryKind = "directory"
	EntrySymlink   EntryKind = "symlink"
	EntryOther     EntryKind = "other"
)

func (k EntryKind) Valid() bool {
	return k == EntryFile || k == EntryDirectory || k == EntrySymlink || k == EntryOther
}

type WorktreeState string

const (
	StatePresent   WorktreeState = "present"
	StateMissing   WorktreeState = "missing"
	StateIgnored   WorktreeState = "ignored"
	StateGenerated WorktreeState = "generated"
	StateExternal  WorktreeState = "external"
)

func (s WorktreeState) Valid() bool {
	switch s {
	case StatePresent, StateMissing, StateIgnored, StateGenerated, StateExternal:
		return true
	default:
		return false
	}
}

type StoragePolicy string

const (
	StorageStored            StoragePolicy = "stored"
	StorageMissing           StoragePolicy = "missing"
	StorageExcludedIgnored   StoragePolicy = "excluded_ignored"
	StorageExcludedGenerated StoragePolicy = "excluded_generated"
	StorageExcludedLarge     StoragePolicy = "excluded_large"
	StorageExcludedSensitive StoragePolicy = "excluded_sensitive"
	StorageExcludedLink      StoragePolicy = "excluded_link"
	StorageExcludedSpecial   StoragePolicy = "excluded_special"
	StorageUnreadable        StoragePolicy = "unreadable"
)

func (p StoragePolicy) Valid() bool {
	switch p {
	case StorageStored, StorageMissing, StorageExcludedIgnored, StorageExcludedGenerated,
		StorageExcludedLarge, StorageExcludedSensitive, StorageExcludedLink,
		StorageExcludedSpecial, StorageUnreadable:
		return true
	default:
		return false
	}
}

type Entry struct {
	Path           string        `json:"path"`
	Kind           EntryKind     `json:"kind"`
	State          WorktreeState `json:"state"`
	StoragePolicy  StoragePolicy `json:"storage_policy"`
	Mode           uint32        `json:"mode"`
	Size           int64         `json:"size"`
	WorktreeSHA256 string        `json:"worktree_sha256"`
	BlobSHA256     string        `json:"blob_sha256,omitempty"`
	IndexOID       string        `json:"index_oid,omitempty"`
	IndexMode      string        `json:"index_mode,omitempty"`
	Tracked        bool          `json:"tracked"`
	Staged         bool          `json:"staged"`
	Binary         bool          `json:"binary"`
	LineEndings    string        `json:"line_endings,omitempty"`
	Recoverable    bool          `json:"recoverable"`
	Reason         string        `json:"reason,omitempty"`
}

func (e Entry) Validate() error {
	if !validPath(e.Path) || !e.Kind.Valid() || !e.State.Valid() ||
		!e.StoragePolicy.Valid() || e.Size < 0 || e.Mode > 0o7777 ||
		!validReason(e.Reason) {
		return errors.New("workspace checkpoint entry metadata is invalid")
	}
	if e.WorktreeSHA256 != "missing" && !validDigest(e.WorktreeSHA256, true) {
		return errors.New("workspace checkpoint entry content digest is invalid")
	}
	if e.BlobSHA256 != "" && !validDigest(e.BlobSHA256, false) {
		return errors.New("workspace checkpoint entry blob digest is invalid")
	}
	if e.StoragePolicy == StorageStored {
		if !e.Recoverable || e.Kind != EntryFile || e.State != StatePresent ||
			e.BlobSHA256 == "" || e.WorktreeSHA256 != e.BlobSHA256 ||
			e.Size > MaxStoredFileBytes {
			return errors.New("stored workspace checkpoint entry is inconsistent")
		}
	} else if e.BlobSHA256 != "" {
		return errors.New("excluded workspace checkpoint entry cannot reference a blob")
	}
	if e.StoragePolicy == StorageMissing &&
		(e.State != StateMissing || e.WorktreeSHA256 != "missing" || !e.Recoverable) {
		return errors.New("missing workspace checkpoint entry is inconsistent")
	}
	if e.Kind != EntryFile && e.Recoverable {
		return errors.New("non-file workspace checkpoint entries cannot be recoverable")
	}
	if e.LineEndings != "" && e.LineEndings != "lf" && e.LineEndings != "crlf" &&
		e.LineEndings != "mixed" && e.LineEndings != "none" {
		return errors.New("workspace checkpoint line-ending metadata is invalid")
	}
	if len(e.IndexOID) > 128 || len(e.IndexMode) > 16 ||
		strings.ContainsRune(e.IndexOID, 0) || strings.ContainsRune(e.IndexMode, 0) {
		return errors.New("workspace checkpoint index metadata is invalid")
	}
	return nil
}

type Blob struct {
	SHA256    string
	Content   []byte
	CreatedAt time.Time
}

func (b Blob) Validate(maxBytes int) error {
	if maxBytes <= 0 || len(b.Content) > maxBytes || !validDigest(b.SHA256, false) ||
		b.CreatedAt.IsZero() {
		return errors.New("workspace checkpoint blob metadata is invalid")
	}
	sum := sha256.Sum256(b.Content)
	if hex.EncodeToString(sum[:]) != b.SHA256 {
		return errors.New("workspace checkpoint blob failed integrity validation")
	}
	return nil
}

type Checkpoint struct {
	ID                   string        `json:"id"`
	ProtocolVersion      string        `json:"protocol_version"`
	RunID                string        `json:"run_id"`
	MissionID            string        `json:"mission_id"`
	SessionID            string        `json:"session_id"`
	WorkspaceID          string        `json:"workspace_id"`
	AttemptID            string        `json:"attempt_id,omitempty"`
	CapabilityGeneration string        `json:"capability_generation,omitempty"`
	Trigger              TriggerKind   `json:"trigger"`
	Phase                Phase         `json:"phase"`
	TriggerReceiptID     string        `json:"trigger_receipt_id"`
	RequestedBy          string        `json:"requested_by,omitempty"`
	Title                string        `json:"title,omitempty"`
	ParentCheckpointID   string        `json:"parent_checkpoint_id,omitempty"`
	RootFingerprint      string        `json:"root_fingerprint"`
	RootPathSHA256       string        `json:"root_path_sha256"`
	BaseCommit           string        `json:"base_commit"`
	Branch               string        `json:"branch"`
	IndexSHA256          string        `json:"index_sha256"`
	IndexBlobSHA256      string        `json:"index_blob_sha256,omitempty"`
	ManifestSHA256       string        `json:"manifest_sha256"`
	RecoveryLevel        RecoveryLevel `json:"recovery_level"`
	IncompleteReasons    []string      `json:"incomplete_reasons"`
	EntryCount           int           `json:"entry_count"`
	StoredBytes          int64         `json:"stored_bytes"`
	CreatedAt            time.Time     `json:"created_at"`
}

func (c Checkpoint) Validate() error {
	for _, value := range []string{c.ID, c.RunID, c.MissionID, c.SessionID,
		c.WorkspaceID, c.TriggerReceiptID} {
		if !validIdentity(value) {
			return errors.New("workspace checkpoint identity is invalid")
		}
	}
	for _, value := range []string{c.AttemptID, c.ParentCheckpointID} {
		if value != "" && !validIdentity(value) {
			return errors.New("workspace checkpoint optional identity is invalid")
		}
	}
	if c.ProtocolVersion != ProtocolVersion || !c.Trigger.Valid() || !c.Phase.Valid() ||
		!c.RecoveryLevel.Valid() || !validDigest(c.RootFingerprint, false) ||
		!validDigest(c.RootPathSHA256, false) || !validDigest(c.IndexSHA256, false) ||
		!validDigest(c.ManifestSHA256, false) || c.EntryCount < 0 ||
		c.EntryCount > MaxEntries || c.StoredBytes < 0 ||
		c.StoredBytes > MaxCheckpointBlobBytes || c.CreatedAt.IsZero() {
		return errors.New("workspace checkpoint metadata is invalid")
	}
	if (c.RequestedBy != "" && !validIdentity(c.RequestedBy)) || !validReason(c.Title) {
		return errors.New("workspace checkpoint attribution is invalid")
	}
	if c.CapabilityGeneration != "" && !validDigest(c.CapabilityGeneration, false) {
		return errors.New("workspace checkpoint capability generation is invalid")
	}
	if c.IndexBlobSHA256 != "" && !validDigest(c.IndexBlobSHA256, false) {
		return errors.New("workspace checkpoint index blob digest is invalid")
	}
	if !validGitBaseCommit(c.BaseCommit) ||
		len(c.Branch) > 255 || strings.ContainsRune(c.Branch, 0) {
		return errors.New("workspace checkpoint Git identity is invalid")
	}
	if len(c.IncompleteReasons) > 32 {
		return errors.New("workspace checkpoint has too many incomplete reasons")
	}
	for _, value := range c.IncompleteReasons {
		if !validReason(value) || value == "" {
			return errors.New("workspace checkpoint incomplete reason is invalid")
		}
	}
	return nil
}

func validGitBaseCommit(value string) bool {
	if value == "unborn" || value == "non-git" {
		return true
	}
	if (len(value) != 40 && len(value) != 64) || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded)*2 == len(value)
}

type Snapshot struct {
	Checkpoint Checkpoint
	Entries    []Entry
	Blobs      []Blob
}

func (s Snapshot) Validate() error {
	if err := s.Checkpoint.Validate(); err != nil {
		return err
	}
	if len(s.Entries) != s.Checkpoint.EntryCount || len(s.Entries) > MaxEntries {
		return errors.New("workspace checkpoint entry count is inconsistent")
	}
	entries := append([]Entry{}, s.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	blobs := make(map[string]Blob, len(s.Blobs))
	referencedBlobs := make(map[string]struct{}, len(s.Blobs))
	var storedBytes int64
	for _, blob := range s.Blobs {
		limit := MaxStoredFileBytes
		if blob.SHA256 == s.Checkpoint.IndexBlobSHA256 {
			limit = MaxStoredIndexBytes
		}
		if err := blob.Validate(limit); err != nil {
			return err
		}
		if _, exists := blobs[blob.SHA256]; exists {
			return errors.New("workspace checkpoint contains a duplicate blob")
		}
		blobs[blob.SHA256] = blob
		storedBytes += int64(len(blob.Content))
	}
	if storedBytes != s.Checkpoint.StoredBytes {
		return errors.New("workspace checkpoint stored byte count is inconsistent")
	}
	if s.Checkpoint.IndexBlobSHA256 != "" {
		if s.Checkpoint.IndexBlobSHA256 != s.Checkpoint.IndexSHA256 {
			return errors.New("workspace checkpoint index digest is inconsistent")
		}
		referencedBlobs[s.Checkpoint.IndexBlobSHA256] = struct{}{}
	}
	previous := ""
	for _, entry := range entries {
		if err := entry.Validate(); err != nil {
			return err
		}
		if entry.Path == previous {
			return errors.New("workspace checkpoint contains a duplicate path")
		}
		previous = entry.Path
		if entry.BlobSHA256 != "" {
			blob, ok := blobs[entry.BlobSHA256]
			if !ok {
				return fmt.Errorf("workspace checkpoint blob %s is absent", entry.BlobSHA256)
			}
			if int64(len(blob.Content)) != entry.Size {
				return errors.New("workspace checkpoint entry size does not match its blob")
			}
			referencedBlobs[entry.BlobSHA256] = struct{}{}
		}
	}
	if s.Checkpoint.IndexBlobSHA256 != "" {
		if _, ok := blobs[s.Checkpoint.IndexBlobSHA256]; !ok {
			return errors.New("workspace checkpoint index blob is absent")
		}
	}
	if len(referencedBlobs) != len(blobs) {
		return errors.New("workspace checkpoint contains an unreferenced blob")
	}
	manifestJSON, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	manifestSHA := sha256.Sum256(manifestJSON)
	if hex.EncodeToString(manifestSHA[:]) != s.Checkpoint.ManifestSHA256 {
		return errors.New("workspace checkpoint manifest digest is inconsistent")
	}
	return nil
}

type CaptureRequest struct {
	ID                   string
	RunID                string
	MissionID            string
	SessionID            string
	WorkspaceID          string
	WorkspaceRoot        string
	AttemptID            string
	CapabilityGeneration string
	Trigger              TriggerKind
	Phase                Phase
	TriggerReceiptID     string
	RequestedBy          string
	Title                string
	ParentCheckpointID   string
	IncompleteReasons    []string
	CreatedAt            time.Time
}

type TransactionKind string

const (
	TransactionFileTool     TransactionKind = "file_tool"
	TransactionCommandBatch TransactionKind = "command_batch"
	TransactionGitMutation  TransactionKind = "git_mutation"
	TransactionAgentMerge   TransactionKind = "agent_merge"
	TransactionRewind       TransactionKind = "rewind"
	TransactionUndo         TransactionKind = "undo"
	TransactionRedo         TransactionKind = "redo"
	TransactionFork         TransactionKind = "fork"
)

func (k TransactionKind) Valid() bool {
	switch k {
	case TransactionFileTool, TransactionCommandBatch, TransactionGitMutation,
		TransactionAgentMerge, TransactionRewind, TransactionUndo, TransactionRedo,
		TransactionFork:
		return true
	default:
		return false
	}
}

type TransactionStatus string

const (
	TransactionPrepared    TransactionStatus = "prepared"
	TransactionApplying    TransactionStatus = "applying"
	TransactionCompleted   TransactionStatus = "completed"
	TransactionFailed      TransactionStatus = "failed"
	TransactionInterrupted TransactionStatus = "interrupted"
)

func (s TransactionStatus) Valid() bool {
	switch s {
	case TransactionPrepared, TransactionApplying, TransactionCompleted,
		TransactionFailed, TransactionInterrupted:
		return true
	default:
		return false
	}
}

func (s TransactionStatus) Terminal() bool {
	return s == TransactionCompleted || s == TransactionFailed || s == TransactionInterrupted
}

type Transaction struct {
	ID                          string          `json:"id"`
	ProtocolVersion             string          `json:"protocol_version"`
	OperationKeyDigest          string          `json:"operation_key_digest"`
	RequestFingerprint          string          `json:"request_fingerprint"`
	RunID                       string          `json:"run_id"`
	WorkspaceID                 string          `json:"workspace_id"`
	Kind                        TransactionKind `json:"kind"`
	TriggerReceiptID            string          `json:"trigger_receipt_id"`
	BeforeCheckpointID          string          `json:"before_checkpoint_id"`
	AfterCheckpointID           string          `json:"after_checkpoint_id,omitempty"`
	ExpectedCurrentCheckpointID string          `json:"expected_current_checkpoint_id,omitempty"`
	TargetCheckpointID          string          `json:"target_checkpoint_id,omitempty"`
	// ForkWorkspaceRoot and ForkBranch are durable crash-recovery metadata. They
	// are intentionally excluded from every public JSON projection because the
	// root can contain an operator-private absolute path.
	ForkWorkspaceRoot string            `json:"-"`
	ForkBranch        string            `json:"-"`
	Status            TransactionStatus `json:"status"`
	RecoveryLevel     RecoveryLevel     `json:"recovery_level"`
	ErrorCode         string            `json:"error_code,omitempty"`
	ConflictJSON      string            `json:"conflict_json,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	CompletedAt       *time.Time        `json:"completed_at,omitempty"`
}

type RunState struct {
	RunID               string    `json:"run_id"`
	WorkspaceID         string    `json:"workspace_id"`
	CurrentCheckpointID string    `json:"current_checkpoint_id"`
	LastTransactionID   string    `json:"last_transaction_id,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type StorageUsage struct {
	BlobCount       int   `json:"blob_count"`
	BlobBytes       int64 `json:"blob_bytes"`
	CheckpointCount int   `json:"checkpoint_count"`
}

func (u StorageUsage) Validate() error {
	if u.BlobCount < 0 || u.BlobBytes < 0 || u.CheckpointCount < 0 {
		return errors.New("workspace checkpoint storage usage is invalid")
	}
	return nil
}

func (s RunState) Validate() error {
	if !validIdentity(s.RunID) || !validIdentity(s.WorkspaceID) ||
		!validIdentity(s.CurrentCheckpointID) ||
		(s.LastTransactionID != "" && !validIdentity(s.LastTransactionID)) ||
		s.UpdatedAt.IsZero() {
		return errors.New("workspace checkpoint Run state is invalid")
	}
	return nil
}

func (t Transaction) Validate() error {
	for _, value := range []string{t.ID, t.RunID, t.WorkspaceID, t.TriggerReceiptID,
		t.BeforeCheckpointID} {
		if !validIdentity(value) {
			return errors.New("workspace checkpoint transaction identity is invalid")
		}
	}
	for _, value := range []string{t.AfterCheckpointID, t.ExpectedCurrentCheckpointID,
		t.TargetCheckpointID} {
		if value != "" && !validIdentity(value) {
			return errors.New("workspace checkpoint transaction optional identity is invalid")
		}
	}
	var conflicts []Conflict
	if t.ConflictJSON == "" || json.Unmarshal([]byte(t.ConflictJSON), &conflicts) != nil {
		return errors.New("workspace checkpoint transaction conflicts are invalid")
	}
	if len(conflicts) > MaxConflictItems {
		return errors.New("workspace checkpoint transaction has too many conflicts")
	}
	for _, conflict := range conflicts {
		if err := conflict.Validate(); err != nil {
			return err
		}
	}
	if t.ProtocolVersion != ProtocolVersion || !validDigest(t.OperationKeyDigest, false) ||
		!validDigest(t.RequestFingerprint, false) || !t.Kind.Valid() || !t.Status.Valid() ||
		!t.RecoveryLevel.Valid() || !validReason(t.ErrorCode) || t.CreatedAt.IsZero() ||
		t.UpdatedAt.Before(t.CreatedAt) || len([]byte(t.ConflictJSON)) > 256*1024 ||
		strings.ContainsRune(t.ConflictJSON, 0) {
		return errors.New("workspace checkpoint transaction metadata is invalid")
	}
	if t.Kind == TransactionFork {
		if t.ForkWorkspaceRoot == "" || !filepath.IsAbs(t.ForkWorkspaceRoot) ||
			filepath.Clean(t.ForkWorkspaceRoot) != t.ForkWorkspaceRoot ||
			len([]byte(t.ForkWorkspaceRoot)) > 4096 ||
			strings.TrimSpace(t.ForkWorkspaceRoot) != t.ForkWorkspaceRoot ||
			strings.ContainsRune(t.ForkWorkspaceRoot, 0) || t.ForkBranch == "" ||
			len([]byte(t.ForkBranch)) > 255 ||
			strings.TrimSpace(t.ForkBranch) != t.ForkBranch ||
			strings.ContainsRune(t.ForkBranch, 0) {
			return errors.New("workspace checkpoint fork recovery metadata is invalid")
		}
	} else if t.ForkWorkspaceRoot != "" || t.ForkBranch != "" {
		return errors.New("non-fork workspace checkpoint transaction has fork metadata")
	}
	if t.Status == TransactionCompleted {
		if t.AfterCheckpointID == "" || t.ErrorCode != "" || t.CompletedAt == nil {
			return errors.New("completed workspace checkpoint transaction is inconsistent")
		}
	} else if t.Status == TransactionFailed || t.Status == TransactionInterrupted {
		if t.ErrorCode == "" || t.CompletedAt == nil {
			return errors.New("failed workspace checkpoint transaction is inconsistent")
		}
	} else if t.CompletedAt != nil || t.ErrorCode != "" || t.AfterCheckpointID != "" {
		return errors.New("active workspace checkpoint transaction is inconsistent")
	}
	if t.CompletedAt != nil && t.CompletedAt.Before(t.UpdatedAt) {
		return errors.New("workspace checkpoint transaction completion time is invalid")
	}
	return nil
}

func (r CaptureRequest) Validate() error {
	checkpoint := Checkpoint{ID: r.ID, ProtocolVersion: ProtocolVersion,
		RunID: r.RunID, MissionID: r.MissionID, SessionID: r.SessionID,
		WorkspaceID: r.WorkspaceID, AttemptID: r.AttemptID,
		CapabilityGeneration: r.CapabilityGeneration, Trigger: r.Trigger,
		Phase: r.Phase, TriggerReceiptID: r.TriggerReceiptID,
		RequestedBy: r.RequestedBy, Title: r.Title,
		ParentCheckpointID: r.ParentCheckpointID, RootFingerprint: strings.Repeat("0", 64),
		RootPathSHA256: strings.Repeat("0", 64), BaseCommit: "unborn", Branch: "",
		IndexSHA256: strings.Repeat("0", 64), ManifestSHA256: strings.Repeat("0", 64),
		RecoveryLevel: RecoveryComplete, EntryCount: 0, CreatedAt: r.CreatedAt}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkspaceRoot) == "" || strings.ContainsRune(r.WorkspaceRoot, 0) {
		return errors.New("workspace checkpoint root is invalid")
	}
	if len(r.IncompleteReasons) > 32 {
		return errors.New("workspace checkpoint capture has too many incomplete reasons")
	}
	for _, reason := range r.IncompleteReasons {
		if reason == "" || !validReason(reason) {
			return errors.New("workspace checkpoint capture reason is invalid")
		}
	}
	return nil
}

func validIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len([]rune(value)) <= 256 && !strings.ContainsRune(value, 0)
}

func validPath(value string) bool {
	if value == "" || !utf8.ValidString(value) ||
		len([]rune(value)) > MaxPathRunes || strings.ContainsRune(value, 0) ||
		strings.HasPrefix(value, "/") || strings.ContainsAny(value, `\:`) ||
		path.Clean(value) != value || value == "." || value == ".." ||
		strings.HasPrefix(value, "../") || value == ".git" ||
		strings.HasPrefix(value, ".git/") {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validReason(value string) bool {
	return value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len([]rune(value)) <= 512 && !strings.ContainsRune(value, 0)
}

func validDigest(value string, allowMissing bool) bool {
	if allowMissing && value == "missing" {
		return true
	}
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
