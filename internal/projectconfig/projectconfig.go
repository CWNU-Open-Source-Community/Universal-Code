package projectconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/workspaceidentity"
)

const (
	ProtocolVersion     = "project_config.v1"
	ConfigFileName      = "config.yaml"
	ConfigDirName       = ".prayu"
	MaxConfigBytes      = 64 * 1024
	MaxYAMLNodes        = 4096
	MaxYAMLDepth        = 32
	MaxExcludePaths     = 64
	MaxSkillSuggestions = 16
	MaxPathBytes        = 1024
)

// Config is the v1 project file. Every field is narrowing-only: it can only
// reduce what the operator/process/policy already allows. There is no field
// for credentials, executables, endpoints, prompts, or permission tiers.
type Config struct {
	Protocol         string   `yaml:"protocol"`
	ReadOnly         *bool    `yaml:"read_only,omitempty"`
	AllowedProfiles  []string `yaml:"allowed_profiles,omitempty"`
	Budget           *Budget  `yaml:"budget,omitempty"`
	ExcludePaths     []string `yaml:"exclude_paths,omitempty"`
	SkillSuggestions []string `yaml:"skill_suggestions,omitempty"`
	TestCommandID    string   `yaml:"test_command_id,omitempty"`
	FormatCommandID  string   `yaml:"format_command_id,omitempty"`
}

type Budget struct {
	MaxTurns     int `yaml:"max_turns,omitempty"`
	MaxToolCalls int `yaml:"max_tool_calls,omitempty"`
}

// Effective is the post-narrowing result with per-field provenance.
type Effective struct {
	Protocol         string   `json:"protocol"`
	ReadOnly         bool     `json:"read_only"`
	AllowedProfiles  []string `json:"allowed_profiles,omitempty"`
	MaxTurns         int      `json:"max_turns,omitempty"`
	MaxToolCalls     int      `json:"max_tool_calls,omitempty"`
	ExcludePaths     []string `json:"exclude_paths,omitempty"`
	SkillSuggestions []string `json:"skill_suggestions,omitempty"`
	TestCommandID    string   `json:"test_command_id,omitempty"`
	FormatCommandID  string   `json:"format_command_id,omitempty"`
}

// Rejection names one fail-closed field with its reason.
type Rejection struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Load reads, bounds, and strictly decodes one project config file. Symlinks,
// junctions, reparse points, oversized files, YAML aliases/anchors, excessive
// depth or node counts, unknown fields, and type errors all fail closed.
func Load(ctx context.Context, configPath string) (Config, error) {
	if ctx != nil && ctx.Err() != nil {
		return Config{}, ctx.Err()
	}
	configPath = filepath.Clean(configPath)
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return Config{}, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !sameInstructionPath(absolute, resolved) {
		return Config{}, errors.New("project config path cannot contain symlink or junction indirection")
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("project config stat: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, errors.New("project config must be a regular file without symlink, junction, or reparse-point indirection")
	}
	if info.Size() <= 0 || info.Size() > MaxConfigBytes {
		return Config{}, fmt.Errorf("project config must contain between 1 and %d bytes", MaxConfigBytes)
	}
	file, err := os.Open(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("project config open: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return Config{}, errors.New("project config changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("project config read: %w", err)
	}
	if len(raw) == 0 || len(raw) > MaxConfigBytes {
		return Config{}, fmt.Errorf("project config must contain between 1 and %d bytes", MaxConfigBytes)
	}
	after, statErr := os.Lstat(configPath)
	resolved, resolveErr := filepath.EvalSymlinks(absolute)
	if statErr != nil || resolveErr != nil || !sameInstructionPath(absolute, resolved) ||
		!os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return Config{}, errors.New("project config changed concurrently")
	}
	if !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
		return Config{}, errors.New("project config must be UTF-8 without NUL bytes")
	}
	// Decode into a node tree first so aliases, merge keys, depth, and node
	// counts can be rejected before any field is trusted.
	var root yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&root); err != nil {
		return Config{}, fmt.Errorf("project config YAML: %w", err)
	}
	if err := rejectHostileYAML(&root); err != nil {
		return Config{}, fmt.Errorf("project config: %w", err)
	}
	// Re-decode into the struct with KnownFields so unknown fields, duplicate
	// keys, and type errors fail closed.
	var config Config
	strict := yaml.NewDecoder(strings.NewReader(string(raw)))
	strict.KnownFields(true)
	if err := strict.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("project config strict decode: %w", err)
	}
	if err := strict.Decode(&struct{}{}); err != io.EOF {
		return Config{}, errors.New("project config contains trailing documents")
	}
	if config.Protocol != ProtocolVersion {
		return Config{}, fmt.Errorf("unsupported project config protocol %q", config.Protocol)
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// LoadWorkspace loads <root>/.prayu/config.yaml when it exists. A missing
// config is not an error; callers decide whether one is required.
func LoadWorkspace(ctx context.Context, workspaceRoot string) (Config, bool, error) {
	root, _, _, _, err := instructionBoundary(workspaceRoot, ".")
	if err != nil {
		return Config{}, false, err
	}
	workspaceRoot = root
	configDir := filepath.Join(root, ConfigDirName)
	if _, err := os.Lstat(configDir); errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, nil
	} else if err != nil {
		return Config{}, false, err
	}
	if err := validateInstructionDirectoryChain(root, configDir); err != nil {
		return Config{}, true, err
	}
	configPath := filepath.Join(workspaceRoot, ConfigDirName, ConfigFileName)
	info, err := os.Lstat(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("project config stat: %w", err)
	}
	_ = info
	config, err := Load(ctx, configPath)
	return config, true, err
}

func rejectHostileYAML(root *yaml.Node) error {
	count := 0
	var walk func(node *yaml.Node, depth int) error
	walk = func(node *yaml.Node, depth int) error {
		if depth > MaxYAMLDepth {
			return errors.New("YAML nesting exceeds the depth bound")
		}
		count++
		if count > MaxYAMLNodes {
			return errors.New("YAML node count exceeds the bound")
		}
		if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
			return errors.New("YAML aliases and anchors are forbidden")
		}
		for _, child := range node.Content {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, 0)
}

func (c Config) validate() error {
	if c.ReadOnly == nil {
		c.ReadOnly = boolPtr(false)
	}
	if len(c.AllowedProfiles) > 0 {
		previous := ""
		for _, value := range c.AllowedProfiles {
			profile, err := domain.ParseProfile(value)
			if err != nil || string(profile) != value {
				return fmt.Errorf("invalid allowed profile %q", value)
			}
			if previous != "" && previous >= value {
				return errors.New("allowed_profiles must be unique and sorted")
			}
			previous = value
		}
	}
	if c.Budget != nil {
		if c.Budget.MaxTurns < 0 || c.Budget.MaxToolCalls < 0 {
			return errors.New("project budget values cannot be negative")
		}
		if c.Budget.MaxTurns == 0 && c.Budget.MaxToolCalls == 0 {
			return errors.New("project budget must narrow at least one positive bound")
		}
	}
	if len(c.ExcludePaths) > MaxExcludePaths {
		return fmt.Errorf("exclude_paths exceeds %d entries", MaxExcludePaths)
	}
	for _, value := range c.ExcludePaths {
		if err := validateRelativePath(value); err != nil {
			return fmt.Errorf("exclude path: %w", err)
		}
	}
	if len(c.SkillSuggestions) > MaxSkillSuggestions {
		return fmt.Errorf("skill_suggestions exceeds %d entries", MaxSkillSuggestions)
	}
	for _, value := range c.SkillSuggestions {
		if err := validateSkillRef(value); err != nil {
			return err
		}
	}
	if strings.ContainsAny(c.TestCommandID, "\r\n\t \"'") || len(c.TestCommandID) > 128 {
		return errors.New("test_command_id must be a bounded single token")
	}
	if strings.ContainsAny(c.FormatCommandID, "\r\n\t \"'") || len(c.FormatCommandID) > 128 {
		return errors.New("format_command_id must be a bounded single token")
	}
	return nil
}

func validateRelativePath(value string) error {
	return (workspaceidentity.PathPolicy{ExcludePaths: []string{value}}).Validate()
}

func validateSkillRef(value string) error {
	parts := strings.SplitN(value, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(value) > 128 {
		return fmt.Errorf("invalid skill suggestion %q (want name@version)", value)
	}
	for _, current := range []byte(value) {
		if (current >= 'a' && current <= 'z') || (current >= '0' && current <= '9') ||
			current == '-' || current == '@' || current == '.' {
			continue
		}
		return fmt.Errorf("invalid skill suggestion %q", value)
	}
	return nil
}

// Ceiling is what the operator/process/policy already allows. Project config
// may only narrow it.
type Ceiling struct {
	AllowedProfiles    []string
	MaxTurns           int
	MaxToolCalls       int
	RegisteredCommands map[string]struct{}
}

// Narrow applies the fail-closed merge: every project field that would widen
// the ceiling becomes a named Rejection, and the effective result is returned
// together with all rejections. Callers must fail closed on any rejection.
func (c Config) Narrow(ceiling Ceiling) (Effective, []Rejection, error) {
	if err := c.validate(); err != nil {
		return Effective{}, nil, err
	}
	effective := Effective{Protocol: ProtocolVersion}
	var rejections []Rejection
	if c.ReadOnly != nil && *c.ReadOnly {
		effective.ReadOnly = true
	}
	// allowed_profiles may only shrink the ceiling set and must stay non-empty.
	if len(c.AllowedProfiles) > 0 {
		if len(ceiling.AllowedProfiles) == 0 {
			rejections = append(rejections, Rejection{Field: "allowed_profiles",
				Reason: "no ceiling profile set to narrow"})
		} else {
			allowed := map[string]bool{}
			for _, value := range ceiling.AllowedProfiles {
				allowed[value] = true
			}
			for _, value := range c.AllowedProfiles {
				if !allowed[value] {
					rejections = append(rejections, Rejection{Field: "allowed_profiles",
						Reason: fmt.Sprintf("profile %q is not allowed by the ceiling", value)})
				}
			}
		}
		if len(rejections) == 0 {
			effective.AllowedProfiles = append([]string{}, c.AllowedProfiles...)
		}
	}
	// Budget bounds may only decrease.
	effective.MaxTurns = ceiling.MaxTurns
	effective.MaxToolCalls = ceiling.MaxToolCalls
	if c.Budget != nil {
		if c.Budget.MaxTurns > 0 {
			if ceiling.MaxTurns > 0 && c.Budget.MaxTurns >= ceiling.MaxTurns {
				rejections = append(rejections, Rejection{Field: "budget.max_turns",
					Reason: "project budget must be strictly lower than the ceiling"})
			} else if c.Budget.MaxTurns > ceiling.MaxTurns {
				rejections = append(rejections, Rejection{Field: "budget.max_turns",
					Reason: "project budget exceeds the ceiling"})
			} else {
				effective.MaxTurns = c.Budget.MaxTurns
			}
		}
		if c.Budget.MaxToolCalls > 0 {
			if ceiling.MaxToolCalls > 0 && c.Budget.MaxToolCalls >= ceiling.MaxToolCalls {
				rejections = append(rejections, Rejection{Field: "budget.max_tool_calls",
					Reason: "project budget must be strictly lower than the ceiling"})
			} else if c.Budget.MaxToolCalls > ceiling.MaxToolCalls {
				rejections = append(rejections, Rejection{Field: "budget.max_tool_calls",
					Reason: "project budget exceeds the ceiling"})
			} else {
				effective.MaxToolCalls = c.Budget.MaxToolCalls
			}
		}
	}
	effective.ExcludePaths = append([]string{}, c.ExcludePaths...)
	effective.SkillSuggestions = append([]string{}, c.SkillSuggestions...)
	if c.TestCommandID != "" {
		if _, registered := ceiling.RegisteredCommands[c.TestCommandID]; !registered {
			rejections = append(rejections, Rejection{Field: "test_command_id",
				Reason: "command id is not a registered typed action"})
		} else {
			effective.TestCommandID = c.TestCommandID
		}
	}
	if c.FormatCommandID != "" {
		if _, registered := ceiling.RegisteredCommands[c.FormatCommandID]; !registered {
			rejections = append(rejections, Rejection{Field: "format_command_id",
				Reason: "command id is not a registered typed action"})
		} else {
			effective.FormatCommandID = c.FormatCommandID
		}
	}
	return effective, rejections, nil
}

// Fingerprint is the stable digest of the normalized effective view. Runs pin
// it at creation; later file edits cannot silently change a running Run.
func (e Effective) Fingerprint() string {
	raw, err := json.Marshal(e)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func boolPtr(value bool) *bool { return &value }
