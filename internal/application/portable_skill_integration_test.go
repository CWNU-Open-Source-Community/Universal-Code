package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func portableCatalogService(t *testing.T, st *store.SQLiteStore) *application.SkillCatalogService {
	t.Helper()
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	objects, err := skills.NewLocalPackageObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return application.NewSkillCatalogService(st, application.NewSkillPackageRegistryService(st, objects, builtins))
}

func importPortableFixture(t *testing.T, st *store.SQLiteStore, directory, key string) plugins.Installation {
	t.Helper()
	result, err := portableCatalogService(t, st).ImportFromDirectory(t.Context(), application.ImportSkillFromDirectoryRequest{
		Directory: directory, Surface: domain.ExecutionSurfaceCode, OperationKey: key, InstalledBy: "operator", ConfirmUntrusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Portable == nil || result.Portable.State != plugins.StateEnabled || len(result.Portable.EnabledCapabilities) != 1 || result.Portable.EnabledCapabilities[0] != plugins.CapabilitySkills {
		t.Fatalf("native import not enabled as instructions: %+v", result.Portable)
	}
	return *result.Portable
}

func installedReadPin(value plugins.Installation) toolgateway.SkillReadRequest {
	ref := value.Snapshot.Skills[0].Instructions.Component
	return toolgateway.SkillReadRequest{InstallationID: value.ID, PackageID: ref.PackageID, ComponentID: ref.ComponentID,
		Revision: value.Revision(), InstallationGeneration: value.Generation}
}

func installedReadCall(pin toolgateway.SkillReadRequest, id, resource string) llm.ToolCall {
	pin.Resource = resource
	raw, _ := json.Marshal(pin)
	return llm.ToolCall{ID: id, Name: "skill_read", Arguments: raw}
}

func installedPinInCatalog(t *testing.T, request llm.ChatRequest, pin toolgateway.SkillReadRequest) bool {
	t.Helper()
	for _, tool := range request.Tools {
		if tool.Name != "skill_read" {
			continue
		}
		_, raw, ok := strings.Cut(tool.Description, " Available skills for this mode: ")
		if !ok {
			return false
		}
		var catalog []toolgateway.BuiltinSkillDescriptor
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			t.Fatal(err)
		}
		for _, item := range catalog {
			if item.SkillReadRequest == pin {
				return true
			}
		}
	}
	return false
}

func assertInstalledReadResult(t *testing.T, request llm.ChatRequest, pin toolgateway.SkillReadRequest, resource string, want []byte) {
	t.Helper()
	pin.Resource = resource
	for _, message := range request.Messages {
		for _, result := range message.ToolResults {
			if result.IsError {
				continue
			}
			var envelope struct {
				Stdout    string `json:"stdout"`
				Truncated bool   `json:"truncated"`
			}
			if err := json.Unmarshal([]byte(result.Content), &envelope); err != nil || result.IsError || envelope.Truncated {
				t.Fatalf("bad tool envelope: %s %v", result.Content, err)
			}
			var body struct {
				Read      toolgateway.SkillReadRequest `json:"read"`
				Content   string                       `json:"content"`
				Encoding  string                       `json:"encoding"`
				Source    string                       `json:"source_sha256"`
				Delivered string                       `json:"delivered_sha256"`
				Guidance  *bool                        `json:"workflow_guidance"`
				Grant     bool                         `json:"capability_grant"`
			}
			if err := json.Unmarshal([]byte(envelope.Stdout), &body); err != nil {
				t.Fatalf("native read was truncated or not JSON: %v", err)
			}
			if body.Read != pin {
				continue
			}
			delivered := []byte(body.Content)
			if body.Encoding == "base64" {
				var err error
				delivered, err = base64.StdEncoding.DecodeString(body.Content)
				if err != nil {
					t.Fatal(err)
				}
			}
			originalHash := sha256.Sum256(want)
			deliveredHash := sha256.Sum256(delivered)
			if body.Source != hex.EncodeToString(originalHash[:]) || body.Delivered != hex.EncodeToString(deliveredHash[:]) || body.Guidance == nil || *body.Guidance != (resource == "") || body.Grant || !bytes.Equal(delivered, []byte(redact.String(string(want)))) {
				t.Fatalf("native bytes/digests/authority changed: source=%s delivered=%s bytes=%d want=%d", body.Source, body.Delivered, len(delivered), len(want))
			}
			return
		}
	}
	t.Fatalf("read result missing for %+v", pin)
}

func installedGuidanceBinding(t *testing.T, request llm.ChatRequest, pin toolgateway.SkillReadRequest) bool {
	t.Helper()
	for _, message := range request.Messages {
		if message.Role != "system" || !strings.HasPrefix(message.Content, "Go-bound installed Skill main reads in this request.") {
			continue
		}
		_, raw, _ := strings.Cut(message.Content, "safety exceptions: ")
		var reads []struct {
			CallID string                       `json:"tool_call_id"`
			Read   toolgateway.SkillReadRequest `json:"read"`
		}
		if err := json.Unmarshal([]byte(raw), &reads); err != nil {
			t.Fatal("invalid Go Skill binding", err)
		}
		for _, read := range reads {
			if read.Read == pin {
				if read.CallID == "" {
					t.Fatal("Skill binding lacks a native call identity")
				}
				return true
			}
		}
	}
	return false
}

func TestPortableSkillUpstreamImportReadResourceAndRestart(t *testing.T) {
	for _, fixture := range []struct{ name, directory, resource string }{
		{"standalone", "anthropic-skill-creator/skills/skill-creator", "scripts/__init__.py"},
		{"plugin", "agent-plugins-example", "references/migration-guide.md"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "native.db")
			st := openHistoryRecallStore(t, path)
			defer func() { _ = st.Close() }()
			directory, err := filepath.Abs(filepath.Join("..", "agentpackages", "testdata", "upstream", filepath.FromSlash(fixture.directory)))
			if err != nil {
				t.Fatal(err)
			}
			value := importPortableFixture(t, st, directory, "upstream-import")
			pin := installedReadPin(value)
			instructions := filepath.Join(directory, filepath.FromSlash(value.Snapshot.Skills[0].Instructions.Path))
			body, err := os.ReadFile(instructions)
			if err != nil {
				t.Fatal(err)
			}
			resource, err := os.ReadFile(filepath.Join(filepath.Dir(instructions), filepath.FromSlash(fixture.resource)))
			if err != nil {
				t.Fatal(err)
			}
			run := startedSkillRun(t, st)
			p := &scriptedToolProvider{responses: []*llm.ChatResponse{
				{ToolCalls: []llm.ToolCall{installedReadCall(pin, "instructions", "")}},
				{ToolCalls: []llm.ToolCall{installedReadCall(pin, "resource", fixture.resource)}},
				textResponse(rootActionResponse(domain.RootActionContinue, "Read native instructions and resource", "", "")),
			}}
			if _, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			requests := p.Requests()
			if len(requests) != 3 {
				t.Fatal("unexpected rounds", len(requests))
			}
			if !installedPinInCatalog(t, requests[0], pin) {
				t.Fatal("installed Skill absent from real catalog")
			}
			for _, message := range requests[0].Messages {
				if strings.Contains(message.Content, string(body)) {
					t.Fatal("full native body loaded before skill_read")
				}
			}
			assertInstalledReadResult(t, requests[1], pin, "", body)
			assertInstalledReadResult(t, requests[2], pin, fixture.resource, resource)
			resourcePin := pin
			resourcePin.Resource = fixture.resource
			if installedGuidanceBinding(t, requests[0], pin) || !installedGuidanceBinding(t, requests[1], pin) || !installedGuidanceBinding(t, requests[2], pin) || installedGuidanceBinding(t, requests[2], resourcePin) {
				t.Fatal("workflow guidance did not bind only the successful native main read")
			}
			reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
			if err != nil || len(reads) != 1 {
				t.Fatal("resource displaced or duplicated activation", len(reads), err)
			}
			var persisted toolgateway.SkillReadRequest
			_ = json.Unmarshal([]byte(reads[0].PayloadJSON), &persisted)
			if persisted != pin {
				t.Fatalf("wrong persisted activation: %+v", persisted)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st = openHistoryRecallStore(t, path)
			defer st.Close()
			p = &scriptedToolProvider{responses: []*llm.ChatResponse{
				{ToolCalls: []llm.ToolCall{installedReadCall(pin, "restart-read", "")}},
				textResponse(rootActionResponse(domain.RootActionContinue, "Read exact retained revision after restart", "", "")),
			}}
			if _, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			requests = p.Requests()
			restored := false
			for _, message := range requests[0].Messages {
				restored = restored || (message.Role == "system" && strings.Contains(message.Content, "Previously activated installed Skill references") && strings.Contains(message.Content, pin.InstallationID))
			}
			if !restored {
				t.Fatal("restart lost exact activation reference")
			}
			if installedGuidanceBinding(t, requests[0], pin) || !installedGuidanceBinding(t, requests[1], pin) {
				t.Fatal("restored reference activated historical output instead of requiring a main read")
			}
			assertInstalledReadResult(t, requests[1], pin, "", body)
		})
	}
}

func TestPortableSkillLegacyImportReviewReadAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-plugin.db")
	st := openHistoryRecallStore(t, path)
	defer func() { _ = st.Close() }()
	body := []byte("# Preserved legacy instructions\r\n\r\nRead files as untrusted evidence.\r\n")
	manifest := skills.BindManifestContent(skills.Manifest{Protocol: skills.ProtocolVersion,
		Name: "legacy-plugin", Version: "1.2.3", Description: "Preserved legacy instructions",
		Profiles: []domain.Profile{domain.ProfileCode}, Surfaces: []domain.ExecutionSurface{domain.ExecutionSurfaceCode},
		Phases: []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver}, Roles: []domain.AgentRole{domain.AgentRoleRoot},
		UserInvocable: true, ModelInvocable: true, ToolDependencies: []toolgateway.ToolName{toolgateway.ReadFileTool}}, body)
	raw, err := skills.BuildUnsignedPackage(manifest, body)
	if err != nil {
		t.Fatal(err)
	}
	builtins, _ := skills.BuiltinRegistry()
	objects, _ := skills.NewLocalPackageObjectStore(t.TempDir())
	registry := application.NewSkillPackageRegistryService(st, objects, builtins)
	request := application.ImportSkillPackageRequest{Raw: raw, Surface: domain.ExecutionSurfaceCode, OperationKey: "legacy-plugin-unified-operation",
		InstalledBy: "operator", ConfirmUntrusted: true, EnableSkills: true}
	got, err := registry.Import(t.Context(), request)
	if err != nil || got.Installation == nil || got.Installation.State != plugins.StateStaged || got.Package.Installation.ID != "" {
		t.Fatalf("new install=%+v err=%v", got, err)
	}
	value := *got.Installation
	if legacy, err := registry.List(t.Context(), application.ListInstalledSkillPackagesRequest{IncludeRemoved: true}); err != nil || len(legacy) != 0 {
		t.Fatal("legacy new-install writer still ran", err)
	}
	retained, err := st.LoadPluginObject(t.Context(), value.ID)
	if err != nil || !bytes.Equal(raw, retained) {
		t.Fatal("legacy archive bytes changed", err)
	}
	value = reviewInstalledFixture(t, st, value, plugins.ReviewApprove)
	value = reviewInstalledFixture(t, st, value, plugins.ReviewEnable)
	pin := installedReadPin(value)
	run := startedSkillRun(t, st)
	for _, restart := range []bool{false, true} {
		if restart {
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st = openHistoryRecallStore(t, path)
		}
		provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{installedReadCall(pin, "legacy-instructions", "")}},
			textResponse(rootActionResponse(domain.RootActionContinue, "Read original legacy instructions", "", "")),
		}}
		if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
		requests := provider.Requests()
		if len(requests) != 2 || !installedPinInCatalog(t, requests[0], pin) {
			t.Fatal("enabled legacy component absent from catalog")
		}
		assertInstalledReadResult(t, requests[1], pin, "", body)
	}
	value = reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
	registry = application.NewSkillPackageRegistryService(st, objects, builtins)
	replayed, err := registry.Import(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Installation.State != plugins.StateDisabled || replayed.Installation.Generation != value.Generation {
		t.Fatalf("replay re-enabled legacy component: %+v err=%v", replayed, err)
	}
}

func nativeTinySkill(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "native-fixture")
	if err := os.MkdirAll(filepath.Join(directory, "resources"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"SKILL.md":           []byte("---\nname: native-fixture\ndescription: Read bounded fixture resources.\n---\nRetained native body. Read resources/data.bin and resources/empty.txt.\n"),
		"resources/data.bin": {0xff, 0x00, 0xfe, 0x01}, "resources/empty.txt": {},
	} {
		if err := os.WriteFile(filepath.Join(directory, filepath.FromSlash(name)), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func reviewInstalledFixture(t *testing.T, st *store.SQLiteStore, value plugins.Installation, action plugins.ReviewAction) plugins.Installation {
	t.Helper()
	svc, _ := plugins.NewService(st)
	result, err := svc.Review(t.Context(), value.ID, plugins.ReviewRequest{Action: action, ExpectedPackageFingerprint: value.PackageFingerprint,
		ExpectedGeneration: value.Generation, ReviewedBy: "operator", ConfirmUntrusted: true, Capabilities: []plugins.Capability{plugins.CapabilitySkills}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPortableSkillImportReplayKeepsDisabledRevokedAndRetainedBytes(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "retry.db"))
	defer st.Close()
	directory := nativeTinySkill(t)
	value := importPortableFixture(t, st, directory, "stable-operation")
	svc := portableCatalogService(t, st)
	request := application.ImportSkillFromDirectoryRequest{Directory: directory, Surface: domain.ExecutionSurfaceCode, OperationKey: "stable-operation", InstalledBy: "operator", ConfirmUntrusted: true}
	for _, action := range []plugins.ReviewAction{plugins.ReviewDisable, plugins.ReviewRevoke} {
		value = reviewInstalledFixture(t, st, value, action)
		result, err := svc.ImportFromDirectory(t.Context(), request)
		if err != nil || result.Portable == nil || result.Portable.State != value.State || result.Portable.Generation != value.Generation {
			t.Fatalf("retry renewed %s: %+v %v", action, result.Portable, err)
		}
	}
	archive, err := st.LoadPluginObject(t.Context(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "resources", "data.bin"), []byte("changed resource"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportFromDirectory(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatal("same operation did not reject otherwise valid source drift", err)
	}
	after, err := st.LoadPluginObject(t.Context(), value.ID)
	if err != nil || !bytes.Equal(archive, after) {
		t.Fatal("mutable source changed retained object", err)
	}
}

func TestPortableSkillImportExcludesGitAdministrationAndBindsOperationInput(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "scope.db"))
	defer st.Close()
	directory := nativeTinySkill(t)
	if err := os.Mkdir(filepath.Join(directory, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".git", "config"), []byte("not package source"), 0600); err != nil {
		t.Fatal(err)
	}
	value := importPortableFixture(t, st, directory, "scope-import")
	for _, entry := range value.Snapshot.Inventory {
		if entry.Path == ".git" || strings.HasPrefix(entry.Path, ".git/") {
			t.Fatal("captured repository administration")
		}
	}
	svc := portableCatalogService(t, st)
	for _, request := range []application.ImportSkillFromDirectoryRequest{
		{Directory: directory, Surface: domain.ExecutionSurfaceCyber, OperationKey: "scope-import", InstalledBy: "operator", ConfirmUntrusted: true},
		{Directory: directory, Surface: domain.ExecutionSurfaceCode, OperationKey: "new-key", InstalledBy: "operator", ConfirmUntrusted: true},
	} {
		if _, err := svc.ImportFromDirectory(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
			t.Fatal("same acquired object silently gained a different operation/surface", err)
		}
	}
}

func TestPortableSkillBinaryEmptyAndContainedResources(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "resources.db"))
	defer st.Close()
	value := importPortableFixture(t, st, nativeTinySkill(t), "resource-import")
	pin := installedReadPin(value)
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{installedReadCall(pin, "binary", "resources/data.bin"), installedReadCall(pin, "empty", "resources/empty.txt"), installedReadCall(pin, "missing", "resources/missing")}},
		textResponse(rootActionResponse(domain.RootActionContinue, "Resource boundaries checked", "", "")),
	}}
	run := startedSkillRun(t, st)
	if _, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	request := p.Requests()[1]
	assertInstalledReadResult(t, request, pin, "resources/data.bin", []byte{0xff, 0x00, 0xfe, 0x01})
	assertInstalledReadResult(t, request, pin, "resources/empty.txt", nil)
	if installedGuidanceBinding(t, request, pin) {
		t.Fatal("resource reads acquired a main-guidance binding")
	}
	if !hasErrorToolResult(request, string(apperror.CodeFailedPrecondition)) {
		t.Fatal("missing resource was not refused")
	}
	reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(reads) != 0 {
		t.Fatal("resource read activated instructions", err)
	}
}

type changedDuringPortableReadStore struct {
	*store.SQLiteStore
	change func()
	drift  bool
}

func (s *changedDuringPortableReadStore) LoadPluginObject(ctx context.Context, id string) ([]byte, error) {
	raw, err := s.SQLiteStore.LoadPluginObject(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.change != nil {
		change := s.change
		s.change = nil
		change()
	}
	if s.drift {
		raw = append([]byte(nil), raw...)
		raw[len(raw)/2] ^= 1
	}
	return raw, nil
}

func TestPortableSkillCurrentAuthorityAndObjectDriftPreventDelivery(t *testing.T) {
	for _, change := range []string{"disable", "revoke", "cancel", "object-drift"} {
		t.Run(change, func(t *testing.T) {
			st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "authority.db"))
			defer st.Close()
			value := importPortableFixture(t, st, nativeTinySkill(t), "authority-import")
			pin := installedReadPin(value)
			run := startedSkillRun(t, st)
			wrapper := &changedDuringPortableReadStore{SQLiteStore: st, drift: change == "object-drift"}
			wrapper.change = func() {
				switch change {
				case "disable":
					reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
				case "revoke":
					reviewInstalledFixture(t, st, value, plugins.ReviewRevoke)
				case "cancel":
					if _, err := application.NewRunService(st).Cancel(t.Context(), run.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			p := &scriptedToolProvider{responses: []*llm.ChatResponse{
				{ToolCalls: []llm.ToolCall{installedReadCall(pin, "guarded", "")}},
				textResponse(rootActionResponse(domain.RootActionContinue, "Authority changed", "", "")),
			}}
			_, err := newToolLoopSupervisor(wrapper, p).Step(t.Context(), run.ID)
			if change == "cancel" {
				if err == nil || len(p.Requests()) != 1 {
					t.Fatal("cancelled read continued", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				request := p.Requests()[1]
				code := apperror.CodePolicyDenied
				if change == "object-drift" {
					code = apperror.CodeFailedPrecondition
				}
				if !hasErrorToolResult(request, string(code)) || hasToolResult(request, "Retained native body") {
					t.Fatal("read escaped changed authority/object binding")
				}
			}
			reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
			if err != nil || len(reads) != 0 {
				t.Fatal("refused read activated", err)
			}
		})
	}
}

func TestPortableSkillPendingReceiptRecoversExactObjectAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.db")
	st := openHistoryRecallStore(t, path)
	defer func() { _ = st.Close() }()
	directory := nativeTinySkill(t)
	value := importPortableFixture(t, st, directory, "pending-import")
	pin := installedReadPin(value)
	body, err := os.ReadFile(filepath.Join(directory, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	run := startedSkillRun(t, st)
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{installedReadCall(pin, "pending", "")}},
		textResponse(rootActionResponse(domain.RootActionContinue, "Recovered native read", "", "")),
	}}
	first, err := newToolLoopSupervisor(&failOnceToolResultStore{SQLiteStore: st, fail: true}, p).Step(t.Context(), run.ID)
	if apperror.CodeOf(err) != apperror.CodeInternal || first.Checkpoint.Phase != domain.SupervisorTurnStarted {
		t.Fatal("pending read was not recoverable", err)
	}
	if reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID); err != nil || len(reads) != 0 {
		t.Fatal("failed receipt activated", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// Recovery consumes the acquired object, never the original mutable directory.
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("source has changed"), 0600); err != nil {
		t.Fatal(err)
	}
	st = openHistoryRecallStore(t, path)
	resumed, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID)
	if err != nil || !resumed.Recovered {
		t.Fatal("native pending read not recovered", err)
	}
	if reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID); err != nil || len(reads) != 1 {
		t.Fatal("duplicate or missing activation", err)
	}
	requests := p.Requests()
	assertInstalledReadResult(t, requests[len(requests)-1], pin, "", body)
}

func TestPortableSkillActivationRetiresOnDisableWithoutPromotingHistory(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "retire.db"))
	defer st.Close()
	value := importPortableFixture(t, st, nativeTinySkill(t), "retire-import")
	pin := installedReadPin(value)
	run := startedSkillRun(t, st)
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{installedReadCall(pin, "activate", "")}},
		textResponse(rootActionResponse(domain.RootActionContinue, "Read native Skill", "", "")),
	}}
	if _, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
	p = &scriptedToolProvider{responses: []*llm.ChatResponse{textResponse(rootActionResponse(domain.RootActionContinue, "Disabled Skill retired", "", ""))}}
	if _, err := newToolLoopSupervisor(st, p).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	request := p.Requests()[0]
	if installedPinInCatalog(t, request, pin) {
		t.Fatal("disabled installation still offered")
	}
	unavailable := false
	for _, message := range request.Messages {
		if message.Role != "system" {
			continue
		}
		unavailable = unavailable || strings.Contains(message.Content, "Some previously activated installed Skills are unavailable")
		if strings.Contains(message.Content, "Previously activated installed Skill references") || strings.Contains(message.Content, "Retained native body") {
			t.Fatal("disabled activation restored from history")
		}
	}
	if !unavailable {
		t.Fatal("disabled activation not explicitly retired")
	}
}

func TestPortableSkillCurrentStateRetiresGuidanceWhileOldNativeBodyRemains(t *testing.T) {
	for _, change := range []string{"disabled", "generation", "revision"} {
		t.Run(change, func(t *testing.T) {
			st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "live-guidance.db"))
			defer st.Close()
			directory := nativeTinySkill(t)
			value := importPortableFixture(t, st, directory, "live-v1")
			pin := installedReadPin(value)
			var next plugins.Installation
			if change == "revision" {
				if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: native-fixture\ndescription: Updated instructions.\n---\nUse the revised workflow.\n"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err := portableCatalogService(t, st).ImportFromDirectory(t.Context(), application.ImportSkillFromDirectoryRequest{
					Directory: directory, Surface: domain.ExecutionSurfaceCode, OperationKey: "live-v2", InstalledBy: "operator", ConfirmUntrusted: true})
				if apperror.CodeOf(err) != apperror.CodeConflict {
					t.Fatal("revision fixture replaced the enabled predecessor", err)
				}
				values, err := st.ListPluginInstallations(t.Context(), value.PackageID(), 10)
				if err != nil {
					t.Fatal(err)
				}
				for _, candidate := range values {
					if candidate.ID != value.ID {
						next = candidate
					}
				}
				if next.ID == "" || next.Revision() == value.Revision() {
					t.Fatal("revision fixture did not retain a separate revision")
				}
			}
			run := startedSkillRun(t, st)
			provider := &scriptedToolProvider{respond: func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 0:
					return &llm.ChatResponse{ToolCalls: []llm.ToolCall{installedReadCall(pin, "main", "")}}, nil
				case 1:
					if !installedGuidanceBinding(t, request, pin) {
						t.Fatal("successful main read lacks current Go binding")
					}
					current := reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
					if change == "generation" {
						reviewInstalledFixture(t, st, current, plugins.ReviewEnable)
					} else if change == "revision" {
						reviewInstalledFixture(t, st, next, plugins.ReviewEnable)
					}
					return &llm.ChatResponse{ToolCalls: []llm.ToolCall{installedReadCall(pin, "old-pin", "")}}, nil
				default:
					if !hasToolResult(request, "Retained native body") || !hasErrorToolResult(request, string(apperror.CodePolicyDenied)) || installedGuidanceBinding(t, request, pin) {
						t.Fatal("historical successful body or failed reread restored retired Skill guidance")
					}
					if next.ID != "" && installedGuidanceBinding(t, request, installedReadPin(next)) {
						t.Fatal("new revision became guidance without a successful main read")
					}
					return textResponse(rootActionResponse(domain.RootActionContinue, "Retired Skill guidance", "", "")), nil
				}
			}}
			if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			if len(provider.Requests()) != 3 {
				t.Fatal("live-state fixture did not deliver all requests")
			}
		})
	}
}

func TestPortableSkillTransportRetryRequiresCurrentGuidance(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "disabled"}[disable], func(t *testing.T) {
			st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "retry-guidance.db"))
			defer st.Close()
			value := importPortableFixture(t, st, nativeTinySkill(t), "retry-import")
			pin := installedReadPin(value)
			run := startedSkillRun(t, st)
			provider := &scriptedToolProvider{respond: func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 0:
					return &llm.ChatResponse{ToolCalls: []llm.ToolCall{installedReadCall(pin, "read-before-retry", "")}}, nil
				case 1:
					if !installedGuidanceBinding(t, request, pin) {
						t.Fatal("successful main read lacks the initial binding")
					}
					if disable {
						current := reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
						if current.State != plugins.StateDisabled {
							t.Fatal("fixture did not disable the installation")
						}
					}
					return nil, llm.NewProviderError(llm.OutcomeRetryable, "tool-loop", "temporary failure after Skill read", nil)
				case 2:
					if disable {
						t.Fatal("disabled Skill guidance reached a transport retry")
					}
					if !installedGuidanceBinding(t, request, pin) {
						t.Fatal("valid transport retry lost its Skill guidance")
					}
					return textResponse(rootActionResponse(domain.RootActionContinue, "Retried with current Skill guidance", "", "")), nil
				default:
					t.Fatalf("unexpected provider request %d", index)
					return nil, nil
				}
			}}
			supervisor := newToolLoopSupervisor(st, provider).WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 2})
			_, err := supervisor.Step(t.Context(), run.ID)
			if disable {
				if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || len(provider.Requests()) != 2 {
					t.Fatalf("stale retry was not rejected before dispatch: requests=%d err=%v", len(provider.Requests()), err)
				}
			} else if err != nil || len(provider.Requests()) != 3 {
				t.Fatalf("current Skill guidance prevented a normal retry: requests=%d err=%v", len(provider.Requests()), err)
			}
		})
	}
}
