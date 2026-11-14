package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ReadOnlyFanoutProtocolVersion         = "readonly_fanout.v1"
	ReadOnlyFanoutCapabilityFingerprintV1 = "735ca1ca0e0cdf09773b15aa7113e328744c6fde267f469c3f414648baf9e47b"
	MaxReadOnlyFanoutParallelism          = 6
	MaxReadOnlyFanoutFiles                = 256
	MaxReadOnlyFanoutFileBytes            = 128 * 1024
	MaxReadOnlyFanoutTotalBytes           = MaxReadOnlyFanoutParallelism * MaxReadOnlyFanoutFileBytes
	MaxReadOnlyFanoutWalkEntries          = 20_000
	MaxReadOnlyFanoutGoalRunes            = 4096
	MaxReadOnlyFanoutPathBytes            = 2048
)

type ReadOnlyFanoutTier string

const (
	ReadOnlyFanoutAuto ReadOnlyFanoutTier = "auto"
	ReadOnlyFanoutOne  ReadOnlyFanoutTier = "1"
	ReadOnlyFanoutTwo  ReadOnlyFanoutTier = "2"
	ReadOnlyFanoutFour ReadOnlyFanoutTier = "4"
	ReadOnlyFanoutSix  ReadOnlyFanoutTier = "6"
)

func ParseReadOnlyFanoutTier(value string) (ReadOnlyFanoutTier, error) {
	tier := ReadOnlyFanoutTier(strings.ToLower(strings.TrimSpace(value)))
	switch tier {
	case ReadOnlyFanoutAuto, ReadOnlyFanoutOne, ReadOnlyFanoutTwo,
		ReadOnlyFanoutFour, ReadOnlyFanoutSix:
		return tier, nil
	default:
		return "", fmt.Errorf("read-only fan-out tier must be auto, 1, 2, 4, or 6")
	}
}

func ResolveReadOnlyFanoutParallelism(tier ReadOnlyFanoutTier, fileCount int) (int, error) {
	parsed, err := ParseReadOnlyFanoutTier(string(tier))
	if err != nil {
		return 0, err
	}
	if fileCount <= 0 || fileCount > MaxReadOnlyFanoutFiles {
		return 0, fmt.Errorf("read-only fan-out file count must be between 1 and %d",
			MaxReadOnlyFanoutFiles)
	}
	requested := 0
	switch parsed {
	case ReadOnlyFanoutOne:
		requested = 1
	case ReadOnlyFanoutTwo:
		requested = 2
	case ReadOnlyFanoutFour:
		requested = 4
	case ReadOnlyFanoutSix:
		requested = 6
	case ReadOnlyFanoutAuto:
		switch {
		case fileCount == 1:
			requested = 1
		case fileCount <= 8:
			requested = 2
		case fileCount <= 32:
			requested = 4
		default:
			requested = 6
		}
	}
	return min(requested, fileCount), nil
}

type ReadOnlyFanoutCapabilities struct {
	WorkspaceList bool `json:"workspace_list"`
	WorkspaceRead bool `json:"workspace_read"`
	Shell         bool `json:"shell"`
	FileWrite     bool `json:"file_write"`
	Process       bool `json:"process"`
	Network       bool `json:"network"`
	ExternalTools bool `json:"external_tools"`
	ChildSpawn    bool `json:"child_spawn"`
}

func DefaultReadOnlyFanoutCapabilities() ReadOnlyFanoutCapabilities {
	return ReadOnlyFanoutCapabilities{WorkspaceList: true, WorkspaceRead: true}
}

func (c ReadOnlyFanoutCapabilities) Validate() error {
	if !c.WorkspaceList || !c.WorkspaceRead || c.Shell || c.FileWrite || c.Process ||
		c.Network || c.ExternalTools || c.ChildSpawn {
		return errors.New("read-only fan-out capabilities must allow only workspace list/read")
	}
	return nil
}

func ReadOnlyFanoutCapabilityFingerprint(c ReadOnlyFanoutCapabilities) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("readonly_fanout_capabilities.v1\x00"), encoded...))
	fingerprint := hex.EncodeToString(digest[:])
	if fingerprint != ReadOnlyFanoutCapabilityFingerprintV1 {
		return "", errors.New("read-only fan-out capability definition drifted from v1")
	}
	return fingerprint, nil
}

type ReadOnlyFanoutStatus string

const ReadOnlyFanoutPlanned ReadOnlyFanoutStatus = "planned"

type ReadOnlyFanoutShardStatus string

const ReadOnlyFanoutShardPending ReadOnlyFanoutShardStatus = "pending"

type ReadOnlyFanoutFile struct {
	PlanID        string
	Ordinal       int
	ShardOrdinal  int
	RelativePath  string
	SizeBytes     int64
	ContentSHA256 string
}

func (f ReadOnlyFanoutFile) Validate() error {
	if !validAgentIdentity(f.PlanID, false) || f.Ordinal <= 0 ||
		f.Ordinal > MaxReadOnlyFanoutFiles || f.ShardOrdinal <= 0 ||
		f.ShardOrdinal > MaxReadOnlyFanoutParallelism ||
		!validReadOnlyFanoutPath(f.RelativePath) || f.SizeBytes < 0 ||
		f.SizeBytes > MaxReadOnlyFanoutFileBytes || !validLowerHexDigest(f.ContentSHA256) {
		return errors.New("read-only fan-out file is invalid")
	}
	return nil
}

type ReadOnlyFanoutShard struct {
	PlanID      string
	Ordinal     int
	Status      ReadOnlyFanoutShardStatus
	FileCount   int
	TotalBytes  int64
	InputDigest string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s ReadOnlyFanoutShard) Validate() error {
	if !validAgentIdentity(s.PlanID, false) || s.Ordinal <= 0 ||
		s.Ordinal > MaxReadOnlyFanoutParallelism ||
		s.Status != ReadOnlyFanoutShardPending || s.FileCount <= 0 ||
		s.FileCount > MaxReadOnlyFanoutFiles || s.TotalBytes < 0 ||
		s.TotalBytes > MaxReadOnlyFanoutTotalBytes || !validLowerHexDigest(s.InputDigest) ||
		s.Version != 1 || s.CreatedAt.IsZero() || !s.UpdatedAt.Equal(s.CreatedAt) {
		return errors.New("read-only fan-out shard is invalid")
	}
	return nil
}

type ReadOnlyFanoutPlan struct {
	ID                    string
	RunID                 string
	WorkspaceID           string
	ScopePath             string
	Goal                  string
	ProtocolVersion       string
	RequestedTier         ReadOnlyFanoutTier
	EffectiveParallelism  int
	Status                ReadOnlyFanoutStatus
	CapabilityFingerprint string
	SnapshotDigest        string
	FileCount             int
	TotalBytes            int64
	ExcludedCount         int
	ShardCount            int
	RequestedBy           string
	Version               int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Files                 []ReadOnlyFanoutFile
	Shards                []ReadOnlyFanoutShard
}

type ReadOnlyFanoutPlanSummary struct {
	ID                   string
	RunID                string
	WorkspaceID          string
	ScopePath            string
	Goal                 string
	ProtocolVersion      string
	RequestedTier        ReadOnlyFanoutTier
	EffectiveParallelism int
	Status               ReadOnlyFanoutStatus
	FileCount            int
	TotalBytes           int64
	ExcludedCount        int
	ShardCount           int
	RequestedBy          string
	Version              int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (p ReadOnlyFanoutPlanSummary) Validate() error {
	for _, value := range []string{p.ID, p.RunID, p.WorkspaceID, p.RequestedBy} {
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) {
			return errors.New("read-only fan-out plan summary identities are invalid")
		}
	}
	tier, err := ParseReadOnlyFanoutTier(string(p.RequestedTier))
	if err != nil || tier != p.RequestedTier || !validReadOnlyFanoutScope(p.ScopePath) ||
		!validReadOnlyFanoutGoal(p.Goal) || p.ProtocolVersion != ReadOnlyFanoutProtocolVersion ||
		p.Status != ReadOnlyFanoutPlanned || p.FileCount <= 0 ||
		p.FileCount > MaxReadOnlyFanoutFiles || p.TotalBytes < 0 ||
		p.TotalBytes > MaxReadOnlyFanoutTotalBytes || p.ExcludedCount < 0 ||
		p.ExcludedCount > MaxReadOnlyFanoutWalkEntries || p.ShardCount <= 0 ||
		p.ShardCount > MaxReadOnlyFanoutParallelism ||
		p.EffectiveParallelism != p.ShardCount || p.Version != 1 ||
		p.CreatedAt.IsZero() || !p.UpdatedAt.Equal(p.CreatedAt) {
		return errors.New("read-only fan-out plan summary metadata is invalid")
	}
	resolved, err := ResolveReadOnlyFanoutParallelism(p.RequestedTier, p.FileCount)
	if err != nil || resolved != p.EffectiveParallelism {
		return errors.New("read-only fan-out plan summary parallelism is invalid")
	}
	return nil
}

func (p ReadOnlyFanoutPlan) Summary() ReadOnlyFanoutPlanSummary {
	return ReadOnlyFanoutPlanSummary{
		ID: p.ID, RunID: p.RunID, WorkspaceID: p.WorkspaceID,
		ScopePath: p.ScopePath, Goal: p.Goal, ProtocolVersion: p.ProtocolVersion,
		RequestedTier: p.RequestedTier, EffectiveParallelism: p.EffectiveParallelism,
		Status: p.Status, FileCount: p.FileCount, TotalBytes: p.TotalBytes,
		ExcludedCount: p.ExcludedCount, ShardCount: p.ShardCount,
		RequestedBy: p.RequestedBy, Version: p.Version,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (p ReadOnlyFanoutPlan) Validate() error {
	for _, value := range []string{p.ID, p.RunID, p.WorkspaceID, p.RequestedBy} {
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) {
			return errors.New("read-only fan-out plan identities are invalid")
		}
	}
	if !validReadOnlyFanoutScope(p.ScopePath) || !validReadOnlyFanoutGoal(p.Goal) ||
		p.ProtocolVersion != ReadOnlyFanoutProtocolVersion ||
		p.Status != ReadOnlyFanoutPlanned || p.Version != 1 ||
		p.CreatedAt.IsZero() || !p.UpdatedAt.Equal(p.CreatedAt) {
		return errors.New("read-only fan-out plan metadata is invalid")
	}
	tier, err := ParseReadOnlyFanoutTier(string(p.RequestedTier))
	if err != nil || tier != p.RequestedTier {
		return errors.New("read-only fan-out requested tier is invalid")
	}
	if p.FileCount <= 0 || p.FileCount > MaxReadOnlyFanoutFiles ||
		len(p.Files) != p.FileCount || p.TotalBytes < 0 ||
		p.TotalBytes > MaxReadOnlyFanoutTotalBytes || p.ExcludedCount < 0 ||
		p.ExcludedCount > MaxReadOnlyFanoutWalkEntries || p.ShardCount <= 0 ||
		p.ShardCount > MaxReadOnlyFanoutParallelism || len(p.Shards) != p.ShardCount ||
		p.EffectiveParallelism != p.ShardCount {
		return errors.New("read-only fan-out plan bounds are invalid")
	}
	resolved, err := ResolveReadOnlyFanoutParallelism(p.RequestedTier, p.FileCount)
	if err != nil || resolved != p.EffectiveParallelism {
		return errors.New("read-only fan-out effective parallelism is invalid")
	}
	expectedCapability, err := ReadOnlyFanoutCapabilityFingerprint(
		DefaultReadOnlyFanoutCapabilities())
	if err != nil || p.CapabilityFingerprint != expectedCapability {
		return errors.New("read-only fan-out capability fingerprint is invalid")
	}
	seenPaths := make(map[string]struct{}, len(p.Files))
	var totalBytes int64
	for index, file := range p.Files {
		if err := file.Validate(); err != nil {
			return err
		}
		if file.PlanID != p.ID || file.Ordinal != index+1 ||
			file.ShardOrdinal > p.ShardCount {
			return errors.New("read-only fan-out files are not contiguous or bound")
		}
		if _, exists := seenPaths[file.RelativePath]; exists {
			return errors.New("read-only fan-out file paths must be unique")
		}
		seenPaths[file.RelativePath] = struct{}{}
		totalBytes += file.SizeBytes
	}
	if totalBytes != p.TotalBytes {
		return errors.New("read-only fan-out byte total is inconsistent")
	}
	for index, shard := range p.Shards {
		if err := shard.Validate(); err != nil {
			return err
		}
		if shard.PlanID != p.ID || shard.Ordinal != index+1 {
			return errors.New("read-only fan-out shards are not contiguous or bound")
		}
		count, bytes := 0, int64(0)
		for _, file := range p.Files {
			if file.ShardOrdinal == shard.Ordinal {
				count++
				bytes += file.SizeBytes
			}
		}
		digest, err := ReadOnlyFanoutShardDigest(shard.Ordinal, p.Files)
		if err != nil || count != shard.FileCount || bytes != shard.TotalBytes ||
			digest != shard.InputDigest {
			return errors.New("read-only fan-out shard projection is inconsistent")
		}
	}
	digest, err := ReadOnlyFanoutSnapshotDigest(p.Files)
	if err != nil || digest != p.SnapshotDigest {
		return errors.New("read-only fan-out snapshot digest is inconsistent")
	}
	return nil
}

type ReadOnlyFanoutOperation struct {
	KeyDigest          string
	RequestFingerprint string
	PlanID             string
	RunID              string
	WorkspaceID        string
	RequestedBy        string
	CreatedAt          time.Time
}

func (o ReadOnlyFanoutOperation) Validate() error {
	for _, value := range []string{o.PlanID, o.RunID, o.WorkspaceID, o.RequestedBy} {
		if !validAgentIdentity(value, false) || strings.ContainsRune(value, 0) {
			return errors.New("read-only fan-out operation identities are invalid")
		}
	}
	if !validLowerHexDigest(o.KeyDigest) || !validLowerHexDigest(o.RequestFingerprint) ||
		o.CreatedAt.IsZero() {
		return errors.New("read-only fan-out operation is invalid")
	}
	return nil
}

func ReadOnlyFanoutSnapshotDigest(files []ReadOnlyFanoutFile) (string, error) {
	if len(files) == 0 || len(files) > MaxReadOnlyFanoutFiles {
		return "", errors.New("read-only fan-out snapshot files are required")
	}
	copyFiles := slices.Clone(files)
	slices.SortFunc(copyFiles, func(left, right ReadOnlyFanoutFile) int {
		return left.Ordinal - right.Ordinal
	})
	h := sha256.New()
	writeFanoutDigestPart(h, "readonly_fanout_snapshot.v1")
	for index, file := range copyFiles {
		if err := file.Validate(); err != nil || file.Ordinal != index+1 {
			return "", errors.New("read-only fan-out snapshot files must be valid and contiguous")
		}
		writeFanoutDigestPart(h, strconv.Itoa(file.Ordinal))
		writeFanoutDigestPart(h, file.RelativePath)
		writeFanoutDigestPart(h, strconv.FormatInt(file.SizeBytes, 10))
		writeFanoutDigestPart(h, file.ContentSHA256)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ReadOnlyFanoutShardDigest(ordinal int, files []ReadOnlyFanoutFile) (string, error) {
	if ordinal <= 0 || ordinal > MaxReadOnlyFanoutParallelism {
		return "", errors.New("read-only fan-out shard ordinal is invalid")
	}
	h := sha256.New()
	writeFanoutDigestPart(h, "readonly_fanout_shard.v1")
	writeFanoutDigestPart(h, strconv.Itoa(ordinal))
	count := 0
	for _, file := range files {
		if file.ShardOrdinal != ordinal {
			continue
		}
		if err := file.Validate(); err != nil {
			return "", err
		}
		writeFanoutDigestPart(h, strconv.Itoa(file.Ordinal))
		writeFanoutDigestPart(h, file.RelativePath)
		writeFanoutDigestPart(h, file.ContentSHA256)
		count++
	}
	if count == 0 {
		return "", errors.New("read-only fan-out shard cannot be empty")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeFanoutDigestPart(h hash.Hash, value string) {
	_, _ = h.Write([]byte(strconv.Itoa(len(value))))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}

func validReadOnlyFanoutGoal(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= MaxReadOnlyFanoutGoalRunes &&
		len([]byte(value)) <= 16*1024
}

func validReadOnlyFanoutScope(value string) bool {
	if value == "." {
		return true
	}
	return validReadOnlyFanoutPath(value)
}

func validReadOnlyFanoutPath(value string) bool {
	if value == "" || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) || strings.Contains(value, "\\") ||
		len([]byte(value)) > MaxReadOnlyFanoutPathBytes || strings.HasPrefix(value, "/") ||
		path.Clean(value) != value || value == "." || value == ".." ||
		strings.HasPrefix(value, "../") || strings.Contains(value, "/../") {
		return false
	}
	return true
}
