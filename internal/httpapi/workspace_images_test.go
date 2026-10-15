package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestWorkspaceImageUploadKeepsPublishedFormatsAndRejectsGIF(t *testing.T) {
	f := newAPIFixture(t)
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var pixels bytes.Buffer
	if err := gif.Encode(&pixels, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(WorkspaceImageUploadRequestView{Version: WorkspaceImageUploadVersion,
		MIMEType: "image/gif", Name: "pixel.gif", DataBase64: base64.StdEncoding.EncodeToString(pixels.Bytes())})
	response := performSessionMessageRequest(t, api, http.MethodPost,
		"/api/v1/workspaces/"+f.workspace.ID+"/image-attachments", testControlToken, "reject-gif-upload", "application/json", bytes.NewReader(body))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("GIF upload changed the public image contract: %d %s", response.Code, response.Body)
	}
}

func TestWorkspaceImageHTTPImmutableUploadAuthenticatedBytesAndScope(t *testing.T) {
	f := newAPIFixture(t)
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(WorkspaceImageUploadRequestView{Version: WorkspaceImageUploadVersion, Name: "截图.png", MIMEType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(pixels.Bytes())})
	path := "/api/v1/workspaces/" + f.workspace.ID + "/image-attachments"
	var first WorkspaceImageView
	for index := 0; index < 2; index++ {
		response := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "http-image-upload-operation", "application/json", bytes.NewReader(body))
		var view WorkspaceImageView
		decodeDataStatus(t, response, http.StatusOK, &view)
		if index == 0 {
			first = view
		} else if view != first {
			t.Fatal("upload replay changed immutable image identity")
		}
	}
	for _, token := range []string{"", testControlToken} {
		response := performSessionMessageRequest(t, api, http.MethodGet, path+"/"+first.Image.ID+"/content", token, "", "", nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("content read accepted wrong bearer: %d", response.Code)
		}
	}
	response := performSessionMessageRequest(t, api, http.MethodGet, path+"/"+first.Image.ID+"/content", testAccessToken, "", "", nil)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), pixels.Bytes()) || response.Header().Get("ETag") != `"`+first.Image.SHA256+`"` || response.Header().Get("X-Cyberagent-Content-SHA256") != first.Image.SHA256 || response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("original content/headers mismatch: %d %v", response.Code, response.Header())
	}
	response = performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/workspaces/another-workspace/image-attachments/"+first.Image.ID+"/content", testAccessToken, "", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace read=%d", response.Code)
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "http-image-upload-operation", "application/json", strings.NewReader(strings.Replace(string(body), "截图.png", "different.png", 1)))
	if response.Code != http.StatusConflict {
		t.Fatalf("same key changed upload metadata: %d %s", response.Code, response.Body)
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testAccessToken, "http-image-upload-other-key", "application/json", bytes.NewReader(body))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("read bearer uploaded image: %d", response.Code)
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "http-image-upload-invalid", "application/json", strings.NewReader(strings.Replace(string(body), "image/png", "image/jpeg", 1)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("mismatched MIME accepted: %d", response.Code)
	}
}

func TestWorkspaceImageRunDetailKeepsSteeringListReadable(t *testing.T) {
	f := newAPIFixture(t)
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	uploaded, err := f.store.SaveWorkspaceImage(t.Context(), f.workspace.ID, "run-detail-image-upload-key", "image/png", "queue.png", content.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	queued, err := f.store.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{RunID: f.run.ID, SessionID: f.run.SessionID,
		Content: "", Images: []domain.ImageReference{{ID: uploaded.ID, SHA256: uploaded.SHA256}}, OperationKey: "run-detail-image-message-key", RequestedBy: "http_test"})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := f.store.ListOperatorSteering(t.Context(), f.run.ID, 20)
	if err != nil || len(listed) != 1 || listed[0].ID != queued.Message.ID || listed[0].ImageCount != 1 || listed[0].Content != "" {
		t.Fatalf("steering list lost its image-aware shape: %#v err=%v", listed, err)
	}
	var detail RunDetailView
	decodeData(t, f.get(t, "/api/v1/runs/"+f.run.ID), &detail)
	if detail.Run.ID != f.run.ID || detail.Steering.Pending != 1 || len(detail.Steering.Messages) != 1 || detail.Steering.Messages[0].ID != queued.Message.ID {
		t.Fatalf("run detail omitted original queued image input: %#v", detail.Steering)
	}
}

func TestThreadImagesHTTPForwardsEmptyOriginalTextAndRejectsUnknownFields(t *testing.T) {
	f := newAPIFixture(t)
	controller := &threadTurnControllerStub{}
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunExecutionEnabled: true, RunLifecycleController: application.NewRunLifecycleControlService(f.store), RunExecutionController: runExecutionControllerFake{}, ThreadTurnController: controller})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"version":"thread_message_submission.v1","content":"","images":[{"id":"image-example","sha256":"` + strings.Repeat("a", 64) + `"}]}`
	path := ThreadCollectionPath + "/" + domain.InitialThreadID(f.run.ID) + "/turns"
	response := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "http-image-turn-operation", "application/json", strings.NewReader(body))
	if controller.request.Content != "" || len(controller.request.Images) != 1 || controller.request.Images[0].ID != "image-example" {
		t.Fatalf("image-only body changed: %#v (%d)", controller.request, response.Code)
	}
	controller.request = application.ExecuteThreadTurnRequest{}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "http-image-turn-operation", "application/json", strings.NewReader(strings.Replace(body, `"id":"image-example"`, `"id":"image-example","path":"C:/private.png"`, 1)))
	if response.Code != http.StatusBadRequest || controller.request.ThreadID != "" {
		t.Fatalf("unknown path was accepted: %d", response.Code)
	}
}
