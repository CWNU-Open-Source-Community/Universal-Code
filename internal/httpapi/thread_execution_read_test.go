package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

type threadTurnWithoutExecution struct{ ThreadTurnController }

func TestThreadExecutionReadCapabilityIsOptionalBooleanInOpenAPI(t *testing.T) {
	data, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document openAPIDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["RuntimeCapabilitiesView"]
	properties := schema["properties"].(map[string]any)
	property := properties["thread_execution_read_enabled"].(map[string]any)
	if property["type"] != "boolean" {
		t.Fatalf("execution read schema = %#v", property)
	}
	for _, name := range schema["required"].([]any) {
		if name == "thread_execution_read_enabled" {
			t.Fatal("older capabilities responses must remain valid")
		}
	}
}

func TestThreadExecutionReadCapabilityMatchesRouteWithoutGrantingControl(t *testing.T) {
	for _, tt := range []struct {
		name       string
		execution  bool
		controller ThreadTurnController
		available  bool
	}{
		{name: "read-only server"},
		{name: "disabled executor with controller", controller: &threadTurnControllerStub{}},
		{name: "Run executor without Thread controller", execution: true},
		{name: "Thread turn without execution reader", execution: true,
			controller: threadTurnWithoutExecution{&threadTurnControllerStub{}}},
		{name: "Thread execution readable", execution: true, controller: &threadTurnControllerStub{}, available: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newAPIFixture(t)
			api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
				RunExecutionEnabled: tt.execution, RunExecutionController: runExecutionControllerFake{},
				ThreadTurnController: tt.controller})
			if tt.name == "Thread turn without execution reader" {
				if apperror.CodeOf(err) != apperror.CodeInvalidArgument {
					t.Fatalf("enabled partial controller accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var capabilities RuntimeCapabilitiesView
			decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/capabilities",
				testAccessToken, "", "", nil), http.StatusOK, &capabilities)
			if capabilities.ThreadExecutionReadEnabled == nil || *capabilities.ThreadExecutionReadEnabled != tt.available || capabilities.RunExecutionEnabled != tt.execution {
				t.Fatalf("read and execution capabilities do not match their independent routes: %#v", capabilities)
			}
			response := performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/threads/thread-observe/execution",
				testAccessToken, "", "", nil)
			if !tt.available {
				if response.Code != http.StatusNotFound {
					t.Fatalf("unavailable route status = %d", response.Code)
				}
				return
			}
			var state application.ThreadExecutionState
			decodeDataStatus(t, response, http.StatusOK, &state)
			if state.ThreadID != "thread-observe" || state.State != "idle" || state.CapabilityGrant {
				t.Fatalf("read projection widened authority: %#v", state)
			}
		})
	}
}
