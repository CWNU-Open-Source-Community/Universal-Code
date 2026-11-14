package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	ManifestProtocolVersion = "sandbox_manifest.v1"
	MaxManifestBytes        = 64 * 1024
	MaxCommandArguments     = 128
	MaxCommandArgumentBytes = 4096
	MaxCommandBytes         = 32 * 1024
	MaxMounts               = 32
	MaxEnvironmentBindings  = 64
	MaxNetworkTargets       = 32
	MaxInputArtifacts       = 16
	MaxOutputPaths          = 16
	MaxTimeoutSeconds       = 3600
	MaxCancellationGraceMS  = 30_000
	MaxCPUQuotaMillis       = 8_000
	MinMemoryBytes          = 16 * 1024 * 1024
	MaxMemoryBytes          = 8 * 1024 * 1024 * 1024
	MaxPIDs                 = 512
	MaxCapturedOutputBytes  = 16 * 1024 * 1024
	maxManifestJSONDepth    = 32
)

type Backend string

const (
	BackendNoop   Backend = "noop"
	BackendLocal  Backend = "local"
	BackendDocker Backend = "docker"
)

func (b Backend) Valid() bool {
	return b == BackendNoop || b == BackendLocal || b == BackendDocker
}

type MountAccess string

const (
	MountReadOnly  MountAccess = "read_only"
	MountReadWrite MountAccess = "read_write"
)

func (a MountAccess) Valid() bool {
	return a == MountReadOnly || a == MountReadWrite
}

type EnvironmentSource string

const (
	EnvironmentLiteral   EnvironmentSource = "literal"
	EnvironmentSecretRef EnvironmentSource = "secret_ref"
)

func (s EnvironmentSource) Valid() bool {
	return s == EnvironmentLiteral || s == EnvironmentSecretRef
}

type CommandSpec struct {
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments,omitempty"`
	WorkingDirectory string   `json:"working_directory"`
}

type Mount struct {
	Source string      `json:"source"`
	Target string      `json:"target"`
	Access MountAccess `json:"access"`
}

type NetworkScope struct {
	Mode           string   `json:"mode"`
	AllowedTargets []string `json:"allowed_targets,omitempty"`
}

type ResourceLimits struct {
	CPUQuotaMillis int   `json:"cpu_quota_millis"`
	MemoryBytes    int64 `json:"memory_bytes"`
	PIDs           int   `json:"pids"`
	MaxOutputBytes int64 `json:"max_output_bytes"`
}

type EnvironmentBinding struct {
	Name   string            `json:"name"`
	Source EnvironmentSource `json:"source"`
	Value  string            `json:"value"`
}

type OutputSpec struct {
	CaptureStdout bool     `json:"capture_stdout"`
	CaptureStderr bool     `json:"capture_stderr"`
	Paths         []string `json:"paths,omitempty"`
}

type CancellationSpec struct {
	GracePeriodMillis int `json:"grace_period_millis"`
}

type Manifest struct {
	ProtocolVersion  string               `json:"protocol_version"`
	Backend          Backend              `json:"backend"`
	Command          CommandSpec          `json:"command"`
	Mounts           []Mount              `json:"mounts"`
	Network          NetworkScope         `json:"network"`
	Resources        ResourceLimits       `json:"resources"`
	Environment      []EnvironmentBinding `json:"environment,omitempty"`
	InputArtifactIDs []string             `json:"input_artifact_ids,omitempty"`
	Output           OutputSpec           `json:"output"`
	TimeoutSeconds   int                  `json:"timeout_seconds"`
	Cancellation     CancellationSpec     `json:"cancellation"`
}

var (
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	hostnameLabelPattern   = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	secretNamePattern      = regexp.MustCompile(`(?i)(?:^|_)(?:api_?key|access_?key|token|secret|client_?secret|session_?secret|password|passwd|credential|private_?key|database_?url|connection_?string)(?:_|$)`)
	manifestJSONFields     = map[string]struct{}{
		"protocol_version": {}, "backend": {}, "command": {}, "executable": {},
		"arguments": {}, "working_directory": {}, "mounts": {}, "source": {},
		"target": {}, "access": {}, "network": {}, "mode": {}, "allowed_targets": {},
		"resources": {}, "cpu_quota_millis": {}, "memory_bytes": {}, "pids": {},
		"max_output_bytes": {}, "environment": {}, "name": {}, "value": {},
		"input_artifact_ids": {}, "output": {}, "capture_stdout": {},
		"capture_stderr": {}, "paths": {}, "timeout_seconds": {}, "cancellation": {},
		"grace_period_millis": {},
	}
)

func DecodeManifest(data []byte) (Manifest, error) {
	if len(data) == 0 {
		return Manifest{}, errors.New("sandbox manifest is empty")
	}
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("sandbox manifest exceeds %d bytes", MaxManifestBytes)
	}
	if !utf8.Valid(data) {
		return Manifest{}, errors.New("sandbox manifest must be valid UTF-8")
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return Manifest{}, err
	}
	if err := requireManifestJSONShape(data); err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode sandbox manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("sandbox manifest contains trailing data")
	}
	return NormalizeManifest(manifest)
}

func NormalizeManifest(manifest Manifest) (Manifest, error) {
	manifest.Command.Arguments = append([]string(nil), manifest.Command.Arguments...)
	manifest.Mounts = append([]Mount(nil), manifest.Mounts...)
	manifest.Network.AllowedTargets = append([]string(nil), manifest.Network.AllowedTargets...)
	manifest.Environment = append([]EnvironmentBinding(nil), manifest.Environment...)
	manifest.InputArtifactIDs = append([]string(nil), manifest.InputArtifactIDs...)
	manifest.Output.Paths = append([]string(nil), manifest.Output.Paths...)

	if manifest.ProtocolVersion != ManifestProtocolVersion {
		return Manifest{}, fmt.Errorf("unsupported sandbox manifest protocol %q", manifest.ProtocolVersion)
	}
	if !manifest.Backend.Valid() {
		return Manifest{}, fmt.Errorf("unsupported sandbox backend %q", manifest.Backend)
	}
	if err := validateCommand(manifest.Command); err != nil {
		return Manifest{}, err
	}
	if err := validateMounts(manifest.Mounts, manifest.Command.WorkingDirectory); err != nil {
		return Manifest{}, err
	}
	for index, target := range manifest.Network.AllowedTargets {
		normalized, err := normalizeNetworkTarget(target)
		if err != nil {
			return Manifest{}, fmt.Errorf("network target %d: %w", index+1, err)
		}
		manifest.Network.AllowedTargets[index] = normalized
	}
	sort.Strings(manifest.Network.AllowedTargets)
	if err := validateNetwork(manifest.Network); err != nil {
		return Manifest{}, err
	}
	if err := validateResources(manifest.Resources); err != nil {
		return Manifest{}, err
	}
	if err := validateEnvironment(manifest.Environment); err != nil {
		return Manifest{}, err
	}
	sort.Slice(manifest.Environment, func(i, j int) bool {
		return manifest.Environment[i].Name < manifest.Environment[j].Name
	})
	if err := validateIdentitySet("input artifact", manifest.InputArtifactIDs, MaxInputArtifacts); err != nil {
		return Manifest{}, err
	}
	sort.Strings(manifest.InputArtifactIDs)
	if err := validateOutput(manifest.Output, manifest.Mounts); err != nil {
		return Manifest{}, err
	}
	sort.Strings(manifest.Output.Paths)
	if manifest.TimeoutSeconds < 1 || manifest.TimeoutSeconds > MaxTimeoutSeconds {
		return Manifest{}, fmt.Errorf("sandbox timeout must be between 1 and %d seconds", MaxTimeoutSeconds)
	}
	if manifest.Cancellation.GracePeriodMillis < 0 ||
		manifest.Cancellation.GracePeriodMillis > MaxCancellationGraceMS {
		return Manifest{}, fmt.Errorf("cancellation grace period must be between 0 and %d milliseconds", MaxCancellationGraceMS)
	}
	sort.Slice(manifest.Mounts, func(i, j int) bool {
		if manifest.Mounts[i].Target != manifest.Mounts[j].Target {
			return manifest.Mounts[i].Target < manifest.Mounts[j].Target
		}
		if manifest.Mounts[i].Source != manifest.Mounts[j].Source {
			return manifest.Mounts[i].Source < manifest.Mounts[j].Source
		}
		return manifest.Mounts[i].Access < manifest.Mounts[j].Access
	})
	return manifest, nil
}

func (m Manifest) CanonicalJSON() ([]byte, error) {
	normalized, err := NormalizeManifest(m)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func (m Manifest) Fingerprint() (string, error) {
	canonical, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return fingerprint(ManifestProtocolVersion, string(canonical)), nil
}

func (m Manifest) OutputCount() int {
	count := len(m.Output.Paths)
	if m.Output.CaptureStdout {
		count++
	}
	if m.Output.CaptureStderr {
		count++
	}
	return count
}

func (m Manifest) SecretReferenceCount() int {
	count := 0
	for _, binding := range m.Environment {
		if binding.Source == EnvironmentSecretRef {
			count++
		}
	}
	return count
}

func (m Manifest) HasWritableMount() bool {
	return m.WritableMountCount() > 0
}

func (m Manifest) WritableMountCount() int {
	count := 0
	for _, mount := range m.Mounts {
		if mount.Access == MountReadWrite {
			count++
		}
	}
	return count
}

func normalizeNetworkTarget(value string) (string, error) {
	if err := validateBoundedText("network target", value, 512, false); err != nil {
		return "", err
	}
	if strings.ContainsAny(value, "*\\/?#@") || strings.Contains(value, "://") {
		return "", errors.New("network target must be an exact host, IP, host:port, or CIDR")
	}
	if ip, network, err := net.ParseCIDR(value); err == nil {
		if !ip.Equal(network.IP) {
			return "", errors.New("CIDR target must use its canonical network address")
		}
		return network.String(), nil
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String(), nil
	}
	host := value
	port := ""
	if parsedHost, parsedPort, err := net.SplitHostPort(value); err == nil {
		host, port = parsedHost, parsedPort
		if host == "" || port == "" {
			return "", errors.New("network target host and port are required")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("network target port is invalid")
		}
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			return net.JoinHostPort(ip.String(), port), nil
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) == 0 || len(host) > 253 {
		return "", errors.New("network target hostname is invalid")
	}
	for _, label := range strings.Split(host, ".") {
		if !hostnameLabelPattern.MatchString(label) {
			return "", errors.New("network target hostname is invalid")
		}
	}
	if port != "" {
		return net.JoinHostPort(host, port), nil
	}
	return host, nil
}

func NormalizeAllowedTarget(value string) (string, error) {
	return normalizeNetworkTarget(value)
}

func validateCommand(command CommandSpec) error {
	if err := validateBoundedText("command executable", command.Executable, 1024, false); err != nil {
		return err
	}
	if strings.IndexFunc(command.Executable, unicode.IsSpace) >= 0 {
		return errors.New("command executable cannot contain whitespace")
	}
	if err := validateExactVirtualPath("command working directory", command.WorkingDirectory); err != nil {
		return err
	}
	if len(command.Arguments) > MaxCommandArguments {
		return fmt.Errorf("command has more than %d arguments", MaxCommandArguments)
	}
	total := len(command.Executable)
	for index, argument := range command.Arguments {
		if err := validateLiteralText(fmt.Sprintf("command argument %d", index+1), argument,
			MaxCommandArgumentBytes, true); err != nil {
			return err
		}
		if redact.String(argument) != argument {
			return fmt.Errorf("command argument %d contains credential-like material; use a secret_ref environment binding", index+1)
		}
		total += len(argument)
	}
	if total > MaxCommandBytes {
		return fmt.Errorf("command and arguments exceed %d bytes", MaxCommandBytes)
	}
	return nil
}

func validateMounts(mounts []Mount, workingDirectory string) error {
	if len(mounts) == 0 || len(mounts) > MaxMounts {
		return fmt.Errorf("sandbox manifest requires between 1 and %d mounts", MaxMounts)
	}
	workdirCovered := false
	for index, mount := range mounts {
		if err := validateWorkspacePath(fmt.Sprintf("mount %d source", index+1), mount.Source); err != nil {
			return err
		}
		if err := validateVirtualPath(fmt.Sprintf("mount %d target", index+1), mount.Target); err != nil {
			return err
		}
		if !mount.Access.Valid() {
			return fmt.Errorf("mount %d has unsupported access %q", index+1, mount.Access)
		}
		if pathWithin(workingDirectory, mount.Target) {
			workdirCovered = true
		}
		for previous := 0; previous < index; previous++ {
			other := mounts[previous]
			if pathWithin(mount.Target, other.Target) || pathWithin(other.Target, mount.Target) {
				return errors.New("sandbox mount targets cannot overlap")
			}
			if (mount.Access == MountReadWrite || other.Access == MountReadWrite) &&
				(workspacePathWithin(mount.Source, other.Source) || workspacePathWithin(other.Source, mount.Source)) {
				return errors.New("writable workspace mount sources cannot overlap")
			}
		}
	}
	if !workdirCovered {
		return errors.New("command working directory must be covered by a sandbox mount")
	}
	return nil
}

func validateNetwork(scope NetworkScope) error {
	switch scope.Mode {
	case "disabled":
		if len(scope.AllowedTargets) != 0 {
			return errors.New("disabled network scope cannot contain allowed targets")
		}
	case "allowlist":
		if len(scope.AllowedTargets) == 0 || len(scope.AllowedTargets) > MaxNetworkTargets {
			return fmt.Errorf("allowlist network scope requires between 1 and %d targets", MaxNetworkTargets)
		}
	default:
		return fmt.Errorf("unsupported sandbox network mode %q", scope.Mode)
	}
	return rejectAdjacentDuplicates("network target", scope.AllowedTargets)
}

func validateResources(resources ResourceLimits) error {
	if resources.CPUQuotaMillis < 1 || resources.CPUQuotaMillis > MaxCPUQuotaMillis {
		return fmt.Errorf("CPU quota must be between 1 and %d milliseconds", MaxCPUQuotaMillis)
	}
	if resources.MemoryBytes < MinMemoryBytes || resources.MemoryBytes > MaxMemoryBytes {
		return fmt.Errorf("memory limit must be between %d and %d bytes", MinMemoryBytes, int64(MaxMemoryBytes))
	}
	if resources.PIDs < 1 || resources.PIDs > MaxPIDs {
		return fmt.Errorf("PID limit must be between 1 and %d", MaxPIDs)
	}
	if resources.MaxOutputBytes < 1 || resources.MaxOutputBytes > MaxCapturedOutputBytes {
		return fmt.Errorf("captured output limit must be between 1 and %d bytes", MaxCapturedOutputBytes)
	}
	return nil
}

func validateEnvironment(bindings []EnvironmentBinding) error {
	if len(bindings) > MaxEnvironmentBindings {
		return fmt.Errorf("sandbox environment has more than %d bindings", MaxEnvironmentBindings)
	}
	seen := make(map[string]struct{}, len(bindings))
	for index, binding := range bindings {
		if !environmentNamePattern.MatchString(binding.Name) {
			return fmt.Errorf("environment binding %d name is invalid", index+1)
		}
		if _, exists := seen[binding.Name]; exists {
			return fmt.Errorf("environment variable %q is duplicated", binding.Name)
		}
		seen[binding.Name] = struct{}{}
		if !binding.Source.Valid() {
			return fmt.Errorf("environment binding %q has unsupported source %q", binding.Name, binding.Source)
		}
		if binding.Source == EnvironmentLiteral {
			if secretNamePattern.MatchString(binding.Name) {
				return fmt.Errorf("environment binding %q must use secret_ref instead of a literal", binding.Name)
			}
			if err := validateBoundedText("environment literal", binding.Value, 4096, true); err != nil {
				return err
			}
			if redact.String(binding.Value) != binding.Value {
				return fmt.Errorf("environment binding %q contains credential-like material and must use secret_ref", binding.Name)
			}
		} else if err := validateIdentity("environment secret reference", binding.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateOutput(output OutputSpec, mounts []Mount) error {
	if len(output.Paths) > MaxOutputPaths {
		return fmt.Errorf("sandbox output has more than %d paths", MaxOutputPaths)
	}
	if !output.CaptureStdout && !output.CaptureStderr && len(output.Paths) == 0 {
		return errors.New("sandbox manifest must capture at least one output")
	}
	seen := make(map[string]struct{}, len(output.Paths))
	for index, outputPath := range output.Paths {
		if err := validateVirtualPath(fmt.Sprintf("output path %d", index+1), outputPath); err != nil {
			return err
		}
		if _, exists := seen[outputPath]; exists {
			return fmt.Errorf("output path %q is duplicated", outputPath)
		}
		seen[outputPath] = struct{}{}
		covered := false
		for _, mount := range mounts {
			if mount.Access == MountReadWrite && outputPath != mount.Target &&
				pathWithin(outputPath, mount.Target) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("output path %q must be covered by a read-write mount", outputPath)
		}
	}
	return nil
}

func validateIdentitySet(label string, values []string, maximum int) error {
	if len(values) > maximum {
		return fmt.Errorf("sandbox manifest has more than %d %ss", maximum, label)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentity(label, value); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s %q is duplicated", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateIdentity(label, value string) error {
	if err := validateBoundedText(label, value, 256, false); err != nil {
		return err
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%s cannot contain whitespace", label)
	}
	return nil
}

func validateWorkspacePath(label, value string) error {
	if err := validateBoundedText(label, value, 1024, false); err != nil {
		return err
	}
	if strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || path.Clean(value) != value ||
		value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("%s must be a clean workspace-relative POSIX path", label)
	}
	return nil
}

func validateVirtualPath(label, value string) error {
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a clean absolute sandbox path below root", label)
	}
	return validateExactVirtualPath(label, value)
}

func validateExactVirtualPath(label, value string) error {
	if err := validateLiteralText(label, value, 1024, false); err != nil {
		return err
	}
	if !strings.HasPrefix(value, "/") || value == "/" || strings.Contains(value, `\`) || path.Clean(value) != value {
		return fmt.Errorf("%s must be a clean absolute sandbox path below root", label)
	}
	return nil
}

func validateBoundedText(label, value string, maxBytes int, allowEmpty bool) error {
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be normalized UTF-8 of at most %d bytes", label, maxBytes)
	}
	return validateLiteralText(label, value, maxBytes, allowEmpty)
}

func validateLiteralText(label, value string, maxBytes int, allowEmpty bool) error {
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return fmt.Errorf("%s must be valid UTF-8 of at most %d bytes", label, maxBytes)
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is required", label)
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return fmt.Errorf("%s cannot contain control characters", label)
		}
	}
	return nil
}

func pathWithin(candidate, parent string) bool {
	return candidate == parent || strings.HasPrefix(candidate, parent+"/")
}

func workspacePathWithin(candidate, parent string) bool {
	if parent == "." {
		return true
	}
	return candidate == parent || strings.HasPrefix(candidate, parent+"/")
}

func rejectAdjacentDuplicates(label string, values []string) error {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return fmt.Errorf("%s %q is duplicated", label, values[index])
		}
	}
	return nil
}

func fingerprint(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		value := []byte(part)
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write(value)
		_, _ = hash.Write([]byte{'|'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func requireManifestJSONShape(data []byte) error {
	top, err := requireJSONObject(data, "sandbox manifest",
		"protocol_version", "backend", "command", "mounts", "network", "resources",
		"output", "timeout_seconds", "cancellation")
	if err != nil {
		return err
	}
	command, err := requireJSONObject(top["command"], "sandbox command",
		"executable", "working_directory")
	if err != nil {
		return err
	}
	if raw, ok := command["arguments"]; ok {
		if err := requireJSONArray(raw, "sandbox command arguments", nil); err != nil {
			return err
		}
	}
	if err := requireJSONArray(top["mounts"], "sandbox mounts", []string{
		"source", "target", "access",
	}); err != nil {
		return err
	}
	network, err := requireJSONObject(top["network"], "sandbox network", "mode")
	if err != nil {
		return err
	}
	if raw, ok := network["allowed_targets"]; ok {
		if err := requireJSONArray(raw, "sandbox network targets", nil); err != nil {
			return err
		}
	}
	if _, err := requireJSONObject(top["resources"], "sandbox resources",
		"cpu_quota_millis", "memory_bytes", "pids", "max_output_bytes"); err != nil {
		return err
	}
	if raw, ok := top["environment"]; ok {
		if err := requireJSONArray(raw, "sandbox environment", []string{
			"name", "source", "value",
		}); err != nil {
			return err
		}
	}
	if raw, ok := top["input_artifact_ids"]; ok {
		if err := requireJSONArray(raw, "sandbox input artifacts", nil); err != nil {
			return err
		}
	}
	output, err := requireJSONObject(top["output"], "sandbox output",
		"capture_stdout", "capture_stderr")
	if err != nil {
		return err
	}
	if raw, ok := output["paths"]; ok {
		if err := requireJSONArray(raw, "sandbox output paths", nil); err != nil {
			return err
		}
	}
	if _, err := requireJSONObject(top["cancellation"], "sandbox cancellation",
		"grace_period_millis"); err != nil {
		return err
	}
	return nil
}

func requireJSONObject(raw []byte, label string, required ...string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	for _, field := range required {
		if _, ok := object[field]; !ok {
			return nil, fmt.Errorf("%s requires field %q", label, field)
		}
	}
	return object, nil
}

func requireJSONArray(raw []byte, label string, objectFields []string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return fmt.Errorf("%s must be a JSON array", label)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return fmt.Errorf("decode %s: %w", label, err)
	}
	if objectFields == nil {
		return nil
	}
	for index, value := range values {
		if _, err := requireJSONObject(value, fmt.Sprintf("%s item %d", label, index+1),
			objectFields...); err != nil {
			return err
		}
	}
	return nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walkValue func(int) error
	walkValue = func(depth int) error {
		if depth > maxManifestJSONDepth {
			return fmt.Errorf("sandbox manifest JSON exceeds depth %d", maxManifestJSONDepth)
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				fieldToken, err := decoder.Token()
				if err != nil {
					return err
				}
				field, ok := fieldToken.(string)
				if !ok {
					return errors.New("sandbox manifest object field name is invalid")
				}
				if _, exists := seen[field]; exists {
					return fmt.Errorf("sandbox manifest contains duplicate field %q", field)
				}
				if _, known := manifestJSONFields[field]; !known {
					return fmt.Errorf("sandbox manifest contains unknown field %q", field)
				}
				seen[field] = struct{}{}
				if err := walkValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("sandbox manifest object is not closed")
			}
		case '[':
			for decoder.More() {
				if err := walkValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("sandbox manifest array is not closed")
			}
		default:
			return errors.New("sandbox manifest contains an unexpected delimiter")
		}
		return nil
	}
	if err := walkValue(1); err != nil {
		return fmt.Errorf("inspect sandbox manifest JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("sandbox manifest contains trailing data")
	}
	return nil
}
