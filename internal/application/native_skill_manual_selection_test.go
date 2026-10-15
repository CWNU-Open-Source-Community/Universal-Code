package application_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func nativeManualFixture(t *testing.T, st *store.SQLiteStore, multiple bool) (plugins.Installation, string, map[string][]byte) {
	t.Helper()
	var directory string
	if multiple {
		directory = filepath.Join(t.TempDir(), "native-plugin")
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"native-manual-plugin"}`
		if err := os.WriteFile(filepath.Join(directory, "plugin.json"), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"native-alpha", "native-beta"} {
			root := filepath.Join(directory, "skills", name)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: " + name + "\ndescription: Read the selected original evidence.\n---\n# " + name + "\nNATIVE_MANUAL_" + name + "_PROOF\n"
			if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		directory = nativeTinySkill(t)
	}
	value := importPortableFixture(t, st, directory, "native-manual-import")
	if value.Snapshot.Legacy != nil || value.Snapshot.AuthorVersion != "" {
		t.Fatalf("fixture must retain native metadata without an invented version: %+v", value.Snapshot)
	}
	if want := map[bool]int{false: 1, true: 2}[multiple]; len(value.Snapshot.Skills) != want {
		t.Fatalf("native component count=%d want=%d", len(value.Snapshot.Skills), want)
	}
	bodies := map[string][]byte{}
	for _, skill := range value.Snapshot.Skills {
		body, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(skill.Instructions.Path)))
		if err != nil {
			t.Fatal(err)
		}
		bodies[skill.Instructions.Component.ComponentID] = body
	}
	return value, directory, bodies
}

func selectNativeManualFixture(t *testing.T, st *store.SQLiteStore, run domain.Run, value plugins.Installation) (skills.ExternalSelection, []toolgateway.SkillReadRequest) {
	t.Helper()
	var pins []toolgateway.SkillReadRequest
	var refs []string
	for _, skill := range value.Snapshot.Skills {
		pin := installedReadPin(value)
		pin.ComponentID = skill.Instructions.Component.ComponentID
		encoded, err := json.Marshal(pin)
		if err != nil {
			t.Fatal(err)
		}
		pins, refs = append(pins, pin), append(refs, string(encoded))
	}
	result, err := application.NewExternalSkillSelectionService(st).Select(t.Context(), application.SelectExternalSkillsRequest{
		RunID: run.ID, PackageRefs: refs, TokenBudget: 4096, OperationKey: "native-manual-select-" + run.ID,
		RequestedBy: "operator", ConfirmUntrustedContext: true})
	if err != nil {
		t.Fatal(err)
	}
	selection := result.Selection
	if selection.ProtocolVersion != skills.PluginExternalSelectionProtocolVersion || selection.ToolCapabilityGrant || len(selection.Items) != len(pins) {
		t.Fatalf("native selection has wrong source or authority: %+v", selection)
	}
	for _, item := range selection.Items {
		if item.Plugin == nil || !item.Plugin.Native || item.InstallationID != value.ID ||
			item.Plugin.PackageID != value.PackageID() || item.Plugin.Revision != value.Revision() ||
			item.Plugin.Generation != value.Generation || item.Version != "" || item.Name == "" ||
			item.InstallResultFingerprint != "" || item.ObjectKey != "" {
			t.Fatalf("native selection did not preserve the real component: %+v", item)
		}
		found := false
		for _, ref := range refs {
			found = found || skills.ExternalSelectionItemRef(item) == ref
		}
		if !found {
			t.Fatalf("selection lost its exact input pin: %+v", item)
		}
	}
	return selection, pins
}

func assertNativeManualGuidance(t *testing.T, request llm.ChatRequest, item skills.ExternalSelectionItem, body []byte) {
	t.Helper()
	assertExplicitGuidance(t, request, "root", body)
	for _, message := range request.Messages {
		var envelope struct {
			Version string `json:"version"`
			Content string `json:"content"`
			Source  struct {
				InstallationID  string `json:"installation_id"`
				Name            string `json:"name"`
				Version         string `json:"version"`
				ContentSHA256   string `json:"content_sha256"`
				DeliveredSHA256 string `json:"delivered_sha256"`
			} `json:"source"`
		}
		if json.Unmarshal([]byte(message.Content), &envelope) != nil || envelope.Version != "external_skill_guidance.v1" || envelope.Content != string(body) {
			continue
		}
		if envelope.Source.InstallationID != item.InstallationID || envelope.Source.Name != item.Name ||
			envelope.Source.Version != "" || envelope.Source.ContentSHA256 != item.ContentSHA256 || envelope.Source.DeliveredSHA256 != item.ContentSHA256 {
			t.Fatalf("native guidance source drifted: %+v", envelope.Source)
		}
		return
	}
	t.Fatal("missing native guidance source")
}

// A restart request contains older successful reads too. Filter by this turn's
// call ID so historical stdout cannot make a failed new read appear to pass.
func assertNativeManualRead(t *testing.T, request llm.ChatRequest, pin toolgateway.SkillReadRequest, body []byte) {
	t.Helper()
	// Go assigns its own durable call IDs. Use the final native call batch
	// that the provider actually received, rather than the model's wire ID.
	callID := ""
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if len(request.Messages[index].ToolCalls) == 0 {
			continue
		}
		for _, call := range request.Messages[index].ToolCalls {
			var actual toolgateway.SkillReadRequest
			if call.Name == "skill_read" && json.Unmarshal(call.Arguments, &actual) == nil && actual == pin {
				callID = call.ID
				break
			}
		}
		break
	}
	if callID == "" {
		t.Fatalf("latest native call batch lacks the requested pin: %+v", pin)
	}
	for _, message := range request.Messages {
		for _, result := range message.ToolResults {
			if result.ToolCallID == callID {
				assertInstalledReadResult(t, llm.ChatRequest{Messages: []llm.Message{{ToolResults: []llm.ToolResult{result}}}}, pin, "", body)
				return
			}
		}
	}
	t.Fatalf("current read result %s was not delivered to the provider", callID)
}

func TestNativeSkillManualSelectionDeliversExactBodyAndSurvivesRestart(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "two-components"}[multiple], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "native.db")
			st := openHistoryRecallStore(t, path)
			defer func() { _ = st.Close() }()
			value, directory, bodies := nativeManualFixture(t, st, multiple)
			run := createExplicitRun(t, st, "tool-loop")
			selected, pins := selectNativeManualFixture(t, st, run, value)
			if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			for round := 0; round < 2; round++ {
				var calls []llm.ToolCall
				for index, pin := range pins {
					calls = append(calls, installedReadCall(pin, fmt.Sprintf("native-read-%d-%d", round, index), ""))
				}
				provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
					{ToolCalls: calls},
					textResponse(rootActionResponse(domain.RootActionContinue, "Read selected native instructions", "", "")),
				}}
				if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err != nil {
					t.Fatal(err)
				}
				requests := provider.Requests()
				if len(requests) != 2 {
					t.Fatalf("provider rounds=%d want=2", len(requests))
				}
				for _, item := range selected.Items {
					assertNativeManualGuidance(t, requests[0], item, bodies[item.Plugin.ComponentID])
				}
				for _, pin := range pins {
					assertNativeManualRead(t, requests[1], pin, bodies[pin.ComponentID])
				}
				if round == 0 {
					// Selection and runtime must keep the retained source after the
					// operator's original directory changes and SQLite is reopened.
					for _, skill := range value.Snapshot.Skills {
						if err := os.WriteFile(filepath.Join(directory, filepath.FromSlash(skill.Instructions.Path)), []byte("Changed after acquisition; never selected.\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
					st = openHistoryRecallStore(t, path)
					durable, found, err := st.GetExternalSkillSelectionByRun(t.Context(), run.ID)
					if err != nil || !found || !reflect.DeepEqual(durable, selected) {
						t.Fatalf("restart changed the exact native selection: %+v %v", durable, err)
					}
				}
			}
			reads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
			if err != nil || len(reads) != len(pins) {
				t.Fatalf("native activations were lost or duplicated across restart: count=%d err=%v", len(reads), err)
			}
		})
	}
}

func TestNativeSkillManualSelectionRejectsLifecycleDrift(t *testing.T) {
	for _, reenable := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "generation-changed"}[reenable], func(t *testing.T) {
			st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "lifecycle.db"))
			defer st.Close()
			value, _, bodies := nativeManualFixture(t, st, false)
			run := createExplicitRun(t, st, "tool-loop")
			selected, pins := selectNativeManualFixture(t, st, run, value)
			if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			current := reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
			if reenable {
				current = reviewInstalledFixture(t, st, current, plugins.ReviewEnable)
			}
			if current.Generation == pins[0].InstallationGeneration || (reenable && current.State != plugins.StateEnabled) {
				t.Fatal("fixture did not change the selected installation generation")
			}
			provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
				textResponse(rootActionResponse(domain.RootActionContinue, "Must not receive stale selected instructions", "", "")),
			}}
			if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err == nil || len(provider.Requests()) != 0 {
				t.Fatalf("stale native selection reached the provider: calls=%d err=%v", len(provider.Requests()), err)
			}
			durable, found, err := st.GetExternalSkillSelectionByRun(t.Context(), run.ID)
			if err != nil || !found || !reflect.DeepEqual(durable, selected) {
				t.Fatalf("lifecycle drift rewrote the immutable native selection: %v", err)
			}
			projection, found, err := st.GetExternalSkillProjectionByRun(t.Context(), run.ID)
			if err != nil || !found || projection.RootCommittedCount != 0 {
				t.Fatalf("stale native body was committed: %+v %v", projection, err)
			}

			// A separate Run can still offer skill_read. Supplying the old pin
			// must fail at the real reader even after the Plugin is re-enabled.
			readerRun := startedSkillRun(t, st)
			reader := &scriptedToolProvider{responses: []*llm.ChatResponse{
				{ToolCalls: []llm.ToolCall{installedReadCall(pins[0], "stale-native-read", "")}},
				textResponse(rootActionResponse(domain.RootActionContinue, "Stale read rejected", "", "")),
			}}
			if _, err := newToolLoopSupervisor(st, reader).Step(t.Context(), readerRun.ID); err != nil {
				t.Fatal(err)
			}
			requests := reader.Requests()
			if len(requests) != 2 || !hasErrorToolResult(requests[1], string(apperror.CodePolicyDenied)) || hasToolResult(requests[1], strings.TrimSpace(string(bodies[pins[0].ComponentID]))) {
				t.Fatal("stale native pin did not produce a content-free policy denial")
			}
			reads, err := st.ListBuiltinSkillReadCalls(t.Context(), readerRun.ID)
			if err != nil || len(reads) != 0 {
				t.Fatalf("denied native read activated instructions: %+v %v", reads, err)
			}
		})
	}
}
