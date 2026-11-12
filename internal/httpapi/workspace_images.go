package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
)

const WorkspaceImageUploadVersion = "workspace_image_upload.v1"

type WorkspaceImageUploadRequestView struct {
	Version    string `json:"version"`
	Name       string `json:"name,omitempty"`
	MIMEType   string `json:"mime_type"`
	DataBase64 string `json:"data_base64"`
}

type WorkspaceImageView struct {
	Image domain.WorkspaceImage `json:"image"`
}

type WorkspaceImageUploadObservationView struct {
	State string                 `json:"state"`
	Image *domain.WorkspaceImage `json:"image,omitempty"`
}

type workspaceImageStore interface {
	SaveWorkspaceImage(context.Context, string, string, string, string, []byte) (domain.WorkspaceImage, error)
	GetWorkspaceImage(context.Context, string, string) (domain.WorkspaceImage, []byte, error)
	ListOperatorMessageImages(context.Context, string, string) ([]domain.WorkspaceImage, error)
}

func matchWorkspaceImagePath(path string) (workspaceID, imageID string, content, matched bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if !strings.HasPrefix(path, "/api/v1/") || len(parts) < 3 || len(parts) > 5 || parts[0] != "workspaces" || parts[2] != "image-attachments" {
		return
	}
	workspaceID = parts[1]
	if len(parts) >= 4 {
		imageID = parts[3]
		if imageID == "" {
			return "", "", false, false
		}
	}
	if len(parts) == 5 {
		if parts[4] != "content" {
			return "", "", false, false
		}
		content = true
	}
	return workspaceID, imageID, content, true
}

func (a *API) serveWorkspaceImage(writer http.ResponseWriter, request *http.Request, requestID, workspaceID, imageID string, content bool) {
	store, ok := a.store.(workspaceImageStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "Image attachments are unavailable"), 0)
		return
	}
	if err := validatePathIdentity(workspaceID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if imageID != "" {
		if !a.authorized(request, a.tokenHash) {
			a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied, "valid read bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", "GET")
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Image reads only support GET"), http.StatusMethodNotAllowed)
			return
		}
		if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Image reads cannot contain a body"), 0)
			return
		}
		if err := validatePathIdentity(imageID); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if imageID == "request" {
			if content {
				a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Image upload lookup cannot request content"), 0)
				return
			}
			key, err := sessionControlIdempotencyKey(request.Header, "image upload lookup")
			if err != nil {
				a.writeError(writer, requestID, err, 0)
				return
			}
			reader, ok := a.store.(interface {
				GetWorkspaceImageByOperationKey(context.Context, string, string) (domain.WorkspaceImage, bool, error)
			})
			if !ok {
				a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "Image upload lookup is unavailable"), 0)
				return
			}
			image, found, err := reader.GetWorkspaceImageByOperationKey(request.Context(), workspaceID, key)
			if err != nil {
				a.writeError(writer, requestID, err, 0)
				return
			}
			value := WorkspaceImageUploadObservationView{State: "not_received"}
			if found {
				value.State, value.Image = "stored", &image
			}
			writer.Header().Set("Cache-Control", "no-store")
			a.writeSuccess(writer, requestID, value, nil)
			return
		}
		image, data, err := store.GetWorkspaceImage(request.Context(), workspaceID, imageID)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if !content {
			a.writeSuccess(writer, requestID, WorkspaceImageView{Image: image}, nil)
			return
		}
		writer.Header().Set("Content-Type", image.MIMEType)
		writer.Header().Set("Content-Length", strconv.Itoa(image.ByteSize))
		writer.Header().Set("ETag", `"`+image.SHA256+`"`)
		writer.Header().Set("X-Cyberagent-Content-SHA256", image.SHA256)
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(data)
		return
	}
	if !a.runCreationEnabled || !a.sessionMessageEnabled {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "Image uploads are unavailable"), 0)
		return
	}
	if !a.authorizeThreadControl(writer, request, requestID) {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Image uploads only support POST"), http.StatusMethodNotAllowed)
		return
	}
	key, err := sessionControlIdempotencyKey(request.Header, "image upload")
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	body, err := readBoundedRequestBody(request, int64(base64.StdEncoding.EncodedLen(imageattachment.MaxBytes)+4096))
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view WorkspaceImageUploadRequestView
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(body) || rejectDuplicateJSONObjectFields(body, "image upload") != nil || decoder.Decode(&view) != nil || ensureJSONEOF(decoder) != nil || view.Version != WorkspaceImageUploadVersion {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Image upload payload is invalid"), 0)
		return
	}
	data, err := base64.StdEncoding.Strict().DecodeString(view.DataBase64)
	if err != nil || base64.StdEncoding.EncodeToString(data) != view.DataBase64 {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Image data must be canonical base64"), 0)
		return
	}
	image, err := store.SaveWorkspaceImage(request.Context(), workspaceID, key, view.MIMEType, view.Name, data)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccess(writer, requestID, WorkspaceImageView{Image: image}, nil)
}
