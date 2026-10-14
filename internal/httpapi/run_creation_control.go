package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

const RunCreationControlPath = "/api/v1/runs"

type RunCreationControlRequestView struct {
	Version           string                     `json:"version"`
	Goal              string                     `json:"goal"`
	WorkspaceID       string                     `json:"workspace_id"`
	Profile           string                     `json:"profile,omitempty"`
	Surface           string                     `json:"surface,omitempty"`
	Phase             string                     `json:"phase,omitempty"`
	NetworkMode       string                     `json:"network_mode,omitempty"`
	AllowedTargets    []string                   `json:"allowed_targets,omitempty"`
	WebAccessMode     domain.WebAccessMode       `json:"web_access_mode,omitempty"`
	WebAllowedTargets []string                   `json:"web_allowed_targets,omitempty"`
	Budget            *domain.TaskBudgetSettings `json:"budget,omitempty"`
}

type RunCreationControlView struct {
	Mission  MissionView `json:"mission"`
	Run      RunView     `json:"run"`
	Session  SessionView `json:"session"`
	Mode     RunModeView `json:"mode"`
	Replayed bool        `json:"replayed"`
}

func (a *API) serveRunCreationControl(writer http.ResponseWriter,
	request *http.Request, requestID string,
) {
	if !a.runCreationEnabled {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return
	}
	if !a.authorized(request, a.controlTokenHash) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent Control API"`)
		a.writeError(writer, requestID,
			apperror.New(apperror.CodePolicyDenied,
				"valid control bearer authorization is required"),
			http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeInvalidArgument,
				"Run creation endpoint only supports POST"),
			http.StatusMethodNotAllowed)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	operationKey, err := runCreationIdempotencyKey(request.Header)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxRunCreationRequestBodyBytes)
	if err != nil {
		status := 0
		if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeResourceExhausted {
			status = http.StatusRequestEntityTooLarge
		}
		a.writeError(writer, requestID, err, status)
		return
	}
	if !utf8.Valid(body) {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"Run creation body must be valid UTF-8 JSON"), 0)
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, "Run creation"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view RunCreationControlRequestView
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&view); err != nil {
		a.writeError(writer, requestID, apperror.Wrap(apperror.CodeInvalidArgument,
			"Run creation body must be one JSON object", err), 0)
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := application.NewControlledRunCreationService(a.store).
		WithLifecycleHooks(a.lifecycleHooks).Create(
		request.Context(), application.ControlledRunCreationRequest{
			Version: view.Version, Goal: view.Goal, WorkspaceID: view.WorkspaceID,
			Profile: view.Profile, Surface: view.Surface, Phase: view.Phase,
			NetworkMode: view.NetworkMode, Budget: view.Budget,
			AllowedTargets: append([]string(nil), view.AllowedTargets...),
			WebAccessMode:  string(view.WebAccessMode), WebAllowedTargets: view.WebAllowedTargets,
			OperationKey: operationKey, RequestedBy: "http_control",
		})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, RunCreationControlView{
		Mission: missionView(result.Mission), Run: runView(result.Run),
		Session: sessionView(result.Session), Mode: runModeView(result.Mode),
		Replayed: result.Replayed,
	}, nil, http.StatusAccepted)
}

func runCreationIdempotencyKey(header http.Header) (string, error) {
	values := header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"Run creation requires exactly one Idempotency-Key header")
	}
	value, err := domain.NormalizeAgentOperationKey(values[0])
	if err != nil {
		return "", apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	return value, nil
}

func rejectDuplicateJSONObjectFields(body []byte, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return apperror.New(apperror.CodeInvalidArgument,
			label+" body must be one JSON object")
	}
	if err := rejectDuplicateJSONObject(decoder, label); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return apperror.New(apperror.CodeInvalidArgument,
			label+" body contains trailing data")
	}
	return nil
}

func rejectDuplicateJSONObject(decoder *json.Decoder, label string) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return apperror.New(apperror.CodeInvalidArgument,
				label+" body must be one JSON object")
		}
		name, ok := key.(string)
		if !ok {
			return apperror.New(apperror.CodeInvalidArgument,
				label+" body contains an invalid field")
		}
		if _, duplicate := seen[name]; duplicate {
			return apperror.New(apperror.CodeInvalidArgument,
				fmt.Sprintf("%s body contains duplicate field %q", label, name))
		}
		seen[name] = struct{}{}
		if err := rejectDuplicateJSONValue(decoder, label, name); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return apperror.New(apperror.CodeInvalidArgument,
			label+" body must be one JSON object")
	}
	return nil
}

func rejectDuplicateJSONValue(decoder *json.Decoder, label string, fieldName ...string) error {
	token, err := decoder.Token()
	if err != nil {
		return apperror.New(apperror.CodeInvalidArgument,
			label+" body contains an invalid field value")
	}
	if len(fieldName) > 0 && fieldName[0] == "budget" && token == nil {
		return apperror.New(apperror.CodeInvalidArgument, label+" budget must be a JSON object")
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		return rejectDuplicateJSONObject(decoder, label)
	case '[':
		for decoder.More() {
			if err := rejectDuplicateJSONValue(decoder, label); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return apperror.New(apperror.CodeInvalidArgument,
				label+" body contains an invalid field value")
		}
		return nil
	default:
		return apperror.New(apperror.CodeInvalidArgument,
			label+" body contains an invalid field value")
	}
}
