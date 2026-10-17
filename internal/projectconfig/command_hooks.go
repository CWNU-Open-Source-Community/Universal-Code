package projectconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// CommandHooks selects registered project actions. A declaration is inert until
// the operator trusts its definition; ordinary execution permissions still apply.
type CommandHooks struct {
	AfterEditFormat  bool `yaml:"after_edit_format,omitempty" json:"after_edit_format,omitempty"`
	BeforeFinishTest bool `yaml:"before_finish_test,omitempty" json:"before_finish_test,omitempty"`
}

func validateCommandHooks(hooks *CommandHooks, testID, formatID string) error {
	if hooks == nil {
		return nil
	}
	if hooks.AfterEditFormat && formatID != "format.go" {
		return errors.New("after_edit_format requires format_command_id: format.go")
	}
	if hooks.BeforeFinishTest && testID != "test.go" && testID != "test.node" && testID != "test.npm" {
		return errors.New("before_finish_test requires test_command_id: test.go, test.node or test.npm")
	}
	return nil
}

// HooksFingerprint excludes task-specific budgets. The same reviewed definition
// remains usable in later tasks, while changed commands or path restrictions
// require another review. It never grants execution authority.
func (e *Effective) HooksFingerprint() string {
	if e == nil || e.Hooks == nil || (!e.Hooks.AfterEditFormat && !e.Hooks.BeforeFinishTest) {
		return ""
	}
	if validateCommandHooks(e.Hooks, e.TestCommandID, e.FormatCommandID) != nil {
		return ""
	}
	definition := struct {
		Version         string       `json:"version"`
		Hooks           CommandHooks `json:"hooks"`
		TestCommandID   string       `json:"test_command_id,omitempty"`
		FormatCommandID string       `json:"format_command_id,omitempty"`
		ReadOnly        bool         `json:"read_only"`
		ExcludePaths    []string     `json:"exclude_paths,omitempty"`
	}{Version: "project-hooks.v1", Hooks: *e.Hooks, ReadOnly: e.ReadOnly, ExcludePaths: e.ExcludePaths}
	if e.Hooks.BeforeFinishTest {
		definition.TestCommandID = e.TestCommandID
	}
	if e.Hooks.AfterEditFormat {
		definition.FormatCommandID = e.FormatCommandID
	}
	raw, _ := json.Marshal(definition)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
