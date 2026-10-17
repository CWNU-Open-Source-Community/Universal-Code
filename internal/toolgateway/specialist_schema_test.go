package toolgateway

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestBatchSpecialistPublishedMutationContract(t *testing.T) {
	before := AgentCodeToolDefinitions()
	batch := map[ToolName]ToolDefinition{}
	for _, definition := range SpecialistNativeToolDefinitions(true) {
		batch[definition.Name] = definition
	}
	for _, definition := range SpecialistNativeToolDefinitions(false) {
		if definition.Name == WorkspaceChangeTool || definition.Name == WorkspaceApplyTool {
			t.Fatalf("read-only Specialist advertised mutation: %s", definition.Name)
		}
	}
	if !reflect.DeepEqual(before, AgentCodeToolDefinitions()) {
		t.Fatal("Batch definitions modified the root/worker tool definitions")
	}
	compile := func(definition ToolDefinition) *jsonschema.Schema {
		t.Helper()
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("tool.json", strings.NewReader(string(definition.InputSchema))); err != nil {
			t.Fatal(err)
		}
		schema, err := compiler.Compile("tool.json")
		if err != nil {
			t.Fatal(err)
		}
		return schema
	}
	batchSchemas, rootSchemas := map[ToolName]*jsonschema.Schema{}, map[ToolName]*jsonschema.Schema{}
	for _, name := range []ToolName{WorkspaceChangeTool, WorkspaceApplyTool} {
		batchSchemas[name] = compile(batch[name])
		root, _ := AgentCodeToolDefinition(name)
		rootSchemas[name] = compile(root)
		if !strings.Contains(batch[name].Description, "apply_arguments") ||
			strings.Contains(batch[name].Description, "apply_authorized") ||
			strings.Contains(batch[name].Description, "code_diagnostics") {
			t.Fatalf("Batch %s description does not match its own receipts: %s", name, batch[name].Description)
		}
	}
	hash := strings.Repeat("a", 64)
	change := func(action, digest, fields string) json.RawMessage {
		return json.RawMessage(`{"version":"agent-code-tools.v1","action":"` + action + `","path":"test.txt","expected_sha256":"` + digest + `"` + fields + `}`)
	}
	apply := func(action, original, proposed string) json.RawMessage {
		raw, _ := json.Marshal(WorkspaceApplyPayload{Version: AgentCodeRegistryVersion,
			EditID: "edit-test", ExpectedAction: action, ExpectedOriginalSHA256: original,
			ExpectedProposedSHA256: proposed})
		return raw
	}
	for _, tc := range []struct {
		name            string
		tool            ToolName
		raw             json.RawMessage
		batchOK, rootOK bool
	}{
		{"patch", WorkspaceChangeTool, change("propose_patch", hash, `,"replacements":[{"old_text":"old","new_text":"new","expected_occurrences":1}]`), true, true},
		{"create", WorkspaceChangeTool, change("create", "missing", `,"content":"new"`), true, true},
		{"empty_create", WorkspaceChangeTool, change("create", "missing", `,"content":""`), true, true},
		{"patch_requires_replacements", WorkspaceChangeTool, change("propose_patch", hash, `,"content":"new"`), false, false},
		{"patch_requires_existing_hash", WorkspaceChangeTool, change("propose_patch", "missing", `,"replacements":[{"old_text":"old","new_text":"new","expected_occurrences":1}]`), false, false},
		{"create_requires_content", WorkspaceChangeTool, change("create", "missing", ""), false, false},
		{"create_requires_missing", WorkspaceChangeTool, change("create", hash, `,"content":"new"`), false, false},
		{"mixed_fields", WorkspaceChangeTool, change("create", "missing", `,"content":"new","replacements":[]`), false, false},
		{"replace_is_root_worker_only", WorkspaceChangeTool, change("replace", hash, `,"content":"new"`), false, true},
		{"move_is_root_worker_only", WorkspaceChangeTool, change("move", hash, `,"destination_path":"other.txt","destination_expected_sha256":"missing"`), false, true},
		{"revert_is_root_worker_only", WorkspaceChangeTool, change("propose_revert", hash, `,"source_run_id":"run-source","source_edit_id":"edit-source"`), false, true},
		{"delete_is_unsupported", WorkspaceChangeTool, change("delete", hash, ""), false, false},
		{"apply_patch", WorkspaceApplyTool, apply("propose_patch", hash, hash), true, true},
		{"apply_create", WorkspaceApplyTool, apply("create", "missing", hash), true, true},
		{"apply_patch_requires_existing_hash", WorkspaceApplyTool, apply("propose_patch", "missing", hash), false, false},
		{"apply_create_requires_missing", WorkspaceApplyTool, apply("create", hash, hash), false, false},
		{"apply_replace_is_root_worker_only", WorkspaceApplyTool, apply("replace", hash, hash), false, true},
		{"apply_move_is_root_worker_only", WorkspaceApplyTool, apply("move", hash, "missing"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal(tc.raw, &value); err != nil {
				t.Fatal(err)
			}
			canonical, parserErr := NormalizeSpecialistNativePayload(string(tc.tool), tc.raw)
			schemaErr := batchSchemas[tc.tool].Validate(value)
			if (schemaErr == nil) != tc.batchOK || (parserErr == nil) != tc.batchOK {
				t.Fatalf("Batch valid=%t schema=%v parser=%v", tc.batchOK, schemaErr, parserErr)
			}
			if tc.batchOK {
				if err := json.Unmarshal(canonical, &value); err != nil || batchSchemas[tc.tool].Validate(value) != nil {
					t.Fatalf("normalization changed the published contract: %s %v", canonical, err)
				}
			}
			_, rootErr := NormalizeAgentCodePayload(tc.tool, tc.raw)
			_, workerErr := NormalizeSupervisorToolPayload(tc.tool, tc.raw)
			if tc.rootOK && rootSchemas[tc.tool].Validate(value) != nil {
				t.Fatal("root/worker schema lost a supported operation")
			}
			if (rootErr == nil) != tc.rootOK || (workerErr == nil) != tc.rootOK {
				t.Fatalf("root/worker valid=%t root=%v worker=%v", tc.rootOK, rootErr, workerErr)
			}
		})
	}
}

func TestBatchSpecialistPublishedCommitMessageContract(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	for _, definition := range SpecialistNativeToolDefinitions(true) {
		if definition.Name == "git_commit" {
			if err := compiler.AddResource("git-commit.json", strings.NewReader(string(definition.InputSchema))); err != nil {
				t.Fatal(err)
			}
		}
	}
	schema, err := compiler.Compile("git-commit.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, message string
		valid         bool
	}{
		{"valid", "  commit the observed change  ", true},
		{"unicode_limit", strings.Repeat("改", 256), true},
		{"overlong", strings.Repeat("改", 257), false},
		{"multiline", "change one\nchange two", false},
		{"carriage_return", "change one\rchange two", false},
		{"nul", "change\x00one", false},
		{"blank", "  \t  ", false},
		{"unicode_blank", "\u3000\u00a0", false},
		{"vertical_tab_blank", "\v", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := map[string]any{"message": tc.message}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			_, parserErr := NormalizeSpecialistNativePayload("git_commit", raw)
			schemaErr := schema.Validate(value)
			if (parserErr == nil) != tc.valid || (schemaErr == nil) != tc.valid {
				t.Fatalf("valid=%t schema=%v parser=%v", tc.valid, schemaErr, parserErr)
			}
		})
	}
}
