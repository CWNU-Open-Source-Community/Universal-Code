package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/projectconfig"
)

const MaxContextContinuityRequestBodyBytes = 128 * 1024

type contextMemoryCreateRequestView struct {
	Scope           contextmgr.MemoryScope `json:"scope"`
	ScopeID         string                 `json:"scope_id"`
	Title           string                 `json:"title"`
	Content         string                 `json:"content"`
	SourceRef       string                 `json:"source_ref,omitempty"`
	References      []string               `json:"references,omitempty"`
	RetentionUntil  *time.Time             `json:"retention_until,omitempty"`
	RedactSensitive bool                   `json:"redact_sensitive,omitempty"`
}

type contextMemoryUpdateRequestView struct {
	ExpectedVersion int64                    `json:"expected_version"`
	Title           *string                  `json:"title,omitempty"`
	Content         *string                  `json:"content,omitempty"`
	SourceRef       *string                  `json:"source_ref,omitempty"`
	References      *[]string                `json:"references,omitempty"`
	RetentionUntil  json.RawMessage          `json:"retention_until,omitempty"`
	Status          *contextmgr.MemoryStatus `json:"status,omitempty"`
	RedactSensitive bool                     `json:"redact_sensitive,omitempty"`
}

type contextMemoryDeleteRequestView struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type projectInstructionRefreshRequestView struct {
	TargetPath              string                                    `json:"target_path,omitempty"`
	ExpectedFingerprint     string                                    `json:"expected_fingerprint"`
	ExpectedLiveFingerprint string                                    `json:"expected_live_fingerprint"`
	Confirm                 bool                                      `json:"confirm"`
	Classifications         []projectconfig.InstructionSourceDelivery `json:"classifications,omitempty"`
}

type continuityCheckpointRequestView struct {
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type continuityBranchRequestView struct {
	Goal string `json:"goal,omitempty"`
}

type continuityBranchView struct {
	Mission         MissionView               `json:"mission"`
	Run             RunView                   `json:"run"`
	Node            contextmgr.ContinuityNode `json:"node"`
	Inherited       []string                  `json:"inherited"`
	NotInherited    []string                  `json:"not_inherited"`
	CapabilityGrant bool                      `json:"capability_grant"`
}

type contextMemoryDeleteView struct {
	ID          string `json:"id"`
	Deleted     bool   `json:"deleted"`
	Recoverable bool   `json:"recoverable"`
}

func isContextContinuityMutationPath(requestPath string) bool {
	segments := strings.Split(strings.TrimPrefix(requestPath, "/api/v1/"), "/")
	if len(segments) == 3 && segments[0] == "memory-candidates" &&
		(segments[2] == "confirm" || segments[2] == "reject") {
		return true
	}
	if len(segments) == 1 && segments[0] == "memories" {
		return true
	}
	if len(segments) == 2 && segments[0] == "memories" && segments[1] != "export" {
		return true
	}
	if len(segments) == 4 && segments[0] == "runs" &&
		segments[2] == "project-instructions" && segments[3] == "refresh" {
		return true
	}
	if len(segments) == 3 && segments[0] == "runs" && segments[2] == "continuity-checkpoints" {
		return true
	}
	return len(segments) == 3 && segments[0] == "continuity-nodes" &&
		(segments[2] == "fork" || segments[2] == "resume")
}

func (a *API) serveContextContinuityMutation(writer http.ResponseWriter,
	request *http.Request, requestID string,
) {
	if !a.controlEnabled {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return
	}
	if !a.authorized(request, a.controlTokenHash) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent Control API"`)
		a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied,
			"valid control bearer authorization is required"), http.StatusUnauthorized)
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
	body, err := readBoundedRequestBody(request, MaxContextContinuityRequestBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, "context continuity"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	segments := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/v1/"), "/")
	for _, segment := range segments {
		if err := validatePathIdentity(segment); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
	}
	switch {
	case len(segments) == 3 && segments[0] == "memory-candidates" &&
		(segments[2] == "confirm" || segments[2] == "reject"):
		a.serveContextMemoryCandidateReview(writer, request, requestID, segments[1], segments[2], body)
	case len(segments) == 1 && segments[0] == "memories":
		a.serveContextMemoryCreate(writer, request, requestID, body)
	case len(segments) == 2 && segments[0] == "memories":
		a.serveContextMemoryMutation(writer, request, requestID, segments[1], body)
	case len(segments) == 4 && segments[0] == "runs" &&
		segments[2] == "project-instructions" && segments[3] == "refresh":
		a.serveProjectInstructionRefresh(writer, request, requestID, segments[1], body)
	case len(segments) == 3 && segments[0] == "runs" && segments[2] == "continuity-checkpoints":
		a.serveContinuityCheckpoint(writer, request, requestID, segments[1], body)
	case len(segments) == 3 && segments[0] == "continuity-nodes" &&
		(segments[2] == "fork" || segments[2] == "resume"):
		a.serveContinuityBranch(writer, request, requestID, segments[1], segments[2], body)
	default:
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
	}
}

func (a *API) serveContextMemoryCreate(writer http.ResponseWriter, request *http.Request,
	requestID string, body []byte,
) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"memory collection mutation only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	store, ok := any(a.store).(application.ContextMemoryStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
			"long-term memory store is unavailable"), 0)
		return
	}
	var view contextMemoryCreateRequestView
	if err := decodeContextContinuityBody(body, &view); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	value, err := application.NewContextMemoryService(store).Create(request.Context(),
		contextmgr.CreateMemoryRequest{Scope: view.Scope, ScopeID: view.ScopeID,
			Title: view.Title, Content: view.Content, SourceRef: view.SourceRef,
			References: view.References, RetentionUntil: view.RetentionUntil,
			RequestedBy: "http_control", RedactSensitive: view.RedactSensitive})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, value, nil, http.StatusCreated)
}

func (a *API) serveContextMemoryMutation(writer http.ResponseWriter, request *http.Request,
	requestID, id string, body []byte,
) {
	if request.Method != http.MethodPatch && request.Method != http.MethodDelete {
		writer.Header().Set("Allow", http.MethodPatch+", "+http.MethodDelete)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"memory item mutation only supports PATCH or DELETE"), http.StatusMethodNotAllowed)
		return
	}
	store, ok := any(a.store).(application.ContextMemoryStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
			"long-term memory store is unavailable"), 0)
		return
	}
	service := application.NewContextMemoryService(store)
	if request.Method == http.MethodDelete {
		var view contextMemoryDeleteRequestView
		if err := decodeContextContinuityBody(body, &view); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		deleted, err := service.Delete(request.Context(), id, view.ExpectedVersion, "http_control")
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccess(writer, requestID, contextMemoryDeleteView{ID: id,
			Deleted: deleted, Recoverable: false}, nil)
		return
	}
	var view contextMemoryUpdateRequestView
	if err := decodeContextContinuityBody(body, &view); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	requestValue := contextmgr.UpdateMemoryRequest{Title: view.Title, Content: view.Content,
		SourceRef: view.SourceRef, References: view.References, Status: view.Status,
		ExpectedVersion: view.ExpectedVersion, RequestedBy: "http_control",
		RedactSensitive: view.RedactSensitive}
	if len(view.RetentionUntil) > 0 {
		var retention *time.Time
		if string(view.RetentionUntil) != "null" {
			var value time.Time
			if err := json.Unmarshal(view.RetentionUntil, &value); err != nil {
				a.writeError(writer, requestID, apperror.Wrap(apperror.CodeInvalidArgument,
					"retention_until must be RFC3339 or null", err), 0)
				return
			}
			retention = &value
		}
		requestValue.RetentionUntil = &retention
	}
	value, err := service.Update(request.Context(), id, requestValue)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccess(writer, requestID, value, nil)
}

func (a *API) serveProjectInstructionRefresh(writer http.ResponseWriter, request *http.Request,
	requestID, runID string, body []byte,
) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"project instruction refresh only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	store, ok := any(a.store).(application.ProjectInstructionStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
			"project instruction store is unavailable"), 0)
		return
	}
	var view projectInstructionRefreshRequestView
	if err := decodeContextContinuityBody(body, &view); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	state, err := application.NewProjectInstructionService(store).RefreshWithDelivery(request.Context(),
		runID, view.TargetPath, view.ExpectedFingerprint, view.ExpectedLiveFingerprint,
		"http_control", view.Confirm, view.Classifications)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccess(writer, requestID, state, nil)
}

func (a *API) serveContinuityCheckpoint(writer http.ResponseWriter, request *http.Request,
	requestID, runID string, body []byte,
) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"continuity checkpoint only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	store, ok := any(a.store).(application.ContextContinuityStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
			"context continuity store is unavailable"), 0)
		return
	}
	var view continuityCheckpointRequestView
	if err := decodeContextContinuityBody(body, &view); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	node, err := application.NewContextContinuityService(store).
		WithLifecycleHooks(a.lifecycleHooks).Checkpoint(request.Context(),
		application.CreateContinuityCheckpointRequest{RunID: runID, Title: view.Title,
			Summary: view.Summary, RequestedBy: "http_control"})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, node, nil, http.StatusCreated)
}

func (a *API) serveContinuityBranch(writer http.ResponseWriter, request *http.Request,
	requestID, sourceNodeID, action string, body []byte,
) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"continuity branch only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	store, ok := any(a.store).(application.ContextContinuityStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition,
			"context continuity store is unavailable"), 0)
		return
	}
	var view continuityBranchRequestView
	if err := decodeContextContinuityBody(body, &view); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	kind := contextmgr.ContinuityNodeFork
	if action == "resume" {
		kind = contextmgr.ContinuityNodeResume
	}
	result, err := application.NewContextContinuityService(store).
		WithLifecycleHooks(a.lifecycleHooks).Branch(request.Context(),
		application.BranchContinuityRequest{SourceNodeID: sourceNodeID, Kind: kind,
			Goal: view.Goal, RequestedBy: "http_control"})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, continuityBranchView{
		Mission: missionView(result.Mission), Run: runView(result.Run), Node: result.Node,
		Inherited: result.Inherited, NotInherited: result.NotInherited,
		CapabilityGrant: false}, nil, http.StatusCreated)
}

func decodeContextContinuityBody(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"context continuity body must be one JSON object", err)
	}
	return ensureJSONEOF(decoder)
}

func (a *API) contextMemories(request *http.Request,
	segments []string,
) (any, *Page, error) {
	store, ok := any(a.store).(application.ContextMemoryStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition,
			"long-term memory store is unavailable")
	}
	service := application.NewContextMemoryService(store)
	if len(segments) == 2 && segments[1] != "export" {
		if err := rejectQuery(request.URL.Query()); err != nil {
			return nil, nil, err
		}
		value, err := service.Get(request.Context(), segments[1])
		return value, nil, err
	}
	if len(segments) > 2 || (len(segments) == 2 && segments[1] != "export") {
		return nil, nil, apperror.New(apperror.CodeNotFound, "memory endpoint was not found")
	}
	values := request.URL.Query()
	if err := validateSingleQueryValues(values, "scope", "scope_id", "include_disabled",
		"include_expired", "limit"); err != nil {
		return nil, nil, err
	}
	scope := contextmgr.MemoryScope(values.Get("scope"))
	scopeID := values.Get("scope_id")
	if scope == "" {
		if scopeID != "" {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"memory scope_id requires an explicit scope")
		}
		scope = contextmgr.MemoryScopeUser
		scopeID = contextmgr.LocalUserMemoryScope
	} else if scope == contextmgr.MemoryScopeUser && scopeID == "" {
		scopeID = contextmgr.LocalUserMemoryScope
	}
	includeDisabled, err := parseOptionalBool(values.Get("include_disabled"))
	if err != nil {
		return nil, nil, err
	}
	includeExpired, err := parseOptionalBool(values.Get("include_expired"))
	if err != nil {
		return nil, nil, err
	}
	limit := 0
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument, "memory limit must be an integer")
		}
	}
	filter := contextmgr.MemoryFilter{Scope: scope, ScopeID: scopeID,
		IncludeDisabled: includeDisabled, IncludeExpired: includeExpired, Limit: limit}
	if len(segments) == 2 {
		value, err := service.Export(request.Context(), filter)
		return value, nil, err
	}
	items, err := service.List(request.Context(), filter)
	return items, nil, err
}

func (a *API) runProjectInstructions(request *http.Request,
	runID string,
) (any, *Page, error) {
	if err := validateSingleQueryValues(request.URL.Query(), "target_path"); err != nil {
		return nil, nil, err
	}
	store, ok := any(a.store).(application.ProjectInstructionStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition,
			"project instruction store is unavailable")
	}
	state, err := application.NewProjectInstructionService(store).Inspect(request.Context(),
		runID, request.URL.Query().Get("target_path"))
	return state, nil, err
}

func (a *API) sessionContinuityTree(request *http.Request,
	sessionID string,
) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	store, ok := any(a.store).(application.ContextContinuityStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition,
			"context continuity store is unavailable")
	}
	tree, err := application.NewContextContinuityService(store).Tree(request.Context(), sessionID)
	return tree, nil, err
}

func parseOptionalBool(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, apperror.New(apperror.CodeInvalidArgument,
			"boolean query parameter must be true or false")
	}
	return parsed, nil
}
