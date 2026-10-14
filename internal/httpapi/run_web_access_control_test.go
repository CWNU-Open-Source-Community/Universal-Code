package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestRunWebAccessHTTPCreateChangeReplayAndRefusal(t *testing.T) {
	fixture := newAPIFixture(t)
	for _, endpoint := range []string{RunCreationControlPath, ThreadCollectionPath} {
		version := "run_creation.v1"
		if endpoint == ThreadCollectionPath {
			version = "thread_creation.v1"
		}
		body := fmt.Sprintf(`{"version":%q,"goal":"Select web access","workspace_id":%q,"web_access_mode":"full"}`, version, fixture.workspace.ID)
		response := performControlPathRequest(t, fixture.api, endpoint, "web-access-create-"+version, strings.NewReader(body))
		var created RunCreationControlView
		if endpoint == ThreadCollectionPath {
			var thread ThreadCreationControlView
			decodeDataStatus(t, response, http.StatusAccepted, &thread)
			created = RunCreationControlView{Run: thread.Run, Mode: thread.Mode}
		} else {
			decodeDataStatus(t, response, http.StatusAccepted, &created)
		}
		if created.Mode.Scope.WebAccessMode != domain.WebAccessFull || created.Mode.Scope.NetworkMode != "disabled" || len(created.Mode.Scope.AllowedTargets) != 0 {
			t.Fatalf("full widened command scope: %+v", created)
		}
		path := "/api/v1/runs/" + created.Run.ID + "/web-access"
		request := `{"version":"run_web_access_control.v1","expected_mode_revision":1,"web_access_mode":"rules","web_allowed_targets":["docs.example.org"]}`
		key := "web-access-control-" + version
		var selected RunWebAccessControlView
		decodeDataStatus(t, performControlPathRequest(t, fixture.api, path, key, strings.NewReader(request)), http.StatusAccepted, &selected)
		if selected.Mode.Revision != 2 || selected.Mode.Scope.WebAccessMode != domain.WebAccessRules || len(selected.Mode.Scope.WebAllowedTargets) != 1 {
			t.Fatalf("selection=%+v", selected)
		}
		var replay RunWebAccessControlView
		decodeDataStatus(t, performControlPathRequest(t, fixture.api, path, key, strings.NewReader(request)), http.StatusAccepted, &replay)
		if !replay.Replayed || replay.Mode.Revision != selected.Mode.Revision {
			t.Fatalf("replay=%+v", replay)
		}
		assertAPIError(t, performControlPathRequest(t, fixture.api, path, key+"-stale", strings.NewReader(request)), http.StatusConflict, "CONFLICT")
		for _, invalid := range []string{`"full","web_allowed_targets":["docs.example.org"]`, `"cache"`, `"rules","web_allowed_targets":["public_https"]`, `"rules","web_allowed_targets":["*.example.org"]`, `"disabled","web_access_mode":"full"`} {
			payload := `{"version":"run_web_access_control.v1","expected_mode_revision":2,"web_access_mode":` + invalid + `}`
			assertAPIError(t, performControlPathRequest(t, fixture.api, path, key+"-invalid", strings.NewReader(payload)), http.StatusBadRequest, "INVALID_ARGUMENT")
		}
		changed := strings.Replace(body, `"full"`, `"disabled"`, 1)
		assertAPIError(t, performControlPathRequest(t, fixture.api, endpoint, "web-access-create-"+version, strings.NewReader(changed)), http.StatusConflict, "CONFLICT")
	}
}
