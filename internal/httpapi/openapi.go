package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/buildinfo"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileattachment"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/operatoraction"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/scheduler"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/standardcodedelivery"
	"cyberagent-workbench/internal/threadtranscript"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/uievidence"
	"cyberagent-workbench/internal/verification"
	"cyberagent-workbench/internal/webevidence"
	"cyberagent-workbench/internal/workspace"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

const (
	openAPISpecVersion       = "3.1.0"
	openAPIJSONSchemaDialect = "https://json-schema.org/draft/2020-12/schema"
	openAPIContentType       = "application/vnd.oai.openapi+json"
)

type openAPIDocument struct {
	OpenAPI           string                     `json:"openapi"`
	JSONSchemaDialect string                     `json:"jsonSchemaDialect"`
	Info              openAPIInfo                `json:"info"`
	Servers           []openAPIServer            `json:"servers"`
	Security          []map[string][]string      `json:"security"`
	Tags              []openAPITag               `json:"tags"`
	Paths             map[string]openAPIPathItem `json:"paths"`
	Components        openAPIComponents          `json:"components"`
	ReadOnly          bool                       `json:"x-cyberagent-read-only"`
}

type openAPIInfo struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	License     openAPILicense `json:"license"`
}

type openAPILicense struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
}

type openAPIServer struct {
	URL         string `json:"url"`
	Description string `json:"description"`
}

type openAPITag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type openAPIPathItem struct {
	Get    *openAPIOperation `json:"get,omitempty"`
	Post   *openAPIOperation `json:"post,omitempty"`
	Put    *openAPIOperation `json:"put,omitempty"`
	Patch  *openAPIOperation `json:"patch,omitempty"`
	Delete *openAPIOperation `json:"delete,omitempty"`
}

type openAPIOperation struct {
	OperationID string                `json:"operationId"`
	Summary     string                `json:"summary"`
	Description string                `json:"description"`
	Tags        []string              `json:"tags"`
	Parameters  []openAPIParameter    `json:"parameters,omitempty"`
	Responses   map[string]any        `json:"responses"`
	Security    []map[string][]string `json:"security,omitempty"`
	RequestBody *openAPIRequestBody   `json:"requestBody,omitempty"`
	ReadOnly    bool                  `json:"x-cyberagent-read-only"`
	Streaming   bool                  `json:"x-cyberagent-streaming,omitempty"`
}

type openAPIRequestBody struct {
	Required bool                        `json:"required"`
	Content  map[string]openAPIMediaType `json:"content"`
}

type openAPIParameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Description string         `json:"description"`
	Required    bool           `json:"required,omitempty"`
	Style       string         `json:"style,omitempty"`
	Explode     *bool          `json:"explode,omitempty"`
	Schema      map[string]any `json:"schema"`
}

type openAPIComponents struct {
	Schemas         map[string]map[string]any      `json:"schemas"`
	Responses       map[string]openAPIResponse     `json:"responses"`
	SecuritySchemes map[string]openAPISecurityType `json:"securitySchemes"`
}

type openAPISecurityType struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme"`
	BearerFormat string `json:"bearerFormat"`
	Description  string `json:"description"`
}

type openAPIResponse struct {
	Description string                      `json:"description"`
	Content     map[string]openAPIMediaType `json:"content"`
}

type openAPIMediaType struct {
	Schema map[string]any `json:"schema"`
}

type openAPIOperationSpec struct {
	Path          string
	Method        string
	OperationID   string
	Summary       string
	Description   string
	Tag           string
	DataType      reflect.Type
	Collection    bool
	Paginated     bool
	NotFound      bool
	RawDocument   bool
	RawArtifact   bool
	Streaming     bool
	Parameters    []openAPIParameter
	RequestType   reflect.Type
	EmptyBody     bool
	Control       bool
	SuccessStatus int
}

// GenerateOpenAPI creates the canonical client contract from the Go response DTOs.
func GenerateOpenAPI() ([]byte, error) {
	registry := newOpenAPISchemaRegistry()
	paths := make(map[string]openAPIPathItem)
	for _, spec := range openAPIOperationSpecs() {
		operation, err := buildOpenAPIOperation(spec, registry)
		if err != nil {
			return nil, err
		}
		item := paths[spec.Path]
		switch spec.Method {
		case "", http.MethodGet:
			if item.Get != nil {
				return nil, fmt.Errorf("duplicate OpenAPI GET path %q", spec.Path)
			}
			item.Get = &operation
		case http.MethodPost:
			if item.Post != nil {
				return nil, fmt.Errorf("duplicate OpenAPI POST path %q", spec.Path)
			}
			item.Post = &operation
		case http.MethodPut:
			if item.Put != nil {
				return nil, fmt.Errorf("duplicate OpenAPI PUT path %q", spec.Path)
			}
			item.Put = &operation
		case http.MethodPatch:
			if item.Patch != nil {
				return nil, fmt.Errorf("duplicate OpenAPI PATCH path %q", spec.Path)
			}
			item.Patch = &operation
		case http.MethodDelete:
			if item.Delete != nil {
				return nil, fmt.Errorf("duplicate OpenAPI DELETE path %q", spec.Path)
			}
			item.Delete = &operation
		default:
			return nil, fmt.Errorf("unsupported OpenAPI method %q", spec.Method)
		}
		paths[spec.Path] = item
	}
	if registry.err != nil {
		return nil, registry.err
	}
	document := openAPIDocument{
		OpenAPI:           openAPISpecVersion,
		JSONSchemaDialect: openAPIJSONSchemaDialect,
		Info: openAPIInfo{
			Title: buildinfo.ProductName + " Local API",
			Description: "Authenticated loopback-only API owned by the Go control plane. " +
				"Read operations expose durable metadata; separately authorized control capabilities permit exact active-call cancellation, non-authorizing execution-profile selection, idempotent creation of a closed workspace-bound Run, durable Run-bound Session steering, idempotent Run lifecycle transitions, and bounded execution through the Go Supervisor.",
			Version: Version,
			License: openAPILicense{Name: "Apache License 2.0", Identifier: "Apache-2.0"},
		},
		Servers:  []openAPIServer{{URL: "http://127.0.0.1:8765", Description: "Default loopback server"}},
		Security: []map[string][]string{{"BearerAuth": {}}},
		Tags: []openAPITag{
			{Name: "System", Description: "API discovery, health, and contract metadata."},
			{Name: "Models", Description: "Redacted Go-owned Provider and model-route availability."},
			{Name: "Threads", Description: "Stable user-facing task identity, Run succession, and lossless lifecycle history."},
			{Name: "Runs", Description: "Durable Run state and Run-scoped projections."},
			{Name: "Git", Description: "Approval-gated, preview-bound advanced Git operations and durable recovery state."},
			{Name: "GitHub Review", Description: "GitHub App authentication, sanitized PR/CI evidence, local mappings, and approval-gated review write-back."},
			{Name: "Agents", Description: "Bounded Agent graph and operator-gated delegation projections."},
			{Name: "Analysis", Description: "Read-only Fan-out plans and execution summaries."},
			{Name: "Reports", Description: "Finding reports and redacted lifecycle evidence summaries."},
			{Name: "Sessions", Description: "Persisted Session metadata and redacted messages."},
			{Name: "Workspaces", Description: "Registered Workspace identities without local root paths."},
			{Name: "Memory", Description: "Structured WorkItems and Notes."},
			{Name: "Artifacts", Description: "Content-free Artifact descriptors."},
			{Name: "UI Evidence", Description: "Source-bound real-browser manifests, fail-closed receipts, and untrusted captured artifacts."},
			{Name: "Sandbox", Description: "Docker Sandbox readiness, admission, bounded execution, cancellation, and status."},
			{Name: "Automation", Description: "Durable, bounded, explicitly targeted scheduled observation jobs."},
			{Name: "Diagnostics", Description: "Read-only structured readiness and redacted event timelines."},
			{Name: "Control", Description: "Separately authorized, audit-first control operations."},
		},
		Paths: paths,
		Components: openAPIComponents{
			Schemas:   registry.schemas,
			Responses: standardOpenAPIErrorResponses(registry),
			SecuritySchemes: map[string]openAPISecurityType{
				"BearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "opaque",
					Description: "Process-scoped read token; never persisted by " + buildinfo.ProductName + "."},
				"ControlBearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "opaque",
					Description: "Distinct optional control token; cannot authorize read operations and is never persisted."},
			},
		},
		ReadOnly: false,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode OpenAPI document: %w", err)
	}
	return append(encoded, '\n'), nil
}

func openAPIOperationSpecs() []openAPIOperationSpec {
	threadID := pathIdentityParameter("thread_id", "Thread identity")
	activityRef := pathIdentityParameter("activity_ref",
		"Opaque durable activity detail reference from the Thread transcript")
	runID := pathIdentityParameter("run_id", "Run identity")
	executionID := pathIdentityParameter("execution_id", "Read-only Fan-out execution identity")
	childTaskProposalID := pathIdentityParameter("proposal_id", "Child task proposal identity")
	batchDeliveryID := pathIdentityParameter("batch_delivery_id", "Batch delivery identity")
	batchDeliveryOrdinal := openAPIParameter{Name: "ordinal", In: "path",
		Description: "Batch delivery child ordinal", Required: true,
		Schema: map[string]any{"type": "integer", "minimum": 1,
			"maximum": domain.MaxBatchDeliveryTasks}}
	verificationPlanID := pathIdentityParameter("plan_id", "Verification plan identity")
	verificationPlanItemOrdinal := openAPIParameter{Name: "ordinal", In: "path",
		Description: "Verification plan item ordinal", Required: true,
		Schema: map[string]any{"type": "integer", "minimum": 1,
			"maximum": verification.MaxPlanItems}}
	agentID := pathIdentityParameter("agent_id", "Specialist Agent identity")
	sessionID := pathIdentityParameter("session_id", "Session identity")
	messageID := pathIdentityParameter("message_id", "Operator steering message identity")
	workItemID := pathIdentityParameter("work_item_id", "WorkItem identity")
	noteID := pathIdentityParameter("note_id", "Note identity")
	artifactID := pathIdentityParameter("artifact_id", "Artifact identity")
	uiEvidenceAttemptID := pathIdentityParameter("attempt_id", "UI evidence attempt identity")
	memoryID := pathIdentityParameter("memory_id", "Long-term memory identity")
	continuityNodeID := pathIdentityParameter("node_id", "Continuity checkpoint identity")
	reportID := pathIdentityParameter("report_id", "Finding Report identity")
	approvalID := pathIdentityParameter("approval_id", "Approval identity")
	githubReviewConnectionID := pathIdentityParameter("connection_id",
		"GitHub review connection identity")
	proposalID := pathIdentityParameter("proposal_id",
		"Controlled command proposal identity")
	editID := pathIdentityParameter("edit_id", "File edit identity")
	jobID := pathIdentityParameter("job_id", "Scheduled job identity")
	scheduledJobAction := openAPIParameter{Name: "action", In: "path",
		Description: "Scheduled job lifecycle action", Required: true,
		Schema: map[string]any{"type": "string", "enum": []string{
			string(domain.ScheduledJobPause), string(domain.ScheduledJobResume),
			string(domain.ScheduledJobCancel)}}}
	workspaceID := pathIdentityParameter("workspace_id", "Workspace identity")
	commitObjectID := openAPIParameter{Name: "object_id", In: "path",
		Description: "Exact lowercase forty-character Git commit object identity",
		Required:    true, Schema: map[string]any{"type": "string", "minLength": 40,
			"maxLength": 40, "pattern": `^[0-9a-f]{40}$`}}
	routeName := pathIdentityParameter("route", "Model route name")
	credentialProviderName := pathIdentityParameter("provider",
		"Built-in or persistent custom Provider credential identity")
	dockerSandboxIdempotencyKey := openAPIParameter{Name: "Idempotency-Key", In: "header",
		Description: "Opaque retry key; only a domain-separated digest is persisted",
		Required:    true, Schema: map[string]any{"type": "string",
			"minLength": domain.MinAgentOperationKeyBytes,
			"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}
	standardCodePresetIdempotencyKey := dockerSandboxIdempotencyKey
	standardCodePresetIdempotencyKey.Description =
		"Opaque Standard Code preset retry key; changed intent conflicts and only a domain-separated digest is persisted"
	batchDeliveryIdempotencyKey := dockerSandboxIdempotencyKey
	batchDeliveryIdempotencyKey.Description =
		"Opaque batch delivery retry key; only a domain-separated digest is persisted"
	batchWorkbenchIdempotencyKey := batchDeliveryIdempotencyKey
	batchWorkbenchIdempotencyKey.Schema = map[string]any{"type": "string",
		"minLength": domain.MinAgentOperationKeyBytes, "maxLength": domain.MaxAgentOperationKeyBytes - 32, "pattern": `^\S+$`}
	return []openAPIOperationSpec{
		{Path: "/api/v1/memories", OperationID: "listContextMemories",
			Summary: "List explicit long-term memories", Tag: "Memory",
			Description: "Returns only explicitly stored user or project memories. Memory is preference/factual context and never grants authority.",
			DataType:    reflect.TypeOf(contextmgr.Memory{}), Collection: true,
			Parameters: []openAPIParameter{
				stringQueryParameter("scope", "Memory scope", []string{"user", "project"}),
				identityQueryParameter("scope_id", "local-user or Workspace identity"),
				booleanQueryParameter("include_disabled", "Include disabled memories"),
				booleanQueryParameter("include_expired", "Include expired memories"),
				{Name: "limit", In: "query", Description: "Maximum items", Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": contextmgr.MaxMemoryListItems}},
			}},
		{Path: "/api/v1/memories", Method: http.MethodPost,
			OperationID: "createContextMemory", Summary: "Create explicit long-term memory",
			Tag: "Control", Description: "Stores one explicit, provenance-bound memory after secret and sensitive-source checks. It cannot be invoked by model context and grants no authority.",
			DataType: reflect.TypeOf(contextmgr.Memory{}), RequestType: reflect.TypeOf(contextMemoryCreateRequestView{}),
			Control: true, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/memories/export", OperationID: "exportContextMemories",
			Summary: "Export long-term memories", Tag: "Memory",
			Description: "Exports active, disabled, and expired records with provenance, retention, and an explicit false capability projection.",
			DataType:    reflect.TypeOf(application.ContextMemoryExport{}),
			Parameters: []openAPIParameter{
				stringQueryParameter("scope", "Memory scope", []string{"user", "project"}),
				identityQueryParameter("scope_id", "local-user or Workspace identity"),
			}},
		{Path: "/api/v1/memories/{memory_id}", OperationID: "getContextMemory",
			Summary: "Inspect one long-term memory", Tag: "Memory",
			Description: "Returns the exact redacted content, provenance, status, retention, digest, and optimistic version.",
			DataType:    reflect.TypeOf(contextmgr.Memory{}), NotFound: true,
			Parameters: []openAPIParameter{memoryID}},
		{Path: "/api/v1/memories/{memory_id}", Method: http.MethodPatch,
			OperationID: "updateContextMemory", Summary: "Edit, enable, or disable memory",
			Tag: "Control", Description: "Updates an explicit memory under optimistic version checking and repeats secret/source validation.",
			DataType: reflect.TypeOf(contextmgr.Memory{}), RequestType: reflect.TypeOf(contextMemoryUpdateRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{memoryID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/memories/{memory_id}", Method: http.MethodDelete,
			OperationID: "deleteContextMemory", Summary: "Permanently delete memory",
			Tag: "Control", Description: "Physically deletes one exact memory version; future prompts and exports cannot recover it.",
			DataType: reflect.TypeOf(contextMemoryDeleteView{}), RequestType: reflect.TypeOf(contextMemoryDeleteRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{memoryID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/project-instructions",
			OperationID: "getRunProjectInstructions", Summary: "Explain effective project instructions",
			Tag: "Runs", Description: "Returns pinned and live hierarchical sources, hashes, scope, precedence, why-effective details, conflicts, drift, and history without changing the Run.",
			DataType: reflect.TypeOf(application.ProjectInstructionState{}), NotFound: true,
			Parameters: []openAPIParameter{runID, stringQueryParameter("target_path", "Workspace-relative target", nil)}},
		{Path: "/api/v1/runs/{run_id}/project-instructions/refresh", Method: http.MethodPost,
			OperationID: "refreshRunProjectInstructions", Summary: "Confirm project instruction refresh",
			Tag: "Control", Description: "Confirms a diff bound to both the pinned and reviewed-live fingerprints. Files never alter an active Run silently, and refreshed instructions remain non-authorizing.",
			DataType: reflect.TypeOf(application.ProjectInstructionState{}), RequestType: reflect.TypeOf(projectInstructionRefreshRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/context-summary", OperationID: "getRunContextSummary",
			Summary: "Read saved context summaries for one execution", Tag: "Runs",
			Description: "Returns the exact Run, Thread and Session binding, latest persisted compaction and inherited context. Saved content is not a claim about the current model window. Redaction and truncation are explicit; this read never changes summaries or starts execution.",
			DataType:    reflect.TypeOf(RunContextSummaryView{}), NotFound: true, Parameters: []openAPIParameter{runID}},
		{Path: "/api/v1/sessions/{session_id}/tree", OperationID: "getSessionContinuityTree",
			Summary: "Browse Session checkpoints and branches", Tag: "Sessions",
			Description: "Returns the connected checkpoint/Fork/Resume component plus compaction, decision, Artifact, Delivery, Git drift, and memory expiry projections.",
			DataType:    reflect.TypeOf(contextmgr.SessionTree{}), NotFound: true,
			Parameters: []openAPIParameter{sessionID}},
		{Path: "/api/v1/runs/{run_id}/continuity-checkpoints", Method: http.MethodPost,
			OperationID: "createContinuityCheckpoint", Summary: "Create a bounded context checkpoint",
			Tag: "Control", Description: "Captures redacted compaction, recent-message provenance, memory references, pinned config fingerprints, and Git identity with an all-false authority projection.",
			DataType: reflect.TypeOf(contextmgr.ContinuityNode{}), RequestType: reflect.TypeOf(continuityCheckpointRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/runs/{run_id}/workspace-checkpoints",
			OperationID: "listWorkspaceCheckpoints", Summary: "Browse the Workspace checkpoint timeline",
			Tag: "Runs", Description: "Returns immutable pre/post Workspace manifests, mutation transactions, the CAS cursor, recovery completeness, and content-store usage without exposing blob content.",
			DataType: reflect.TypeOf(application.WorkspaceCheckpointTimeline{}), NotFound: true,
			Parameters: []openAPIParameter{runID, {Name: "limit", In: "query",
				Description: "Maximum checkpoints and transactions", Schema: map[string]any{
					"type": "integer", "minimum": 1, "maximum": 2000, "default": 100}},
				{Name: "checkpoint_id", In: "query",
					Description: "Require one exact checkpoint in the returned timeline",
					Schema:      map[string]any{"type": "string", "minLength": 1, "maxLength": 256}}}},
		{Path: StandardCodeDeliveryPathTemplate,
			OperationID: "getStandardCodeDelivery", Summary: "Inspect the Standard Code delivery truth receipt",
			Tag: "Runs", Description: "Returns the one durable standard_code_delivery.v1 projection shared by Desktop, CLI, Code Handoff, GitHub Review, and final completion. The live observation marks a previously passed receipt stale when the current Drydock Workspace revision no longer matches; no Agent prose or CI check name is treated as verification evidence.",
			DataType: reflect.TypeOf(StandardCodeDeliveryReportView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: StandardCodeDeliveryPathTemplate, Method: http.MethodPost,
			OperationID: "recordStandardCodeDelivery", Summary: "Record a Standard Code delivery truth receipt",
			Tag: "Control", Description: "Creates an immutable final Checkpoint, binds the exact Diff and Command Runtime artifacts to the current Drydock Workspace revision, and records a closed delivery conclusion. This operation does not commit, push, merge, overwrite the source Workspace, or retain raw output, environment, reasoning, or host paths.",
			DataType:    reflect.TypeOf(StandardCodeDeliveryRecordResultView{}),
			RequestType: reflect.TypeOf(StandardCodeDeliveryRecordView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/workspace-checkpoints", Method: http.MethodPost,
			OperationID: "createWorkspaceCheckpoint", Summary: "Create a manual Workspace checkpoint",
			Tag: "Control", Description: "Captures a bounded content-addressed manifest and exact Git index under an idempotency key; excluded content and incomplete attribution remain explicit.",
			DataType:    reflect.TypeOf(workspacecheckpoint.Checkpoint{}),
			RequestType: reflect.TypeOf(workspaceCheckpointCaptureView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/runs/{run_id}/workspace-checkpoints/preview", Method: http.MethodPost,
			OperationID: "previewWorkspaceRewind", Summary: "Preview a Workspace rewind",
			Tag: "Control", Description: "Computes a bounded three-way diff against live files and the Git index without writing. External drift is returned as explicit conflicts.",
			DataType:    reflect.TypeOf(application.WorkspaceRestoreResult{}),
			RequestType: reflect.TypeOf(workspaceCheckpointPreviewView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/workspace-checkpoints/rewind", Method: http.MethodPost,
			OperationID: "rewindWorkspaceCheckpoint", Summary: "Rewind to a Workspace checkpoint",
			Tag: "Control", Description: "Requires confirm=true, a paused quiescent Run, current operator authorization, and an unchanged preview cursor. Restore is an auditable new write and never uses hard reset or blanket untracked deletion.",
			DataType:    reflect.TypeOf(application.WorkspaceRestoreResult{}),
			RequestType: reflect.TypeOf(workspaceCheckpointRewindView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/workspace-checkpoints/undo", Method: http.MethodPost,
			OperationID: "undoWorkspaceMutation", Summary: "Undo the current Workspace mutation",
			Tag: "Control", Description: "Restores the current mutation's before checkpoint with three-way conflict detection and appends a new immutable undo transaction.",
			DataType:    reflect.TypeOf(application.WorkspaceRestoreResult{}),
			RequestType: reflect.TypeOf(workspaceCheckpointCursorActionView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/workspace-checkpoints/redo", Method: http.MethodPost,
			OperationID: "redoWorkspaceMutation", Summary: "Redo the latest Workspace undo",
			Tag: "Control", Description: "Restores the original mutation's after checkpoint under the same paused-Run, CAS, permission, and conflict checks as rewind.",
			DataType:    reflect.TypeOf(application.WorkspaceRestoreResult{}),
			RequestType: reflect.TypeOf(workspaceCheckpointCursorActionView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/workspace-checkpoints/fork", Method: http.MethodPost,
			OperationID: "forkWorkspaceCheckpoint", Summary: "Fork a checkpoint into an independent Run",
			Tag: "Control", Description: "Creates an exact branch-backed Git worktree, a distinct Workspace and a new Run. Context is bounded; approvals, credentials, capabilities, leases, processes, terminals, network authority, and execution profiles are reset.",
			DataType:    reflect.TypeOf(workspaceCheckpointForkResultView{}),
			RequestType: reflect.TypeOf(workspaceCheckpointForkView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/runs/{run_id}/git-advanced",
			OperationID: "getGitAdvancedProjection", Summary: "Inspect advanced Git state",
			Tag: "Git", Description: "Returns the current repository binding, process capability generation, conflict stages, durable sequence recovery, managed worktrees, and recent immutable operation receipts without exposing filesystem roots.",
			DataType: reflect.TypeOf(application.GitAdvancedProjection{}), NotFound: true,
			Parameters: []openAPIParameter{runID, {Name: "limit", In: "query",
				Description: "Maximum operation receipts", Schema: map[string]any{
					"type": "integer", "minimum": 1, "maximum": 500, "default": 100}}}},
		{Path: "/api/v1/runs/{run_id}/git-advanced/discover-hunks", Method: http.MethodPost,
			OperationID: "discoverGitAdvancedHunks", Summary: "Discover stable Git hunk identities",
			Tag: "Git", Description: "Reads one exact repository/index/worktree snapshot and returns stable hunk identities and patches. Discovery never creates an Approval and selected identities must be reviewed again before mutation.",
			DataType:    reflect.TypeOf(application.GitAdvancedReviewResult{}),
			RequestType: reflect.TypeOf(gitAdvancedDiscoverView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/git-advanced/review", Method: http.MethodPost,
			OperationID: "reviewGitAdvancedOperation", Summary: "Review an exact advanced Git mutation",
			Tag: "Git", Description: "Revalidates the active Run permission, lease generation, process capability, repository binding, target objects, selected hunks, conflicts, file impact, and recovery plan, then persists an immutable pending Approval source.",
			DataType:    reflect.TypeOf(application.GitAdvancedReviewResult{}),
			RequestType: reflect.TypeOf(gitAdvancedReviewView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/runs/{run_id}/git-advanced/execute", Method: http.MethodPost,
			OperationID: "executeGitAdvancedOperation", Summary: "Execute an approved advanced Git mutation",
			Tag: "Git", Description: "Consumes one exact approved preview after revalidating capability, permission, lease, repository, index, worktree, upstream, hunk, sequence, and worktree-registry state. Every mutation is bracketed by Workspace checkpoints and yields a durable receipt.",
			DataType:    reflect.TypeOf(application.GitAdvancedExecuteResult{}),
			RequestType: reflect.TypeOf(gitAdvancedExecuteView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/github-review/connections",
			OperationID: "listGitHubReviewConnections", Summary: "List GitHub review connections",
			Tag: "GitHub Review", Description: "Returns repository, credential reference, fixed network scope, generation, and credential-store status. No token or secret value crosses the API boundary.",
			DataType: reflect.TypeOf(application.GitHubReviewCredentialView{}), Collection: true,
			Parameters: []openAPIParameter{{Name: "enabled_only", In: "query",
				Description: "Return only enabled connections", Schema: map[string]any{
					"type": "boolean", "default": false}}}},
		{Path: "/api/v1/github-review/connections", Method: http.MethodPost,
			OperationID: "configureGitHubReviewConnection", Summary: "Configure a GitHub review connection",
			Tag: "GitHub Review", Description: "Creates or compare-and-swap updates one github.com repository binding. GitHub App Device Flow is preferred; only a credential reference and public client ID are persisted.",
			DataType:    reflect.TypeOf(application.GitHubReviewConfigureResult{}),
			RequestType: reflect.TypeOf(githubReviewConfigureView{}), Control: true,
			SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/github-review/connections/{connection_id}",
			OperationID: "getGitHubReviewConnection", Summary: "Inspect a GitHub review connection",
			Tag: "GitHub Review", Description: "Returns the non-secret connection and OS credential-store status without resolving the token into the response.",
			DataType: reflect.TypeOf(application.GitHubReviewCredentialView{}), NotFound: true,
			Parameters: []openAPIParameter{githubReviewConnectionID}},
		{Path: "/api/v1/github-review/connections/{connection_id}/device", Method: http.MethodPost,
			OperationID: "beginGitHubReviewDeviceFlow", Summary: "Begin GitHub App Device Flow",
			Tag: "GitHub Review", Description: "Returns a GitHub verification URI and short-lived user code. The device code remains process memory only.",
			DataType:    reflect.TypeOf(githubreview.DeviceAuthorization{}),
			RequestType: reflect.TypeOf(githubReviewEmptyView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{githubReviewConnectionID},
			SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/github-review/connections/{connection_id}/device-poll", Method: http.MethodPost,
			OperationID: "pollGitHubReviewDeviceFlow", Summary: "Poll GitHub App Device Flow",
			Tag: "GitHub Review", Description: "Polls one in-memory authorization session and writes the resulting access/refresh bundle only to the OS credential store.",
			DataType:    reflect.TypeOf(githubreview.DevicePollResult{}),
			RequestType: reflect.TypeOf(githubReviewDevicePollView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{githubReviewConnectionID},
			SuccessStatus: http.StatusOK},
		{Path: "/api/v1/github-review/connections/{connection_id}/disconnect", Method: http.MethodPost,
			OperationID: "disconnectGitHubReviewCredential", Summary: "Delete a GitHub review credential",
			Tag: "GitHub Review", Description: "Deletes only the referenced OS credential-store entry; the durable repository configuration remains reusable.",
			DataType:    reflect.TypeOf(application.GitHubReviewCredentialView{}),
			RequestType: reflect.TypeOf(githubReviewEmptyView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{githubReviewConnectionID},
			SuccessStatus: http.StatusOK},
		{Path: "/api/v1/github-review/connections/{connection_id}/qualify", Method: http.MethodPost,
			OperationID: "qualifyGitHubReviewConnection", Summary: "Diagnose GitHub review eligibility",
			Tag: "GitHub Review", Description: "Checks account, App installation, repository, PR, permissions, SSO, fixed API version, rate limit, and network scope with stable diagnostics.",
			DataType:    reflect.TypeOf(application.GitHubReviewQualificationResult{}),
			RequestType: reflect.TypeOf(githubReviewPullRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{githubReviewConnectionID},
			SuccessStatus: http.StatusOK},
		{Path: "/api/v1/github-review/connections/{connection_id}/fetch", Method: http.MethodPost,
			OperationID: "fetchGitHubReviewSnapshot", Summary: "Fetch bounded PR and CI evidence",
			Tag: "GitHub Review", Description: "Fetches and immutably stores sanitized PR metadata, bounded complete changed-file pagination, review threads, comments, checks, jobs, failed log excerpts, and Artifact metadata.",
			DataType:    reflect.TypeOf(application.GitHubReviewFetchResult{}),
			RequestType: reflect.TypeOf(githubReviewPullRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{githubReviewConnectionID},
			SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/runs/{run_id}/github-review",
			OperationID: "getGitHubReviewProjection", Summary: "Inspect GitHub review evidence and receipts",
			Tag: "GitHub Review", Description: "Returns immutable sanitized snapshots, local evidence graphs, pending writes, and terminal receipts for one Code Surface Run.",
			DataType: reflect.TypeOf(application.GitHubReviewProjection{}), NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "connection_id", In: "query", Description: "Exact connection identity",
					Required: true, Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
				{Name: "pull_request", In: "query", Description: "Optional PR number",
					Schema: map[string]any{"type": "integer", "minimum": 0, "default": 0}},
				{Name: "limit", In: "query", Description: "Maximum records",
					Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 100}}}},
		{Path: "/api/v1/runs/{run_id}/github-review/evidence", Method: http.MethodPost,
			OperationID: "buildGitHubReviewEvidence", Summary: "Bind a GitHub snapshot to local Git evidence",
			Tag: "GitHub Review", Description: "Binds exact repo/PR/base/head/merge-base, changed files, stable hunks, worktree hashes, conflicts, and optional trusted LSP evidence. Drift becomes stale rather than current.",
			DataType:    reflect.TypeOf(application.GitHubReviewEvidenceResult{}),
			RequestType: reflect.TypeOf(githubReviewEvidenceView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/runs/{run_id}/github-review/review", Method: http.MethodPost,
			OperationID: "reviewGitHubReviewWrite", Summary: "Review one exact GitHub write-back",
			Tag: "GitHub Review", Description: "Requalifies capability and persists an exact reply, thread state, review, or reviewer request preview plus a separate one-time Approval.",
			DataType:    reflect.TypeOf(application.GitHubReviewWriteReviewResult{}),
			RequestType: reflect.TypeOf(githubReviewWriteReviewView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/runs/{run_id}/github-review/execute", Method: http.MethodPost,
			OperationID: "executeGitHubReviewWrite", Summary: "Execute one approved GitHub write-back",
			Tag: "GitHub Review", Description: "Rechecks Run phase, network permission, credential/capability generation, PR identity and remote state; hidden idempotency markers and recovery receipts prevent duplicate effects.",
			DataType:    reflect.TypeOf(application.GitHubReviewWriteExecuteResult{}),
			RequestType: reflect.TypeOf(githubReviewWriteExecuteView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/runs/{run_id}/ui-evidence", OperationID: "listRunUIEvidence",
			Summary: "List source-bound UI evidence attempts", Tag: "UI Evidence",
			Description: "Returns immutable manifests and fail-closed outcomes. not_run is unknown and never a passing result.",
			DataType:    reflect.TypeOf(uievidence.Attempt{}), Collection: true, NotFound: true,
			Parameters: []openAPIParameter{runID,
				stringQueryParameter("status", "Optional exact attempt status", []string{
					"not_run", "running", "passed", "failed", "cancelled", "timed_out", "interrupted"}),
				{Name: "limit", In: "query", Description: "Maximum attempts", Schema: map[string]any{
					"type": "integer", "minimum": 1, "maximum": 500, "default": 100}}}},
		{Path: "/api/v1/runs/{run_id}/ui-evidence", Method: http.MethodPost,
			OperationID: "startRunUIEvidence", Summary: "Start real-browser UI evidence",
			Tag: "UI Evidence", Description: "Persists not_run before asynchronously executing the exact source-bound build/start recipe in a Run-owned process tree, a temporary browser Profile, and a loopback-only reviewed Safe Web runtime.",
			DataType: reflect.TypeOf(uievidence.Attempt{}), RequestType: reflect.TypeOf(uiEvidenceStartView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{runID}, SuccessStatus: http.StatusAccepted},
		{Path: "/api/v1/ui-evidence/{attempt_id}", OperationID: "getUIEvidence",
			Summary: "Inspect one UI evidence bundle", Tag: "UI Evidence",
			Description: "Returns the exact manifest, step receipts, and artifact metadata without binary content.",
			DataType:    reflect.TypeOf(application.UIEvidenceBundle{}), NotFound: true,
			Parameters: []openAPIParameter{uiEvidenceAttemptID}},
		{Path: "/api/v1/ui-evidence/{attempt_id}/cancel", Method: http.MethodPost,
			OperationID: "cancelUIEvidence", Summary: "Cancel one UI evidence attempt",
			Tag: "UI Evidence", Description: "Cancels the service-owned context and waits for bounded process-tree, Profile, network, and port cleanup.",
			DataType: reflect.TypeOf(uievidence.Attempt{}), RequestType: reflect.TypeOf(uiEvidenceCancelView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{uiEvidenceAttemptID}, SuccessStatus: http.StatusOK},
		{Path: "/api/v1/ui-evidence/{attempt_id}/artifacts/{artifact_id}",
			OperationID: "downloadUIEvidenceArtifact", Summary: "Download one verified UI evidence artifact",
			Tag: "UI Evidence", Description: "Returns exact hash-verified untrusted bytes with MIME, ETag, source digest, and no-store headers.",
			RawArtifact: true, NotFound: true,
			Parameters: []openAPIParameter{uiEvidenceAttemptID, artifactID}},
		{Path: "/api/v1/continuity-nodes/{node_id}/fork", Method: http.MethodPost,
			OperationID: "forkContinuityNode", Summary: "Fork a new Run from a checkpoint",
			Tag: "Control", Description: "Creates a new Run and Session with the exact bounded context snapshot while resetting approvals, capabilities, credentials, processes, leases, network authorization, and execution profiles.",
			DataType: reflect.TypeOf(continuityBranchView{}), RequestType: reflect.TypeOf(continuityBranchRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{continuityNodeID}, SuccessStatus: http.StatusCreated},
		{Path: "/api/v1/continuity-nodes/{node_id}/resume", Method: http.MethodPost,
			OperationID: "resumeContinuityNode", Summary: "Resume in a new Run from a checkpoint",
			Tag: "Control", Description: "Creates an auditable new Run/Session branch from the checkpoint; durable historical context never resurrects authority.",
			DataType: reflect.TypeOf(continuityBranchView{}), RequestType: reflect.TypeOf(continuityBranchRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{continuityNodeID}, SuccessStatus: http.StatusCreated},
		{Path: SandboxEnvironmentPath, Method: http.MethodGet,
			OperationID: "getSandboxEnvironment", Summary: "Inspect Local, Docker Engine and Docker Sandboxes environments", Tag: "Sandbox",
			Description: "Reads saved and active settings with bounded installation and readiness observations.",
			DataType:    reflect.TypeOf(SandboxEnvironmentView{}), NotFound: true},
		{Path: SandboxEnvironmentPath, Method: http.MethodPut,
			OperationID: "saveSandboxEnvironment", Summary: "Save sandbox environment preferences", Tag: "Sandbox",
			Description: "Atomically saves revision-bound preferences for the next desktop startup. Execution continues to require task approval and current backend readiness.",
			DataType:    reflect.TypeOf(SandboxEnvironmentView{}), RequestType: reflect.TypeOf(SandboxEnvironmentControlRequestView{}),
			Control: true, NotFound: true, SuccessStatus: http.StatusOK},
		{Path: DockerEnvironmentPath, Method: http.MethodGet,
			OperationID: "getDockerEnvironment", Summary: "Inspect the fixed Docker coding environment", Tag: "Sandbox",
			Description: "Fresh bounded read-only daemon and pinned image observations. Does not pull images, change configuration or grant execution authority.",
			DataType:    reflect.TypeOf(DockerEnvironmentView{}), NotFound: true},
		{Path: DockerSandboxReadinessPath, Method: http.MethodPost,
			OperationID: "evaluateDockerSandboxReadiness",
			Summary:     "Evaluate Docker Sandbox readiness", Tag: "Sandbox",
			Description:   "Performs a non-mutating, no-cache readiness evaluation for one exact Go-validated plan and Manifest. It cannot accept a daemon endpoint, host path, image override, mount override, or Docker flag.",
			DataType:      reflect.TypeOf(DockerSandboxReadinessView{}),
			RequestType:   reflect.TypeOf(DockerSandboxReadinessRequestView{}),
			SuccessStatus: http.StatusOK},
		{Path: DockerSandboxAdmissionPath, Method: http.MethodPost,
			OperationID: "admitDockerSandbox", Summary: "Admit an exact Docker Sandbox request",
			Tag: "Sandbox", Description: "Recomputes every current Go-owned gate and records an idempotent process-bound admission. Idempotency-Key is required.",
			DataType:    reflect.TypeOf(DockerSandboxAdmissionView{}),
			RequestType: reflect.TypeOf(DockerSandboxAdmissionRequestView{}),
			Control:     true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{dockerSandboxIdempotencyKey}},
		{Path: DockerSandboxStartPath, Method: http.MethodPost,
			OperationID: "startDockerSandbox", Summary: "Start an admitted Docker Sandbox",
			Tag: "Sandbox", Description: "Synchronously executes the exact admitted plan through the Go-owned lifecycle; client disconnect cancels and cleans up. Idempotency-Key is required.",
			DataType:    reflect.TypeOf(DockerSandboxStatusView{}),
			RequestType: reflect.TypeOf(DockerSandboxStartRequestView{}),
			Control:     true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{dockerSandboxIdempotencyKey}},
		{Path: DockerSandboxCancelPath, Method: http.MethodPost,
			OperationID: "cancelDockerSandbox", Summary: "Cancel a Docker Sandbox attempt",
			Tag: "Sandbox", Description: "Persists sticky cancellation and converges bounded cleanup without restoring start authority. Idempotency-Key is required.",
			DataType:    reflect.TypeOf(DockerSandboxCancellationView{}),
			RequestType: reflect.TypeOf(DockerSandboxCancelRequestView{}),
			Control:     true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{dockerSandboxIdempotencyKey}},
		{Path: DockerSandboxStatusPath, OperationID: "getDockerSandboxStatus",
			Summary: "Inspect Docker Sandbox status", Tag: "Sandbox",
			Description: "Returns the content-free admission, launch, terminal outcome, cleanup, and artifact-count projection.",
			DataType:    reflect.TypeOf(DockerSandboxStatusView{}), NotFound: true,
			Parameters: []openAPIParameter{{Name: "admission_id", In: "query",
				Description: "Opaque Docker Sandbox admission identity", Required: true,
				Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 256}}}},
		{Path: "/api/v1", OperationID: "getAPIIndex", Summary: "Inspect API resources",
			Description: "Returns API and application versions plus top-level resources.", Tag: "System",
			DataType: reflect.TypeOf(IndexView{})},
		{Path: "/api/v1/health", OperationID: "getHealth", Summary: "Inspect local API health",
			Description: "Reads the current SQLite schema version without mutating state.", Tag: "System",
			DataType: reflect.TypeOf(HealthView{})},
		{Path: "/api/v1/capabilities", OperationID: "getRuntimeCapabilities",
			Summary: "Inspect process-local runtime capabilities", Tag: "System",
			Description: "Returns bounded enablement metadata, including whether the agent-code-tools.v1 runtime is compiled in, and Run wake worker health without bearer tokens, owner or lease identities, private errors, runtime enable controls, or persistent-service authority.",
			DataType:    reflect.TypeOf(RuntimeCapabilitiesView{})},
		{Path: StandardCodePresetCreatePath, Method: http.MethodPost,
			OperationID: "createStandardCodeRun", Summary: "Create and configure a Standard Code Run",
			Tag: "Control", Description: "Creates a Code Surface Run in Plan phase and atomically applies the controlled Ask preset after exact Workspace Trust and Drydock readiness checks. Auto selects only a ready Local backend; Docker always requires explicit intent. The receipt never grants runtime authority or returns a bearer token.",
			DataType:    reflect.TypeOf(StandardCodePresetControlView{}),
			RequestType: reflect.TypeOf(StandardCodePresetControlRequestView{}),
			Control:     true, SuccessStatus: http.StatusAccepted,
			Parameters: []openAPIParameter{standardCodePresetIdempotencyKey}},
		{Path: StandardCodePresetRunPathTemplate, Method: http.MethodPost,
			OperationID: "configureStandardCodeRun", Summary: "Configure an existing Run for Standard Code",
			Tag: "Control", Description: "Idempotently applies the complete Standard Code tuple only to a created or paused quiescent Code Run. An incompatible Surface produces a new Code Run; a running Run returns the separate pause-and-configure next step. No partial policy tuple is committed.",
			DataType:    reflect.TypeOf(StandardCodePresetControlView{}),
			RequestType: reflect.TypeOf(StandardCodePresetControlRequestView{}),
			Control:     true, NotFound: true, SuccessStatus: http.StatusAccepted,
			Parameters: []openAPIParameter{runID, standardCodePresetIdempotencyKey}},
		{Path: StandardCodePauseAndConfigurePathTemplate, Method: http.MethodPost,
			OperationID: "pauseAndConfigureStandardCodeRun", Summary: "Pause then configure a running Standard Code Run",
			Tag: "Control", Description: "Records an explicit durable pause-and-configure intent, waits for the active execution lease and Supervisor work to become quiescent, then commits the pause and complete Standard Code tuple in one SQLite transaction. Retrying the same intent is idempotent.",
			DataType:    reflect.TypeOf(StandardCodePresetControlView{}),
			RequestType: reflect.TypeOf(StandardCodePresetControlRequestView{}),
			Control:     true, NotFound: true, SuccessStatus: http.StatusAccepted,
			Parameters: []openAPIParameter{runID, standardCodePresetIdempotencyKey}},
		{Path: MCPInputsPathTemplate, OperationID: "listMCPInputs", Summary: "Read live MCP operator input requests", Tag: "Extensions", NotFound: true, DataType: reflect.TypeOf(MCPInputListView{}), Parameters: []openAPIParameter{runID}},
		{Path: MCPInputResponsePathTemplate, Method: http.MethodPost, OperationID: "respondMCPInput", Summary: "Answer one live MCP operator input request", Tag: "Extensions", Control: true, NotFound: true, SuccessStatus: http.StatusOK, DataType: reflect.TypeOf(MCPInputResponseView{}), RequestType: reflect.TypeOf(mcp.InputResponse{}), Parameters: []openAPIParameter{runID, pathIdentityParameter("input_id", "Live input request identity")}},
		{Path: ExtensionInventoryPath, OperationID: "getExtensionInventory",
			Summary: "Inspect MCP Client and Plugin state", Tag: "Extensions",
			Description: "Returns bounded MCP server health, capability fingerprints, metadata-only call audits, and Plugin installation state. Credential plaintext, MCP arguments and results, and package bytes are never returned.",
			DataType:    reflect.TypeOf(ExtensionInventoryView{}),
			Parameters: []openAPIParameter{
				identityQueryParameter("run_id", "Optional Run used to resolve exact Run and Workspace MCP scope"),
				identityQueryParameter("workspace_id", "Optional Workspace for first registration before a Run exists; must match a supplied Run"),
			}},
		{Path: ExtensionMCPRegistrationPath, Method: http.MethodPost,
			OperationID: "registerMCPServer", Summary: "Register one inert MCP server",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Stages a manual MCP descriptor in an existing Run or Workspace. The host supplies source provenance; registration starts no process or discovery and does not grant execution authority. Exact repeats return the current persisted state.",
			DataType:    reflect.TypeOf(ExtensionMCPRegistrationView{}),
			RequestType: reflect.TypeOf(ExtensionMCPRegistrationRequestView{})},
		{Path: MCPCredentialPathTemplate, OperationID: "getMCPCredentialStatus",
			Summary: "Read local MCP bearer credential presence", Tag: "Extensions", NotFound: true,
			Description: "Reads only local OS credential presence for the exact registered HTTPS descriptor and scope. It does not test remote authentication, discover capabilities or invoke tools. Shared MCP registration metadata is bound to a reference fingerprint; plaintext is never returned.",
			DataType:    reflect.TypeOf(MCPCredentialStatusView{}),
			Parameters: []openAPIParameter{pathIdentityParameter("server_id", "MCP server identity"),
				{Name: "workspace_id", In: "query", Required: true, Description: "Exact descriptor Workspace", Schema: identitySchema()},
				identityQueryParameter("run_id", "Exact descriptor Run, if Run scoped"),
				{Name: "expected_descriptor_fingerprint", In: "query", Required: true, Description: "Exact descriptor SHA-256",
					Schema: map[string]any{"type": "string", "minLength": 64, "maxLength": 64, "pattern": "^[a-f0-9]{64}$"}},
				{Name: "target", In: "query", Required: true, Description: "Exact registered HTTPS endpoint",
					Schema: map[string]any{"type": "string", "format": "uri", "minLength": 1, "maxLength": mcp.MaxClientTargetBytes}},
				{Name: "credential_ref", In: "query", Required: true, Description: "Exact OS credential name",
					Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 64}}}},
		{Path: MCPCredentialPathTemplate, Method: http.MethodPost, OperationID: "changeMCPCredential",
			Summary: "Set or remove an exact MCP bearer credential", Tag: "Extensions", Control: true, NotFound: true, SuccessStatus: http.StatusOK,
			Description: "Explicitly confirmed set/delete through the Go-owned OS credential store. Rechecks descriptor, endpoint, scope and shared reference fingerprint. No plaintext fallback, connection, discovery, enablement or remote token revocation occurs.",
			DataType:    reflect.TypeOf(MCPCredentialStatusView{}), RequestType: reflect.TypeOf(MCPCredentialRequestView{}),
			Parameters: []openAPIParameter{pathIdentityParameter("server_id", "MCP server identity")}},
		{Path: ExtensionPluginImportPath, Method: http.MethodPost,
			OperationID: "importPlugin", Summary: "Import an inert Plugin archive",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Stages a bounded uploaded ZIP or fetches a fixed HTTPS URL/SHA-256 or Git commit/archive path. Remote URLs reject credentials, query parameters and redirects. Uses the existing inert validator and explicit review flow. Exact archive repeats return the current persisted installation.",
			DataType:    reflect.TypeOf(ExtensionPluginImportView{}),
			RequestType: reflect.TypeOf(ExtensionPluginImportRequestView{})},
		{Path: CodeIntelInventoryPath, OperationID: "getCodeIntelInventory",
			Summary: "Inspect reviewed language-server state", Tag: "System",
			Description: "Returns code-intel-lsp.v1 server source, language, health, capabilities, generation, recent bounded error, and model-visible read-only tools. Executable paths, argv, environment, credentials, server output, and Workspace content are never returned.",
			DataType:    reflect.TypeOf(CodeIntelInventoryView{}),
			Parameters: []openAPIParameter{
				identityQueryParameter("workspace_id", "Optional Workspace used for static executable-hash and review qualification"),
			}},
		{Path: CodeIntelConfigurationsPath, Method: http.MethodPost,
			OperationID: "stageCodeIntelConfiguration", Summary: "Stage a language-server configuration for review",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Validates operator-supplied language-server launch metadata and executable SHA-256 in one registered Workspace. The process-local draft starts no server and returns only safe review metadata.",
			DataType:    reflect.TypeOf(CodeIntelConfigurationView{}),
			RequestType: reflect.TypeOf(CodeIntelConfigurationRequestView{})},
		{Path: CodeIntelConfigurationReviewPath, Method: http.MethodPost,
			OperationID: "reviewCodeIntelConfiguration", Summary: "Review and persist one exact language-server configuration",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Reviews the pinned staged descriptor and saves the existing operator configuration format. It does not initialize a server or claim a successful query.",
			DataType:    reflect.TypeOf(CodeIntelConfigurationView{}),
			RequestType: reflect.TypeOf(CodeIntelConfigurationReviewRequestView{}),
			Parameters:  []openAPIParameter{pathIdentityParameter("server_id", "Language-server identity")}},
		{Path: CodeIntelConfigurationTestPath, Method: http.MethodPost,
			OperationID: "testCodeIntelConfiguration", Summary: "Run one explicit read-only language-server probe",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Rechecks the exact reviewed descriptor, initializes only that language server, and returns a bounded symbol-query result with server, capability, query, and document provenance. Successful configuration and successful execution remain distinct states.",
			DataType:    reflect.TypeOf(CodeIntelConfigurationTestView{}),
			RequestType: reflect.TypeOf(CodeIntelConfigurationTestRequestView{}),
			Parameters:  []openAPIParameter{pathIdentityParameter("server_id", "Language-server identity")}},
		{Path: ExtensionMCPReviewPath, Method: http.MethodPost,
			OperationID: "reviewMCPServer", Summary: "Review or disable one MCP server",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Applies an explicit two-stage descriptor or capability review, or immediately disables or revokes one server using pinned fingerprints.",
			DataType:    reflect.TypeOf(ExtensionMCPServerView{}),
			RequestType: reflect.TypeOf(ExtensionMCPReviewRequestView{}),
			Parameters:  []openAPIParameter{pathIdentityParameter("server_id", "MCP server identity")}},
		{Path: ExtensionMCPRefreshPath, Method: http.MethodPost,
			OperationID: "refreshMCPServer", Summary: "Rediscover one MCP server",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Performs bounded MCP initialization and discovery. Capability drift is fail-closed and moves the server out of enabled state until reviewed.",
			DataType:    reflect.TypeOf(ExtensionMCPServerView{}),
			RequestType: reflect.TypeOf(ExtensionRefreshRequestView{}),
			Parameters:  []openAPIParameter{pathIdentityParameter("server_id", "MCP server identity")}},
		{Path: ExtensionPluginReviewPath, Method: http.MethodPost,
			OperationID: "reviewPluginInstallation", Summary: "Review or disable one Plugin",
			Tag: "Extensions", Control: true, NotFound: true,
			Description: "Applies an explicit capability review or immediately disables, quarantines, or revokes one inert Plugin installation using a pinned package fingerprint and generation.",
			DataType:    reflect.TypeOf(ExtensionPluginInstallationView{}),
			RequestType: reflect.TypeOf(ExtensionPluginReviewRequestView{}),
			Parameters:  []openAPIParameter{pathIdentityParameter("installation_id", "Plugin installation identity")}},
		{Path: ExtensionPluginHistoryPath, OperationID: "getPluginHistory", Summary: "Inspect installed Plugin versions and publisher trust", Tag: "Extensions", NotFound: true,
			Description: "Reads retained installation versions in the same package, protocol and surface, plus redacted publisher trust and the installation identities affected by revocation. Never returns package bytes or publisher keys.",
			DataType:    reflect.TypeOf(PluginHistoryView{}), Parameters: []openAPIParameter{pathIdentityParameter("installation_id", "Plugin installation identity")}},
		{Path: ExtensionPluginRollbackPath, Method: http.MethodPost, OperationID: "rollbackPluginInstallation", Summary: "Switch to a retained Plugin version", Tag: "Extensions", Control: true, NotFound: true,
			Description: "Atomically retires the enabled version and enables the selected retained version with explicit capabilities. Both package fingerprints and generations must match. Existing publisher trust gates apply.",
			DataType:    reflect.TypeOf(PluginRollbackView{}), RequestType: reflect.TypeOf(PluginRollbackRequestView{}), Parameters: []openAPIParameter{pathIdentityParameter("installation_id", "Currently enabled Plugin installation identity")}},
		{Path: ExtensionPluginPublisherRevocationPath, Method: http.MethodPost, OperationID: "revokePluginPublisher", Summary: "Revoke one Plugin publisher trust record", Tag: "Extensions", Control: true, NotFound: true,
			Description: "Revokes the exact publisher fingerprint and trust generation bound to the selected signed installation, atomically revoking that publisher's nonterminal installations across packages.",
			DataType:    reflect.TypeOf(PluginPublisherRevocationView{}), RequestType: reflect.TypeOf(PluginPublisherRevocationRequestView{}), Parameters: []openAPIParameter{pathIdentityParameter("installation_id", "Signed Plugin installation identity")}},
		{Path: ExtensionPluginPublisherTrustPath, Method: http.MethodPost, OperationID: "trustPluginPublisher", Summary: "Grant one Plugin publisher trust record", Tag: "Extensions", Control: true, NotFound: true,
			Description: "Grants trust for the exact publisher fingerprint and trust generation the operator reviewed on one signed installation, so a stale view cannot trust another record. Trust identifies the publisher only: it grants no capability, and approval and enablement remain separate reviewed actions.",
			DataType:    reflect.TypeOf(PluginPublisherTrustResponseView{}), RequestType: reflect.TypeOf(PluginPublisherTrustRequestView{}), Parameters: []openAPIParameter{pathIdentityParameter("installation_id", "Signed Plugin installation identity")}},
		{Path: ExtensionHookDiagnosticsPath, OperationID: "getHookDiagnostics", Summary: "Inspect declarative Hook scope and observed decisions", Tag: "Extensions", NotFound: true,
			Description: "Read-only projection of local declarations and the newest 200 scoped observations. New observations bind the actual package fingerprint and rejection decision; historical missing decisions remain unknown. Hook payloads and plugin-controlled messages are omitted.",
			DataType:    reflect.TypeOf(HookDiagnosticsView{}), Parameters: []openAPIParameter{identityQueryParameter("run_id", "Optional exact Run scope"), identityQueryParameter("workspace_id", "Optional Workspace scope; must match a supplied Run")}},
		{Path: "/api/v1/browser/safe-web-readiness", OperationID: "getBrowserSafeWebReadiness",
			Summary: "Inspect Safe Web readiness", Tag: "Browser",
			Description: "Returns a fail-closed readiness receipt over the latest production containment evidence and operator review for one accepted browser. It never starts a browser and never grants authority.",
			Parameters: []openAPIParameter{{Name: "product", In: "query",
				Description: "Accepted browser product", Required: false,
				Schema: map[string]any{"type": "string",
					"enum": []string{"chrome", "edge", "chromium"}}}},
			DataType: reflect.TypeOf(browserruntime.BrowserSafeWebReadiness{})},
		{Path: "/api/v1/models", OperationID: "getModelAvailability",
			Summary: "Inspect redacted model availability", Tag: "Models",
			Description: "Returns deterministic Provider registration and route metadata without API keys, base URLs, environment variable names, network probes, or model calls.",
			DataType:    reflect.TypeOf(ModelAvailabilityView{})},
		{Path: AvailableModelRoutesPath, OperationID: "listAvailableModelRoutes",
			Summary: "List selectable Provider/model routes", Tag: "Models",
			Description: "Returns the credential-free, qualification-aware model routes that the Thread composer may display. The selectable flag is the server-authoritative admission decision; no credential plaintext is returned.",
			DataType:    reflect.TypeOf(AvailableModelRouteCollectionView{})},
		{Path: ProviderDefinitionsPath, OperationID: "listProviderDefinitions",
			Summary: "List credential-free custom Provider definitions", Tag: "Models",
			Description: "Returns operator-authored Provider protocol, endpoint, model mapping, search policy, and bounded advanced JSON. Credentials are represented only by same-Provider references and plaintext is never returned.",
			DataType:    reflect.TypeOf(ProviderDefinitionCollectionView{})},
		{Path: ProviderModelDiscoveryPath, Method: http.MethodPost,
			OperationID: "discoverProviderModels", Summary: "Fetch the draft Provider model catalog",
			Tag: "Control", Control: true,
			Description: "Fetches a bounded catalog with a transient draft key or a revision-bound stored key. Never persists credentials, saves definitions, follows redirects, or grants model qualification. Input and output capacities are separate optional upstream metadata.",
			DataType:    reflect.TypeOf(modelregistry.ModelDiscoveryResult{}),
			RequestType: reflect.TypeOf(application.ProviderModelDiscoveryRequest{})},
		{Path: ProviderDefinitionPathTemplate, Method: http.MethodPost,
			OperationID: "upsertProviderDefinition", Summary: "Create or update a custom Provider",
			Tag: "Control", Control: true,
			Description: "Persists one revision-bound credential-free Provider definition, then atomically installs a new Registry generation. Native search declarations remain unverified and grant no hosted tool or network authority.",
			DataType:    reflect.TypeOf(ProviderDefinitionMutationView{}),
			RequestType: reflect.TypeOf(ProviderDefinitionUpsertRequestView{}),
			Parameters:  []openAPIParameter{pathIdentityParameter("provider", "Custom Provider identity")}},
		{Path: ProviderDefinitionDeletePathTemplate, Method: http.MethodPost,
			OperationID: "deleteProviderDefinition", Summary: "Delete a custom Provider",
			Tag: "Control", Control: true, NotFound: true,
			Description: "Deletes one exact revision only when no persisted model route still selects it, then installs a new Registry generation. The separately stored OS credential is not exposed.",
			DataType:    reflect.TypeOf(ProviderDefinitionMutationView{}),
			RequestType: reflect.TypeOf(ProviderDefinitionDeleteRequestView{}),
			Parameters:  []openAPIParameter{pathIdentityParameter("provider", "Custom Provider identity")}},
		{Path: ModelRouteControlPathTemplate, Method: http.MethodPost,
			OperationID: "selectModelRoute", Summary: "Persist a model route selection",
			Tag:         "Control",
			Description: "Persists one available Provider/model selection before updating the in-process Router. It does not call a model or expose credentials.",
			DataType:    reflect.TypeOf(ModelRouteAvailabilityView{}),
			RequestType: reflect.TypeOf(ModelRouteControlRequestView{}), Control: true,
			Parameters: []openAPIParameter{routeName}},
		{Path: ProviderDiagnosticPath, Method: http.MethodPost,
			OperationID: "diagnoseProvider", Summary: "Run an explicit Provider diagnostic",
			Tag:         "Control",
			Description: "Makes at most one minimal no-tool model request after explicit confirmation. The response is content-free and never includes model text, raw Provider errors, credentials, or endpoint URLs.",
			DataType:    reflect.TypeOf(ProviderDiagnosticView{}),
			RequestType: reflect.TypeOf(ProviderDiagnosticRequestView{}), Control: true},
		{Path: ModelHarnessQualificationPath, Method: http.MethodPost,
			OperationID: "qualifyModelHarness",
			Summary:     "Qualify one exact model Harness", Tag: "Control",
			Description: "Makes at most two bounded model requests using one synthetic in-memory echo tool. It executes no tool, shell, file, browser, Docker, or network target action; response text and raw Provider errors are never returned.",
			DataType:    reflect.TypeOf(ModelHarnessQualificationView{}),
			RequestType: reflect.TypeOf(ModelHarnessQualificationRequestView{}),
			Control:     true},
		{Path: ProviderCredentialsPath, OperationID: "listProviderCredentials",
			Summary: "Inspect Provider credential status", Tag: "Models",
			Description: "Returns only configured status and OS-store availability. It never reads credential plaintext into an API response.",
			DataType:    reflect.TypeOf(ProviderCredentialListView{})},
		{Path: ChatGPTAuthPath, OperationID: "getChatGPTAuthStatus",
			Summary: "Inspect ChatGPT accounts and pending sign-in", Tag: "Models",
			Description: "Returns account labels, model choices and a one-time local browser entry point. Tokens, authorization URLs, PKCE values and OAuth state are never returned.",
			DataType:    reflect.TypeOf(application.ChatGPTAuthStatus{})},
		{Path: ChatGPTAuthPath + "/login", Method: http.MethodPost, OperationID: "startChatGPTLogin",
			Summary: "Continue with ChatGPT in the system browser", Tag: "Control", Control: true,
			RequestType: reflect.TypeOf(application.ChatGPTAuthRequest{}), DataType: reflect.TypeOf(application.ChatGPTAuthStatus{})},
		{Path: ChatGPTAuthPath + "/cancel", Method: http.MethodPost, OperationID: "cancelChatGPTLogin",
			Summary: "Cancel the exact pending ChatGPT sign-in", Tag: "Control", Control: true,
			RequestType: reflect.TypeOf(application.ChatGPTAuthRequest{}), DataType: reflect.TypeOf(application.ChatGPTAuthStatus{})},
		{Path: ChatGPTAuthPath + "/logout", Method: http.MethodPost, OperationID: "logoutChatGPTAccount",
			Summary: "Revoke and clear one ChatGPT account session", Tag: "Control", Control: true,
			RequestType: reflect.TypeOf(application.ChatGPTAuthRequest{}), DataType: reflect.TypeOf(application.ChatGPTAuthStatus{})},
		{Path: ChatGPTAuthPath + "/models", Method: http.MethodPost, OperationID: "refreshChatGPTModels",
			Summary: "Refresh the selected ChatGPT account model catalog", Tag: "Control", Control: true,
			RequestType: reflect.TypeOf(application.ChatGPTAuthRequest{}), DataType: reflect.TypeOf(application.ChatGPTAuthStatus{})},
		{Path: ChatGPTAuthPath + "/usage", Method: http.MethodPost, OperationID: "openChatGPTUsage",
			Summary: "Open the fixed ChatGPT usage settings page", Tag: "Control", Control: true,
			RequestType: reflect.TypeOf(application.ChatGPTAuthRequest{}), DataType: reflect.TypeOf(application.ChatGPTAuthStatus{})},
		{Path: ProviderCredentialPathTemplate, Method: http.MethodPost,
			OperationID: "changeProviderCredential", Summary: "Set or delete an OS-owned Provider credential",
			Tag:         "Control",
			Description: "Accepts one transient secret for direct storage in the OS credential manager, or deletes one exact Provider credential, then atomically reloads a Go-owned Provider Registry generation. Active calls retain their captured Provider and plaintext is never returned, persisted in SQLite, logged, or placed in an event.",
			DataType:    reflect.TypeOf(ProviderCredentialStatusView{}),
			RequestType: reflect.TypeOf(ProviderCredentialRequestView{}), Control: true,
			Parameters: []openAPIParameter{credentialProviderName}},
		{Path: PriceSnapshotsPath, Method: http.MethodGet,
			OperationID: "listPriceSnapshots", Summary: "List operator price snapshots",
			Tag: "Models", Description: "Returns the bounded imported price table history with entry-level micro-USD prices. Prices are operator configuration, never Provider output.",
			DataType: reflect.TypeOf(PriceSnapshotListView{})},
		{Path: PriceSnapshotsPath, Method: http.MethodPost,
			OperationID: "importPriceSnapshot", Summary: "Import an operator price snapshot",
			Tag: "Control", Description: "Atomically replaces the active operator price table from one bounded document. Same-content imports replay idempotently; a Provider, README, Skill, or repository file can never reach this operation.",
			DataType:    reflect.TypeOf(PriceSnapshotImportView{}),
			RequestType: reflect.TypeOf(PriceSnapshotImportRequestView{}), Control: true,
			SuccessStatus: http.StatusOK},
		{Path: OpenAPIPath, OperationID: "getOpenAPI", Summary: "Read the OpenAPI contract",
			Description: "Returns the raw deterministic OpenAPI 3.1 JSON document under the same authentication boundary.",
			Tag:         "System", RawDocument: true},
		{Path: "/api/v1/operation-receipts", OperationID: "listOperationReceipts",
			Summary: "List durable operation receipts", Tag: "Operations",
			Description: "Returns a refreshable bounded history derived from terminal FileEdit apply, foreground Run wake, and inert Skill installation facts. It omits operation keys, paths, content digests, requester identities, lease identities, and package archive metadata.",
			DataType:    reflect.TypeOf(OperationReceiptHistoryView{}),
			Parameters: []openAPIParameter{
				identityQueryParameter("run_id", "Optional exact Run filter"),
				{Name: "limit", In: "query", Description: "Maximum terminal receipts",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": operationreceipt.MaxHistoryItems,
						"default": application.DefaultOperationReceiptHistoryLimit}},
			}},
		{Path: ThreadApplicationServicesPathTemplate, OperationID: "listThreadApplicationServices", Summary: "List task command service candidates", Tag: "Threads",
			Description: "Bounded metadata from Jobs in every Run belonging to this Thread. Source message identity is present only when an exact background start and operator delivery are proven. Does not read command output, start work, probe URLs, or assert readiness.",
			DataType:    reflect.TypeOf(application.ThreadApplicationServicesView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID, {Name: "limit", In: "query", Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": application.MaxThreadApplicationServicesLimit, "default": application.DefaultThreadApplicationServicesLimit}}}},
		{Path: ThreadApplicationServicePathTemplate, OperationID: "getThreadApplicationService", Summary: "Read one exact task command service", Tag: "Threads",
			Description: "Reads an exact Thread-owned Run/Job and a bounded sanitized output tail. Loopback URLs are unverified command-output candidates; no network or readiness check is performed.",
			DataType:    reflect.TypeOf(application.ThreadApplicationServiceDetailView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID, pathIdentityParameter("job_id", "Exact command Job identity")}},
		{Path: ThreadApplicationServiceStopPathTemplate, Method: http.MethodPost, OperationID: "stopThreadApplicationService", Summary: "Stop one exact owned task command service", Tag: "Control", Control: true, NotFound: true, SuccessStatus: http.StatusOK,
			Description: "Cleanup only under Run execution control. Requires the expected original Run and the fixed application-stop-{job_id} Idempotency-Key. Only this process's exact Job owner may cancel; repeated stopping observes its fact without upgrading to Kill. Reaped terminal Jobs replay and cold active Jobs fail closed. No persisted PID is signalled.",
			DataType:    reflect.TypeOf(application.ThreadApplicationServiceStopView{}), RequestType: reflect.TypeOf(ThreadApplicationServiceStopRequestView{}),
			Parameters: []openAPIParameter{threadID, pathIdentityParameter("job_id", "Exact command Job identity"), {Name: "Idempotency-Key", In: "header", Required: true, Schema: map[string]any{"type": "string"}}}},
		{Path: ThreadCollectionPath, OperationID: "listThreads", Summary: "List Threads", Tag: "Threads",
			Description: "Returns canonical tasks in created_at DESC, id DESC order. Optional q filters all matching titles before pagination, not only loaded pages. q is trimmed, uses ASCII case-insensitive literal substring matching (other Unicode is exact), and treats % and _ literally. Empty q preserves the unfiltered listing. Cursors are scoped to the normalized title query and lifecycle filters; restart pagination when changing filters. Every list item includes execution_state from a bounded Go-owned live/durable projection; unknown is never idle. composer_state separately describes input availability. This is a current observation, not a frozen snapshot of concurrent title, lifecycle or execution changes.",
			DataType:    reflect.TypeOf(ThreadView{}), Collection: true, Paginated: true,
			Parameters: append(paginationParameters(),
				stringQueryParameter("status", "Exact Thread lifecycle status", []string{
					string(domain.ThreadActive), string(domain.ThreadArchived), string(domain.ThreadDeleted)}),
				openAPIParameter{Name: "q", In: "query", Description: "Trimmed literal title substring across all matching tasks; at most 256 Unicode characters. ASCII case-insensitive, other Unicode exact; no message-body search.", Schema: map[string]any{"type": "string", "maxLength": domain.MaxThreadTitleQueryRunes}},
				booleanQueryParameter("include_deleted", "Include soft-deleted Threads"))},
		{Path: TaskSkillCandidatesPath, OperationID: "listTaskSkillCandidates", Summary: "List exact installed Skill candidates for one task mode", Tag: "Runs",
			Description: "Read-only, workspace-bound candidates for explicit per-Run context selection. Resolves historical installations and enabled Plugin Skill components through the same exact-ref, catalog, surface, phase, Profile and token checks as selection. Native refs copy the exact canonical skill_read component pin, including revision and installation generation. Missing objects and ambiguous or ineligible refs are withheld individually. A complete bounded inventory is required. No source content, installation, activation or capability grant is returned.",
			DataType:    reflect.TypeOf(application.TaskSkillCandidatesView{}), NotFound: true,
			Parameters: []openAPIParameter{
				{Name: "workspace_id", In: "query", Required: true, Schema: map[string]any{"type": "string"}},
				{Name: "surface", In: "query", Required: true, Schema: map[string]any{"type": "string", "enum": []string{"code", "cyber"}}},
				{Name: "phase", In: "query", Required: true, Schema: map[string]any{"type": "string", "enum": []string{"plan", "deliver"}}},
				{Name: "profile", In: "query", Required: true, Schema: map[string]any{"type": "string", "enum": []string{"code", "learn", "review", "script"}}},
			}},
		{Path: TaskConfigurationPreviewPath, Method: http.MethodPost, OperationID: "previewTaskConfiguration", Summary: "Preview task budget and project narrowing", Tag: "Runs", SuccessStatus: http.StatusOK,
			Description: "Read-only bounded workspace configuration preview using the read bearer. Loads inert .prayu/config.yaml through the same Go resolver as creation. Rejections block the entire creation; no source bytes, host paths or secrets are returned. No command, model, network, Skill or capability is executed or granted. Cost limits require an operator price snapshot at execution and do not represent exact billing.",
			DataType:    reflect.TypeOf(application.TaskConfigurationView{}), RequestType: reflect.TypeOf(application.TaskConfigurationRequest{}), NotFound: true},
		{Path: RunTaskConfigurationPathTemplate, OperationID: "getRunTaskConfiguration", Summary: "Read immutable task configuration", Tag: "Runs",
			Description: "Projects only stored Run budget and project snapshot, never live repository configuration. Legacy snapshots use snapshot provenance when operator input was not retained. This read grants no capability.",
			DataType:    reflect.TypeOf(application.TaskConfigurationView{}), Parameters: []openAPIParameter{runID}, NotFound: true},
		{Path: ProjectSkillSelectionPathTemplate, Method: http.MethodPost, OperationID: "selectProjectSkills", Summary: "Select exact pinned project Skill recommendations", Tag: "Control", SuccessStatus: http.StatusOK,
			Description: "Explicit selection before the first turn through the existing built-in and installed external Skill services. Refs must match the immutable project suggestions exactly, including version. External context requires confirmation. This endpoint never installs a package or grants a capability. Same-key recovery preserves selection receipts and the Created Run on failure.",
			DataType:    reflect.TypeOf(application.ProjectSkillSelectionResult{}), RequestType: reflect.TypeOf(ProjectSkillSelectionRequestView{}), Control: true,
			Parameters: []openAPIParameter{runID, dockerSandboxIdempotencyKey}, NotFound: true},
		{Path: InstalledSkillSelectionPathTemplate, Method: http.MethodPost, OperationID: "selectInstalledSkills", Summary: "Select exact installed Skills for one Run", Tag: "Control", SuccessStatus: http.StatusOK,
			Description: "Explicit selection before the first turn for any installed Skill ref, without editing the pinned project configuration. Legacy refs retain name@version; native refs are the exact canonical skill_read component pin returned by task Skill candidates, including revision and installation generation. Reuses the built-in and installed external Skill selection services, so Run state, mode binding, untrusted-context confirmation, catalog eligibility and replay rules are identical to project-pinned selection. This endpoint never installs a package or grants a capability. Same-key recovery preserves selection receipts and the Created Run on failure.",
			DataType:    reflect.TypeOf(application.ProjectSkillSelectionResult{}), RequestType: reflect.TypeOf(InstalledSkillSelectionRequestView{}), Control: true,
			Parameters: []openAPIParameter{runID, dockerSandboxIdempotencyKey}, NotFound: true},
		{Path: ThreadCollectionPath, Method: http.MethodPost,

			OperationID: "createThread", Summary: "Create a Thread", Tag: "Control",
			Description: "Atomically creates one stable Thread with its initial Mission, Run, Session, closed execution mode, all-denied process authority snapshots, root Agent, and audit events. Network remains disabled unless the request supplies a bounded exact public HTTPS host allowlist.",
			DataType:    reflect.TypeOf(ThreadCreationControlView{}),
			RequestType: reflect.TypeOf(ThreadCreationControlRequestView{}), Control: true,
			Parameters: []openAPIParameter{dockerSandboxIdempotencyKey}},
		{Path: ThreadCreationRequestPath, OperationID: "inspectThreadCreationRequest",
			Summary: "Observe an original Thread creation request", Tag: "Threads",
			Description: "Pure read using the original Idempotency-Key and workspace identity. Does not create, replay, admit models, or execute. not_received means absent at this read; an in-flight original request may still arrive. completed confirms only the creation transaction, including after its Run has changed state.",
			DataType:    reflect.TypeOf(ThreadRequestObservationView{}), NotFound: true,
			Parameters: []openAPIParameter{dockerSandboxIdempotencyKey, {Name: "workspace_id", In: "query", Required: true, Schema: map[string]any{"type": "string"}}}},
		{Path: ThreadTurnRequestPathTemplate, OperationID: "inspectThreadTurnRequest",
			Summary: "Observe an original Thread turn request", Tag: "Threads",
			Description: "Pure read of the original Thread-scoped intent, message and exact durable completion or failure. Never reserves, enqueues, seals failures, grants authority or resumes execution. received includes reserved or unsettled work; committed input alone is not success. A not_received snapshot is not a guarantee against later arrival.",
			DataType:    reflect.TypeOf(ThreadRequestObservationView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey}},
		{Path: ThreadPlanControlPathTemplate, OperationID: "inspectThreadPlanRequest",
			Summary: "Observe the original Thread Plan operation", Tag: "Threads",
			Description: "Pure read of the original selection, mode transition and Thread turn ledgers. Requires the original key and run/action binding. Does not replay, enqueue or execute; prepared means saved planning steps with no confirmed execution message.",
			DataType:    reflect.TypeOf(ThreadPlanControlView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey,
				{Name: "run_id", In: "query", Required: true, Schema: map[string]any{"type": "string"}},
				{Name: "action", In: "query", Required: true, Schema: map[string]any{"type": "string", "enum": []string{"enter_plan", "enter_deliver", "confirm"}}}}},
		{Path: ThreadPlanControlPathTemplate, Method: http.MethodPost, OperationID: "controlThreadPlan",
			Summary: "Plan or confirm and execute in the same Thread", Tag: "Control", Control: true,
			Description: "Reuses Plan selection, mode transition and Thread turn services. Confirmation binds the current proposal, direction, acceptance and visible operator message. A stale plan is rejected; retry preserves the original key. Plan confirmation never grants tool or filesystem authority.",
			DataType:    reflect.TypeOf(ThreadPlanControlView{}), RequestType: reflect.TypeOf(ThreadPlanControlRequestView{}),
			SuccessStatus: http.StatusAccepted, NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey}},
		{Path: ThreadModelRoutePathTemplate, OperationID: "getThreadModelRoute",
			Summary: "Read a Thread's preferred next-Run model route", Tag: "Threads",
			Description: "Returns the explicit Thread preference or the inherited default without exposing credentials. A running Run is never changed.",
			DataType:    reflect.TypeOf(ThreadModelRouteView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: ThreadModelRoutePathTemplate, Method: http.MethodPut,
			OperationID: "selectThreadModelRoute",
			Summary:     "Select or reset a Thread's next-Run model route", Tag: "Control",
			Description: "Persists an idempotent qualified Provider/model preference for the next successor Run. It never mutates the current Run, calls a model, or returns credential material.",
			DataType:    reflect.TypeOf(ThreadModelRouteView{}),
			RequestType: reflect.TypeOf(ThreadModelRouteControlRequestView{}),
			Control:     true, SuccessStatus: http.StatusOK, NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: ProviderSearchReadinessPathTemplate,
			OperationID: "getProviderSearchReadiness",
			Summary:     "Inspect a Thread's Web search readiness", Tag: "Threads",
			Description: "Returns a credential-free, read-only projection over the active Run network allowlist, exact model route, configured search policy, and already-observed Provider qualification cache. It performs no Provider request, never treats declared_unverified as ready, and grants no capability.",
			DataType:    reflect.TypeOf(ProviderSearchReadinessView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/search-diagnostics", Method: http.MethodPost,
			OperationID: "diagnoseThreadSearch", Summary: "Check the selected search backend", Tag: "Control",
			Description: "Explicit bounded search connection check using the active Run model and current authority. May incur provider search costs. Does not approve tools, replay a task, probe other backends or test specialized source connectors.",
			DataType:    reflect.TypeOf(application.SearchDiagnostics{}), RequestType: reflect.TypeOf(searchDiagnosticsRequest{}),
			Control: true, SuccessStatus: http.StatusOK, NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}", OperationID: "getThread",
			Summary: "Inspect a Thread", Tag: "Threads",
			Description: "Returns the same canonical Thread projection with its Mission, active and last Run, and complete succession chain.",
			DataType:    reflect.TypeOf(ThreadDetailView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/runs", OperationID: "listThreadRuns",
			Summary: "List Thread Runs", Tag: "Threads",
			Description: "Returns the immutable ordered predecessor/successor Run chain for one Thread.",
			DataType:    reflect.TypeOf(ThreadRunView{}), Collection: true, NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: ThreadExecutionPermissionControlPathTemplate, OperationID: "getThreadExecutionPermission",
			Summary: "Inspect a Thread execution permission preference", Tag: "Threads",
			Description: "Returns the immutable, non-authorizing Thread permission preference and whether the current active Run has the same exact policy. Future successor Runs materialize this preference without inheriting a lease, approval, process, adapter, network, credential, or capability grant.",
			DataType:    reflect.TypeOf(ThreadExecutionPermissionControlView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: ThreadExecutionPermissionControlPathTemplate, Method: http.MethodPost,
			OperationID: "selectThreadExecutionPermission",
			Summary:     "Select the execution permission for a Thread", Tag: "Control",
			Description: "Records an explicit operator preference under the same exact risk confirmation and process-local startup gate as a Run permission selection. The transaction applies it to a created or paused current Run, safely pauses an idle running Run before applying it, fails closed while an execution lease or non-quiescent work is active, and materializes it into future successor Runs. No runtime authority is inherited or granted.",
			DataType:    reflect.TypeOf(ThreadExecutionPermissionControlView{}),
			RequestType: reflect.TypeOf(ThreadExecutionPermissionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				threadID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: "/api/v1/threads/{thread_id}/messages", OperationID: "listThreadMessages",
			Summary: "List Thread messages", Tag: "Threads",
			Description: "Returns one ordered projection across every Run-local Session in the Thread without moving or duplicating messages.",
			DataType:    reflect.TypeOf(ThreadMessageView{}), Collection: true, Paginated: true,
			NotFound: true,
			Parameters: append(append([]openAPIParameter{threadID}, paginationParameters()...),
				booleanQueryParameter("include_compacted", "Include compacted historical messages"))},
		{Path: "/api/v1/threads/{thread_id}/transcript", OperationID: "listThreadTranscript",
			Summary: "Read the unified Thread transcript", Tag: "Threads",
			Description: "Returns the newest bounded page of one Go-owned narrative projection across immutable Run boundaries, public messages, allowlisted Harness facts, structured tool item stages, approvals, verification, checkpoints, and delivery. The opaque keyset cursor pages older records without shifting when the active Run appends events or a successor Run is created. Tool arguments, raw output, provider bytes, secrets, and private reasoning are never included.",
			DataType:    reflect.TypeOf(ThreadTranscriptItemView{}), Collection: true,
			Paginated: true, NotFound: true,
			Parameters: append([]openAPIParameter{threadID}, threadTranscriptPaginationParameters()...)},
		{Path: ThreadQueuedMessagesPathTemplate, OperationID: "listThreadQueuedMessages",
			Summary: "Read every durable queued message for the current Thread Run", Tag: "Threads",
			Description: "Reads the complete bounded pending queue and prepared flags in one transaction. Counts exclude committed and cancelled messages. It does not start or resume execution.",
			DataType:    reflect.TypeOf(ThreadQueuedMessagesView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: SessionSteeringRevisionObservationPathTemplate, OperationID: "inspectSessionSteeringRevision",
			Summary: "Observe one exact message revision receipt", Tag: "Sessions",
			Description: "Read-only confirmation of the original operation key and fixed HTTP operator. An absent receipt is not proof that an in-flight request will not commit. This never resubmits the modification.",
			DataType:    reflect.TypeOf(SessionSteeringRevisionObservationView{}), NotFound: true,
			Parameters: []openAPIParameter{sessionID, messageID,
				{Name: "operation_key", In: "path", Required: true, Schema: map[string]any{"type": "string",
					"minLength": domain.MinAgentOperationKeyBytes, "maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: SessionSteeringPromotionObservationPathTemplate, OperationID: "inspectSessionSteeringPromotion",
			Summary: "Observe an exact queue promotion or rejection receipt", Tag: "Sessions",
			Description: "Returns the immutable success or rejection receipt, or current source status from one read transaction. An absent receipt and process-local execution state never prove an in-flight request cannot commit. Never resubmits a promotion. An explicit retry can seal a rejection that fences any delayed original POST.",
			DataType:    reflect.TypeOf(SessionSteeringPromotionObservationView{}), NotFound: true,
			Parameters: []openAPIParameter{sessionID, messageID,
				{Name: "operation_key", In: "path", Required: true, Schema: map[string]any{"type": "string",
					"minLength": domain.MinAgentOperationKeyBytes, "maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: SessionSteeringCancellationObservationPathTemplate, OperationID: "inspectSessionSteeringCancellation",
			Summary: "Observe one exact message cancellation receipt", Tag: "Sessions",
			Description: "Read-only confirmation bound to the original operation key and HTTP operator. If absent, returns the current message status from the same transaction; another cancellation is never reported as this operation's success.",
			DataType:    reflect.TypeOf(SessionSteeringCancellationObservationView{}), NotFound: true,
			Parameters: []openAPIParameter{sessionID, messageID,
				{Name: "operation_key", In: "path", Required: true, Schema: map[string]any{"type": "string",
					"minLength": domain.MinAgentOperationKeyBytes, "maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: ThreadActivityDetailPathTemplate, OperationID: "getThreadActivityDetail",
			Summary: "Inspect safe typed Thread activity details", Tag: "Threads",
			Description: "Lazily returns one Go-owned discriminated detail branch for command, Web search/fetch, file read/edit, MCP, verification, or browser activity. References are resolved through the Thread-to-Run binding and cannot expose another Thread. Environment values, stdin, credentials, raw payload/result JSON, process identities, private host paths, edit bodies, MCP scalar values, and private reasoning are never returned.",
			DataType:    reflect.TypeOf(ThreadActivityDetailView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID, activityRef}},
		{Path: ThreadActivityArtifactPathTemplate,
			OperationID: "getThreadActivityArtifact",
			Summary:     "Read a safe Thread command output artifact", Tag: "Threads",
			Description: "Returns one Thread-, activity-, Run-, and Job-bound command stdout/stderr artifact after a second Go-owned redaction and host-path projection. Interactive-stdin Jobs fail closed. Raw payload JSON, environment values, stdin, credentials, process identities, and private reasoning are never returned.",
			DataType:    reflect.TypeOf(ThreadActivityArtifactView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID, activityRef, artifactID}},
		{Path: "/api/v1/threads/{thread_id}/messages", Method: http.MethodPost,
			OperationID: "submitThreadMessage", Summary: "Continue a Thread", Tag: "Control",
			Description: "Queues input on the live Run, including while waiting for approval. If the last Run is terminal, atomically creates or reuses exactly one fresh successor Run and Session; no approval, lease, process, network, credential, execution profile, or capability authority is inherited.",
			DataType:    reflect.TypeOf(ThreadMessageControlView{}),
			RequestType: reflect.TypeOf(ThreadMessageControlRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey}},
		{Path: ThreadTurnControlPathTemplate, Method: http.MethodPost,
			OperationID: "executeThreadTurn", Summary: "Execute a Thread turn", Tag: "Control",
			Description: "Submits operator text and/or up to four immutable workspace image references, plus up to four optional workspace_file references. Text may be empty when images or uploaded file attachments are present. Up to four immutable uploaded file references are accepted; binary attachments are stored_only, bounded text remains nonauthorizing evidence. The Thread idempotency key permanently binds content and ordered file/image/attachment identities. Image bytes are validated within the Thread workspace and sent natively only to a declared vision-capable route; images confer no authority. File snapshots require an idle task without queued messages or approval. Existing lifecycle, execution-permission, approval, lease, and idempotency boundaries remain authoritative.",
			DataType:    reflect.TypeOf(ThreadMessageControlView{}),
			RequestType: reflect.TypeOf(ThreadTurnControlRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey}},
		{Path: "/api/v1/workspaces/{workspace_id}/file-attachments", Method: http.MethodPost,
			OperationID: "uploadWorkspaceFile", Summary: "Save an immutable file attachment", Tag: "Control", Control: true,
			SuccessStatus: http.StatusOK, Description: "Save up to 5 MiB of original bytes without writing into the repository. The original upload key binds bytes and metadata. Bounded UTF-8 text is redacted; binary files are explicitly stored_only, not parsed. Import grants no execution authority.",
			DataType: reflect.TypeOf(WorkspaceFileAttachmentView{}), RequestType: reflect.TypeOf(WorkspaceFileUploadRequestView{}),
			NotFound: true, Parameters: []openAPIParameter{workspaceID, dockerSandboxIdempotencyKey}},
		{Path: "/api/v1/workspaces/{workspace_id}/file-attachments/request",
			OperationID: "inspectWorkspaceFileUpload", Summary: "Observe an original file upload without retrying", Tag: "Workspaces",
			Description: "GET with the original Idempotency-Key only reads stored receipts. not_received does not rule out a request still arriving. No reservation, clipboard read, upload or model call.",
			DataType:    reflect.TypeOf(domain.FileAttachmentObservation{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID, dockerSandboxIdempotencyKey}},
		{Path: "/api/v1/workspaces/{workspace_id}/file-attachments/{attachment_id}",
			OperationID: "readWorkspaceFileAttachment", Summary: "Read an exact file attachment receipt", Tag: "Workspaces",
			DataType: reflect.TypeOf(WorkspaceFileAttachmentView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID, pathIdentityParameter("attachment_id", "Immutable attachment identity")}},
		{Path: "/api/v1/workspaces/{workspace_id}/file-attachments/{attachment_id}/content",
			OperationID: "readWorkspaceFileAttachmentContent", Summary: "Download original authenticated file bytes", Tag: "Workspaces",
			Description: "Exact original bytes with application/octet-stream attachment disposition, Content-Length, SHA256 ETag, X-Cyberagent-Content-SHA256, nosniff and no-store. Workspace identity and read bearer are required.",
			RawArtifact: true, NotFound: true,
			Parameters: []openAPIParameter{workspaceID, pathIdentityParameter("attachment_id", "Immutable attachment identity")}},
		{Path: "/api/v1/workspaces/{workspace_id}/image-attachments", Method: http.MethodPost,
			OperationID: "uploadWorkspaceImage", Summary: "Save an immutable workspace image", Tag: "Control", Control: true,
			SuccessStatus: http.StatusOK,
			Description:   "Validates original PNG/JPEG/WebP bytes and binds the upload key to exact bytes and display metadata. At most 5 MiB, 8192 pixels per side and 16 Mi pixels. No filesystem path, URL retrieval or execution authority.",
			DataType:      reflect.TypeOf(WorkspaceImageView{}), RequestType: reflect.TypeOf(WorkspaceImageUploadRequestView{}),
			NotFound: true, Parameters: []openAPIParameter{workspaceID, dockerSandboxIdempotencyKey}},
		{Path: "/api/v1/workspaces/{workspace_id}/image-attachments/{image_id}",
			OperationID: "readWorkspaceImage", Summary: "Read an exact workspace image receipt", Tag: "Workspaces",
			DataType: reflect.TypeOf(WorkspaceImageView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID, pathIdentityParameter("image_id", "Immutable image identity")}},
		{Path: "/api/v1/workspaces/{workspace_id}/image-attachments/{image_id}/content",
			OperationID: "readWorkspaceImageContent", Summary: "Read original authenticated image bytes", Tag: "Workspaces",
			Description: "Returns exact original bytes with stored MIME type, Content-Length, SHA256 ETag and X-Cyberagent-Content-SHA256. Read bearer required; no-store and nosniff. The image must belong to the exact workspace.",
			RawArtifact: true, NotFound: true,
			Parameters: []openAPIParameter{workspaceID, pathIdentityParameter("image_id", "Immutable image identity")}},
		{Path: ThreadExecutionPathTemplate,
			OperationID: "getThreadExecution", Summary: "Read current Thread execution ownership", Tag: "Threads",
			Description: "Reports the active request owned by this service, its stopping state and durable queued input count. Idle does not assert that a different process is idle. This identity is not an execution lease or a capability and is never restored after restart.",
			DataType:    reflect.TypeOf(application.ThreadExecutionState{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/review",
			OperationID: "getThreadReview", Summary: "Review recorded task changes and current workspace revision", Tag: "Threads",
			Description: "Aggregates bounded existing records across successor Runs and observes the actual current target without granting authority or attributing arbitrary workspace changes to the task. Revision freshness, missing bindings and omissions remain explicit.",
			DataType:    reflect.TypeOf(application.ThreadReview{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/git", OperationID: "getThreadGit", Summary: "Inspect current task Git target", Tag: "Git",
			DataType: reflect.TypeOf(application.ThreadGitState{}), NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/git/preview", Method: http.MethodPost, OperationID: "previewThreadGit", Summary: "Preview an exact selected-file Git operation", Tag: "Git", Control: true, SuccessStatus: http.StatusOK,
			DataType: reflect.TypeOf(application.ThreadGitPreview{}), RequestType: reflect.TypeOf(application.ThreadGitPreviewRequest{}), NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/git/execute", Method: http.MethodPost, OperationID: "executeThreadGit", Summary: "Execute the explicitly confirmed Git preview once", Tag: "Git", Control: true, SuccessStatus: http.StatusOK,
			DataType: reflect.TypeOf(application.ThreadGitResult{}), RequestType: reflect.TypeOf(application.ThreadGitExecuteRequest{}), NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/git/requests/{operation_key}", OperationID: "observeThreadGit", Summary: "Observe the original Git intent without retrying a write", Tag: "Git",
			DataType: reflect.TypeOf(application.ThreadGitResult{}), NotFound: true, Parameters: []openAPIParameter{threadID, pathIdentityParameter("operation_key", "Original operation key")}},
		{Path: "/api/v1/threads/{thread_id}/pull-request", OperationID: "discoverThreadPullRequest", Summary: "Discover remote PRs for the actual task branch", Tag: "GitHub Review",
			DataType: reflect.TypeOf(application.ThreadPullRequestDiscovery{}), NotFound: true, Parameters: []openAPIParameter{threadID,
				{Name: "connection_id", In: "query", Schema: map[string]any{"type": "string"}}, {Name: "base_branch", In: "query", Schema: map[string]any{"type": "string"}}}},
		{Path: "/api/v1/threads/{thread_id}/pull-request/preview", Method: http.MethodPost, OperationID: "previewThreadPullRequest", Summary: "Prepare an exact draft PR and one-time approval", Tag: "GitHub Review", Control: true, SuccessStatus: http.StatusOK,
			DataType: reflect.TypeOf(application.ThreadPullRequestPreviewResult{}), RequestType: reflect.TypeOf(application.ThreadPullRequestPreviewRequest{}), NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/pull-request/create", Method: http.MethodPost, OperationID: "createThreadPullRequest", Summary: "Create the approved draft PR at most once", Tag: "GitHub Review", Control: true, SuccessStatus: http.StatusOK,
			DataType: reflect.TypeOf(application.ThreadPullRequestResult{}), RequestType: reflect.TypeOf(application.ThreadPullRequestCreateRequest{}), NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/pull-request/request", OperationID: "observeThreadPullRequest", Summary: "Observe original draft PR intent without repeating creation", Tag: "GitHub Review",
			DataType: reflect.TypeOf(application.ThreadPullRequestResult{}), NotFound: true, Parameters: []openAPIParameter{threadID, {Name: "operation_key", In: "query", Required: true, Schema: map[string]any{"type": "string"}}}},
		{Path: "/api/v1/threads/{thread_id}/pull-request/refresh", Method: http.MethodPost, OperationID: "refreshThreadPullRequest", Summary: "Fetch remote CI and comments and recheck PR head", Tag: "GitHub Review", Control: true, SuccessStatus: http.StatusOK,
			DataType: reflect.TypeOf(application.ThreadPullRequestRefreshResult{}), RequestType: reflect.TypeOf(application.ThreadPullRequestRefreshRequest{}), NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/threads/{thread_id}/pull-request/credential", Method: http.MethodPost, OperationID: "setThreadPullRequestCredential", Summary: "Save a connection credential in the system store", Tag: "GitHub Review", Control: true, SuccessStatus: http.StatusOK,
			DataType: reflect.TypeOf(application.GitHubReviewCredentialView{}), RequestType: reflect.TypeOf(application.ThreadPullRequestCredentialRequest{}), NotFound: true, Parameters: []openAPIParameter{threadID}},
		{Path: ThreadInterruptPathTemplate, Method: http.MethodPost,
			OperationID: "interruptThreadExecution", Summary: "Stop the exact current Thread execution", Tag: "Control",
			Description: "Cancels only the matching live execution context. Stopping remains visible until owned work and pending-message cleanup return. A stale identity cannot stop a later execution. Repeated requests are harmless; no running process authority is restored from persisted history.",
			DataType:    reflect.TypeOf(application.ThreadExecutionState{}),
			RequestType: reflect.TypeOf(ThreadInterruptRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey}},
		{Path: ThreadRunRecoveryControlPathTemplate, Method: http.MethodPost,
			OperationID: "recoverThreadRun", Summary: "End a failed Thread Run", Tag: "Control",
			Description: "Explicitly ends the exact active running Run only when its latest durable execution handoff failed and no execution lease remains active. Pending steering on the old Run is cancelled and never copied. The next Thread submission atomically creates a successor using the selected next-Run model and permission preferences.",
			DataType:    reflect.TypeOf(ThreadRunRecoveryControlView{}),
			RequestType: reflect.TypeOf(ThreadRunRecoveryControlRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey},
			SuccessStatus: http.StatusOK},
		{Path: "/api/v1/threads/{thread_id}/archive", Method: http.MethodPost,
			OperationID: "archiveThread", Summary: "Archive a Thread", Tag: "Control",
			Description: "Archives the stable Thread and its Run-local Sessions without deleting or orphaning Run, message, or audit history.",
			DataType:    reflect.TypeOf(ThreadLifecycleControlView{}),
			RequestType: reflect.TypeOf(ThreadLifecycleControlRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey},
			SuccessStatus: http.StatusOK},
		{Path: "/api/v1/threads/{thread_id}/restore", Method: http.MethodPost,
			OperationID: "restoreThread", Summary: "Restore a Thread", Tag: "Control",
			Description: "Restores the same Thread identity and its Sessions; it does not create a new Run unless later composer input requires a successor.",
			DataType:    reflect.TypeOf(ThreadLifecycleControlView{}),
			RequestType: reflect.TypeOf(ThreadLifecycleControlRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey},
			SuccessStatus: http.StatusOK},
		{Path: "/api/v1/threads/{thread_id}/delete", Method: http.MethodPost,
			OperationID: "deleteThread", Summary: "Soft-delete a Thread", Tag: "Control",
			Description: "Removes the Thread from ordinary lists while retaining a lossless exportable Run, Session, message, and audit graph.",
			DataType:    reflect.TypeOf(ThreadLifecycleControlView{}),
			RequestType: reflect.TypeOf(ThreadLifecycleControlRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{threadID, dockerSandboxIdempotencyKey},
			SuccessStatus: http.StatusOK},
		{Path: "/api/v1/threads/{thread_id}/export", OperationID: "exportThread",
			Summary: "Export a Thread", Tag: "Threads",
			Description: "Returns the complete self-contained Thread, Mission, Run chain, Run-local Sessions, message provenance, and Thread/Run audit events. The HTTP response limit fails closed instead of silently truncating history.",
			DataType:    reflect.TypeOf(ThreadExportView{}), NotFound: true,
			Parameters: []openAPIParameter{threadID}},
		{Path: "/api/v1/runs", OperationID: "listRuns", Summary: "List Runs", Tag: "Runs",
			Description: "Returns a bounded creation-ordered keyset page of durable Runs. Updates and later Runs cannot shift continuation pages.", DataType: reflect.TypeOf(RunView{}),
			Collection: true, Paginated: true, Parameters: append(paginationParameters(),
				stringQueryParameter("status", "Exact Run status filter", runStatuses()),
				identityQueryParameter("mission_id", "Exact Mission identity filter"))},
		{Path: WorkspaceImportPath, Method: http.MethodPost,
			OperationID: "importWorkspace", Summary: "Register an existing server-side directory", Tag: "Control",
			Description: "Requires an explicit startup capability and the distinct control token. Accepts a confirmed absolute directory on the server computer, resolves its canonical root and returns bounded workspace metadata. Repeating the same canonical directory returns the same durable workspace. Registration never overwrites another workspace root, modifies directory contents or grants Agent authority. No directory enumeration or browser-file upload is performed; the native Desktop picker remains a separate pathless entry point.",
			DataType:    reflect.TypeOf(WorkspaceImportView{}),
			RequestType: reflect.TypeOf(WorkspaceImportRequestView{}), Control: true,
			SuccessStatus: http.StatusOK},
		{Path: RunCreationControlPath, Method: http.MethodPost,
			OperationID: "createRun", Summary: "Create a controlled Run", Tag: "Control",
			Description: "Atomically creates one Mission, interactive Run, active Session, closed Run mode, preview execution profile, root Agent, and initial events. Network remains disabled unless the request supplies a bounded exact public HTTPS host allowlist; wildcard and global network grants are rejected. The request cannot select a model, budget, existing Session, process backend, or capability grant.",
			DataType:    reflect.TypeOf(RunCreationControlView{}),
			RequestType: reflect.TypeOf(RunCreationControlRequestView{}), Control: true,
			Parameters: []openAPIParameter{
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: EmbeddedAnalyzerExecutionPathTemplate, Method: http.MethodPost,
			OperationID: "executeEmbeddedAnalyzer", Summary: "Execute the fixed embedded analyzer",
			Tag:         "Control",
			Description: "Consumes one bounded text value or workspace-relative regular file through the fixed embedded Rust/WASI fixture. The guest receives only bounded stdin/stdout, no filesystem, environment, network, subprocess, arbitrary module, or reusable execution authority.",
			DataType:    reflect.TypeOf(EmbeddedAnalyzerExecutionControlView{}),
			RequestType: reflect.TypeOf(EmbeddedAnalyzerExecutionRequestView{}), Control: true,
			SuccessStatus: http.StatusCreated,
			Parameters:    []openAPIParameter{runID}},
		{Path: "/api/v1/workspaces", OperationID: "listWorkspaces", Summary: "List Workspaces",
			Tag: "Workspaces", Description: "Returns registered Workspace ids and names without local root paths.",
			DataType: reflect.TypeOf(WorkspaceView{}), Collection: true, Paginated: true,
			Parameters: paginationParameters()},
		{Path: "/api/v1/workspaces/{workspace_id}/explore",
			OperationID: "exploreWorkspace", Summary: "Inspect a bounded Workspace entry",
			Tag:         "Workspaces",
			Description: "Lists one directory level or returns a bounded redacted UTF-8 file preview. Go resolves the registered Workspace root, rejects traversal and symbolic links, omits internal staging files, and marks all content as non-authorizing evidence. Local root paths are never returned.",
			DataType:    reflect.TypeOf(WorkspaceExplorerView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID,
				{Name: "path", In: "query", Description: "Canonical Workspace-relative path returned by a previous explorer response; defaults to the root",
					Schema: map[string]any{"type": "string", "maxLength": workspace.MaxExplorerPathRunes,
						"default": "."}}}},
		{Path: "/api/v1/workspaces/{workspace_id}/search",
			OperationID: "searchWorkspace", Summary: "Search bounded Workspace evidence",
			Tag:         "Workspaces",
			Description: "Performs one deterministic bounded scan over redacted UTF-8 Explorer projections. It follows no links, starts no indexer, returns canonical relative references and snippets only, and marks every result as non-authorizing evidence.",
			DataType:    reflect.TypeOf(WorkspaceSearchView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID,
				{Name: "query", In: "query", Description: "Normalized case-insensitive filename or redacted text query",
					Required: true, Schema: map[string]any{"type": "string", "minLength": 1,
						"maxLength": workspace.MaxSearchQueryRunes}}}},
		{Path: "/api/v1/workspaces/{workspace_id}/repository-state",
			OperationID: "getWorkspaceRepositoryState",
			Summary:     "Inspect read-only repository state", Tag: "Workspaces",
			Description: "Returns a bounded status-only projection for a Git repository rooted exactly at the registered Workspace. Go does not discover parent repositories, return file content, patches, remotes or local root paths, start a process, execute hooks, or use the network.",
			DataType:    reflect.TypeOf(RepositoryStateView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID}},
		{Path: "/api/v1/workspaces/{workspace_id}/repository-diff",
			OperationID: "getWorkspaceRepositoryDiff",
			Summary:     "Inspect bounded repository patches", Tag: "Workspaces",
			Description: "Returns secret-redacted bounded UTF-8 patches for a Git repository rooted exactly at the registered Workspace. Go follows no links, discovers no parent repository, starts no process, executes no hooks, uses no network, and grants no mutation authority.",
			DataType:    reflect.TypeOf(RepositoryDiffView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID}},
		{Path: "/api/v1/workspaces/{workspace_id}/repository-history",
			OperationID: "getWorkspaceRepositoryHistory",
			Summary:     "Inspect bounded local repository history", Tag: "Workspaces",
			Description: "Returns at most fifty first-parent commit subjects and sixty-four local branch heads for a Git repository rooted exactly at the registered Workspace. Subjects are secret-redacted; author identities, commit bodies, remotes, host paths, processes, hooks, and network access are excluded.",
			DataType:    reflect.TypeOf(RepositoryHistoryView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID}},
		{Path: "/api/v1/workspaces/{workspace_id}/repository-file-history",
			OperationID: "getWorkspaceRepositoryFileHistory",
			Summary:     "Inspect bounded history for one exact file path", Tag: "Workspaces",
			Description: "Follows at most 512 first-parent commits and returns at most fifty metadata-only changes for one canonical repository-relative path. It does not infer renames or expose blob/patch content, identities, remotes, host roots, checkout, reference mutation, processes, hooks, network, or authority.",
			DataType:    reflect.TypeOf(RepositoryFileHistoryView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID,
				{Name: "path", In: "query", Description: "Canonical repository-relative file path",
					Required: true, Schema: map[string]any{"type": "string", "minLength": 1,
						"maxLength": repository.MaxPathRunes}}}},
		{Path: "/api/v1/workspaces/{workspace_id}/repository-commits/{object_id}",
			OperationID: "getWorkspaceRepositoryCommit",
			Summary:     "Inspect exact commit changed-file metadata", Tag: "Workspaces",
			Description: "Compares one exact local commit object with its first parent and returns only bounded path, change-kind, file-kind, and content/mode-change metadata. It returns no author, body, blob, patch, remote, or host path and performs no checkout, ref update, process, hook, or network operation.",
			DataType:    reflect.TypeOf(RepositoryCommitDetailView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID, commitObjectID}},
		{Path: "/api/v1/workspaces/{workspace_id}/repository-commit-comparison",
			OperationID: "compareWorkspaceRepositoryCommits",
			Summary:     "Compare two exact commit trees", Tag: "Workspaces",
			Description: "Compares two exact local commit objects through bounded tree metadata. The commits need not have an ancestor relationship. The response contains no author, body, blob, patch, remote, host path, rename inference, mutation, process, hook, network, or authority.",
			DataType:    reflect.TypeOf(RepositoryCommitComparisonView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID,
				{Name: "base_object_id", In: "query", Description: "Exact lowercase base commit object identity",
					Required: true, Schema: map[string]any{"type": "string", "minLength": 40,
						"maxLength": 40, "pattern": `^[0-9a-f]{40}$`}},
				{Name: "head_object_id", In: "query", Description: "Exact lowercase head commit object identity",
					Required: true, Schema: map[string]any{"type": "string", "minLength": 40,
						"maxLength": 40, "pattern": `^[0-9a-f]{40}$`}}}},
		{Path: "/api/v1/workspaces/{workspace_id}/repository-commits/{object_id}/file-preview",
			OperationID: "getWorkspaceRepositoryCommitFilePreview",
			Summary:     "Preview one redacted file from an exact commit", Tag: "Workspaces",
			Description: "Returns a bounded secret-redacted UTF-8 projection of one regular file from one exact local commit object. Links, binary data, oversized content, missing objects, raw blobs, host roots, remotes, checkout, ref mutation, processes, hooks, and network access are refused or excluded; returned text remains non-authorizing evidence.",
			DataType:    reflect.TypeOf(RepositoryCommitFilePreviewView{}), NotFound: true,
			Parameters: []openAPIParameter{workspaceID, commitObjectID,
				{Name: "path", In: "query", Description: "Canonical repository-relative file path",
					Required: true, Schema: map[string]any{"type": "string", "minLength": 1,
						"maxLength": repository.MaxPathRunes}}}},
		{Path: EvidenceAttachmentPathTemplate, Method: http.MethodPost,
			OperationID: "attachRunEvidence", Summary: "Attach non-authorizing Workspace evidence",
			Tag:         "Control",
			Description: "Revalidates one exact redacted Workspace file projection and atomically appends it to the bound Session as tool-role evidence. Document text never becomes operator instruction and the operation starts no model, tool, process, or network call.",
			DataType:    reflect.TypeOf(EvidenceAttachmentView{}),
			RequestType: reflect.TypeOf(EvidenceAttachmentRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: EvidenceAttachmentPathTemplate, OperationID: "listRunEvidence",
			Summary: "List attached non-authorizing evidence", Tag: "Runs",
			Description: "Returns a bounded metadata-only inventory of immutable Workspace evidence attachments. It omits document and Session message content, operation keys, request fingerprints, requester identities, event sequence, and capability metadata.",
			DataType:    reflect.TypeOf(EvidenceInventoryView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: VerificationEvidencePathTemplate, OperationID: "listRunVerificationEvidence",
			Summary: "List immutable operator verification evidence", Tag: "Runs",
			Description: "Returns at most one hundred exact Run/Session/Workspace-bound operator observations. Entries are secret-redacted and are neither model assertions, approvals, commands, nor execution authority.",
			DataType:    reflect.TypeOf(VerificationEvidenceInventoryView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: VerificationEvidencePathTemplate, Method: http.MethodPost,
			OperationID: "recordRunVerificationEvidence", Summary: "Record operator verification evidence",
			Tag:         "Control",
			Description: "Atomically records one secret-redacted immutable pass, fail, or unknown operator observation against the exact active Run Session and Workspace. It executes no command, accepts no model assertion, grants no approval, and authorizes no action.",
			DataType:    reflect.TypeOf(VerificationEvidenceControlView{}),
			RequestType: reflect.TypeOf(VerificationEvidenceRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: VerificationPlanPathTemplate, OperationID: "listRunVerificationPlans",
			Summary: "List immutable operator verification plans", Tag: "Runs",
			Description: "Returns at most fifty exact Code Run/Session/Workspace-bound operator checklists. Plans contain guidance only and cannot represent a result, model assertion, command, approval, or execution authority.",
			DataType:    reflect.TypeOf(VerificationPlanInventoryView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: VerificationPlanPathTemplate, Method: http.MethodPost,
			OperationID: "recordRunVerificationPlan", Summary: "Record an operator verification plan",
			Tag:         "Control",
			Description: "Atomically records one secret-redacted immutable operator checklist against the exact active Code Run Session and Workspace. It executes no command and has no outcome, pass inference, model assertion, approval, or action authority.",
			DataType:    reflect.TypeOf(VerificationPlanControlView{}),
			RequestType: reflect.TypeOf(VerificationPlanRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: VerificationCoveragePathTemplate,
			OperationID: "getRunVerificationPlanCoverage",
			Summary:     "Inspect explicit verification plan coverage", Tag: "Runs",
			Description: "Derives bounded metadata-only per-item counts from immutable operator associations. It exposes explicit pass, fail, and unknown observation counts but infers no item result, rewrites no record, and grants no command, approval, or execution authority.",
			DataType:    reflect.TypeOf(VerificationPlanCoverageInventoryView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: VerificationCoverageDetailPathTemplate,
			OperationID: "getRunVerificationPlanItemCoverage",
			Summary:     "Inspect evidence associations for one verification plan item", Tag: "Runs",
			Description: "Returns one opaque, route-scoped page of exact metadata-only evidence references plus explicit pass, fail, and unknown counts for one immutable plan item. Private plan guidance, evidence bodies, operator identity, aggregate result inference, mutation, approval, commands, models, and authority are excluded.",
			DataType:    reflect.TypeOf(VerificationPlanItemCoverageDetailView{}), NotFound: true,
			Paginated: true, Parameters: append([]openAPIParameter{runID, verificationPlanID,
				verificationPlanItemOrdinal}, paginationParameters()...)},
		{Path: VerificationSnapshotExportPathTemplate,
			OperationID: "exportRunVerificationPlanItemSnapshot",
			Summary:     "Export one verification item snapshot", Tag: "Runs",
			Description: "Builds a deterministic bounded JSON or Markdown receipt from the current frozen association high-water for one exact verification plan item. It includes only opaque association references and explicit outcome counts; private plan/evidence bodies, operator identity, verdict inference, mutation, approval, command, model, and execution authority remain excluded.",
			DataType:    reflect.TypeOf(VerificationSnapshotExportView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, verificationPlanID,
				verificationPlanItemOrdinal,
				{Name: "format", In: "query", Description: "Bounded snapshot representation",
					Required: true, Schema: map[string]any{"type": "string",
						"enum": []string{application.VerificationSnapshotExportFormatMarkdown,
							application.VerificationSnapshotExportFormatJSON}}}}},
		{Path: VerificationAssociationPathTemplate, Method: http.MethodPost,
			OperationID: "associateRunVerificationEvidence",
			Summary:     "Associate evidence with one verification plan item", Tag: "Control",
			Description: "Appends one immutable operator association between an earlier checklist item and a later observation in the same active Code Run. It does not alter either record, execute the check, infer a result, call a model, approve an action, or grant authority.",
			DataType:    reflect.TypeOf(VerificationAssociationControlView{}),
			RequestType: reflect.TypeOf(VerificationAssociationRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: VerificationSnapshotReceiptPathTemplate,
			OperationID: "listRunVerificationSnapshotReceipts",
			Summary:     "List immutable verification snapshot receipts", Tag: "Runs",
			Description: "Returns bounded metadata-only history proving that an operator retained exact deterministic verification snapshots. Snapshot/result acceptance, private bodies, operator identity, record rewriting, approval, authority, and execution remain false.",
			DataType:    reflect.TypeOf(VerificationSnapshotReceiptInventoryView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: VerificationSnapshotReceiptPathTemplate, Method: http.MethodPost,
			OperationID: "recordRunVerificationSnapshotReceipt",
			Summary:     "Record one exact verification snapshot receipt", Tag: "Control",
			Description: "Rebuilds and digest-checks one current deterministic item snapshot before appending an immutable metadata-only receipt. Recording does not accept a snapshot or result, expose private bodies or operator identity, rewrite records, approve work, grant authority, or start execution.",
			DataType:    reflect.TypeOf(VerificationSnapshotReceiptControlView{}),
			RequestType: reflect.TypeOf(VerificationSnapshotReceiptRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: VerificationSnapshotReceiptReviewPathTemplate,
			OperationID: "listRunVerificationSnapshotReceiptReviews",
			Summary:     "List immutable verification snapshot receipt reviews", Tag: "Runs",
			Description: "Returns bounded metadata-only operator decisions about exact snapshot receipt metadata. A review is explicitly non-authorizing and never accepts a snapshot or verification result, rewrites a record, approves work, grants authority, or starts execution.",
			DataType:    reflect.TypeOf(VerificationSnapshotReceiptReviewInventoryView{}),
			NotFound:    true, Parameters: []openAPIParameter{runID}},
		{Path: VerificationSnapshotReceiptReviewPathTemplate, Method: http.MethodPost,
			OperationID: "recordRunVerificationSnapshotReceiptReview",
			Summary:     "Record one non-authorizing snapshot receipt review", Tag: "Control",
			Description: "Binds one immutable metadata_confirmed or metadata_disputed operator fact to an exact receipt digest and event sequence. Each receipt can be reviewed once. The decision does not accept the snapshot or result, expose private bodies or identity, rewrite records, approve work, grant authority, or start execution.",
			DataType:    reflect.TypeOf(VerificationSnapshotReceiptReviewControlView{}),
			RequestType: reflect.TypeOf(VerificationSnapshotReceiptReviewRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: CodeHandoffPathTemplate, OperationID: "getCodeHandoff",
			Summary: "Build a resumable Code handoff", Tag: "Runs",
			Description: "Regenerates a metadata-only Code Run handoff from durable Plan, queue, change-set, verification, explicit per-item coverage, non-authorizing snapshot-receipt reviews, pending-action, and report records. Coverage and review decisions preserve explicit metadata without private bodies, reviewer identity, or an inferred aggregate result. The handoff performs no composite mutation, starts no execution, and grants no resume authority.",
			DataType:    reflect.TypeOf(CodeHandoffView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: CodeHandoffExportPathTemplate, OperationID: "exportCodeHandoff",
			Summary: "Export the current Code handoff", Tag: "Runs",
			Description: "Builds a bounded Markdown or JSON download projection from one stable Code handoff snapshot, including non-authorizing snapshot-receipt review metadata. The response includes the durable source event high-water mark and SHA-256 of the exact content; it grants no resume, mutation, report-acceptance, or execution authority.",
			DataType:    reflect.TypeOf(CodeHandoffExportView{}), NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "format", In: "query", Description: "Bounded export representation",
					Required: true, Schema: map[string]any{"type": "string",
						"enum": []string{application.CodeHandoffExportFormatMarkdown,
							application.CodeHandoffExportFormatJSON}}}}},
		{Path: "/api/v1/runs/{run_id}/operator-actions",
			OperationID: "listRunOperatorActions", Summary: "List pending operator actions",
			Tag:         "Runs",
			Description: "Returns one bounded Go-owned projection of pending steering, approval, file-edit review/apply, and due wake facts. Items contain opaque identities and navigation destinations only; no operation key, command, prompt, Diff, path, content, digest, or execution authority is exposed.",
			DataType:    reflect.TypeOf(OperatorActionCenterView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: "/api/v1/runs/{run_id}", OperationID: "getRun", Summary: "Inspect a Run", Tag: "Runs",
			Description: "Returns Run, Mission, checkpoint, tool usage, token-free execution-lease metadata, read-only Plan/Delivery and external-Skill metadata projections, and the Go-derived agent-code-tools.v1 generation with per-tool availability or refusal when present. The capability projection is informational and grants no execution authority.",
			DataType:    reflect.TypeOf(RunDetailView{}), NotFound: true, Parameters: []openAPIParameter{runID}},
		{Path: RunCapabilityReadinessPathTemplate, OperationID: "getRunCapabilityReadiness",
			Summary: "Inspect Run capability readiness", Tag: "Runs",
			Description: "Returns the Go-owned selected, selectable, runtime-available, stable blocker, remediation, and restart-required projection for every Permission, Profile, Interaction, browser CDP, and Standard Code preset option. This read-only projection is not a bearer, does not grant capability, and exposes no Workspace path, Docker endpoint, browser Profile, credential, lease token, or process identity.",
			DataType:    reflect.TypeOf(RunCapabilityReadinessView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: "/api/v1/runs/{run_id}/events", OperationID: "listRunEvents", Summary: "List Run events",
			Tag: "Runs", Description: "Returns the ordered append-only Run event stream with a strict Go-owned metadata projection for every payload. Commands, paths, prompts, tool arguments and results, output, headers, credentials, and extension-defined string fields are omitted before the response reaches the renderer.",
			DataType: reflect.TypeOf(EventView{}), Collection: true, Paginated: true, NotFound: true,
			Parameters: append([]openAPIParameter{runID}, paginationParameters()...)},
		{Path: "/api/v1/runs/{run_id}/activity", OperationID: "getRunActivity",
			Summary: "Project public Run activity", Tag: "Runs",
			Description: "Returns bounded public model updates, operator messages, and a Go-owned allowlist of verifiable Harness lifecycle events. It never includes private chain-of-thought, provider thinking blocks, raw prompts, event payloads, tool arguments, tool output, commands, or hidden error text.",
			DataType:    reflect.TypeOf(RunActivityView{}), NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "limit", In: "query", Description: "Maximum recent durable source events considered by the projection",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": runactivity.MaxSourceEvents,
						"default": runactivity.MaxSourceEvents}}}},
		{Path: "/api/v1/runs/{run_id}/web-evidence", OperationID: "getRunWebEvidence",
			Summary: "Inspect Run-local Web evidence", Tag: "Runs",
			Description: "Returns bounded source, snapshot, and citation presentation metadata from the immutable Run-local Web evidence ledger. It never returns page bodies, search snippets, credentials, cookies, tool arguments, or authorization. Every entry remains untrusted and non-instructional; only fetched or partial snapshots are citeable.",
			DataType:    reflect.TypeOf(webevidence.Inventory{}), NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "limit", In: "query", Description: "Maximum entries loaded from each Web evidence ledger",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": webevidence.MaxInventoryItems, "default": 100}}}},
		{Path: ProjectPreparationPathTemplate, OperationID: "inspectProjectPreparation", Summary: "Inspect project dependencies and approved-source requirements", Tag: "Runs",
			Description: "Detects a relative project directory, lock files and fixed install/test commands. Reports verified cached artifacts and the current preparation. Reads grant no network or execution authority.",
			DataType:    reflect.TypeOf(ProjectPreparationView{}), NotFound: true, Parameters: []openAPIParameter{runID, {Name: "target", In: "query", Schema: map[string]any{"type": "string", "default": "."}}}},
		{Path: ProjectPreparationPathTemplate, Method: http.MethodPost, OperationID: "prepareProject", Summary: "Prepare locked project dependencies in the selected sandbox", Tag: "Control", Control: true, NotFound: true,
			Description: "Confirms the inspected lock fingerprint, exact public package origins and optional offline installation scripts. Downloads verified artifacts and runs the fixed install through the existing sandbox permission and lease checks. The idempotency key returns the original operation.",
			DataType:    reflect.TypeOf(ProjectPreparationView{}), RequestType: reflect.TypeOf(ProjectPreparationRequestView{}), SuccessStatus: http.StatusAccepted, Parameters: []openAPIParameter{runID, dockerSandboxIdempotencyKey}},
		{Path: ProjectPreparationOperationPathTemplate, OperationID: "getProjectPreparation", Summary: "Read a saved project preparation", Tag: "Runs", NotFound: true,
			DataType: reflect.TypeOf(ProjectPreparationOperationView{}), Parameters: []openAPIParameter{runID, pathIdentityParameter("operation_id", "Saved preparation operation")}},
		{Path: ProjectPreparationCancelPathTemplate, Method: http.MethodPost, OperationID: "cancelProjectPreparation", Summary: "Cancel an owned project preparation", Tag: "Control", Control: true, NotFound: true,
			Description: "Durably requests cancellation for the exact operation. Repeated cancellation reads its current state; no new work is started.",
			DataType:    reflect.TypeOf(ProjectPreparationOperationView{}), RequestType: reflect.TypeOf(ProjectPreparationCancelRequestView{}), SuccessStatus: http.StatusOK, Parameters: []openAPIParameter{runID, pathIdentityParameter("operation_id", "Saved preparation operation"), dockerSandboxIdempotencyKey}},
		{Path: ProjectActionsPathTemplate, OperationID: "listProjectActions", Summary: "Inspect configured project actions", Tag: "Runs",
			Description: "Reads pinned test/format action IDs and current runtime availability without creating execution authority or exposing host executable paths.",
			DataType:    reflect.TypeOf(ProjectActionsView{}), NotFound: true, Parameters: []openAPIParameter{runID, {Name: "target", In: "query", Description: "Relative directory inspected for project tests", Schema: map[string]any{"type": "string", "default": "."}}}},
		{Path: ProjectActionRunPathTemplate, Method: http.MethodPost, OperationID: "runProjectAction", Summary: "Run one configured project action", Tag: "Control", Control: true, NotFound: true,
			Description: "Starts only the pinned test or format preset through the selected Command Runtime adapter and its existing permission and lease checks. Returns the saved Job promptly; retrying the same action key reads the original Job and never starts it again.",
			DataType:    reflect.TypeOf(ProjectActionResultView{}), RequestType: reflect.TypeOf(ProjectActionRequestView{}), SuccessStatus: http.StatusAccepted,
			Parameters: []openAPIParameter{runID, {Name: "slot", In: "path", Required: true, Schema: map[string]any{"type": "string", "enum": []string{"test", "format"}}}, dockerSandboxIdempotencyKey}},
		{Path: ProjectActionJobPathTemplate, OperationID: "getProjectActionJob", Summary: "Read a saved project action Job", Tag: "Runs",
			Description: "Reads bounded real output and status from an existing Run-owned project-action Job. It requires no new execution authority.",
			DataType:    reflect.TypeOf(ProjectActionResultView{}), NotFound: true, Parameters: []openAPIParameter{runID, pathIdentityParameter("job_id", "Saved project action Job")}},
		{Path: "/api/v1/runs/{run_id}/agent-graph", OperationID: "getRunAgentGraph",
			Summary: "Inspect the bounded Agent graph", Tag: "Agents",
			Description: "Returns root and Specialist projections plus completion summaries without lease or fencing state.",
			DataType:    reflect.TypeOf(AgentGraphView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: "/api/v1/runs/{run_id}/external-skills", OperationID: "getRunExternalSkills",
			Summary: "Inspect bounded external Skill provenance", Tag: "Runs",
			Description: "Returns Run-pinned external Skill names, versions, trust and bounded delivery counts without package content, paths, digests, installation identities, requester identities, or operation identities.",
			DataType:    reflect.TypeOf(ExternalSkillProjectionView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: "/api/v1/runs/{run_id}/delegations", OperationID: "listRunDelegations",
			Summary: "List operator-gated delegations", Tag: "Agents",
			Description: "Returns proposal, review, application, and latest scheduling summaries without operation digests or review reasons.",
			DataType:    reflect.TypeOf(DelegationView{}), Collection: true, Paginated: true,
			NotFound: true, Parameters: append([]openAPIParameter{runID}, paginationParameters()...)},
		{Path: "/api/v1/runs/{run_id}/fanout-plans", OperationID: "listRunFanoutPlans",
			Summary: "List read-only Fan-out plans", Tag: "Analysis",
			Description: "Returns bounded plan metadata and the latest shard execution summary without file manifests or model report JSON.",
			DataType:    reflect.TypeOf(FanoutPlanView{}), Collection: true, Paginated: true,
			NotFound: true, Parameters: append([]openAPIParameter{runID}, paginationParameters()...)},
		{Path: ChildTaskProposalsPathTemplate, OperationID: "listRunChildTaskProposals",
			Summary: "List model-proposed child task proposals", Tag: "Analysis",
			Description: "Returns bounded child task proposals with their redacted tasks, resolved surface, and admission assignments for operator review.",
			DataType:    reflect.TypeOf(ChildTaskProposalsListView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: ChildTaskProposalReviewPathTemplate, Method: http.MethodPost,
			OperationID: "reviewRunChildTaskProposal",
			Summary:     "Approve or deny one child task proposal", Tag: "Control",
			Description: "Records one operator decision; approve may pin the read-only fan-out tier to an explicit 1/2/4/6 ceiling. The model can never raise it.",
			DataType:    reflect.TypeOf(ChildTaskProposalView{}),
			RequestType: reflect.TypeOf(ChildTaskReviewRequestView{}),
			Control:     true, NotFound: true,
			SuccessStatus: http.StatusOK, Parameters: []openAPIParameter{runID, childTaskProposalID}},
		{Path: ChildTaskProposalAdmitPathTemplate, Method: http.MethodPost,
			OperationID: "admitRunChildTaskProposal",
			Summary:     "Admit one approved child task proposal", Tag: "Control",
			Description: "Admits an operator-approved proposal: core tasks go through the fenced Specialist admission and bind declared dependencies onto the schema v101 wait ledger; read-only tasks record their binding intent.",
			DataType:    reflect.TypeOf(ChildTaskProposalView{}),
			RequestType: reflect.TypeOf(ChildTaskAdmitRequestView{}),
			Control:     true, NotFound: true,
			SuccessStatus: http.StatusOK, Parameters: []openAPIParameter{runID, childTaskProposalID}},
		{Path: SpecialistExecutionPathTemplate, Method: http.MethodPost, OperationID: "executeRunSpecialist", Summary: "Execute one already admitted independent child", Tag: "Control", Description: "Runs one bounded Specialist turn under its own identity, Session, usage and Run lease. Optional workspace_tools enables Go-scoped project reads/search; operator-confirmed Batch work uses its separate isolated authority. A stable operation key replays the recorded attempt without dispatching another model call.", DataType: reflect.TypeOf(SpecialistExecutionView{}), RequestType: reflect.TypeOf(SpecialistExecutionRequestView{}), Control: true, NotFound: true, SuccessStatus: http.StatusOK, Parameters: []openAPIParameter{runID, agentID}},
		{Path: SpecialistContinuationPathTemplate, Method: http.MethodPost, OperationID: "continueRunSpecialist", Summary: "Run an admitted child continuously and summarize its completion", Tag: "Control", Description: "Operator-confirmed continuation uses the existing Specialist scheduler for at most 32 rounds, with the child's original turn, token, monetary and Run execution budgets and Go-owned read/search tools. An actual child completion queues an attributed completion notification and wakes one bounded parent turn. Waiting, exhausted and cancelled children retain their precise stop reason. Stable operation identity replays both the schedule and completion wake.", DataType: reflect.TypeOf(SpecialistContinuationView{}), RequestType: reflect.TypeOf(SpecialistContinuationRequestView{}), Control: true, NotFound: true, SuccessStatus: http.StatusOK, Parameters: []openAPIParameter{runID, agentID}},
		{Path: SpecialistToolReceiptsPathTemplate, OperationID: "getRunSpecialistToolReceipts", Summary: "Read durable child tool receipts", Tag: "Agents", Description: "Returns the child attempt's bounded native tool calls and results. Private provider replay is never returned.", DataType: reflect.TypeOf(SpecialistToolReceiptsView{}), NotFound: true, Parameters: []openAPIParameter{runID, agentID, pathIdentityParameter("attempt_id", "Specialist attempt identity")}},
		{Path: BatchDeliveriesPathTemplate, OperationID: "listRunBatchDeliveries",
			Summary: "List durable batch deliveries", Tag: "Agents",
			Description: "Lists bounded batch-delivery.v1 plans without local worktree roots, owner-token digests, operation digests, or request fingerprints.",
			DataType:    reflect.TypeOf(BatchDeliveriesListView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, {Name: "limit", In: "query",
				Description: "Maximum batch delivery plans", Schema: map[string]any{
					"type": "integer", "minimum": 1, "maximum": 64, "default": 50}}}},
		{Path: BatchDeliveriesPathTemplate, Method: http.MethodPost,
			OperationID: "prepareRunBatchDelivery", Summary: "Prepare isolated delivery worktrees",
			Tag: "Control", Description: "Materializes the confirmed batch-delivery.v1 DAG into independent branch-backed worktrees and returns raw owner tokens exactly once. Replays never recover discarded tokens; rotate the affected owner generation instead.",
			DataType:    reflect.TypeOf(BatchDeliveryPrepareView{}),
			RequestType: reflect.TypeOf(BatchDeliveryPrepareRequestView{}), Control: true,
			NotFound: true, SuccessStatus: http.StatusCreated,
			Parameters: []openAPIParameter{runID, batchDeliveryIdempotencyKey}},
		{Path: BatchDeliveryPathTemplate, OperationID: "getRunBatchDelivery",
			Summary: "Inspect one batch delivery", Tag: "Agents",
			Description: "Returns the plan, closed child tool profiles, mailbox, receipts, independent reviews, and ordered merge state while omitting filesystem roots and private operation identities.",
			DataType:    reflect.TypeOf(BatchDeliverySnapshotView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, batchDeliveryID}},
		{Path: BatchWorkbenchPreparePathTemplate, Method: http.MethodPost,
			OperationID: "prepareBatchWorkbench", Summary: "Prepare admitted tasks for isolated delivery",
			Tag: "Control", Description: "Copies admitted budgets, dependencies and artifacts in Go, materializes independent worktrees, and retains narrowed owner tokens inside the process. Preparation does not start model work.",
			DataType: reflect.TypeOf(BatchWorkbenchView{}), RequestType: reflect.TypeOf(BatchWorkbenchPrepareRequestView{}),
			Control: true, NotFound: true, SuccessStatus: http.StatusCreated,
			Parameters: []openAPIParameter{runID, batchWorkbenchIdempotencyKey}},
		{Path: BatchWorkbenchPathTemplate, OperationID: "getBatchWorkbench",
			Summary: "Inspect child execution readiness and delivery state", Tag: "Agents",
			Description: "Reads exact delivery state and process-local worker/owner availability without returning secrets or minting authority.",
			DataType:    reflect.TypeOf(BatchWorkbenchView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, batchDeliveryID}},
		{Path: BatchWorkbenchOwnerPathTemplate, Method: http.MethodPost,
			OperationID: "recoverBatchWorkbenchOwner", Summary: "Recover or retry an observed child generation",
			Tag: "Control", Description: "Explicitly rotates the observed generation with CAS, fences previous owners and records recovery. Replays never rotate a newer generation or restart model work.",
			DataType: reflect.TypeOf(BatchWorkbenchView{}), RequestType: reflect.TypeOf(BatchWorkbenchOwnerRequestView{}),
			Control: true, NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID, batchDeliveryID, batchDeliveryOrdinal, batchWorkbenchIdempotencyKey}},
		{Path: BatchWorkbenchExecutePathTemplate, Method: http.MethodPost,
			OperationID: "executeBatchWorkbenchChild", Summary: "Explicitly execute one isolated child task",
			Tag: "Control", Description: "Invokes an installed accounted Go child runtime through narrowed Batch tools, then measures and submits its committed result. A durable intent prevents duplicate model dispatch on replay; uncertain outcomes require inspecting state and explicit recovery.",
			DataType: reflect.TypeOf(BatchWorkbenchView{}), RequestType: reflect.TypeOf(BatchWorkbenchExecuteRequestView{}),
			Control: true, NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID, batchDeliveryID, batchDeliveryOrdinal, batchWorkbenchIdempotencyKey}},
		{Path: BatchDeliveryReviewPathTemplate, Method: http.MethodPost,
			OperationID: "reviewRunBatchDeliveryChild", Summary: "Review one complete child delivery",
			Tag: "Control", Description: "Re-inspects the exact merge-base diff, call-chain digest, lifecycle state, and required tests before recording an independent accepted or changes-requested verdict.",
			DataType:    reflect.TypeOf(BatchDeliveryReviewControlView{}),
			RequestType: reflect.TypeOf(BatchDeliveryReviewRequestView{}), Control: true,
			NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID, batchDeliveryID, batchDeliveryOrdinal,
				batchDeliveryIdempotencyKey}},
		{Path: BatchDeliveryRenewPathTemplate, Method: http.MethodPost,
			OperationID: "renewRunBatchDeliveryChildOwner", Summary: "Rotate or retry one child owner",
			Tag: "Control", Description: "CAS-rotates the expected generation, fences the previous token, and returns one replacement narrowed owner authority. A retry generation retains committed evidence while refusing stale writers.",
			DataType:    reflect.TypeOf(BatchDeliveryRenewView{}),
			RequestType: reflect.TypeOf(BatchDeliveryRenewRequestView{}), Control: true,
			NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID, batchDeliveryID, batchDeliveryOrdinal}},
		{Path: BatchDeliveryMergePathTemplate, Method: http.MethodPost,
			OperationID: "mergeRunBatchDelivery", Summary: "Run the ordered local merge queue",
			Tag: "Control", Description: "Replays every accepted child onto the latest confirmed base in declared order, blocks overlap or drift, runs focused validation after each step, and never pushes, opens a PR, or selects conflicts automatically.",
			DataType:    reflect.TypeOf(BatchDeliveryMergeControlView{}),
			RequestType: reflect.TypeOf(BatchDeliveryMergeRequestView{}), Control: true,
			NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID, batchDeliveryID,
				batchDeliveryIdempotencyKey}},
		{Path: BatchDeliveryCancelPathTemplate, Method: http.MethodPost,
			OperationID: "cancelRunBatchDelivery", Summary: "Cancel one batch delivery",
			Tag: "Control", Description: "Fences active generations and cleans only exact clean worktrees; dirty, committed, or identity-drifted evidence is preserved and reported for operator recovery.",
			DataType:    reflect.TypeOf(BatchDeliveryCancelView{}),
			RequestType: reflect.TypeOf(BatchDeliveryCancelRequestView{}), Control: true,
			NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID, batchDeliveryID,
				batchDeliveryIdempotencyKey}},
		{Path: BatchDeliveryRecoverPathTemplate, Method: http.MethodPost,
			OperationID: "reconcileRunBatchDelivery", Summary: "Reconcile one interrupted batch delivery",
			Tag: "Control", Description: "Converges durable worktree and merge-queue intent after restart without minting tokens, trusting completion text, or deleting uncertain state.",
			DataType:    reflect.TypeOf(BatchDeliveryReconcileView{}),
			RequestType: reflect.TypeOf(BatchDeliveryReconcileRequestView{}), Control: true,
			NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID, batchDeliveryID}},
		{Path: FanoutExecutionsPathTemplate, OperationID: "listRunFanoutExecutions",
			Summary: "List one Fan-out plan's execution history", Tag: "Analysis",
			Description: "Returns the bounded shard execution history of one read-only Fan-out plan; shard rows carry only status, bounded counts, and model identity.",
			DataType:    reflect.TypeOf(FanoutExecutionsListView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, identityQueryParameter("plan_id", "Exact Fan-out plan identity")}},
		{Path: "/api/v1/runs/{run_id}/reports", OperationID: "listRunFindingReports",
			Summary: "List Finding Report summaries", Tag: "Reports",
			Description: "Returns bounded report counts and severity summaries without Finding narratives.",
			DataType:    reflect.TypeOf(FindingReportSummaryView{}), Collection: true, Paginated: true,
			NotFound: true, Parameters: append([]openAPIParameter{runID}, paginationParameters()...)},
		{Path: "/api/v1/runs/{run_id}/reports/{report_id}", OperationID: "getRunFindingReport",
			Summary: "Inspect one Finding Report", Tag: "Reports",
			Description: "Returns Findings and evidence references while omitting operator reasons, Evidence notes, digests, and Artifact content.",
			DataType:    reflect.TypeOf(FindingReportView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, reportID}},
		{Path: RunEventStreamPathTemplate, OperationID: "streamRunEvents", Summary: "Stream Run events",
			Tag: "Runs", Description: "Streams bounded durable Run events as SSE and resumes from a Run-bound cursor.",
			DataType: reflect.TypeOf(RunEventStreamView{}), Streaming: true, NotFound: true,
			Parameters: []openAPIParameter{
				runID,
				{Name: "cursor", In: "query", Description: "Opaque Run-bound cursor from a previous SSE id; cannot be combined with Last-Event-ID",
					Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": MaxEventStreamCursorBytes}},
				{Name: "Last-Event-ID", In: "header", Description: "SSE reconnect cursor from the final received frame; cannot be combined with cursor",
					Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": MaxEventStreamCursorBytes}},
			}},
		{Path: RunEventPollPathTemplate, OperationID: "pollRunEvents", Summary: "Poll Run events",
			Tag: "Runs", Description: "Returns one bounded append-only event batch using the same Run-bound high-water cursor as the SSE stream. Intended for embedded renderers without response streaming.",
			DataType: reflect.TypeOf(RunEventPollView{}), NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "cursor", In: "query", Description: "Opaque final cursor from a previous poll or SSE frame",
					Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": MaxEventStreamCursorBytes}},
				{Name: "limit", In: "query", Description: "Maximum events returned in this bounded poll",
					Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": MaxEventPollBatchSize,
						"default": DefaultEventPollBatchSize}},
			}},
		{Path: PublicModelStreamPathTemplate, OperationID: "getPublicModelStream",
			Summary: "Inspect the active safe model preview", Tag: "Runs",
			Description: "Returns one process-local, provisional snapshot extracted only from the root lifecycle message field after streaming redaction and Policy checks. The snapshot never persists raw provider output, prompts, tool arguments, hidden reasoning, or partial sensitive tails, and disappears when the active call ends.",
			DataType:    reflect.TypeOf(application.PublicModelStreamSnapshot{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: PublicModelStreamPollPathTemplate, OperationID: "pollPublicModelStream",
			Summary: "Poll the active safe model preview", Tag: "Runs",
			Description: "Returns an explicit inactive projection between model calls so embedded renderers can poll without treating the expected idle state as an HTTP error. An active snapshot has the same redacted, provisional boundary as the exact active-call endpoint.",
			DataType:    reflect.TypeOf(PublicModelStreamPollView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: ModelCancellationPathTemplate, Method: http.MethodPost,
			OperationID: "requestModelCancellation", Summary: "Cancel an active model call", Tag: "Control",
			Description: "Persists an audit-first cancellation request bound to the exact active Supervisor and model attempt. The worker consumes it with its private execution lease; clients never provide a fencing token.",
			DataType:    reflect.TypeOf(ModelCancellationView{}), RequestType: reflect.TypeOf(ModelCancellationRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string", "minLength": domain.MinModelCancellationKeyBytes,
						"maxLength": domain.MaxModelCancellationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: FanoutExecutionCancelPathTemplate, Method: http.MethodPost,
			OperationID: "cancelRunFanoutExecution",
			Summary:     "Cancel one read-only Fan-out execution", Tag: "Control",
			Description: "Cancels one non-terminal read-only Fan-out execution under a fresh Run execution lease fence; terminal executions return their stored state unchanged.",
			DataType:    reflect.TypeOf(FanoutExecutionView{}),
			RequestType: reflect.TypeOf(FanoutExecutionCancelRequestView{}),
			Control:     true, NotFound: true,
			SuccessStatus: http.StatusOK, Parameters: []openAPIParameter{runID, executionID}},
		{Path: SpecialistModelCancellationPathTemplate, Method: http.MethodPost,
			OperationID: "requestSpecialistModelCancellation",
			Summary:     "Cancel an active Specialist model call", Tag: "Control",
			Description: "Persists an audit-first cancellation request bound to one exact Specialist AgentAttempt and model attempt. Only the worker holding that Attempt's private Run lease can observe and apply it.",
			DataType:    reflect.TypeOf(SpecialistModelCancellationView{}),
			RequestType: reflect.TypeOf(ModelCancellationRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID, agentID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string", "minLength": domain.MinModelCancellationKeyBytes,
						"maxLength": domain.MaxModelCancellationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: RunExecutionProfileControlPathTemplate, Method: http.MethodPost,
			OperationID: "selectRunExecutionProfile",
			Summary:     "Select a Run execution profile", Tag: "Control",
			Description: "Records operator intent for Preview, Docker, or local workspace execution. Selection never starts a process and never grants execution authority; backend-specific production gates remain mandatory.",
			DataType:    reflect.TypeOf(RunExecutionProfileControlView{}),
			RequestType: reflect.TypeOf(RunExecutionProfileControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: RunWebAccessControlPathTemplate, Method: http.MethodPost,
			OperationID: "changeRunWebAccess", Summary: "Select a quiescent Run web access mode", Tag: "Control",
			Description: "Selects disabled, exact-host rules, or full anonymous public HTTPS access independently of command, filesystem and sandbox permissions. Requires created or paused status, no active lease, exact current mode revision and Idempotency-Key. Appends an audited revision and fences stale tool calls. Saved snapshots remain readable when disabled. Rules control direct fetch hosts; configured search provider policy remains independent.",
			DataType:    reflect.TypeOf(RunWebAccessControlView{}), RequestType: reflect.TypeOf(RunWebAccessControlRequestView{}), Control: true, NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque revision-bound operation key", Required: true, Schema: map[string]any{"type": "string", "minLength": domain.MinAgentOperationKeyBytes, "maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: RunNetworkAuthorityControlPathTemplate, Method: http.MethodPost,
			OperationID: "expandRunNetworkAuthority",
			Summary:     "Add exact HTTPS hosts to a quiescent Run", Tag: "Control",
			Description: "Appends a new Run mode revision that adds only the explicitly listed canonical public HTTPS hostnames. The transition requires an exact expected mode revision, a created or paused Run, no active execution lease, and an idempotency key. It never grants public_https, wildcard, implicit search-provider, process, credential, or filesystem authority; all prior tool fences become stale.",
			DataType:    reflect.TypeOf(RunNetworkAuthorityControlView{}),
			RequestType: reflect.TypeOf(RunNetworkAuthorityControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque revision-bound operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: RunExecutionPermissionControlPathTemplate, Method: http.MethodPost,
			OperationID: "selectRunExecutionPermission",
			Summary:     "Select a Run execution permission mode", Tag: "Control",
			Description: "Selects Ask, Auto or Full operation approval. Full requires explicit confirmation and live activation in the current process. Persisted snapshots and historical approvals never restore execution authority; each operation rechecks its exact inputs, policy, runtime epoch and Run fence.",
			DataType:    reflect.TypeOf(RunExecutionPermissionControlView{}),
			RequestType: reflect.TypeOf(RunExecutionPermissionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: RunBrowserCDPPermissionControlPathTemplate, Method: http.MethodPost,
			OperationID: "selectRunBrowserCDPPermission",
			Summary:     "Select a Run browser CDP permission mode", Tag: "Control",
			Description: "Records either restricted exact-scope navigation, DOM, and screenshot intent or the highly sensitive Full CDP sub-permission. Selection never starts a browser, opens a CDP transport, authorizes a target, or grants runtime capability. Full CDP is available only under modern Full with live process activation; it defaults on when entering Full, can be disabled independently, and is forced off outside Full.",
			DataType:    reflect.TypeOf(RunBrowserCDPPermissionControlView{}),
			RequestType: reflect.TypeOf(RunBrowserCDPPermissionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: AgentBrowserStatusPathTemplate, Method: http.MethodGet,
			OperationID: "getRunAgentBrowser", Summary: "Read the Agent browser state", Tag: "Runs",
			Description: "Reads the Run's process-local Agent browser status without starting a browser or executing an action. Excludes runtime authority and profile/DevTools handles.",
			DataType:    reflect.TypeOf(AgentBrowserStatusView{}), NotFound: true, Parameters: []openAPIParameter{runID}},
		{Path: AgentBrowserClosePathTemplate, Method: http.MethodPost,
			OperationID: "closeRunAgentBrowser", Summary: "Stop one exact Agent browser session", Tag: "Control", Control: true,
			Description: "Permanently invalidates and cleans the exact session. Repeating a close cannot target another session or start a browser. Cleanup does not require a still-live execution grant.",
			DataType:    reflect.TypeOf(AgentBrowserStatusView{}), RequestType: reflect.TypeOf(AgentBrowserCloseRequestView{}), NotFound: true,
			SuccessStatus: http.StatusOK, Parameters: []openAPIParameter{runID}},
		{Path: AgentBrowserScreenshotPathTemplate, Method: http.MethodGet,
			OperationID: "readRunAgentBrowserScreenshot", Summary: "Read a saved Agent browser screenshot", Tag: "Runs", RawArtifact: true, NotFound: true,
			Description: "Reads exact completed screenshot evidence for this Run, manager session and artifact. Requires the expected PNG hash and never captures again.",
			Parameters: []openAPIParameter{runID,
				{Name: "session_id", In: "query", Required: true, Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
				{Name: "artifact_locator", In: "query", Required: true, Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 1024}},
				{Name: "sha256", In: "query", Required: true, Schema: map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "minLength": 64, "maxLength": 64}}}},
		{Path: FullCDPSessionControlPathTemplate, Method: http.MethodGet,
			OperationID: "getRunFullCDPSession", Summary: "Get the current Full CDP session state",
			Tag:         "Runs",
			Description: "Returns only the redacted process-local lifecycle state for the Run's current or most recent Full CDP session. It never exposes a PID, executable or Profile path, DevTools endpoint, WebSocket URL, permission snapshot identity, runtime fence, token, or authorization fingerprint.",
			DataType:    reflect.TypeOf(FullCDPSessionControlView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: FullCDPSessionControlPathTemplate, Method: http.MethodPost,
			OperationID: "openRunFullCDPSession", Summary: "Open a confirmed Full CDP session",
			Tag:         "Control",
			Description: "Starts one backend-discovered, Job-owned browser with an exact disposable Profile and opens a TTL-bounded Full CDP transport only for one literal loopback origin. Requires modern Full with live process activation, the independently enabled Full CDP sub-permission, exact permission revision CAS, and per-call confirmation. The request cannot supply process, executable, Profile, DevTools, argv, environment, or WebSocket data.",
			DataType:    reflect.TypeOf(FullCDPSessionControlView{}),
			RequestType: reflect.TypeOf(FullCDPSessionOpenRequestView{}),
			Control:     true, NotFound: true, SuccessStatus: http.StatusCreated,
			Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header",
					Description: "Opaque process-local Full CDP open operation key",
					Required:    true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes,
						"pattern":   `^\S+$`}},
			}},
		{Path: FullCDPPreviewPathTemplate, Method: http.MethodPost,
			OperationID: "captureRunFullCDPPreview", Summary: "Refresh the current browser preview",
			SuccessStatus: http.StatusOK,
			Tag:           "Control", Control: true, NotFound: true,
			Description: "Observes the exact operator-opened session through the existing live CDP permission and runtime fence. Keeps only its latest bounded PNG in memory; this is not a durable test receipt. Page text and screenshot are sequential observations of the same URL.",
			DataType:    reflect.TypeOf(application.FullCDPPreviewView{}), RequestType: reflect.TypeOf(FullCDPPreviewRequestView{}),
			Parameters: []openAPIParameter{runID}},
		{Path: FullCDPPreviewActionPathTemplate, Method: http.MethodPost,
			OperationID: "actRunFullCDPPreview", Summary: "Interact with an observed preview element",
			SuccessStatus: http.StatusOK,
			Tag:           "Control", Control: true, NotFound: true,
			Description: "Consumes one exact latest preview snapshot and invokes the existing browser click/type action for an observed enabled selector. Requires live session authority and runtime selector provenance. No JavaScript or coordinate action. On unknown response, refresh the observation instead of retrying the action.",
			DataType:    reflect.TypeOf(application.FullCDPPreviewView{}), RequestType: reflect.TypeOf(FullCDPPreviewActionRequestView{}),
			Parameters: []openAPIParameter{runID}},
		{Path: FullCDPPreviewImagePathTemplate, Method: http.MethodGet,
			OperationID: "readRunFullCDPPreviewImage", Summary: "Read the exact current preview PNG",
			Tag: "Runs", RawArtifact: true, NotFound: true,
			Description: "Returns authenticated image/png with exact SHA256 ETag and X-Cyberagent-Content-SHA256. Never captures again. Revalidates the current session and live permission; closed, revoked, or replaced references fail instead of returning another image.",
			Parameters: []openAPIParameter{runID,
				{Name: "session_id", In: "query", Required: true, Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
				{Name: "sha256", In: "query", Required: true, Schema: map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "minLength": 64, "maxLength": 64}}}},
		{Path: FullCDPSessionCloseControlPathTemplate, Method: http.MethodPost,
			OperationID: "closeRunFullCDPSession", Summary: "Close and clean one exact Full CDP session",
			Tag:         "Control",
			Description: "Closes the CDP transport, terminates and reaps the complete browser process tree, releases and deletes only the exact owned disposable Profile, then records a redacted terminal audit event. Closing is a cleanup operation and does not require a high-risk confirmation or still-live Full CDP permission.",
			DataType:    reflect.TypeOf(FullCDPSessionControlView{}),
			RequestType: reflect.TypeOf(FullCDPSessionCloseRequestView{}),
			Control:     true, NotFound: true, SuccessStatus: http.StatusOK,
			Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header",
					Description: "Opaque process-local Full CDP close operation key",
					Required:    true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes,
						"pattern":   `^\S+$`}},
			}},
		{Path: RunExecutionInteractionControlPathTemplate, Method: http.MethodPost,
			OperationID: "selectRunExecutionInteraction",
			Summary:     "Select a Run execution interaction boundary", Tag: "Control",
			Description: "Records operator-selected Preview, controlled Code, Debug, or Cyber interaction intent. It never starts a process, grants terminal input, enables networking, or bypasses the execution profile and process-local gates.",
			DataType:    reflect.TypeOf(RunExecutionInteractionControlView{}),
			RequestType: reflect.TypeOf(RunExecutionInteractionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque operation key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: SessionSteeringCancellationPathTemplate, Method: http.MethodPost,
			OperationID: "cancelSessionSteering", Summary: "Cancel queued Session steering",
			Tag:         "Control",
			Description: "Cancels one exact pending operator-steering message for the bound Session. Prepared, committed, and already consumed input is immutable; this operation does not stop a model call or start execution.",
			DataType:    reflect.TypeOf(SessionSteeringCancellationView{}),
			RequestType: reflect.TypeOf(SessionSteeringCancellationRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				sessionID, messageID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: SessionSteeringRevisionPathTemplate, Method: http.MethodPost,
			OperationID: "reviseSessionSteering", Summary: "Revise one queued message without changing its order", Tag: "Control",
			Description: "CAS replaces the body of the exact pending, unprepared message. Original submission identity and attachment references remain immutable. The sealed receipt supports safe explicit retries; no execution is started.",
			DataType:    reflect.TypeOf(SessionSteeringRevisionView{}), RequestType: reflect.TypeOf(SessionSteeringRevisionRequestView{}),
			Control: true, NotFound: true, SuccessStatus: http.StatusAccepted,
			Parameters: []openAPIParameter{sessionID, messageID,
				{Name: "Idempotency-Key", In: "header", Required: true, Schema: map[string]any{"type": "string",
					"minLength": domain.MinAgentOperationKeyBytes, "maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: SessionSteeringPromotionPathTemplate, Method: http.MethodPost,
			OperationID: "promoteSessionSteering", Summary: "Guide the current task with one queued text message", Tag: "Control",
			Description: "Atomically cancels one exact unprepared next-turn revision, enqueues its text as steering for the captured live execution and attempt, and seals a receipt linking both messages. Attachments are rejected without changing the queue. Replays the original receipt after execution ends; never starts another execution or grants capabilities.",
			DataType:    reflect.TypeOf(SessionSteeringPromotionView{}), RequestType: reflect.TypeOf(SessionSteeringPromotionRequestView{}),
			Control: true, NotFound: true, SuccessStatus: http.StatusAccepted,
			Parameters: []openAPIParameter{sessionID, messageID,
				{Name: "Idempotency-Key", In: "header", Required: true, Schema: map[string]any{"type": "string",
					"minLength": domain.MinAgentOperationKeyBytes, "maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: RunLifecycleControlPathTemplate, Method: http.MethodPost,
			OperationID: "controlRunLifecycle", Summary: "Start, pause, or resume a Run",
			Tag:         "Control",
			Description: "Applies one exact idempotent Run lifecycle transition. Start atomically crosses created, preparing, and running; pause requires a quiescent Supervisor with no active execution lease; resume requires paused state. This operation never calls a model or tool.",
			DataType:    reflect.TypeOf(RunLifecycleControlView{}),
			RequestType: reflect.TypeOf(RunLifecycleControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: PlanDirectionControlPathTemplate, Method: http.MethodPost,
			OperationID: "selectPlanDirection", Summary: "Select one Plan direction",
			Tag:         "Control",
			Description: "Selects one actual direction from a persisted proposal of one to three directions and atomically records the operator's manual_acceptance choice with its bounded WorkItems and handoff Note. Omitted manual_acceptance retains required checkpoints; on_demand makes manual records optional without changing execution or real verification gates. Exact original-key replay remains available after phase changes. It does not change phase, start execution, call a model, or grant capability.",
			DataType:    reflect.TypeOf(PlanDirectionControlView{}),
			RequestType: reflect.TypeOf(PlanDirectionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: PlanModeControlPathTemplate, Method: http.MethodPost,
			OperationID: "enterPlanMode", Summary: "Enter Plan mode",
			Tag:         "Control",
			Description: "Explicitly transitions a created or paused Run from Deliver to a new Plan revision. It does not resume the Run, start execution, call a model, reuse a prior Plan selection, or grant capability.",
			DataType:    reflect.TypeOf(PlanModeTransitionControlView{}),
			RequestType: reflect.TypeOf(PlanModeTransitionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: PlanDeliveryControlPathTemplate, Method: http.MethodPost,
			OperationID: "enterPlanDelivery", Summary: "Enter Deliver after Plan selection",
			Tag:         "Control",
			Description: "Explicitly transitions a created or paused Run from Plan to Deliver only after an immutable operator selection exists. It does not resume the Run, start execution, call a model, or grant capability.",
			DataType:    reflect.TypeOf(PlanDeliveryTransitionControlView{}),
			RequestType: reflect.TypeOf(PlanDeliveryTransitionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: PlanDeliveryWorkItemStartPathTemplate, Method: http.MethodPost,
			OperationID: "startPlanDeliveryWorkItem", Summary: "Start a selected Plan work item", Tag: "Control",
			Description: "Moves a pending selected WorkItem to in-progress only while its Run is paused in Deliver with completed dependencies and no active execution lease. The shared Run retry key binds the exact item, action and expected version. No execution or capability is started.",
			DataType:    reflect.TypeOf(PlanDeliveryWorkItemControlView{}), RequestType: reflect.TypeOf(PlanDeliveryWorkItemControlRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{runID, workItemID, planDeliveryWorkItemIdempotencyParameter()}, SuccessStatus: http.StatusAccepted},
		{Path: PlanDeliveryWorkItemCheckpointPathTemplate, Method: http.MethodPost,
			OperationID: "recordPlanDeliveryCheckpoint", Summary: "Record a manual Delivery checkpoint", Tag: "Control",
			Description: "Records bounded operator evidence for the exact selected in-progress WorkItem/version in a paused Deliver Run. Final modules require functional verification and robustness audit; non-final modules cannot submit those fields. Evidence remains operator attestation, not verified test success. Exact retries return the immutable checkpoint and current WorkItem without requiring the Run to remain writable.",
			DataType:    reflect.TypeOf(PlanDeliveryCheckpointControlView{}), RequestType: reflect.TypeOf(PlanDeliveryCheckpointControlRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{runID, workItemID, planDeliveryWorkItemIdempotencyParameter()}, SuccessStatus: http.StatusAccepted},
		{Path: PlanDeliveryWorkItemCompletePathTemplate, Method: http.MethodPost,
			OperationID: "completePlanDeliveryWorkItem", Summary: "Complete a checked Plan work item", Tag: "Control",
			Description: "Completes an in-progress selected WorkItem only when an exact current Delivery checkpoint satisfies the existing completion gate in a paused Deliver Run. Does not complete the Run, mark a Standard Code report passed, run a model, or grant capability. Exact retries identify the original applied transition separately from current WorkItem state.",
			DataType:    reflect.TypeOf(PlanDeliveryWorkItemControlView{}), RequestType: reflect.TypeOf(PlanDeliveryWorkItemControlRequestView{}),
			Control: true, NotFound: true, Parameters: []openAPIParameter{runID, workItemID, planDeliveryWorkItemIdempotencyParameter()}, SuccessStatus: http.StatusAccepted},
		{Path: "/api/v1/runs/{run_id}/approvals/{approval_id}/preview",
			OperationID: "getApprovalPreview", Summary: "Inspect the exact approval-bound proposal",
			Tag: "Control", NotFound: true, DataType: reflect.TypeOf(ApprovalPreviewView{}),
			Description: "Returns a bounded, redacted read-only projection of the exact proposal bound to this Run and approval. Dry-run, Git authorization, file review and public HTTPS effects are distinguished; no decision or execution is performed. Stale bindings are refused and truncated previews cannot be approved in the UI.",
			Parameters:  []openAPIParameter{runID, approvalID}},
		{Path: "/api/v1/runs/{run_id}/approvals", OperationID: "listRunApprovals",
			Summary: "List pending Run approvals", Tag: "Control",
			Description: "Returns at most one hundred pending approval metadata records and their bounded operator actions. Commands, file content, fingerprints, decision reasons, paths, capability grants, and execution authority are omitted.",
			DataType:    reflect.TypeOf(ApprovalQueueView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: ControlledCommandProposalCollectionPathTemplate,
			OperationID: "listControlledCommandProposals",
			Summary:     "List fixed command proposals", Tag: "Control",
			Description: "Returns at most one hundred metadata-only Agent proposals for Go-owned fixed command templates. No executable, argv, shell text, environment, raw output, or execution authority is exposed.",
			DataType:    reflect.TypeOf(ControlledCommandProposalView{}),
			Collection:  true, NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "limit", In: "query",
					Description: "Maximum number of newest proposals",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": MaxPageLimit, "default": DefaultPageLimit}}}},
		{Path: ControlledCommandProposalDetailPathTemplate,
			OperationID: "getControlledCommandProposal",
			Summary:     "Inspect one fixed command proposal", Tag: "Control",
			Description: "Returns the exact Run-bound proposal, operator review, result, and metadata-only receipt. Raw stdout and stderr are never persisted or returned by this read endpoint.",
			DataType:    reflect.TypeOf(ControlledCommandProposalView{}),
			NotFound:    true, Parameters: []openAPIParameter{runID, proposalID}},
		{Path: HostCommandProposalCollectionPathTemplate,
			OperationID: "listHostCommandProposals",
			Summary:     "List historical host command proposals", Tag: "Control",
			Description: "Reads saved Host and Risk proposal evidence. Creation, review and execution are retired; these records cannot authorize a new command. Executable identity, every argv item, working directory, environment names and digest, network targets and purpose, credential kinds without values, host paths, policy refusal, immutable Run/Supervisor/snapshot bindings, resource limits, and the non-sandboxed boundary are explicit; environment values, credential values, capability bearers, and raw output are omitted.",
			DataType:    reflect.TypeOf(HostCommandProposalView{}),
			Collection:  true, NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "limit", In: "query",
					Description: "Maximum number of newest proposals",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": MaxPageLimit, "default": DefaultPageLimit}}}},
		{Path: HostCommandProposalDetailPathTemplate,
			OperationID: "getHostCommandProposal",
			Summary:     "Inspect one historical host command proposal", Tag: "Control",
			Description: "Reads the saved immutable command envelope, review, historical grant and consumption metadata, invalidation state, result and receipt. Only a saved outcome may continue its exact original call; pending or approved records do not regain execution authority. An execution intent without a durable result remains unknown and is never resent.",
			DataType:    reflect.TypeOf(HostCommandProposalView{}),
			NotFound:    true, Parameters: []openAPIParameter{runID, proposalID}},
		{Path: HostCommandProposalResumePathTemplate, Method: http.MethodPost,
			OperationID: "resumeHostCommandProposal", Summary: "Resume a saved historical command outcome", Tag: "Control",
			Description: "Resumes only the exact durable Supervisor call or approval continuation from saved history. Requires control authorization and an available Run controller. Does not accept command, review or grant parameters. A historical intent without a result remains unknown and is never re-executed.",
			DataType:    reflect.TypeOf(HostCommandProposalView{}), Control: true, EmptyBody: true, NotFound: true,
			Parameters: []openAPIParameter{runID, proposalID}},
		{Path: FileEditQueuePathTemplate, OperationID: "listRunFileEdits",
			Summary: "List Run file edit previews", Tag: "Runs",
			Description: "Returns at most one hundred Run-bound metadata-only file edit previews. Original and proposed file bodies are omitted and apply authority is always false.",
			DataType:    reflect.TypeOf(FileEditQueueView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: FileEditChangeSetPathTemplate, OperationID: "getRunFileEditChangeSet",
			Summary: "Inspect a bounded multi-file change set", Tag: "Runs",
			Description: "Returns status and size metadata for at most one hundred Run-bound FileEdits. Every review and apply remains independently authorized through the existing per-file endpoints; no batch mutation or atomic apply authority is introduced, and Diff content is omitted.",
			DataType:    reflect.TypeOf(FileEditChangeSetView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: FileEditProposalSourcePathTemplate,
			OperationID: "issueFileEditProposalSource", Summary: "Issue an exact interactive edit source",
			Tag:         "Runs",
			Description: "Returns a complete unredacted bounded UTF-8 file body plus a short-lived opaque handle bound to the Run, Session, Workspace, path, and current hash. Redacted or truncated files are refused; issuing the handle writes nothing.",
			DataType:    reflect.TypeOf(FileEditProposalSourceView{}), NotFound: true,
			Parameters: []openAPIParameter{runID,
				{Name: "path", In: "query", Description: "Canonical Go-projected Workspace-relative file path",
					Required: true, Schema: map[string]any{"type": "string", "minLength": 1,
						"maxLength": workspace.MaxExplorerPathRunes}},
				{Name: "expected_sha256", In: "query", Description: "Optional previously issued SHA-256 required when rotating an expired source handle",
					Schema: map[string]any{"type": "string", "pattern": `^[0-9a-f]{64}$`}}}},
		{Path: FileEditProposalRecoveryPathTemplate,
			OperationID: "recoverFileEditProposal", Summary: "Recover one durable pending FileEdit proposal",
			Tag:         "Runs",
			Description: "Returns integrity-checked original and proposed bodies for one exact pending Run-bound proposal, plus a stale-file flag. It issues no source handle and cannot mutate, approve, or apply the proposal.",
			DataType:    reflect.TypeOf(FileEditProposalRecoveryView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, editID}},
		{Path: FileEditProposalPathTemplate, Method: http.MethodPost,
			OperationID: "createFileEditProposal", Summary: "Create a pending FileEdit from a Go-issued source",
			Tag:         "Control",
			Description: "Consumes only an opaque Go-issued source handle and bounded text. It rechecks the exact file hash and Policy, creates a pending approval-backed FileEdit, and cannot approve or write the file.",
			DataType:    reflect.TypeOf(FileEditProposalView{}),
			RequestType: reflect.TypeOf(FileEditProposalRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}},
		{Path: FileEditRevertProposalPathTemplate, Method: http.MethodPost,
			OperationID: "createFileEditRevertProposal", Summary: "Propose reverting one applied file edit",
			Tag:         "Control",
			Description: "Derives a new pending FileEdit from one exact applied replacement, creation, or deletion. Requires the file-review capability, a running Run and active Session for a new proposal, complete unredacted source content, and an unchanged target file. The source approval must belong to this Run. No path or content is accepted from the client. The key is scoped to Run and source Edit; retries preserve the existing proposal and its decision even after the Run ends. No Workspace file is written, approval granted, or execution permission expanded. Move operations are unsupported.",
			DataType:    reflect.TypeOf(FileEditRevertProposalView{}),
			RequestType: reflect.TypeOf(FileEditRevertProposalRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "source_edit_id", In: "path", Required: true, Schema: map[string]any{"type": "string"}},
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key scoped to Run and source Edit; preserve it after an uncertain response",
					Required: true, Schema: map[string]any{"type": "string", "minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: "/api/v1/runs/{run_id}/file-edits/{edit_id}",
			OperationID: "getRunFileEdit", Summary: "Inspect a Run file edit preview", Tag: "Runs",
			Description: "Returns one exact Run-bound redacted diff without original or proposed file bodies.",
			DataType:    reflect.TypeOf(FileEditPreviewView{}), NotFound: true,
			Parameters: []openAPIParameter{runID, editID}},
		{Path: FileEditReviewPathTemplate, Method: http.MethodPost,
			OperationID: "reviewRunFileEdit", Summary: "Approve intent or deny a file edit",
			Tag:         "Control",
			Description: "Records an exact Run-bound review decision. Approve-intent never writes the file; applying an approved edit remains a separate non-HTTP operation.",
			DataType:    reflect.TypeOf(FileEditReviewView{}),
			RequestType: reflect.TypeOf(FileEditReviewRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID, editID}},
		{Path: FileEditApplyPathTemplate, Method: http.MethodPost,
			OperationID: "applyRunFileEdit", Summary: "Apply an approved file edit",
			Tag:         "Control",
			Description: "Separately applies one exact Run-bound approved edit after fresh Run, Workspace, approval, Policy, path, and current-hash checks. The renderer supplies no path or file content, and retries replay the durable operation result.",
			DataType:    reflect.TypeOf(FileEditApplyView{}),
			RequestType: reflect.TypeOf(FileEditApplyRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID, editID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: RunWakeIntentPathTemplate, OperationID: "getRunWakeIntent",
			Summary: "Inspect the latest Run wake intent", Tag: "Runs",
			Description: "Returns a closed-authority wake projection without lease owner or fencing metadata.",
			DataType:    reflect.TypeOf(RunWakeStateView{}), NotFound: true,
			Parameters: []openAPIParameter{runID}},
		{Path: RunWakeIntentPathTemplate, Method: http.MethodPost,
			OperationID: "scheduleRunWake", Summary: "Schedule a bounded Run wake intent",
			Tag:         "Control",
			Description: "Persists bounded retry timing and single-owner intent for queued operator work. It does not start a background loop, model call, tool call, or Run execution.",
			DataType:    reflect.TypeOf(RunWakeControlView{}),
			RequestType: reflect.TypeOf(RunWakeScheduleRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: RunWakeCancellationPathTemplate, Method: http.MethodPost,
			OperationID: "cancelRunWake", Summary: "Cancel an active Run wake intent",
			Tag:         "Control",
			Description: "Cancels one active wake intent and revokes any current ownership lease without starting execution.",
			DataType:    reflect.TypeOf(RunWakeControlView{}),
			RequestType: reflect.TypeOf(RunWakeCancelRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}}}},
		{Path: RunWakeExecutionPathTemplate, Method: http.MethodPost,
			OperationID: "consumeRunWake", Summary: "Consume one due Run wake intent",
			Tag:         "Control",
			Description: "Explicitly claims one due wake generation and hands its bounded queued selection to the existing Go AgentRunner. It starts no hidden worker or background loop; retries replay the generation-fenced durable handoff.",
			DataType:    reflect.TypeOf(RunWakeExecutionView{}),
			RequestType: reflect.TypeOf(RunWakeExecutionRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID}},
		{Path: ScheduledJobsPath, OperationID: "listScheduledJobs",
			Summary: "List durable scheduled jobs", Tag: "Automation",
			Description: "Returns a bounded metadata-only projection. Lease fence tokens, operation keys, event payloads, prompts, terminal input, and command arguments are never returned.",
			DataType:    reflect.TypeOf(ScheduledJobListView{}), Parameters: []openAPIParameter{
				identityQueryParameter("run_id", "Optional exact owner Run filter"),
				{Name: "limit", In: "query", Description: "Maximum scheduled jobs",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": 100, "default": 50}},
			}},
		{Path: RunScheduledJobsPathTemplate, OperationID: "listRunScheduledJobs",
			Summary: "List one Run's scheduled jobs", Tag: "Automation",
			Description: "Returns the bounded scheduled-job projection for one exact owner and target Run.",
			DataType:    reflect.TypeOf(ScheduledJobListView{}), Parameters: []openAPIParameter{
				runID, {Name: "limit", In: "query", Description: "Maximum scheduled jobs",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": 100, "default": 50}},
			}},
		{Path: ScheduledJobPathTemplate, OperationID: "getScheduledJob",
			Summary: "Inspect a scheduled job", Tag: "Automation",
			Description: "Returns one job with bounded recent rounds, notifications, and an optional immutable authorization snapshot. Raw observation content is withheld.",
			DataType:    reflect.TypeOf(ScheduledJobDetailView{}), NotFound: true,
			Parameters: []openAPIParameter{jobID,
				{Name: "round_limit", In: "query", Description: "Maximum recent rounds",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": 100, "default": 20}},
				{Name: "notification_limit", In: "query", Description: "Maximum recent notifications",
					Schema: map[string]any{"type": "integer", "minimum": 1,
						"maximum": 100, "default": 20}},
			}},
		{Path: RunScheduledJobsPathTemplate, Method: http.MethodPost,
			OperationID: "createRunScheduledJob", Summary: "Create a bounded scheduled job",
			Tag:         "Automation",
			Description: "Creates one exact Run-bound once or periodic job. Time arrival grants only a fenced recheck; read-only jobs require Code/Plan and repair jobs require a current Code/Deliver permission snapshot plus exact operator confirmation. Creation starts no process or model call.",
			DataType:    reflect.TypeOf(ScheduledJobControlView{}),
			RequestType: reflect.TypeOf(ScheduledJobCreateRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID, dockerSandboxIdempotencyKey}},
		{Path: ScheduledJobActionPathTemplate, Method: http.MethodPost,
			OperationID: "transitionRunScheduledJob", Summary: "Pause, resume, or cancel a scheduled job",
			Tag:         "Automation",
			Description: "Applies one optimistic, idempotent lifecycle transition. Pause and cancel revoke the current lease; resume schedules an immediate bounded recheck without restoring stale repair authority.",
			DataType:    reflect.TypeOf(ScheduledJobControlView{}),
			RequestType: reflect.TypeOf(ScheduledJobTransitionRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID, jobID,
				scheduledJobAction, dockerSandboxIdempotencyKey}},
		{Path: ScheduledJobObservationPathTemplate, Method: http.MethodPost,
			OperationID: "enableRunScheduledJobObservation", Summary: "Confirm durable read-only observation of one scheduled job",
			Tag:         "Automation",
			Description: "Records one immutable, idempotent consent for an exact revision of a read-only zero-model job. The ordinary desktop worker observes confirmed jobs on later launches. This does not resume a paused job, grant repair authority, or start an OS service.",
			DataType:    reflect.TypeOf(ScheduledJobControlView{}),
			RequestType: reflect.TypeOf(ScheduledJobObservationRequestView{}), Control: true,
			NotFound: true, Parameters: []openAPIParameter{runID, jobID, dockerSandboxIdempotencyKey}},
		{Path: DoctorSnapshotPath, OperationID: "getDoctorSnapshot",
			Summary: "Read a structured doctor snapshot", Tag: "Diagnostics",
			Description: "Reports versioned Provider, model Harness, Run, Workspace, network, tool, sandbox, browser, and plugin readiness metadata. It performs no mutation, installation, process start, network probe, or model call.",
			DataType:    reflect.TypeOf(application.DoctorSnapshot{}), Parameters: []openAPIParameter{
				identityQueryParameter("run_id", "Optional exact Run diagnostic scope")}},
		{Path: DebugQueryPath, OperationID: "queryDebugTimeline",
			Summary: "Query a bounded redacted debug timeline", Tag: "Diagnostics",
			Description: "Returns monotonic persisted event-envelope evidence with payloads withheld. The query is limited to seven days, 100 results, and 500 scanned events and cannot return secrets, prompts, terminal input, or command arguments.",
			DataType:    reflect.TypeOf(application.DebugQueryResult{}),
			Parameters:  debugOpenAPIParameters()},
		{Path: DiagnosticBundlePath, OperationID: "exportDiagnosticBundle",
			Summary: "Export a bounded diagnostic bundle", Tag: "Diagnostics",
			Description: "Combines one structured doctor snapshot and one redacted debug page under the same hard cursor, window, scan, and content-withholding boundaries.",
			DataType:    reflect.TypeOf(application.DiagnosticBundle{}),
			Parameters:  debugOpenAPIParameters()},
		{Path: SkillPackageInstallPath, Method: http.MethodPost,
			OperationID: "installSkillPackage", Summary: "Install one inert Skill package",
			Tag:         "Control",
			Description: "Stages one explicitly confirmed archive or uploaded native directory in the existing Plugin lifecycle. Directory uploads contain bounded relative file names and base64 bytes; Go parses metadata and creates the snapshot. Archive/snapshot and directory inputs are mutually exclusive. Installation executes no scripts, commands or Provider calls. Capabilities require separate Plugin review.",
			DataType:    reflect.TypeOf(SkillPackageInstallView{}),
			RequestType: reflect.TypeOf(SkillPackageInstallRequestView{}), Control: true,
			Parameters: []openAPIParameter{
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: "/api/v1/skills/catalog/publishers", OperationID: "listSkillCatalogPublishers",
			Summary: "List legacy Skill Catalog publisher trust", Tag: "Extensions",
			DataType:    reflect.TypeOf(SkillCatalogPublishersView{}),
			Description: "Returns public publisher identities and the exact observed trust version. Listing does not grant trust or enable a package."},
		{Path: "/api/v1/skills/catalog/publishers", Method: http.MethodPost, OperationID: "controlSkillCatalogPublisher",
			Summary: "Review one legacy Skill Catalog publisher", Tag: "Extensions", Control: true, SuccessStatus: http.StatusOK,
			DataType: reflect.TypeOf(SkillCatalogPublisherView{}), RequestType: reflect.TypeOf(SkillCatalogPublisherControlRequestView{}),
			Description: "Explicitly trusts or revokes an exact Ed25519 publisher identity using expected_updated_at for atomic compare-and-swap. Empty expected_updated_at permits only first registration; stale requests cannot restore revoked trust."},
		{Path: SkillPackageInventoryPath, OperationID: "listSkillPackages",
			Summary: "List installed Skill packages", Tag: "Extensions",
			Description: "Returns inert installation metadata, eligibility lists, trust class and immutable removal tombstones. Each item reports the live readback result of its retained package object, so one unreadable object is shown as unverified instead of hiding the Registry. It never exposes package bodies, local paths, operation keys or digests, and listing grants no selection, execution or tool capability.",
			DataType:    reflect.TypeOf(SkillPackageInventoryView{}), Collection: true,
			Parameters: []openAPIParameter{
				stringQueryParameter("surface", "Filter by execution surface",
					[]string{string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)}),
				identityQueryParameter("profile", "Filter by one exact Profile identity"),
				booleanQueryParameter("include_removed", "Include removed packages"),
			}},
		{Path: SkillPackageRemovePath, Method: http.MethodPost,
			OperationID: "removeSkillPackage",
			Summary:     "Remove one installed Skill package",
			Tag:         "Control",
			Description: "Writes an immutable removal tombstone for one exact installed package after explicit operator confirmation. The retained object and historical Run facts stay unchanged, and removal grants no capability or re-authorization anywhere.",
			DataType:    reflect.TypeOf(SkillPackageRemovalView{}),
			RequestType: reflect.TypeOf(SkillPackageRemoveRequestView{}), Control: true,
			Parameters: []openAPIParameter{
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: ApprovalDecisionControlPathTemplate, Method: http.MethodPost,
			OperationID: "decideRunApproval", Summary: "Approve once or deny a pending request",
			Tag:         "Control",
			Description: "Applies an idempotent operator decision to one exact Run-bound approval. Approve-once is limited to Policy-rechecked dry-run Shell and process-disabled ScriptProcess proposals; file edits can only be denied. It never creates a Session grant, starts a real process, writes a file, starts Docker, or grants capability.",
			DataType:    reflect.TypeOf(ApprovalDecisionControlView{}),
			RequestType: reflect.TypeOf(ApprovalDecisionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID, approvalID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: RunExecutionControlPathTemplate, Method: http.MethodPost,
			OperationID: "executeRunSelection", Summary: "Execute a bounded queued Run batch",
			Tag:         "Control",
			Description: "Freezes at most eight currently queued Session steering messages, then executes only those exact message identities through the Go AgentRunner under one private execution lease. Retries replay the durable result and cannot consume messages appended after selection.",
			DataType:    reflect.TypeOf(RunExecutionControlView{}),
			RequestType: reflect.TypeOf(RunExecutionControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{
				runID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: "/api/v1/runs/{run_id}/work-items", OperationID: "listRunWorkItems",
			Summary: "List Run WorkItems", Tag: "Memory", Description: "Returns structured Work Board items.",
			DataType: reflect.TypeOf(WorkItemView{}), Collection: true, Paginated: true, NotFound: true,
			Parameters: append(append([]openAPIParameter{runID}, paginationParameters()...),
				arrayQueryParameter("status", "Repeat or comma-separate WorkItem statuses", workItemStatusesOpenAPI(), 5),
				stringQueryParameter("owner", "Exact legacy WorkItem owner-label filter", nil),
				identityQueryParameter("owner_agent_id", "Exact WorkItem owner Agent identity"))},
		{Path: "/api/v1/runs/{run_id}/notes", OperationID: "listRunNotes", Summary: "List Run Notes",
			Tag: "Memory", Description: "Returns structured, redacted Run Notes.", DataType: reflect.TypeOf(NoteView{}),
			Collection: true, Paginated: true, NotFound: true,
			Parameters: append(append([]openAPIParameter{runID}, paginationParameters()...),
				arrayQueryParameter("status", "Repeat or comma-separate Note statuses", noteStatusesOpenAPI(), 2),
				arrayQueryParameter("category", "Repeat or comma-separate Note categories", noteCategoriesOpenAPI(), 5),
				arrayQueryParameter("visibility", "Repeat or comma-separate Note visibility values", noteVisibilitiesOpenAPI(), 3),
				stringQueryParameter("owner", "Exact legacy Note owner-label filter", nil),
				identityQueryParameter("owner_agent_id", "Exact Note owner Agent identity"),
				arrayQueryParameter("tag", "Repeat or comma-separate normalized Note tags", nil, domain.MaxNoteTags),
				booleanQueryParameter("pinned", "Filter by pinned state"))},
		{Path: "/api/v1/runs/{run_id}/artifacts", OperationID: "listRunArtifacts",
			Summary: "List Run Artifact descriptors", Tag: "Artifacts",
			Description: "Returns metadata and hashes only; Artifact content is never returned by HTTP.",
			DataType:    reflect.TypeOf(ArtifactView{}), Collection: true, Paginated: true, NotFound: true,
			Parameters: append(append([]openAPIParameter{runID}, paginationParameters()...),
				identityQueryParameter("source_id", "Exact source proposal or invocation identity"),
				stringQueryParameter("stream", "Artifact output stream", artifactStreams()))},
		{Path: "/api/v1/runs/{run_id}/tool-rounds", OperationID: "listRunToolRounds",
			Summary: "List Supervisor tool rounds", Tag: "Runs",
			Description: "Returns persisted Supervisor tool rounds with the same Go-owned closed thread_activity_detail.v2 union used by the Thread conversation. Raw durable payload/result JSON and generic name/value fact arrays are never exposed; unsupported historical calls fail closed with detail_available=false.",
			DataType:    reflect.TypeOf(SupervisorToolRoundView{}), Collection: true, Paginated: true,
			NotFound: true, Parameters: append([]openAPIParameter{runID}, paginationParameters()...)},
		{Path: "/api/v1/sessions", OperationID: "listSessions", Summary: "List Sessions", Tag: "Sessions",
			Description: "Returns a bounded creation-ordered keyset page of persisted Sessions. Updates and later Sessions cannot shift continuation pages.", DataType: reflect.TypeOf(SessionView{}),
			Collection: true, Paginated: true, Parameters: paginationParameters()},
		{Path: "/api/v1/sessions/{session_id}", OperationID: "getSession", Summary: "Inspect a Session",
			Tag: "Sessions", Description: "Returns Session metadata and its optional bound Run.",
			DataType: reflect.TypeOf(SessionDetailView{}), NotFound: true, Parameters: []openAPIParameter{sessionID}},
		{Path: SessionArchiveControlPathTemplate, Method: http.MethodPost,
			OperationID: "archiveSession", Summary: "Archive a legacy unbound Session", Tag: "Control",
			Description: "Archives only a legacy Session that is not owned by a Thread while retaining its messages and audit records. Thread-backed conversations must use the Thread lifecycle endpoints so Run, Session, message, and archive state cannot diverge. Replays are idempotent.",
			DataType:    reflect.TypeOf(SessionArchiveControlView{}),
			RequestType: reflect.TypeOf(SessionArchiveControlRequestView{}),
			Control:     true, NotFound: true, Parameters: []openAPIParameter{sessionID}},
		{Path: "/api/v1/sessions/{session_id}/messages", OperationID: "listSessionMessages",
			Summary: "List Session messages", Tag: "Sessions", Description: "Returns persisted redacted messages.",
			DataType: reflect.TypeOf(MessageView{}), Collection: true, Paginated: true, NotFound: true,
			Parameters: append(append([]openAPIParameter{sessionID}, paginationParameters()...),
				booleanQueryParameter("include_compacted", "Include compacted historical messages"))},
		{Path: SessionMessageControlPathTemplate, Method: http.MethodPost,
			OperationID: "submitSessionMessage", Summary: "Submit a Run-bound Session message",
			Tag:         "Control",
			Description: "Creates or replays one redacted durable operator message for the exact Run-bound Session. delivery_mode defaults to next_turn for old clients; steer accepts text only and binds the active Supervisor attempt without granting capability. Replays keep their original mode and attempt. Submission does not append Session history early or itself call a model or tool.",
			DataType:    reflect.TypeOf(SessionMessageControlView{}),
			RequestType: reflect.TypeOf(SessionMessageControlRequestView{}),
			Control:     true, NotFound: true,
			Parameters: []openAPIParameter{
				sessionID,
				{Name: "Idempotency-Key", In: "header", Description: "Opaque retry key; only a domain-separated digest is persisted",
					Required: true, Schema: map[string]any{"type": "string",
						"minLength": domain.MinAgentOperationKeyBytes,
						"maxLength": domain.MaxAgentOperationKeyBytes, "pattern": `^\S+$`}},
			}},
		{Path: SessionMessageOperationObservationPathTemplate,
			OperationID: "inspectSessionMessageOperation", Summary: "Observe an exact Session message submission", Tag: "Control",
			Description: "Read the accepted status of an original idempotency key without replaying the write or exposing message content. A missing key returns not_received.",
			DataType:    reflect.TypeOf(SessionMessageOperationObservationView{}), NotFound: true,
			Parameters: []openAPIParameter{sessionID, pathIdentityParameter("operation_key", "Original operation key")}},
		{Path: "/api/v1/work-items/{work_item_id}", OperationID: "getWorkItem", Summary: "Inspect a WorkItem",
			Tag: "Memory", Description: "Returns one structured WorkItem.", DataType: reflect.TypeOf(WorkItemView{}),
			NotFound: true, Parameters: []openAPIParameter{workItemID}},
		{Path: "/api/v1/notes/{note_id}", OperationID: "getNote", Summary: "Inspect a Note", Tag: "Memory",
			Description: "Returns one redacted structured Note.", DataType: reflect.TypeOf(NoteView{}),
			NotFound: true, Parameters: []openAPIParameter{noteID}},
		{Path: "/api/v1/artifacts/{artifact_id}", OperationID: "getArtifact", Summary: "Inspect an Artifact descriptor",
			Tag: "Artifacts", Description: "Returns content-free Artifact metadata and its integrity hash.",
			DataType: reflect.TypeOf(ArtifactView{}), NotFound: true, Parameters: []openAPIParameter{artifactID}},
	}
}

func buildOpenAPIOperation(spec openAPIOperationSpec, registry *openAPISchemaRegistry) (openAPIOperation, error) {
	responses := standardOperationResponses(spec.NotFound, spec.Control)
	successStatus, err := openAPISuccessStatus(spec)
	if err != nil {
		return openAPIOperation{}, err
	}
	successDescription := "Successful read"
	if spec.Control {
		successDescription = "Control request accepted or idempotently replayed"
		if successStatus == "201" {
			successDescription = "Resource created or idempotently replayed"
		}
	}
	if spec.Streaming {
		responses[successStatus] = openAPIResponse{Description: "Bounded Server-Sent Event stream", Content: map[string]openAPIMediaType{
			"text/event-stream": {Schema: registry.ref(spec.DataType)},
		}}
	} else if spec.RawDocument {
		responses[successStatus] = openAPIResponse{Description: "OpenAPI 3.1 document", Content: map[string]openAPIMediaType{
			openAPIContentType: {Schema: map[string]any{"type": "object", "additionalProperties": true}},
		}}
	} else if spec.RawArtifact {
		responses[successStatus] = openAPIResponse{Description: "Hash-verified untrusted evidence bytes", Content: map[string]openAPIMediaType{
			"application/octet-stream": {Schema: map[string]any{"type": "string", "format": "binary"}},
			"image/png":                {Schema: map[string]any{"type": "string", "format": "binary"}},
			"application/json":         {Schema: map[string]any{"type": "string", "format": "binary"}},
		}}
		if spec.OperationID == "readWorkspaceImageContent" || spec.OperationID == "readRunFullCDPPreviewImage" || spec.OperationID == "readRunAgentBrowserScreenshot" {
			mediaTypes := []string{"image/png"}
			if spec.OperationID == "readWorkspaceImageContent" {
				mediaTypes = append(mediaTypes, "image/jpeg", "image/webp")
			}
			content := make(map[string]openAPIMediaType, len(mediaTypes))
			for _, mediaType := range mediaTypes {
				content[mediaType] = openAPIMediaType{Schema: map[string]any{"type": "string", "format": "binary"}}
			}
			responses[successStatus] = openAPIResponse{Description: "Authenticated hash-verified image bytes", Content: content}
		}
		if spec.OperationID == "readWorkspaceFileAttachmentContent" {
			responses[successStatus] = openAPIResponse{Description: "Authenticated hash-verified original file bytes", Content: map[string]openAPIMediaType{
				"application/octet-stream": {Schema: map[string]any{"type": "string", "format": "binary"}},
			}}
		}
	} else {
		if spec.DataType == nil {
			return openAPIOperation{}, fmt.Errorf("OpenAPI path %q has no response DTO", spec.Path)
		}
		dataSchema := registry.ref(spec.DataType)
		if spec.Path == SessionSteeringPromotionPathTemplate {
			dataSchema = map[string]any{"oneOf": []any{dataSchema, registry.ref(reflect.TypeOf(SessionSteeringPromotionRejectionView{}))}}
		}
		if spec.OperationID == "installSkillPackage" {
			dataSchema = map[string]any{"oneOf": []any{dataSchema, registry.ref(reflect.TypeOf(PluginSkillInstallView{}))}}
		}
		if spec.Collection {
			dataSchema = map[string]any{"type": "array", "items": dataSchema}
		}
		responses[successStatus] = openAPIResponse{Description: successDescription, Content: map[string]openAPIMediaType{
			"application/json": {Schema: successEnvelopeSchema(dataSchema, spec.Paginated, registry)},
		}}
	}
	operation := openAPIOperation{OperationID: spec.OperationID, Summary: spec.Summary,
		Description: spec.Description, Tags: []string{spec.Tag}, Parameters: spec.Parameters,
		Responses: responses, ReadOnly: !spec.Control, Streaming: spec.Streaming}
	if spec.Control && spec.RequestType == nil && !spec.EmptyBody {
		return openAPIOperation{}, fmt.Errorf("OpenAPI control path %q has no request DTO", spec.Path)
	}
	if spec.Control {
		operation.Security = []map[string][]string{{"ControlBearerAuth": {}}}
	}
	if spec.RequestType != nil {
		operation.RequestBody = &openAPIRequestBody{Required: true, Content: map[string]openAPIMediaType{
			"application/json": {Schema: registry.ref(spec.RequestType)},
		}}
	}
	return operation, nil
}

func openAPISuccessStatus(spec openAPIOperationSpec) (string, error) {
	status := spec.SuccessStatus
	if status == 0 {
		if spec.Control {
			status = http.StatusAccepted
		} else {
			status = http.StatusOK
		}
	}
	if status < 200 || status > 299 {
		return "", fmt.Errorf("OpenAPI path %q has invalid success status %d", spec.Path, status)
	}
	if !spec.Control && status != http.StatusOK {
		return "", fmt.Errorf("OpenAPI read path %q must use status 200", spec.Path)
	}
	return fmt.Sprint(status), nil
}

func successEnvelopeSchema(dataSchema map[string]any, paginated bool, registry *openAPISchemaRegistry) map[string]any {
	properties := map[string]any{
		"version":    map[string]any{"type": "string", "const": Version},
		"request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		"data":       dataSchema,
	}
	required := []string{"version", "request_id", "data"}
	if paginated {
		properties["page"] = registry.refNamed("Page", reflect.TypeOf(Page{}))
		required = append(required, "page")
	}
	return map[string]any{"type": "object", "additionalProperties": false,
		"properties": properties, "required": required}
}

func standardOperationResponses(notFound bool, control bool) map[string]any {
	responses := map[string]any{
		"400": responseReference("BadRequest"),
		"401": responseReference("Unauthorized"),
		"403": responseReference("Forbidden"),
		"414": responseReference("RequestTooLarge"),
		"429": responseReference("ResourceExhausted"),
		"500": responseReference("InternalError"),
		"503": responseReference("Unavailable"),
		"504": responseReference("GatewayTimeout"),
	}
	if notFound {
		responses["404"] = responseReference("NotFound")
	}
	if control {
		responses["409"] = responseReference("Conflict")
		responses["412"] = responseReference("FailedPrecondition")
		responses["413"] = responseReference("RequestEntityTooLarge")
		responses["415"] = responseReference("UnsupportedMediaType")
	}
	return responses
}

func responseReference(name string) map[string]any {
	return map[string]any{"$ref": "#/components/responses/" + name}
}

func standardOpenAPIErrorResponses(registry *openAPISchemaRegistry) map[string]openAPIResponse {
	schema := registry.refNamed("ErrorEnvelope", reflect.TypeOf(errorEnvelope{}))
	makeResponse := func(description string) openAPIResponse {
		return openAPIResponse{Description: description,
			Content: map[string]openAPIMediaType{"application/json": {Schema: schema}}}
	}
	return map[string]openAPIResponse{
		"BadRequest":            makeResponse("Invalid path, query, method, or request body"),
		"Unauthorized":          makeResponse("Missing or invalid bearer token"),
		"Forbidden":             makeResponse("Request is outside the loopback security boundary"),
		"NotFound":              makeResponse("Requested durable resource was not found"),
		"Conflict":              makeResponse("Idempotency key, Run, or active attempt changed"),
		"FailedPrecondition":    makeResponse("The target model attempt is not active or cancellable"),
		"RequestEntityTooLarge": makeResponse("Control request body exceeded its hard limit"),
		"UnsupportedMediaType":  makeResponse("Control request must use application/json"),
		"RequestTooLarge":       makeResponse("Request target or query exceeded its hard limit"),
		"ResourceExhausted":     makeResponse("Bounded response or resource limit was exhausted"),
		"InternalError":         makeResponse("Redacted internal server failure"),
		"Unavailable":           makeResponse("Required local runtime or trusted browser is unavailable"),
		"GatewayTimeout":        makeResponse("A bounded local runtime operation exceeded its deadline"),
	}
}

func paginationParameters() []openAPIParameter {
	return []openAPIParameter{
		{Name: "limit", In: "query", Description: "Page size from 1 to 100; defaults to 50",
			Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": MaxPageLimit, "default": DefaultPageLimit}},
		{Name: "cursor", In: "query", Description: "Opaque cursor bound to this route and exact filter set",
			Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": MaxCursorBytes}},
	}
}

func threadTranscriptPaginationParameters() []openAPIParameter {
	return []openAPIParameter{
		{Name: "limit", In: "query",
			Description: "Maximum durable source records from 1 to 100; a bounded structured tool batch expands item-by-item; defaults to 50",
			Schema:      map[string]any{"type": "integer", "minimum": 1, "maximum": MaxPageLimit, "default": DefaultPageLimit}},
		{Name: "cursor", In: "query",
			Description: "Opaque cursor bound to this Thread transcript route and source-record ordering",
			Schema:      map[string]any{"type": "string", "minLength": 1, "maxLength": MaxCursorBytes}},
	}
}

func debugOpenAPIParameters() []openAPIParameter {
	runID := identityQueryParameter("run_id", "Exact Run diagnostic scope")
	runID.Required = true
	correlationID := identityQueryParameter("correlation_id",
		"Exact correlation identity paired with correlation_kind")
	return []openAPIParameter{
		runID,
		{Name: "after_sequence", In: "query",
			Description: "Exclusive durable event-sequence cursor",
			Schema:      map[string]any{"type": "integer", "minimum": 0, "default": 0}},
		{Name: "from", In: "query", Description: "Inclusive RFC3339 lower time bound",
			Schema: map[string]any{"type": "string", "format": "date-time"}},
		{Name: "to", In: "query", Description: "Inclusive RFC3339 upper time bound",
			Schema: map[string]any{"type": "string", "format": "date-time"}},
		{Name: "limit", In: "query", Description: "Maximum returned timeline items",
			Schema: map[string]any{"type": "integer", "minimum": 1,
				"maximum": 100, "default": 100}},
		{Name: "correlation_kind", In: "query",
			Description: "Optional exact correlation dimension",
			Schema: map[string]any{"type": "string", "enum": []string{
				"run", "attempt", "tool", "process", "request"}}},
		correlationID,
		{Name: "type_prefix", In: "query", Description: "Optional event type prefix",
			Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
		{Name: "source_prefix", In: "query", Description: "Optional event source prefix",
			Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
	}
}

func pathIdentityParameter(name string, description string) openAPIParameter {
	return openAPIParameter{Name: name, In: "path", Description: description, Required: true,
		Schema: identitySchema()}
}

func identityQueryParameter(name string, description string) openAPIParameter {
	return openAPIParameter{Name: name, In: "query", Description: description, Schema: identitySchema()}
}

func identitySchema() map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "maxLength": 256,
		"pattern": `^[^/\\\x00-\x1f\x7f]+$`}
}

func stringQueryParameter(name string, description string, enum []string) openAPIParameter {
	schema := map[string]any{"type": "string", "minLength": 1}
	if len(enum) != 0 {
		schema["enum"] = enum
	}
	return openAPIParameter{Name: name, In: "query", Description: description, Schema: schema}
}

func arrayQueryParameter(name string, description string, enum []string, maxItems int) openAPIParameter {
	items := map[string]any{"type": "string", "minLength": 1}
	if len(enum) != 0 {
		items["enum"] = enum
	}
	explode := false
	return openAPIParameter{Name: name, In: "query", Description: description, Style: "form", Explode: &explode,
		Schema: map[string]any{"type": "array", "items": items, "minItems": 1, "maxItems": maxItems,
			"uniqueItems": true}}
}

func booleanQueryParameter(name string, description string) openAPIParameter {
	return openAPIParameter{Name: name, In: "query", Description: description,
		Schema: map[string]any{"type": "boolean"}}
}

type openAPISchemaRegistry struct {
	schemas map[string]map[string]any
	names   map[reflect.Type]string
	types   map[string]reflect.Type
	err     error
}

func newOpenAPISchemaRegistry() *openAPISchemaRegistry {
	registry := &openAPISchemaRegistry{schemas: map[string]map[string]any{},
		names: map[reflect.Type]string{}, types: map[string]reflect.Type{}}
	registry.refNamed("Page", reflect.TypeOf(Page{}))
	registry.refNamed("APIError", reflect.TypeOf(apiErrorView{}))
	registry.refNamed("ErrorEnvelope", reflect.TypeOf(errorEnvelope{}))
	return registry
}

func (r *openAPISchemaRegistry) ref(valueType reflect.Type) map[string]any {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	if valueType == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if valueType == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	if valueType.Kind() != reflect.Struct {
		return r.schema(valueType)
	}
	name := valueType.Name()
	if strings.HasSuffix(valueType.PkgPath(), "/toolcontract") {
		name = "ToolContract" + name
	}
	if valueType == reflect.TypeOf(skills.Manifest{}) {
		name = "SkillPackageManifest"
	}
	if valueType == reflect.TypeOf(repository.Change{}) {
		name = "RepositoryChange"
	}
	if strings.HasSuffix(valueType.PkgPath(), "/uievidence") {
		name = "UIEvidence" + name
	}
	if strings.HasSuffix(valueType.PkgPath(), "/gitadvanced") {
		name = "GitAdvanced" + name
	}
	if strings.HasSuffix(valueType.PkgPath(), "/githubreview") {
		name = "GitHubReview" + name
	}
	if strings.HasSuffix(valueType.PkgPath(), "/standardcodedelivery") {
		name = "StandardCodeDelivery" + name
	}
	return r.refNamed(name, valueType)
}

func (r *openAPISchemaRegistry) refNamed(name string, valueType reflect.Type) map[string]any {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	if existing, ok := r.names[valueType]; ok {
		return map[string]any{"$ref": "#/components/schemas/" + existing}
	}
	if previous, ok := r.types[name]; ok && previous != valueType {
		r.err = fmt.Errorf("OpenAPI schema name %q is shared by %s and %s", name, previous, valueType)
		return map[string]any{}
	}
	r.names[valueType] = name
	r.types[name] = valueType
	r.schemas[name] = map[string]any{}
	r.schemas[name] = r.objectSchema(valueType)
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func (r *openAPISchemaRegistry) schema(valueType reflect.Type) map[string]any {
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	if valueType == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if valueType == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	switch valueType.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return map[string]any{"type": "integer", "format": "int32"}
	case reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.Float32:
		return map[string]any{"type": "number", "format": "float"}
	case reflect.Float64:
		return map[string]any{"type": "number", "format": "double"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": r.ref(valueType.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": r.ref(valueType.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		return r.ref(valueType)
	default:
		r.err = fmt.Errorf("unsupported OpenAPI DTO type %s", valueType)
		return map[string]any{}
	}
}

func (r *openAPISchemaRegistry) objectSchema(valueType reflect.Type) map[string]any {
	// The HTTP-only sidecar is flattened beside the sealed Report. Keep the
	// original Report schema/field metadata intact instead of adding receipt data.
	if valueType == reflect.TypeOf(StandardCodeDeliveryReportView{}) {
		schema := r.objectSchema(reflect.TypeOf(standardcodedelivery.Report{}))
		properties := schema["properties"].(map[string]any)
		properties["output_sources"] = r.schema(reflect.TypeOf([]StandardCodeDeliveryOutputSourceView{}))
		properties["output_sources"].(map[string]any)["maxItems"] = standardcodedelivery.MaxVerifications * standardcodedelivery.MaxArtifactsPerCommand
		return schema
	}
	properties := make(map[string]any)
	required := make([]string, 0, valueType.NumField())
	metadataTypeName := valueType.Name()
	if strings.HasSuffix(valueType.PkgPath(), "/standardcodedelivery") {
		metadataTypeName = "StandardCodeDelivery" + metadataTypeName
	}
	for index := 0; index < valueType.NumField(); index++ {
		field := valueType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		// These DTOs use encoding/json's promoted anonymous context fields.
		if field.Anonymous && field.Type == reflect.TypeOf(application.ThreadGitContext{}) {
			contextSchema := r.objectSchema(field.Type)
			for name, property := range contextSchema["properties"].(map[string]any) {
				properties[name] = property
			}
			if contextRequired, ok := contextSchema["required"].([]string); ok {
				required = append(required, contextRequired...)
			}
			continue
		}
		name, omitEmpty, skip := jsonField(field)
		if skip {
			continue
		}
		property := r.ref(field.Type)
		applyOpenAPIFieldMetadata(metadataTypeName, name, property)
		properties[name] = property
		if !omitEmpty {
			required = append(required, name)
		}
	}
	if metadataTypeName == "ThreadActivityTypedDetailView" {
		return threadActivityTypedDetailSchema(properties)
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) != 0 {
		schema["required"] = required
	}
	if metadataTypeName == "ThreadTurnControlRequestView" {
		alternatives := []any{
			map[string]any{"required": []string{"content"}, "properties": map[string]any{"content": map[string]any{"type": "string", "minLength": 1}}},
			map[string]any{"required": []string{"images"}, "properties": map[string]any{"images": map[string]any{"type": "array", "minItems": 1}}},
			map[string]any{"required": []string{"attachments"}, "properties": map[string]any{"attachments": map[string]any{"type": "array", "minItems": 1}}},
			map[string]any{"required": []string{"mcp_references"}, "properties": map[string]any{"mcp_references": map[string]any{"type": "array", "minItems": 1}}},
		}
		return map[string]any{"allOf": []any{schema, map[string]any{"anyOf": alternatives}}}
	}
	return schema
}

func threadActivityTypedDetailSchema(properties map[string]any) map[string]any {
	branches := []struct {
		kind, field string
	}{
		{kind: "command", field: "command"},
		{kind: "web_search", field: "web_search"},
		{kind: "web_fetch", field: "web_fetch"},
		{kind: "file_read", field: "file_read"},
		{kind: "file_edit", field: "file_edit"},
		{kind: "mcp", field: "mcp"},
		{kind: "verification", field: "verification"},
		{kind: "browser", field: "browser"},
	}
	oneOf := make([]any, 0, len(branches))
	for _, branch := range branches {
		oneOf = append(oneOf, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"kind":       map[string]any{"type": "string", "enum": []string{branch.kind}},
				branch.field: properties[branch.field],
			},
			"required": []string{"kind", branch.field},
		})
	}
	return map[string]any{
		"oneOf":         oneOf,
		"discriminator": map[string]any{"propertyName": "kind"},
	}
}

func jsonField(field reflect.StructField) (string, bool, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = field.Name
	}
	omitEmpty := false
	for _, option := range parts[1:] {
		omitEmpty = omitEmpty || option == "omitempty"
	}
	return name, omitEmpty, false
}

func applyOpenAPIFieldMetadata(typeName string, fieldName string, schema map[string]any) {
	if typeName == "MCPCredentialStatusView" && fieldName == "plaintext_returned" {
		schema["enum"] = []bool{false}
	}
	if typeName == "TaskBudgetSettings" {
		bounds := map[string][2]float64{
			"max_turns": {1, domain.MaxTaskTurns}, "max_tokens": {0, float64(domain.MaxTaskTokens)},
			"max_tool_calls": {1, float64(domain.MaxTaskToolCalls)}, "max_cost_usd": {0, domain.MaxTaskCostUSD},
			"timeout_seconds": {0, float64(domain.MaxTaskTimeoutSeconds)},
		}
		if bound, found := bounds[fieldName]; found {
			schema["minimum"], schema["maximum"] = bound[0], bound[1]
		}
	}
	if (typeName == "TaskConfigurationView" || typeName == "TaskSkillCandidatesView") && fieldName == "capability_grant" {
		schema["enum"] = []bool{false}
	}
	if typeName == "TaskConfigurationView" && (fieldName == "fingerprint" || fieldName == "project_fingerprint") {
		schema["pattern"] = "^[0-9a-f]{64}$"
	}
	if typeName == "TaskSkillCandidatesView" && fieldName == "items" {
		schema["maxItems"] = application.MaxTaskSkillCandidates
	}
	if typeName == "TaskSkillCandidatesView" && fieldName == "unavailable_count" {
		schema["minimum"] = 0
	}
	if typeName == "TaskSkillCandidateItem" && fieldName == "ref" {
		schema["maxLength"] = 8192
	}
	if typeName == "TaskSkillCandidateItem" && fieldName == "token_upper_bound" {
		schema["minimum"], schema["maximum"] = 1, skills.MaxExternalSelectionTokenBudget
	}

	if typeName == "BoundedCommandGrantView" && fieldName == "each_command_requires_review" {
		schema["enum"] = []bool{true}
	}
	if typeName == "SessionSteeringRevisionRequestView" && fieldName == "content" {
		schema["minLength"], schema["maxLength"] = 0, domain.MaxOperatorSteeringContentBytes
	}
	if (typeName == "SessionSteeringRevisionRequestView" && fieldName == "expected_revision") ||
		(typeName == "QueueRevisionUnchangedView" && fieldName == "expected_revision") ||
		(typeName == "OperatorSteeringObservationMessageView" && fieldName == "revision") ||
		(typeName == "ThreadQueuedMessageView" && fieldName == "revision") ||
		(typeName == "SessionSteeringRevisionReceiptView" && (fieldName == "from_revision" || fieldName == "to_revision")) {
		schema["minimum"] = 0
	}
	if typeName == "QueueRevisionUnchangedView" && strings.HasSuffix(fieldName, "_sha256") {
		schema["pattern"] = "^[0-9a-f]{64}$"
	}
	if typeName == "QueueRevisionUnchangedView" && (fieldName == "execution_started" || fieldName == "model_called" || fieldName == "tool_called" || fieldName == "capability_grant") {
		schema["enum"] = []bool{false}
	}
	if typeName == "ThreadQueuedMessagesView" && fieldName == "items" {
		schema["maxItems"] = domain.MaxPendingOperatorSteering
	}
	if typeName == "RunContextDiagnosticsView" && fieldName == "records" {
		schema["maxItems"] = contextDiagnosticLimit
	}
	if typeName == "HealthView" && fieldName == "data_store_id" {
		schema["pattern"] = "^ds1_[0-9a-f]{64}$"
		schema["minLength"] = 68
		schema["maxLength"] = 68
		schema["description"] = "Optional non-secret namespace for recovering local UI drafts and original request identities. Stable across restarts at the same data store path; different data stores or copied paths are isolated. Absence means durable recovery is unavailable. Never an authorization grant."
		return
	}
	if typeName == "PlanDeliveryProposalView" && fieldName == "directions" {
		schema["minItems"] = domain.MinPlanDeliveryDirections
		schema["maxItems"] = domain.PlanDeliveryDirectionCount
	}
	if values := openAPIFieldEnums[typeName+"."+fieldName]; len(values) != 0 {
		if schema["type"] == "array" {
			if items, ok := schema["items"].(map[string]any); ok {
				items["enum"] = values
			}
		} else {
			schema["enum"] = values
		}
	}
	if minimum, ok := openAPIFieldMinimums[typeName+"."+fieldName]; ok {
		schema["minimum"] = minimum
	}
	if maximum, ok := openAPIFieldMaximums[typeName+"."+fieldName]; ok {
		schema["maximum"] = maximum
	}
	if maximum, ok := openAPIFieldMaxLengths[typeName+"."+fieldName]; ok {
		schema["maxLength"] = maximum
	}
	if typeName == "PlanDeliveryWorkItemControlRequestView" || typeName == "PlanDeliveryCheckpointControlRequestView" {
		if fieldName == "expected_work_item_version" {
			schema["minimum"] = 1
		}
		if typeName == "PlanDeliveryCheckpointControlRequestView" {
			switch fieldName {
			case "focused_verification", "diff_audit", "security_audit":
				schema["minLength"], schema["maxLength"] = 1, domain.MaxDeliveryEvidenceRunes
			case "functional_verification", "robustness_audit":
				schema["maxLength"] = domain.MaxDeliveryEvidenceRunes
			case "handoff_summary":
				schema["minLength"], schema["maxLength"] = 1, domain.MaxDeliveryHandoffSummaryRunes
			}
		}
	}
	if typeName == "PlanDeliveryWorkItemControlView" && fieldName == "applied_version" {
		schema["minimum"] = 2
	}
	if strings.HasPrefix(typeName, "StandardCodeDelivery") &&
		(schema["type"] == "string" || schema["type"] == nil) {
		switch {
		case strings.HasSuffix(fieldName, "_sha256") || fieldName == "request_fingerprint" ||
			fieldName == "root_fingerprint":
			schema["minLength"] = 64
			schema["maxLength"] = 64
			schema["pattern"] = "^[0-9a-f]{64}$"
		case fieldName == "base_commit" || fieldName == "head_commit":
			schema["minLength"] = 40
			schema["maxLength"] = 64
			schema["pattern"] = "^(?:[0-9a-f]{40}|[0-9a-f]{64})$"
		case fieldName == "path":
			schema["maxLength"] = standardcodedelivery.MaxPathRunes
		case fieldName == "summary":
			schema["maxLength"] = standardcodedelivery.MaxTextRunes
		case strings.HasSuffix(fieldName, "_url") || fieldName == "url" ||
			fieldName == "self" || fieldName == "checkpoint" ||
			fieldName == "checkpoint_timeline" || fieldName == "undo" ||
			fieldName == "rewind" || fieldName == "fork":
			schema["maxLength"] = 2048
		}
	}
	if strings.HasSuffix(fieldName, "_id") || fieldName == "id" || fieldName == "event_id" {
		if _, ref := schema["$ref"]; !ref {
			schema["minLength"] = 1
			schema["maxLength"] = 256
		}
	}
	if typeName == "RepositoryCommitComparisonView" &&
		(fieldName == "base_object_id" || fieldName == "head_object_id") {
		schema["minLength"] = 40
		schema["maxLength"] = 40
		schema["pattern"] = "^[0-9a-f]{40}$"
	}
	if typeName == "ArtifactView" && fieldName == "sha256" {
		schema["pattern"] = "^[a-f0-9]{64}$"
	}
	if fieldName == "content_sha256" || fieldName == "receipt_content_sha256" {
		schema["pattern"] = "^[a-f0-9]{64}$"
	}
	if (typeName == "StandardCodePresetControlRequestView" &&
		fieldName == "expected_trust_digest") ||
		(typeName == "StandardCodePresetControlView" && fieldName == "trust_digest") {
		schema["minLength"] = 64
		schema["maxLength"] = 64
		schema["pattern"] = "^[a-f0-9]{64}$"
	}
	if typeName == "StandardCodePresetControlView" && fieldName == "blocked_by" {
		schema["maxItems"] = len(openAPIFieldEnums["StandardCodePresetControlView.blocked_by"])
	}
	if typeName == "StandardCodePresetControlView" && fieldName == "next_steps" {
		schema["maxItems"] = 7
	}
	if (typeName == "RunCreationControlRequestView" ||
		typeName == "ThreadCreationControlRequestView") && fieldName == "allowed_targets" {
		schema["maxItems"] = 256
		schema["uniqueItems"] = true
		if items, ok := schema["items"].(map[string]any); ok {
			items["minLength"] = 1
			items["maxLength"] = 512
		}
	}
	if (typeName == "RunNetworkAuthorityControlRequestView" &&
		fieldName == "add_allowed_targets") ||
		(typeName == "RunNetworkAuthorityControlView" && fieldName == "added_targets") {
		schema["minItems"] = 1
		schema["maxItems"] = 256
		schema["uniqueItems"] = true
		if items, ok := schema["items"].(map[string]any); ok {
			items["minLength"] = 1
			items["maxLength"] = 512
		}
	}
	if typeName == "RunNetworkAuthorityControlView" && fieldName == "capability_grant" {
		schema["const"] = true
	}
	if typeName == "WorkspaceImportRequestView" {
		if fieldName == "confirmed" {
			schema["const"] = true
		}
		if fieldName == "directory_path" {
			schema["minLength"] = 1
			schema["maxLength"] = MaxWorkspaceImportDirectoryBytes
			schema["description"] = "Operator-entered absolute existing directory on the server computer; at most 4096 UTF-8 bytes. Never a browser upload or directory enumeration request."
		}
	}
	if typeName == "WorkspaceImportView" &&
		(fieldName == "directory_content_modified" || fieldName == "agent_authority_granted") {
		schema["const"] = false
	}
	if typeName == "HostCommandProposalView" {
		switch fieldName {
		case "saved_output":
			schema["description"] = "Bound, saved and redacted stdout/stderr from newer Host results. Omitted for legacy results and metadata-only responses. Never reconstruct missing streams by parsing the untrusted evidence wrapper."
		case "risk_kinds":
			schema["minItems"] = 1
			schema["maxItems"] = 6
			schema["uniqueItems"] = true
		case "network_targets", "credential_kinds", "host_paths":
			schema["minItems"] = 1
			schema["maxItems"] = runner.MaxRiskEscalationItems
			schema["uniqueItems"] = true
		case "executable_sha256", "environment_sha256", "spec_fingerprint",
			"fingerprint", "workspace_root_fingerprint", "capability_generation",
			"scope_fingerprint":
			schema["minLength"] = 64
			schema["maxLength"] = 64
			schema["pattern"] = "^[0-9a-f]{64}$"
		}
	}
	if typeName == "StandardCodeBackendReadinessView" && fieldName == "blocked_by" {
		schema["maxItems"] = len(openAPIFieldEnums["StandardCodeBackendReadinessView.blocked_by"])
	}
	if typeName == "StandardCodeBackendReadinessView" && fieldName == "remediation" {
		schema["maxItems"] = len(openAPIFieldEnums["StandardCodeBackendReadinessView.remediation"])
	}
	if typeName == "WorkspaceExplorerView" && fieldName == "entries" {
		schema["maxItems"] = workspace.MaxExplorerEntries
	}
	if typeName == "WorkspaceSearchView" && fieldName == "results" {
		schema["maxItems"] = workspace.MaxSearchResults
	}
	if typeName == "RepositoryStateView" && fieldName == "changes" {
		schema["maxItems"] = repository.MaxChangeItems
	}
	if typeName == "RepositoryDiffView" && fieldName == "items" {
		schema["maxItems"] = repository.MaxDiffItems
	}
	if typeName == "RepositoryFileHistoryView" && fieldName == "entries" {
		schema["maxItems"] = repository.MaxFileHistoryEntries
	}
	if typeName == "RepositoryCommitComparisonView" && fieldName == "changes" {
		schema["maxItems"] = repository.MaxCommitChangedFiles
	}
	if typeName == "VerificationPlanCoverageInventoryView" && fieldName == "plans" {
		schema["maxItems"] = verification.MaxPlanInventoryItems
	}
	if typeName == "VerificationPlanCoverageInventoryView" && fieldName == "associations" {
		schema["maxItems"] = verification.MaxCoverageAssociations
	}
	if typeName == "VerificationPlanCoverageView" && fieldName == "items" {
		schema["maxItems"] = verification.MaxPlanItems
	}
	if typeName == "VerificationPlanItemCoverageDetailView" && fieldName == "associations" {
		schema["maxItems"] = verification.MaxCoverageAssociations
	}
	if typeName == "FileEditChangeSetView" && fieldName == "items" {
		schema["maxItems"] = application.MaxFileEditChangeSetItems
	}
	if typeName == "AgentCodeCapabilitiesView" && fieldName == "tools" {
		schema["minItems"] = 7
		schema["maxItems"] = 7
	}
	if typeName == "OperationReceiptHistoryView" && fieldName == "items" {
		schema["maxItems"] = operationreceipt.MaxHistoryItems
	}
	if typeName == "OperatorActionCenterView" && fieldName == "items" {
		schema["maxItems"] = operatoraction.MaxItems
	}
	if typeName == "EvidenceInventoryView" && fieldName == "items" {
		schema["maxItems"] = session.MaxEvidenceInventoryItems
	}
	if typeName == "VerificationEvidenceInventoryView" && fieldName == "items" {
		schema["maxItems"] = verification.MaxInventoryItems
	}
	if typeName == "CodeHandoffVerificationView" && fieldName == "references" {
		schema["maxItems"] = application.MaxCodeHandoffVerifyReferences
	}
	if typeName == "CodeHandoffVerificationCoverageView" && fieldName == "items" {
		schema["maxItems"] = application.MaxCodeHandoffCoverageItemRefs
	}
	if typeName == "CodeHandoffSnapshotReceiptReviewsView" && fieldName == "references" {
		schema["maxItems"] = application.MaxCodeHandoffReceiptReviewRefs
	}
	if typeName == "CodeHandoffView" && fieldName == "pending_actions" {
		schema["maxItems"] = application.MaxCodeHandoffActionReferences
	}
	if typeName == "CodeHandoffView" && fieldName == "report_references" {
		schema["maxItems"] = application.MaxCodeHandoffReportReferences
	}
	if typeName == "CodeHandoffHostCommands" && fieldName == "items" {
		schema["maxItems"] = application.MaxCodeHandoffHostCommands
	}
	if typeName == "StandardCodeDeliveryDiff" && fieldName == "files" {
		schema["maxItems"] = standardcodedelivery.MaxChangedFiles
	}
	if typeName == "StandardCodeDeliveryReport" {
		switch fieldName {
		case "verifications":
			schema["maxItems"] = standardcodedelivery.MaxVerifications
		case "reasons":
			schema["minItems"] = 1
			schema["maxItems"] = standardcodedelivery.MaxReasons
		case "uncovered_items":
			schema["maxItems"] = standardcodedelivery.MaxUncoveredItems
		}
	}
	if typeName == "StandardCodeDeliveryVerification" && fieldName == "artifacts" {
		schema["maxItems"] = standardcodedelivery.MaxArtifactsPerCommand
	}
	if typeName == "StandardCodeDeliveryCheckpoint" && fieldName == "incomplete_reason_sha256" {
		schema["maxItems"] = standardcodedelivery.MaxReasons
	}
	if typeName == "ProviderCredentialListView" && fieldName == "items" {
		schema["minItems"] = 4
		schema["maxItems"] = 4 + modelregistry.MaxCustomProviderDefinitions
	}
	if typeName == "ProviderCredentialStatusView" && fieldName == "provider" {
		schema["maxLength"] = 64
	}
	if (typeName == "ProviderCredentialRequestView" || typeName == "ProviderModelDiscoveryRequest" || typeName == "MCPCredentialRequestView") && fieldName == "secret" {
		schema["writeOnly"] = true
		if typeName == "MCPCredentialRequestView" {
			schema["maxLength"] = credential.MaxSecretBytes
		}
	}
	if typeName == "MCPCredentialRequestView" {
		switch fieldName {
		case "oauth_client_secret":
			schema["writeOnly"], schema["maxLength"] = true, 1024
		case "oauth_callback_port":
			schema["minimum"], schema["maximum"] = 0, 65535
		}
	}
	if typeName == "ModelDiscoveryResult" && fieldName == "models" {
		schema["maxItems"] = 512
	}
	if typeName == "FileEditProposalSourceView" && fieldName == "source_handle" {
		schema["minLength"] = 43
		schema["maxLength"] = 43
		schema["pattern"] = "^[A-Za-z0-9_-]{43}$"
	}
	if typeName == "FileEditProposalRecoveryView" &&
		(fieldName == "original_content" || fieldName == "proposed_content") {
		schema["maxLength"] = fileedit.MaxContentBytes
	}
	if typeName == "FileEditProposalRecoveryView" &&
		(fieldName == "original_sha256" || fieldName == "proposed_sha256") {
		schema["pattern"] = "^[0-9a-f]{64}$"
	}
	if typeName == "FileEditProposalRecoveryView" && fieldName == "original_sha256" {
		schema["pattern"] = "^([0-9a-f]{64}|missing)$"
	}
	if typeName == "FileEditProposalRecoveryView" && fieldName == "current_content_sha256" {
		schema["pattern"] = "^([0-9a-f]{64}|missing)$"
	}
	if (typeName == "FileEditPreviewView" &&
		(fieldName == "original_hash" || fieldName == "proposed_hash")) ||
		(typeName == "FileEditPreviewView" &&
			(fieldName == "destination_original_hash" || fieldName == "destination_proposed_hash")) {
		schema["pattern"] = "^([0-9a-f]{64}|missing)$"
	}
	if typeName == "AgentCodeCapabilitiesView" && fieldName == "generation" {
		schema["minLength"] = 64
		schema["maxLength"] = 64
		schema["pattern"] = "^[0-9a-f]{64}$"
	}
	if (typeName == "VerificationEvidenceItemView" ||
		typeName == "VerificationEvidenceControlView") && fieldName == "summary_sha256" {
		schema["pattern"] = "^[0-9a-f]{64}$"
	}
	if typeName == "ModelAvailabilityView" && fieldName == "generation" {
		schema["minimum"] = 1
	}
	if typeName == "ModelHarnessQualificationView" && fieldName == "model_calls" {
		schema["minimum"] = 0
		schema["maximum"] = 2
	}
	if typeName == "ModelHarnessQualificationView" && fieldName == "synthetic_tool_calls" {
		schema["minimum"] = 0
		schema["maximum"] = llm.MaxProviderToolCalls
	}
	if typeName == "ModelHarnessQualificationView" && fieldName == "duration_ms" {
		schema["minimum"] = 0
		schema["maximum"] = 60_000
	}
	if typeName == "RunWakeWorkerHealthView" && fieldName == "concurrency" {
		schema["minimum"] = application.RunWakeWorkerConcurrency
		schema["maximum"] = application.RunWakeWorkerConcurrency
	}
	if typeName == "RunWakeWorkerHealthView" && fieldName == "max_steps" {
		schema["minimum"] = application.RunWakeWorkerMaxSteps
		schema["maximum"] = application.RunWakeWorkerMaxSteps
	}
	if typeName == "ScheduledJobWorkerHealthView" && fieldName == "concurrency" {
		schema["minimum"] = scheduler.WorkerConcurrency
		schema["maximum"] = scheduler.WorkerConcurrency
	}
	if typeName == "ScheduledJobListView" && fieldName == "items" {
		schema["maxItems"] = 100
	}
	if typeName == "ThreadTurnControlRequestView" && fieldName == "files" {
		schema["maxItems"] = domain.MaxThreadMessageFiles
	}
	if typeName == "ThreadTurnControlRequestView" && fieldName == "mcp_references" {
		schema["maxItems"] = domain.MaxThreadMCPReferences
	}
	if typeName == "ThreadTurnControlRequestView" && fieldName == "content" {
		schema["minLength"], schema["maxLength"] = 0, 16384
		schema["description"] = "Trimmed UTF-8 operator text, at most 16 KiB. Empty only with images, uploaded attachments or MCP selections. Reference-only messages use the display label MCP 内容."
	}
	if (typeName == "ThreadTurnControlRequestView" || typeName == "ThreadTranscriptItemView") && fieldName == "attachments" {
		schema["maxItems"] = domain.MaxThreadMessageAttachments
	}
	if typeName == "FileAttachmentReference" || typeName == "WorkspaceFileAttachment" {
		switch fieldName {
		case "sha256", "text_sha256":
			schema["pattern"] = "^[a-f0-9]{64}$"
		case "byte_size":
			schema["minimum"], schema["maximum"] = 0, fileattachment.MaxBytes
		case "text_bytes":
			schema["minimum"], schema["maximum"] = 0, 64*1024
		case "name":
			schema["minLength"], schema["maxLength"] = 1, 160
		}
	}
	if typeName == "WorkspaceFileUploadRequestView" {
		switch fieldName {
		case "name":
			schema["minLength"], schema["maxLength"] = 1, 160
		case "data_base64":
			schema["minLength"], schema["maxLength"] = 0, base64.StdEncoding.EncodedLen(fileattachment.MaxBytes)
			schema["contentEncoding"] = "base64"
		}
	}
	if (typeName == "ThreadTurnControlRequestView" || typeName == "ThreadTranscriptItemView") && fieldName == "images" {
		schema["maxItems"] = domain.MaxThreadMessageImages
	}
	if (typeName == "ImageReference" || typeName == "WorkspaceImage") && fieldName == "sha256" {
		schema["pattern"] = "^[a-f0-9]{64}$"
	}
	if typeName == "WorkspaceImage" {
		switch fieldName {
		case "byte_size":
			schema["minimum"], schema["maximum"] = 1, imageattachment.MaxBytes
		case "width", "height":
			schema["minimum"], schema["maximum"] = 1, imageattachment.MaxDimension
		case "name":
			schema["maxLength"] = 160
		}
	}
	if typeName == "WorkspaceImageUploadRequestView" {
		switch fieldName {
		case "name":
			schema["maxLength"] = 160
		case "data_base64":
			schema["minLength"], schema["maxLength"] = 4, base64.StdEncoding.EncodedLen(imageattachment.MaxBytes)
			schema["contentEncoding"] = "base64"
		}
	}
	if typeName == "WorkspaceFileReference" {
		if fieldName == "path" {
			schema["minLength"] = 1
			schema["maxLength"] = workspace.MaxExplorerPathRunes
		}
		if fieldName == "expected_sha256" {
			schema["pattern"] = "^[0-9a-f]{64}$"
		}
	}
	if typeName == "HostCommandSavedStreamView" {
		switch fieldName {
		case "redacted":
			schema["const"] = true
		case "utf8_bytes":
			schema["minimum"] = 0
			schema["maximum"] = runner.MaxHostCommandSavedOutputBytes
		case "text":
			schema["maxLength"] = runner.MaxHostCommandSavedOutputBytes
			schema["description"] = "Saved redacted UTF-8 text. Both streams share a 16 KiB byte budget; utf8_bytes describes saved text, not the raw captured receipt byte count."
		}
	}
	if typeName == "apiErrorView" && fieldName == "turn_failure" {
		schema["description"] = "Exact identity of an already sealed failure; present only with turn_failed=true. This reference grants no retry or execution authority."
	}
	if typeName == "ThreadTurnFailureReferenceView" && fieldName == "event_sequence" {
		schema["minimum"] = 1
	}
	if typeName == "apiErrorView" && fieldName == "message_queued" {
		schema["const"] = false
		schema["description"] = "Only present as false after the identical Thread message intent is durably rejected before enqueue. A corrected submission must use a new turn idempotency key. Omission does not establish whether a message was accepted."
	}
	if typeName == "apiErrorView" && fieldName == "turn_failed" {
		schema["const"] = true
		schema["description"] = "Only present as true after the exact product Thread turn is durably closed as failed. The original input and completed tool evidence remain in the conversation. A new request must use a new operation key; absence does not establish the previous outcome."
	}
	if typeName == "apiErrorView" && fieldName == "operation_key_invalidated" {
		schema["const"] = true
		schema["description"] = "Only present as true when a pending Standard Code preset's immutable Thread preference binding is obsolete and the original operation key cannot apply. Explicitly review a new configuration attempt with a new key and fresh workspace trust preflight. Omission does not establish whether the operation succeeded."
	}
	if typeName == "ScheduledJobSnapshot" &&
		(fieldName == "rounds" || fieldName == "notifications") {
		schema["maxItems"] = 100
	}
	if typeName == "DebugQueryResult" && fieldName == "items" {
		schema["maxItems"] = 100
	}
	if typeName == "ThreadActivityDetailView" && fieldName == "tools" {
		schema["minItems"] = 1
		schema["maxItems"] = 1
	}
	if typeName == "ThreadActivityCommandGroupView" && fieldName == "commands" {
		schema["maxItems"] = application.MaxThreadActivityCommands
	}
	if typeName == "ThreadActivityWebSearchDetailView" && fieldName == "sources" {
		schema["maxItems"] = application.MaxThreadActivitySearchSources
	}
	if (typeName == "ThreadActivityMCPDetailView" && fieldName == "arguments") ||
		(typeName == "ThreadActivityJSONSummaryView" && fieldName == "fields") {
		schema["maxItems"] = application.MaxThreadActivityMCPFields
	}
	if typeName == "ThreadActivityMCPDetailView" && fieldName == "contents" {
		schema["maxItems"] = application.MaxThreadActivityMCPContents
	}
	if typeName == "ThreadActivityCommandDetailView" {
		switch fieldName {
		case "command":
			schema["maxLength"] = application.MaxThreadActivityCommandRunes
		case "working_directory":
			schema["maxLength"] = 1024
		case "stdout_preview", "stderr_preview":
			schema["maxLength"] = application.MaxThreadActivityOutputRunes
		case "artifacts":
			schema["maxItems"] = 2
		}
	}
	if typeName == "ThreadActivityArtifactView" && fieldName == "content" {
		schema["maxLength"] = application.MaxThreadActivityArtifactBytes
	}
	if typeName == "ThreadActivityFileEditDetailView" && fieldName == "edit_id" {
		schema["maxLength"] = 256
	}
	if typeName == "ThreadActivitySummaryView" && fieldName == "command" {
		schema["maxLength"] = application.MaxThreadActivityCommandRunes
	}
}

var openAPIFieldEnums = map[string][]string{
	"SpecialistContinuationRequestView.version":                 {application.SpecialistContinuationVersion},
	"SpecialistContinuationView.version":                        {application.SpecialistContinuationVersion},
	"SpecialistContinuationView.status":                         {"running", "completed", "failed", "cancelled", "abandoned"},
	"SpecialistContinuationView.summary_status":                 {"not_requested", "queued", "completed", "failed", "cancelled"},
	"SandboxEnvironmentControlRequestView.version":              {application.SandboxEnvironmentVersion},
	"SandboxEnvironmentView.protocol_version":                   {application.SandboxEnvironmentVersion},
	"SandboxEnvironmentSettingsView.default_backend":            {"local", "docker", "sbx"},
	"SandboxEnvironmentBackendView.backend":                     {"local", "docker", "sbx"},
	"SandboxEnvironmentBackendView.status":                      {"ready", "disabled", "unavailable", "configuration_required", "not_checked"},
	"SandboxEnvironmentView.probe_status":                       {"checked", "not_checked"},
	"StandardCodeDeliveryVerification.executable_identity_kind": {runner.CommandRuntimeExecutableTemplatePathSHA256},
	"CommandRuntimeJobSnapshot.executable_identity_kind":        {runner.CommandRuntimeExecutableTemplatePathSHA256},

	"WorkspaceImageUploadRequestView.version":     {WorkspaceImageUploadVersion},
	"WorkspaceFileUploadRequestView.version":      {WorkspaceFileUploadVersion},
	"WorkspaceFileAttachment.readability":         {"text", "partial_text", "stored_only"},
	"FileAttachmentObservation.state":             {"not_received", "stored"},
	"WorkspaceImageUploadRequestView.mime_type":   {"image/png", "image/jpeg", "image/webp"},
	"WorkspaceImage.mime_type":                    {"image/png", "image/jpeg", "image/webp"},
	"VisionCapability.state":                      {"supported", "unsupported", "unknown"},
	"VisionCapability.source":                     {"operator_declared", "provider_metadata", "unknown", "adapter_unsupported"},
	"StandardCodeDeliveryOutputSourceView.status": {"available", "metadata_only"},
	"StandardCodeDeliveryOutputSourceView.reason": {"activity_source_unavailable", "output_not_public", "artifact_binding_mismatch"},
	"DockerSandboxReadinessView.protocol_version": {sandbox.DockerReadinessProtocolVersion},
	"DockerSandboxReadinessView.status": {sandbox.DockerReadinessStatusReady,
		sandbox.DockerReadinessStatusDisabled, sandbox.DockerReadinessStatusUnavailable},
	"DockerSandboxReadinessView.network_mode": {"disabled"},
	"DockerSandboxAdmissionView.decision": {domain.DockerSandboxAdmissionAuthorized,
		domain.DockerSandboxAdmissionDenied},
	"DockerSandboxAdmissionView.permission_mode": {
		string(domain.RunExecutionPermissionApproval),
		string(domain.RunExecutionPermissionFullAccess),
		string(domain.RunExecutionPermissionDebug),
	},
	"DockerSandboxStatusView.state": {"admitted", "launched", "terminal"},
	"DockerSandboxStatusView.outcome": {domain.DockerSandboxOutcomeSucceeded,
		domain.DockerSandboxOutcomeFailed, domain.DockerSandboxOutcomeTimedOut,
		domain.DockerSandboxOutcomeCancelled},
	"DockerSandboxCancellationView.reason_code":  {domain.DockerSandboxReasonCancelled},
	"EventView.version":                          {events.EnvelopeVersion},
	"RunActivityView.version":                    {runactivity.ProtocolVersion},
	"RunActivityItemView.kind":                   {string(runactivity.KindHarnessStatus), string(runactivity.KindModelUpdate), string(runactivity.KindOperatorInput), string(runactivity.KindModelCall), string(runactivity.KindToolCall), string(runactivity.KindApproval), string(runactivity.KindFileChange), string(runactivity.KindPlan), string(runactivity.KindDependency), string(runactivity.KindBrowser)},
	"RunActivityItemView.source":                 {string(runactivity.SourceHarness), string(runactivity.SourceModel), string(runactivity.SourceOperator)},
	"ThreadTranscriptItemView.version":           {threadtranscript.ProtocolVersion},
	"ThreadTranscriptItemView.activity_type":     {string(threadtranscript.TypeMessage), string(threadtranscript.TypeSearch), string(threadtranscript.TypeRead), string(threadtranscript.TypeEdit), string(threadtranscript.TypeExecute), string(threadtranscript.TypeVerify), string(threadtranscript.TypeApproval), string(threadtranscript.TypeCheckpoint), string(threadtranscript.TypeDelivery)},
	"ThreadTranscriptItemView.stage":             {string(threadtranscript.StageStarted), string(threadtranscript.StageArgumentsReady), string(threadtranscript.StageRunning), string(threadtranscript.StageResult), string(threadtranscript.StageBlocked)},
	"ThreadTranscriptItemView.kind":              {string(runactivity.KindHarnessStatus), string(runactivity.KindModelUpdate), string(runactivity.KindOperatorInput), string(runactivity.KindModelCall), string(runactivity.KindToolCall), string(runactivity.KindApproval), string(runactivity.KindFileChange), string(runactivity.KindPlan), string(runactivity.KindDependency), string(runactivity.KindBrowser)},
	"ThreadTranscriptItemView.source":            {string(runactivity.SourceHarness), string(runactivity.SourceModel), string(runactivity.SourceOperator)},
	"ThreadActivityDetailView.version":           {application.ThreadActivityDetailProtocolVersion},
	"ThreadActivityTypedDetailView.kind":         {"command", "web_search", "web_fetch", "file_read", "file_edit", "verification", "mcp", "browser"},
	"ThreadActivityBoundaryView.authorization":   {"policy_checked", "pending", "denied"},
	"ThreadActivityJSONFieldSummaryView.type":    {"null", "boolean", "number", "string", "array", "object", "unknown"},
	"ThreadActivityJSONSummaryView.type":         {"unavailable", "text", "null", "boolean", "number", "string", "array", "object", "unknown"},
	"ThreadActivityArtifactView.version":         {application.ThreadActivityArtifactProtocolVersion},
	"ThreadActivityArtifactView.stream":          {"stdout", "stderr"},
	"ThreadActivityArtifactReferenceView.stream": {"stdout", "stderr"},
	"ThreadActivityArtifactView.mime":            {"text/plain; charset=utf-8"},
	"ThreadActivityArtifactReferenceView.mime":   {"text/plain; charset=utf-8"},
	"ThreadActivitySummaryView.version":          {application.ThreadActivitySummaryProtocolVersion},
	"ThreadActivitySummaryView.status":           {string(domain.SupervisorToolPending), string(domain.SupervisorToolCompleted), string(domain.SupervisorToolDenied), string(domain.SupervisorToolFailed), string(runner.CommandRuntimeJobPrepared), string(runner.CommandRuntimeJobRunning), string(runner.CommandRuntimeJobStopping), string(runner.CommandRuntimeJobCompleted), string(runner.CommandRuntimeJobFailed), string(runner.CommandRuntimeJobTimedOut), string(runner.CommandRuntimeJobCancelled), string(runner.CommandRuntimeJobKilled), string(runner.CommandRuntimeJobInterrupted)},
	"ThreadActivityToolDetailView.name": {string(toolgateway.CommandRuntimeTool),
		string(toolgateway.WorkspaceListTool), string(toolgateway.WorkspaceReadTool),
		string(toolgateway.WorkspaceGlobTool), string(toolgateway.WorkspaceGrepTool),
		string(toolgateway.WorkspaceChangeTool), string(toolgateway.WorkspaceApplyTool),
		string(toolgateway.WorkspaceDeleteTool), string(toolgateway.WebSearchTool),
		string(toolgateway.WebFetchTool), string(toolgateway.WebCitationTool),
		string(toolgateway.MCPToolSearchTool), string(toolgateway.MCPToolCallTool), string(toolgateway.CodeWorkspaceSymbolsTool),
		string(toolgateway.CodeDocumentSymbolsTool), string(toolgateway.CodeDefinitionTool),
		string(toolgateway.CodeReferencesTool), string(toolgateway.CodeImplementationTool),
		string(toolgateway.CodeHoverTool), string(toolgateway.CodeSignatureHelpTool),
		string(toolgateway.CodeDiagnosticsTool), string(toolgateway.CodeCallHierarchyTool),
		string(toolgateway.CodeTypeHierarchyTool), string(toolgateway.GitHubEvidenceListTool),
		string(toolgateway.GitHubEvidenceReadTool), string(toolgateway.BrowserStatusTool),
		string(toolgateway.BrowserNavigateTool), string(toolgateway.BrowserSnapshotTool),
		string(toolgateway.BrowserClickTool), string(toolgateway.BrowserTypeTool),
		string(toolgateway.BrowserScreenshotTool)},
	"ThreadActivityToolDetailView.agent_role":                  {string(domain.AgentRoleRoot), string(domain.AgentRoleSpecialist), "unknown"},
	"ThreadActivityToolDetailView.status":                      {string(domain.SupervisorToolPending), string(domain.SupervisorToolCompleted), string(domain.SupervisorToolDenied), string(domain.SupervisorToolFailed)},
	"ThreadActivityCommandDetailView.execution_environment":    {"Workspace Sandbox", "Host · Full Access", "Legacy execution boundary"},
	"ThreadActivityCommandDetailView.network":                  {"disabled"},
	"ThreadActivityCommandDetailView.status":                   {string(domain.SupervisorToolPending), string(domain.SupervisorToolCompleted), string(domain.SupervisorToolDenied), string(domain.SupervisorToolFailed), string(runner.CommandRuntimeJobPrepared), string(runner.CommandRuntimeJobRunning), string(runner.CommandRuntimeJobStopping), string(runner.CommandRuntimeJobCompleted), string(runner.CommandRuntimeJobFailed), string(runner.CommandRuntimeJobTimedOut), string(runner.CommandRuntimeJobCancelled), string(runner.CommandRuntimeJobKilled), string(runner.CommandRuntimeJobInterrupted)},
	"IndexView.api_version":                                    {Version},
	"ProjectActionsView.protocol_version":                      {application.ProjectActionsProtocolVersion},
	"ProjectPreparationView.protocol_version":                  {application.ProjectPreparationProtocolVersion},
	"ProjectPreparationOperationView.protocol_version":         {application.ProjectPreparationProtocolVersion},
	"ProjectPreparationRequestView.version":                    {application.ProjectPreparationProtocolVersion},
	"ProjectPreparationCancelRequestView.version":              {application.ProjectPreparationProtocolVersion},
	"ProjectActionResultView.protocol_version":                 {application.ProjectActionsProtocolVersion},
	"ProjectActionRequestView.version":                         {application.ProjectActionsProtocolVersion},
	"ProjectActionView.slot":                                   {"test", "format"},
	"ProjectActionResultView.slot":                             {"test", "format"},
	"ProjectActionView.target_kind":                            {"directory", "go_file"},
	"ProjectActionJobView.state":                               {string(runner.CommandRuntimeJobPrepared), string(runner.CommandRuntimeJobRunning), string(runner.CommandRuntimeJobStopping), string(runner.CommandRuntimeJobCompleted), string(runner.CommandRuntimeJobFailed), string(runner.CommandRuntimeJobTimedOut), string(runner.CommandRuntimeJobCancelled), string(runner.CommandRuntimeJobKilled), string(runner.CommandRuntimeJobInterrupted)},
	"HealthView.status":                                        {"ok"},
	"HealthView.api_version":                                   {Version},
	"RuntimeCapabilitiesView.protocol_version":                 {RuntimeCapabilitiesProtocolVersion},
	"WorkspaceImportRequestView.version":                       {WorkspaceImportProtocolVersion},
	"WorkspaceImportView.protocol_version":                     {WorkspaceImportProtocolVersion},
	"StandardCodePresetControlRequestView.version":             {domain.StandardCodePresetProtocolVersion},
	"StandardCodePresetControlRequestView.backend_intent":      {string(domain.StandardCodeBackendAuto), string(domain.StandardCodeBackendLocal), string(domain.StandardCodeBackendDocker), string(domain.StandardCodeBackendSBX)},
	"StandardCodePresetControlView.protocol_version":           {domain.StandardCodePresetProtocolVersion},
	"StandardCodePresetControlView.status":                     {string(application.StandardCodeResultBlocked), string(application.StandardCodeResultWaitingForPause), string(application.StandardCodeResultConfigured)},
	"StandardCodePresetControlView.action":                     {string(domain.StandardCodePresetConfigure), string(domain.StandardCodePresetPauseAndConfigure)},
	"StandardCodePresetControlView.backend_intent":             {string(domain.StandardCodeBackendAuto), string(domain.StandardCodeBackendLocal), string(domain.StandardCodeBackendDocker), string(domain.StandardCodeBackendSBX)},
	"StandardCodePresetControlView.selected_backend":           {string(domain.StandardCodeSelectedLocal), string(domain.StandardCodeSelectedDocker), string(domain.StandardCodeSelectedSBX)},
	"StandardCodePresetControlView.selection_reason":           {string(domain.StandardCodeReasonAutoLocalReady), string(domain.StandardCodeReasonExplicitLocal), string(domain.StandardCodeReasonExplicitDocker), string(domain.StandardCodeReasonExplicitSBX)},
	"StandardCodePresetControlView.blocked_by":                 {string(application.CapabilityBlockerRunNotQuiescent), string(application.CapabilityBlockerExecutionLeaseActive), string(application.CapabilityBlockerStartupGateClosed), string(application.CapabilityBlockerCapabilityUnimplemented), string(application.CapabilityBlockerSurfaceMismatch), string(application.CapabilityBlockerProfileMismatch), string(application.CapabilityBlockerPermissionMismatch), string(application.CapabilityBlockerWorkspaceUntrusted), string(application.CapabilityBlockerSandboxUnproven), string(application.CapabilityBlockerDockerUnavailable), string(application.CapabilityBlockerBackendNotReady), string(application.CapabilityBlockerSBXUnavailable)},
	"StandardCodePresetControlView.next_steps":                 {string(application.StandardCodeNextConfirmWorkspaceTrust), string(application.StandardCodeNextPauseAndConfigure), string(application.StandardCodeNextWaitForQuiescence), string(application.StandardCodeNextSelectDocker), string(application.StandardCodeNextSelectSBX), string(application.StandardCodeNextSelectAsk), string(application.StandardCodeNextRetryReadiness), string(application.StandardCodeNextCreateNewRun)},
	"StandardCodePresetControlView.network":                    {"disabled"},
	"StandardCodePresetControlView.credentials":                {"none"},
	"StandardCodeBackendReadinessView.backend":                 {string(domain.StandardCodeSelectedLocal), string(domain.StandardCodeSelectedDocker), string(domain.StandardCodeSelectedSBX)},
	"StandardCodeBackendReadinessView.blocked_by":              {string(application.CapabilityBlockerRunNotQuiescent), string(application.CapabilityBlockerExecutionLeaseActive), string(application.CapabilityBlockerStartupGateClosed), string(application.CapabilityBlockerCapabilityUnimplemented), string(application.CapabilityBlockerSurfaceMismatch), string(application.CapabilityBlockerProfileMismatch), string(application.CapabilityBlockerPermissionMismatch), string(application.CapabilityBlockerWorkspaceUntrusted), string(application.CapabilityBlockerSandboxUnproven), string(application.CapabilityBlockerDockerUnavailable), string(application.CapabilityBlockerBackendNotReady), string(application.CapabilityBlockerSBXUnavailable)},
	"StandardCodeBackendReadinessView.remediation":             {string(application.CapabilityRemediationPauseRun), string(application.CapabilityRemediationCreateNewRun), string(application.CapabilityRemediationWaitForExecutionLease), string(application.CapabilityRemediationRestartWithStartupGate), string(application.CapabilityRemediationUpgradeApplication), string(application.CapabilityRemediationSelectRequiredSurface), string(application.CapabilityRemediationSelectRequiredProfile), string(application.CapabilityRemediationSelectRequiredPermission), string(application.CapabilityRemediationTrustWorkspace), string(application.CapabilityRemediationVerifySandbox), string(application.CapabilityRemediationInstallOrStartDocker), string(application.CapabilityRemediationRetryBackendReadiness), string(application.CapabilityRemediationInstallOrStartSBX)},
	"RunWakeWorkerHealthView.protocol_version":                 {application.RunWakeWorkerHealthProtocolVersion},
	"RunWakeWorkerHealthView.state":                            {"disabled", string(application.RunWakeWorkerReady), string(application.RunWakeWorkerRunning), string(application.RunWakeWorkerDraining), string(application.RunWakeWorkerStopped)},
	"ScheduledJobWorkerHealthView.protocol_version":            {scheduler.WorkerHealthProtocolVersion},
	"ScheduledJobWorkerHealthView.state":                       {"disabled", string(scheduler.WorkerReady), string(scheduler.WorkerRunning), string(scheduler.WorkerDraining), string(scheduler.WorkerStopped)},
	"ScheduledJobScheduleRequestView.kind":                     {string(domain.ScheduledJobOnce), string(domain.ScheduledJobPeriodic)},
	"ScheduledJobScheduleRequestView.misfire_policy":           {string(domain.ScheduledJobMisfireRunOnce), string(domain.ScheduledJobMisfireSkip)},
	"ScheduledJobCreateRequestView.version":                    {domain.ScheduledJobProtocolVersion},
	"ScheduledJobCreateRequestView.notification":               {string(domain.ScheduledJobNotifySilent), string(domain.ScheduledJobNotifyChange), string(domain.ScheduledJobNotifyFailure), string(domain.ScheduledJobNotifyAll)},
	"ScheduledJobCreateRequestView.execution_mode":             {string(domain.ScheduledJobReadOnly), string(domain.ScheduledJobApprovedRepair)},
	"ScheduledJobTransitionRequestView.version":                {domain.ScheduledJobControlProtocolVersion},
	"ScheduledJobObservationRequestView.version":               {domain.ScheduledJobControlProtocolVersion},
	"ScheduledJobWorkerHealthView.selection_scope":             {"confirmed_read_only", "all_jobs"},
	"ScheduledJobControlView.protocol_version":                 {domain.ScheduledJobControlProtocolVersion},
	"ScheduledJobControlView.action":                           {string(domain.ScheduledJobCreate), string(domain.ScheduledJobPause), string(domain.ScheduledJobResume), string(domain.ScheduledJobCancel)},
	"ScheduledJobListView.protocol_version":                    {domain.ScheduledJobProtocolVersion},
	"ScheduledJobDetailView.protocol_version":                  {domain.ScheduledJobProtocolVersion},
	"ScheduledJobSchedule.kind":                                {string(domain.ScheduledJobOnce), string(domain.ScheduledJobPeriodic)},
	"ScheduledJobSchedule.misfire_policy":                      {string(domain.ScheduledJobMisfireRunOnce), string(domain.ScheduledJobMisfireSkip)},
	"ScheduledJobSpec.version":                                 {domain.ScheduledJobProtocolVersion},
	"ScheduledJobSpec.notification":                            {string(domain.ScheduledJobNotifySilent), string(domain.ScheduledJobNotifyChange), string(domain.ScheduledJobNotifyFailure), string(domain.ScheduledJobNotifyAll)},
	"ScheduledJobSpec.execution_mode":                          {string(domain.ScheduledJobReadOnly), string(domain.ScheduledJobApprovedRepair)},
	"ScheduledJob.status":                                      {string(domain.ScheduledJobActive), string(domain.ScheduledJobPaused), string(domain.ScheduledJobCompleted), string(domain.ScheduledJobFailed), string(domain.ScheduledJobCancelled), string(domain.ScheduledJobExhausted)},
	"ScheduledJob.stop_reason":                                 {string(domain.ScheduledJobStopNone), string(domain.ScheduledJobStopOnceCompleted), string(domain.ScheduledJobStopTargetTerminal), string(domain.ScheduledJobStopDeadline), string(domain.ScheduledJobStopRoundBudget), string(domain.ScheduledJobStopModelBudget), string(domain.ScheduledJobStopElapsedBudget), string(domain.ScheduledJobStopRetryExhausted), string(domain.ScheduledJobStopAuthorization), string(domain.ScheduledJobStopCancelled)},
	"ScheduledJobAuthorization.protocol_version":               {domain.ScheduledJobAuthProtocolVersion},
	"ScheduledJobRound.protocol_version":                       {domain.ScheduledJobRoundProtocolVersion},
	"ScheduledJobRound.status":                                 {string(domain.ScheduledJobRoundClaimed), string(domain.ScheduledJobRoundRetryWait), string(domain.ScheduledJobRoundUnchanged), string(domain.ScheduledJobRoundCompleted), string(domain.ScheduledJobRoundFailed), string(domain.ScheduledJobRoundSkipped)},
	"ScheduledJobNotification.kind":                            {"change", "failure", "recovery", "completed"},
	"DoctorSnapshot.protocol_version":                          {application.DoctorSnapshotProtocolVersion},
	"DiagnosticCheck.status":                                   {"ready", "degraded", "not_configured", "not_probed"},
	"DebugQueryResult.protocol_version":                        {application.DebugQueryProtocolVersion},
	"DebugTimelineItem.category":                               {"model", "tool", "policy", "application", "infrastructure"},
	"DebugTimelineItem.evidence":                               {"persisted_event"},
	"DebugTimelineItem.payload_state":                          {"withheld"},
	"DiagnosticBundle.protocol_version":                        {application.DiagnosticBundleProtocolVersion},
	"ModelAvailabilityView.protocol_version":                   {modelregistry.ProtocolVersion},
	"AvailableModelRouteCollectionView.protocol_version":       {application.ModelRouteCatalogProtocolVersion},
	"AvailableModelRouteView.credential_status":                {"not_required", "configured", "not_configured", "invalid_configuration", "disabled", "unavailable"},
	"AvailableModelRouteView.qualification_status":             {"unavailable", modelregistry.QualificationStatusNotConfigured, modelregistry.QualificationStatusAvailable, modelregistry.QualificationStatusProtocolMismatch, modelregistry.QualificationStatusAuthFailed, modelregistry.QualificationStatusNetworkFailed, modelregistry.QualificationStatusRateLimit, modelregistry.QualificationStatusCapacity, modelregistry.QualificationStatusModelUnsupported, modelregistry.QualificationStatusResponseIncomplete, llm.HarnessQualificationTrusted, llm.HarnessQualificationRequired, llm.HarnessQualificationVerified},
	"AvailableModelRouteView.unavailable_reason":               {"", "provider_disabled", "credential_not_configured", "invalid_configuration", "provider_unavailable", "harness_qualification_required", modelregistry.QualificationStatusNotConfigured, modelregistry.QualificationStatusProtocolMismatch, modelregistry.QualificationStatusAuthFailed, modelregistry.QualificationStatusNetworkFailed, modelregistry.QualificationStatusRateLimit, modelregistry.QualificationStatusCapacity, modelregistry.QualificationStatusModelUnsupported, modelregistry.QualificationStatusResponseIncomplete},
	"ThreadModelRouteView.protocol_version":                    {domain.ThreadModelRouteProtocolVersion},
	"ThreadModelRouteView.source":                              {"thread_preference", "default", "active_run"},
	"ThreadModelRouteView.applies_to":                          {"next_run", "current_and_next"},
	"ThreadModelRouteControlRequestView.version":               {domain.ThreadModelRouteControlProtocolVersion},
	"ThreadModelRouteControlRequestView.action":                {string(domain.ThreadModelRouteSelect), string(domain.ThreadModelRouteReset)},
	"ProviderAvailabilityView.transport":                       {llm.HarnessTransportMock, llm.HarnessTransportAnthropicMessages, llm.HarnessTransportOpenAIChatCompletions, llm.HarnessTransportOpenAIResponses, llm.HarnessTransportOllamaChat},
	"ProviderAvailabilityView.search_mode":                     {modelregistry.ProviderSearchModeDisabled, modelregistry.ProviderSearchModeAuto, modelregistry.ProviderSearchModeWeb, modelregistry.ProviderSearchModeSearXNG, modelregistry.ProviderSearchModeProviderNative},
	"ProviderAvailabilityView.native_web_search_capability":    {modelregistry.NativeWebSearchUnsupported, modelregistry.NativeWebSearchDeclaredUnverified},
	"ProviderSearchReadinessView.protocol_version":             {application.ProviderSearchReadinessProtocolVersion},
	"SearchDiagnostics.protocol_version":                       {application.SearchDiagnosticsProtocolVersion},
	"searchDiagnosticsRequest.version":                         {application.SearchDiagnosticsProtocolVersion},
	"SearchDiagnostics.state":                                  {"succeeded", "failed"},
	"SearchDiagnostics.code":                                   {"none", "network", "rate_limited", "access_challenge", "authentication", "provider_rejected", "tool_unsupported", "no_usable_results", "invalid_response", "not_configured", "not_authorized", "timeout", "configuration_changed"},
	"ProviderSearchReadinessView.search_policy":                {webevidence.SearchPolicyDisabled, webevidence.SearchPolicyAuto, webevidence.SearchPolicyWeb, webevidence.SearchPolicySearXNG, webevidence.SearchPolicyProviderNative},
	"ProviderSearchReadinessView.state":                        {application.ProviderSearchStateNetworkDisabled, application.ProviderSearchStateMissingAllowlist, application.ProviderSearchStateProviderUnqualified, application.ProviderSearchStateProviderUnavailable, application.ProviderSearchStateReady},
	"ProviderSearchReadinessView.reason":                       {application.ProviderSearchReasonRunNetworkDisabled, application.ProviderSearchReasonEndpointNotAllowlisted, application.ProviderSearchReasonQualificationRequired, application.ProviderSearchReasonQualificationFailed, application.ProviderSearchReasonNoActiveRun, application.ProviderSearchReasonModelProviderUnavailable, application.ProviderSearchReasonPolicyDisabled, application.ProviderSearchReasonBackendNotConfigured, application.ProviderSearchReasonConfigurationInvalid, application.ProviderSearchReasonBackendReady},
	"ProviderSearchReadinessView.remediation":                  {application.ProviderSearchRemediationEnableNetwork, application.ProviderSearchRemediationAddRequiredTarget, application.ProviderSearchRemediationQualifyProvider, application.ProviderSearchRemediationCreateSuccessor, application.ProviderSearchRemediationConfigureProvider, application.ProviderSearchRemediationEnablePolicy, application.ProviderSearchRemediationRepairConfiguration, application.ProviderSearchRemediationNone},
	"ProviderSearchReadinessView.network_mode":                 {"disabled", "allowlist"},
	"ProviderDiagnosticRequestView.version":                    {modelregistry.DiagnosticProtocolVersion},
	"ModelHarnessQualificationRequestView.version":             {modelregistry.HarnessQualificationProtocolVersion},
	"ModelHarnessAvailabilityView.protocol_version":            {llm.ModelHarnessProtocolVersion},
	"ModelHarnessAvailabilityView.transport_protocol":          {llm.HarnessTransportMock, llm.HarnessTransportAnthropicMessages, llm.HarnessTransportOpenAIChatCompletions, llm.HarnessTransportOpenAIResponses, llm.HarnessTransportOllamaChat, llm.HarnessTransportProviderContract},
	"ProviderDefinition.version":                               {modelregistry.ProviderDefinitionVersion},
	"ProviderModelDiscoveryRequest.version":                    {modelregistry.ModelDiscoveryVersion},
	"ProviderModelDiscoveryRequest.transport":                  {modelregistry.ProviderTransportOpenAIChatCompletions, modelregistry.ProviderTransportOpenAIResponses, modelregistry.ProviderTransportAnthropicMessages},
	"ModelDiscoveryResult.version":                             {modelregistry.ModelDiscoveryVersion},
	"ModelDiscoveryResult.source":                              {"provider_api"},
	"ProviderDefinition.transport":                             {modelregistry.ProviderTransportOpenAIChatCompletions, modelregistry.ProviderTransportOpenAIResponses, modelregistry.ProviderTransportAnthropicMessages},
	"ProviderDefinition.search_mode":                           {modelregistry.ProviderSearchModeDisabled, modelregistry.ProviderSearchModeAuto, modelregistry.ProviderSearchModeWeb, modelregistry.ProviderSearchModeSearXNG, modelregistry.ProviderSearchModeProviderNative},
	"ProviderDefinition.native_web_search_capability":          {modelregistry.NativeWebSearchUnsupported, modelregistry.NativeWebSearchDeclaredUnverified},
	"ModelHarnessAvailabilityView.tool_strategy":               {llm.HarnessToolStrategyNative, llm.HarnessToolStrategyNone},
	"ModelHarnessAvailabilityView.json_strategy":               {llm.HarnessJSONStrategyNative, llm.HarnessJSONStrategyPrompt, llm.HarnessJSONStrategyNone},
	"ModelHarnessAvailabilityView.qualification_status":        {llm.HarnessQualificationTrusted, llm.HarnessQualificationRequired, llm.HarnessQualificationVerified},
	"ModelHarnessQualificationView.protocol_version":           {modelregistry.HarnessQualificationProtocolVersion},
	"ModelHarnessQualificationView.status":                     {modelregistry.HarnessDiagnosticQualified, modelregistry.HarnessDiagnosticIncompatible, modelregistry.HarnessDiagnosticUnreachable},
	"ModelHarnessQualificationView.outcome":                    {string(llm.OutcomeSuccess), string(llm.OutcomeRetryable), string(llm.OutcomeRateLimited), string(llm.OutcomeInvalidResponse), string(llm.OutcomeCancelled), string(llm.OutcomePermanent)},
	"ModelHarnessQualificationView.failure_reason":             {string(llm.ProviderFailureNone), string(llm.ProviderFailureNotConfigured), string(llm.ProviderFailureAuthentication), string(llm.ProviderFailureNetwork), string(llm.ProviderFailureRateLimit), string(llm.ProviderFailureCapacity), string(llm.ProviderFailureModelNotFound), string(llm.ProviderFailureProtocolIncompatible), string(llm.ProviderFailureContextLimit), string(llm.ProviderFailureOutputLimit), string(llm.ProviderFailurePaused), string(llm.ProviderFailureRefusal)},
	"ProviderDiagnosticView.protocol_version":                  {modelregistry.DiagnosticProtocolVersion},
	"ProviderDiagnosticView.status":                            {modelregistry.DiagnosticReachable, modelregistry.DiagnosticUnreachable},
	"ProviderDiagnosticView.outcome":                           {string(llm.OutcomeSuccess), string(llm.OutcomeRetryable), string(llm.OutcomeRateLimited), string(llm.OutcomeInvalidResponse), string(llm.OutcomeCancelled), string(llm.OutcomePermanent)},
	"ProviderDiagnosticView.failure_reason":                    {string(llm.ProviderFailureNone), string(llm.ProviderFailureNotConfigured), string(llm.ProviderFailureAuthentication), string(llm.ProviderFailureNetwork), string(llm.ProviderFailureRateLimit), string(llm.ProviderFailureCapacity), string(llm.ProviderFailureModelNotFound), string(llm.ProviderFailureProtocolIncompatible), string(llm.ProviderFailureContextLimit), string(llm.ProviderFailureOutputLimit), string(llm.ProviderFailurePaused), string(llm.ProviderFailureRefusal)},
	"ProviderDiagnosticView.qualification_status":              {modelregistry.QualificationStatusNotConfigured, modelregistry.QualificationStatusAvailable, modelregistry.QualificationStatusProtocolMismatch, modelregistry.QualificationStatusAuthFailed, modelregistry.QualificationStatusNetworkFailed, modelregistry.QualificationStatusRateLimit, modelregistry.QualificationStatusCapacity, modelregistry.QualificationStatusModelUnsupported, modelregistry.QualificationStatusResponseIncomplete},
	"ModelHarnessQualificationView.qualification_status":       {modelregistry.QualificationStatusNotConfigured, modelregistry.QualificationStatusAvailable, modelregistry.QualificationStatusProtocolMismatch, modelregistry.QualificationStatusAuthFailed, modelregistry.QualificationStatusNetworkFailed, modelregistry.QualificationStatusRateLimit, modelregistry.QualificationStatusCapacity, modelregistry.QualificationStatusModelUnsupported, modelregistry.QualificationStatusResponseIncomplete},
	"ModelHarnessAvailabilityView.latest_qualification_status": {"", modelregistry.QualificationStatusNotConfigured, modelregistry.QualificationStatusAvailable, modelregistry.QualificationStatusProtocolMismatch, modelregistry.QualificationStatusAuthFailed, modelregistry.QualificationStatusNetworkFailed, modelregistry.QualificationStatusRateLimit, modelregistry.QualificationStatusCapacity, modelregistry.QualificationStatusModelUnsupported, modelregistry.QualificationStatusResponseIncomplete},
	"ModelRouteControlRequestView.version":                     {modelregistry.RouteControlProtocolVersion},
	"ProviderCredentialListView.protocol_version":              {credential.ProtocolVersion},
	"ProviderCredentialStatusView.protocol_version":            {credential.ProtocolVersion},
	"ProviderCredentialRequestView.version":                    {credential.ProtocolVersion},
	"ProviderCredentialRequestView.action":                     {string(application.ProviderCredentialSet), string(application.ProviderCredentialDelete)},
	"MCPCredentialStatusView.protocol_version":                 {application.MCPCredentialProtocolVersion},
	"MCPCredentialRequestView.version":                         {application.MCPCredentialProtocolVersion},
	"MCPCredentialRequestView.action":                          {"set", "delete", "oauth-discover", "oauth-start", "oauth-cancel"},
	"MCPCredentialRequestView.oauth_client_id_mode":            {"auto", "dynamic", "pre_registered", "metadata_url"},
	"FileEditProposalSourceView.protocol_version":              {application.FileEditProposalProtocolVersion},
	"FileEditProposalRequestView.version":                      {application.FileEditProposalProtocolVersion},
	"FileEditProposalView.protocol_version":                    {application.FileEditProposalProtocolVersion},
	"FileEditProposalRecoveryView.protocol_version":            {application.FileEditRecoveryProtocolVersion},
	"FileEditProposalRecoveryView.status":                      {fileedit.StatusProposed},
	"FileEditQueueView.protocol_version":                       {application.FileEditReviewProtocolVersion},
	"FileEditChangeSetView.protocol_version":                   {application.FileEditChangeSetProtocolVersion},
	"FileEditChangeSetItemView.operation":                      {fileedit.OperationReplace, fileedit.OperationCreate, fileedit.OperationMove, fileedit.OperationDelete},
	"FileEditChangeSetItemView.status":                         {fileedit.StatusProposed, fileedit.StatusApproved, fileedit.StatusApplied, fileedit.StatusDenied, fileedit.StatusFailed},
	"FileEditReviewRequestView.version":                        {application.FileEditReviewProtocolVersion},
	"FileEditReviewRequestView.action":                         {string(application.FileEditApproveIntent), string(application.FileEditDeny)},
	"FileEditReviewView.protocol_version":                      {application.FileEditReviewProtocolVersion},
	"FileEditReviewView.action":                                {string(application.FileEditApproveIntent), string(application.FileEditDeny)},
	"ApprovalContinuationResult.state":                         {"not_started", "queued", "completed", "waiting_approval", "failed"},
	"FileEditPreviewView.status":                               {fileedit.StatusProposed, fileedit.StatusApproved, fileedit.StatusApplied, fileedit.StatusDenied, fileedit.StatusFailed},
	"FileEditPreviewView.operation":                            {fileedit.OperationReplace, fileedit.OperationCreate, fileedit.OperationMove, fileedit.OperationDelete},
	"FileEditApplyRequestView.version":                         {fileedit.FileEditApplyProtocolVersion},
	"FileEditRevertProposalRequestView.version":                {application.FileEditProposalProtocolVersion},
	"FileEditRevertProposalView.protocol_version":              {application.FileEditProposalProtocolVersion},
	"FileEditApplyView.protocol_version":                       {fileedit.FileEditApplyProtocolVersion},
	"FileEditApplyView.status":                                 {string(fileedit.ApplyCompleted), string(fileedit.ApplyFailed)},
	"RunWakeScheduleRequestView.version":                       {domain.RunWakeControlProtocolVersion},
	"RunWakeCancelRequestView.version":                         {domain.RunWakeControlProtocolVersion},
	"RunWakeStateView.protocol_version":                        {domain.RunWakeIntentProtocolVersion},
	"RunWakeIntentView.protocol_version":                       {domain.RunWakeIntentProtocolVersion},
	"RunWakeIntentView.status":                                 {string(domain.RunWakeQueued), string(domain.RunWakeLeased), string(domain.RunWakeCompleted), string(domain.RunWakeCancelled), string(domain.RunWakeExhausted)},
	"RunWakeControlView.protocol_version":                      {domain.RunWakeControlProtocolVersion},
	"RunWakeControlView.action":                                {string(domain.RunWakeSchedule), string(domain.RunWakeCancel)},
	"RunWakeExecutionRequestView.version":                      {domain.RunWakeConsumerProtocolVersion},
	"RunWakeExecutionView.protocol_version":                    {domain.RunWakeConsumerProtocolVersion},
	"RunWakeExecutionView.consumption_status":                  {string(domain.RunWakeConsumptionPrepared), string(domain.RunWakeConsumptionCompleted), string(domain.RunWakeConsumptionFailed)},
	"AgentCodeCapabilitiesView.protocol_version":               {toolgateway.AgentCodeRegistryVersion},
	"AgentCodeCapabilitiesView.surface":                        {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"AgentCodeCapabilitiesView.phase":                          {string(domain.ExecutionPhasePlan), string(domain.ExecutionPhaseDeliver)},
	"AgentCodeCapabilitiesView.role":                           {string(domain.AgentRoleRoot)},
	"AgentCodeCapabilitiesView.profile":                        {string(domain.ProfileCode), string(domain.ProfileReview), string(domain.ProfileLearn), string(domain.ProfileScript)},
	"AgentCodeCapabilitiesView.permission_mode":                {string(domain.RunExecutionPermissionAsk), string(domain.RunExecutionPermissionAuto), string(domain.RunExecutionPermissionFull), string(domain.RunExecutionPermissionConservative), string(domain.RunExecutionPermissionApproval), string(domain.RunExecutionPermissionFullAccess), string(domain.RunExecutionPermissionDebug)},
	"AgentCodeToolCapabilityView.name":                         {string(toolgateway.WorkspaceListTool), string(toolgateway.WorkspaceReadTool), string(toolgateway.WorkspaceGlobTool), string(toolgateway.WorkspaceGrepTool), string(toolgateway.WorkspaceChangeTool), string(toolgateway.WorkspaceApplyTool), string(toolgateway.WorkspaceDeleteTool)},
	"AgentCodeToolCapabilityView.class":                        {string(toolgateway.ClassWorkspaceRead), string(toolgateway.ClassWorkspaceWrite)},
	"AgentCodeToolCapabilityView.source":                       {toolgateway.AgentCodeRegistryVersion},
	"AgentCodeToolCapabilityView.approval":                     {"automatic", "proposal_then_operator_review", "approved_proposal_only"},
	"SkillPackageInstallRequestView.version":                   {skills.PackageInstallationProtocolVersion, plugins.PortableInstallationProtocol},
	"SkillCatalogPublisherControlRequestView.version":          {"skill-catalog-control.v1"},
	"SkillCatalogPublisherControlRequestView.action":           {"trust", "revoke"},
	"SkillCatalogPublishersView.protocol_version":              {"skill-catalog-control.v1"},
	"SkillCatalogPublisherView.trust_class":                    {"trusted", "revoked"},
	"ExtensionPluginImportSourceView.kind":                     {"https", "git"},
	"PluginSkillInstallView.protocol_version":                  {plugins.PortableInstallationProtocol},
	"SkillPackageInstallRequestView.surface":                   {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"SkillPackageInstallView.protocol_version":                 {skills.PackageInstallationProtocolVersion},
	"SkillPackageInstallView.surface":                          {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"SkillPackageInstallView.trust_class":                      {string(skills.PackageTrustOperatorInstalledUntrusted)},
	"SkillPackageInventoryView.protocol_version":               {skills.PackageInventoryProtocolVersion},
	"SkillPackageItemView.status":                              {"installed", "removed"},
	"SkillPackageItemView.trust_class":                         {string(skills.PackageTrustOperatorInstalledUntrusted)},
	"SkillPackageRemoveRequestView.protocol_version":           {skills.PackageRemovalRequestProtocolVersion},
	"SkillPackageRemovalView.protocol_version":                 {skills.PackageRemovalControlProtocolVersion},
	"SkillPackageRemovalView.status":                           {"removed"},
	"OperationReceiptView.protocol_version":                    {operationreceipt.ProtocolVersion},
	"OperationReceiptView.kind":                                {string(operationreceipt.KindFileEditApply), string(operationreceipt.KindRunWakeConsume), string(operationreceipt.KindSkillPackageInstall), string(operationreceipt.KindSkillPackageRemove)},
	"OperationReceiptView.outcome":                             {"applied", "failed", "completed", "installed", "removed"},
	"OperationReceiptView.retry_strategy":                      {string(operationreceipt.RetrySameOperationKey), string(operationreceipt.RetrySameWakeGeneration)},
	"OperationReceiptView.recovery_action":                     {string(operationreceipt.RecoveryNone), string(operationreceipt.RecoveryRetryAfterGrace)},
	"OperationReceiptView.cleanup_state":                       {string(operationreceipt.CleanupNotApplicable), string(operationreceipt.CleanupComplete), string(operationreceipt.CleanupPendingReview)},
	"OperationReceiptHistoryView.protocol_version":             {operationreceipt.HistoryProtocolVersion},
	"OperationReceiptHistoryItemView.scope":                    {"run", "skill_registry"},
	"WorkspaceExplorerView.protocol_version":                   {workspace.ExplorerProtocolVersion},
	"WorkspaceExplorerView.kind":                               {"directory", "file"},
	"WorkspaceExplorerEntryView.kind":                          {"directory", "file", "blocked"},
	"WorkspaceExplorerProvenanceView.version":                  {session.ContextProvenanceVersion},
	"WorkspaceExplorerProvenanceView.source_kind":              {session.SourceWorkspaceFile, session.SourceWorkspaceList},
	"WorkspaceSearchView.protocol_version":                     {workspace.SearchProtocolVersion},
	"WorkspaceSearchResultView.match_kind":                     {"filename", "content", "filename_and_content"},
	"RepositoryStateView.protocol_version":                     {repository.ProtocolVersion},
	"RepositoryStateView.kind":                                 {"none", "git"},
	"RepositoryChangeView.staging":                             {"unmodified", "untracked", "modified", "added", "deleted", "renamed", "copied", "conflicted"},
	"RepositoryChangeView.worktree":                            {"unmodified", "untracked", "modified", "added", "deleted", "renamed", "copied", "conflicted"},
	"RepositoryDiffView.protocol_version":                      {repository.DiffProtocolVersion},
	"RepositoryDiffView.kind":                                  {"none", "git"},
	"RepositoryDiffItemView.staging":                           {"unmodified", "untracked", "modified", "added", "deleted", "renamed", "copied", "conflicted"},
	"RepositoryDiffItemView.worktree":                          {"unmodified", "untracked", "modified", "added", "deleted", "renamed", "copied", "conflicted"},
	"RepositoryDiffItemView.content_state":                     {repository.DiffContentText, repository.DiffContentBinary, repository.DiffContentTooLarge, repository.DiffContentLinked, repository.DiffContentMissing},
	"RepositoryFileHistoryView.protocol_version":               {repository.FileHistoryProtocolVersion},
	"RepositoryFileHistoryView.kind":                           {"none", "git"},
	"RepositoryFileHistoryEntryView.change":                    {"added", "modified", "deleted"},
	"RepositoryFileHistoryEntryView.previous_kind":             {"", "regular", "executable", "symlink", "submodule"},
	"RepositoryFileHistoryEntryView.current_kind":              {"", "regular", "executable", "symlink", "submodule"},
	"RepositoryCommitComparisonView.protocol_version":          {repository.CommitComparisonProtocolVersion},
	"RepositoryCommitComparisonView.kind":                      {"none", "git"},
	"RepositoryCommitFileChangeView.change":                    {"added", "modified", "deleted"},
	"RepositoryCommitFileChangeView.previous_kind":             {"", "regular", "executable", "symlink", "submodule"},
	"RepositoryCommitFileChangeView.current_kind":              {"", "regular", "executable", "symlink", "submodule"},
	"RepositoryCommitFilePreviewView.protocol_version":         {repository.CommitFilePreviewProtocolVersion},
	"RepositoryCommitFilePreviewView.kind":                     {"regular", "executable"},
	"RepositoryCommitFilePreviewProvenanceView.version":        {session.ContextProvenanceVersion},
	"RepositoryCommitFilePreviewProvenanceView.source_kind":    {repository.CommitFilePreviewSourceKind},
	"EvidenceAttachmentRequestView.version":                    {session.EvidenceAttachmentProtocolVersion},
	"EvidenceAttachmentRequestView.source_kind":                {session.SourceWorkspaceFile},
	"EvidenceAttachmentView.protocol_version":                  {session.EvidenceAttachmentProtocolVersion},
	"EvidenceAttachmentView.source_kind":                       {session.SourceWorkspaceFile},
	"EvidenceInventoryView.protocol_version":                   {session.EvidenceInventoryProtocolVersion},
	"EvidenceInventoryItemView.source_kind":                    {session.SourceWorkspaceFile},
	"VerificationEvidenceRequestView.version":                  {verification.EvidenceProtocolVersion},
	"VerificationEvidenceRequestView.outcome":                  {string(verification.OutcomePass), string(verification.OutcomeFail), string(verification.OutcomeUnknown)},
	"VerificationEvidenceItemView.protocol_version":            {verification.EvidenceProtocolVersion},
	"VerificationEvidenceItemView.outcome":                     {string(verification.OutcomePass), string(verification.OutcomeFail), string(verification.OutcomeUnknown)},
	"VerificationEvidenceControlView.protocol_version":         {verification.EvidenceProtocolVersion},
	"VerificationEvidenceControlView.outcome":                  {string(verification.OutcomePass), string(verification.OutcomeFail), string(verification.OutcomeUnknown)},
	"VerificationEvidenceInventoryView.protocol_version":       {verification.InventoryProtocolVersion},
	"VerificationPlanItemCoverageDetailView.protocol_version":  {verification.PlanItemCoverageProtocolVersion},
	"VerificationAssociationReferenceView.evidence_outcome":    {string(verification.OutcomePass), string(verification.OutcomeFail), string(verification.OutcomeUnknown)},
	"StandardCodeDeliveryReport.protocol_version":              {standardcodedelivery.ProtocolVersion},
	"StandardCodeDeliveryReport.status":                        {string(standardcodedelivery.StatusPassed), string(standardcodedelivery.StatusFailed), string(standardcodedelivery.StatusPartial), string(standardcodedelivery.StatusNotRun), string(standardcodedelivery.StatusBlocked), string(standardcodedelivery.StatusStale)},
	"StandardCodeDeliveryReport.receipt_status":                {string(standardcodedelivery.StatusPassed), string(standardcodedelivery.StatusFailed), string(standardcodedelivery.StatusPartial), string(standardcodedelivery.StatusNotRun), string(standardcodedelivery.StatusBlocked), string(standardcodedelivery.StatusStale)},
	"StandardCodeDeliveryReport.declaration":                   {string(standardcodedelivery.DeclarationNoApplicableTests), string(standardcodedelivery.DeclarationUserSkipped), string(standardcodedelivery.DeclarationBudgetExhausted), string(standardcodedelivery.DeclarationMissingDependency), string(standardcodedelivery.DeclarationApprovalDenied)},
	"StandardCodeDeliveryVerification.conclusion":              {string(standardcodedelivery.StatusPassed), string(standardcodedelivery.StatusFailed), string(standardcodedelivery.StatusPartial), string(standardcodedelivery.StatusBlocked), string(standardcodedelivery.StatusStale)},
	"StandardCodeDeliveryVerification.state":                   {string(runner.CommandRuntimeJobPrepared), string(runner.CommandRuntimeJobRunning), string(runner.CommandRuntimeJobStopping), string(runner.CommandRuntimeJobCompleted), string(runner.CommandRuntimeJobFailed), string(runner.CommandRuntimeJobTimedOut), string(runner.CommandRuntimeJobCancelled), string(runner.CommandRuntimeJobKilled), string(runner.CommandRuntimeJobInterrupted)},
	"StandardCodeDeliveryArtifact.stream":                      {string(artifact.StreamStdout), string(artifact.StreamStderr)},
	"StandardCodeDeliveryCheckpoint.recovery_level":            {string(workspacecheckpoint.RecoveryComplete), string(workspacecheckpoint.RecoveryPartial), string(workspacecheckpoint.RecoveryUnavailable)},
	"StandardCodeDeliveryRecordView.declaration":               {string(standardcodedelivery.DeclarationNoApplicableTests), string(standardcodedelivery.DeclarationUserSkipped), string(standardcodedelivery.DeclarationBudgetExhausted), string(standardcodedelivery.DeclarationMissingDependency), string(standardcodedelivery.DeclarationApprovalDenied)},
	"CodeHandoffView.protocol_version":                         {application.CodeHandoffProtocolVersion},
	"CodeHandoffView.run_status":                               runStatuses(),
	"CodeHandoffView.surface":                                  {string(domain.ExecutionSurfaceCode)},
	"CodeHandoffView.phase":                                    {string(domain.ExecutionPhasePlan), string(domain.ExecutionPhaseDeliver)},
	"CodeHandoffPlanView.state":                                {"none", "proposed", "selected"},
	"CodeHandoffVerificationReferenceView.outcome":             {string(verification.OutcomePass), string(verification.OutcomeFail), string(verification.OutcomeUnknown)},
	"CodeHandoffVerificationCoverageView.protocol_version":     {verification.PlanCoverageProtocolVersion},
	"CodeHandoffActionReferenceView.kind":                      {string(operatoraction.KindSteeringPending), string(operatoraction.KindApprovalPending), string(operatoraction.KindFileEditReview), string(operatoraction.KindFileEditApply), string(operatoraction.KindWakeDue)},
	"CodeHandoffActionReferenceView.state":                     {"pending", "proposed", "approved", "queued"},
	"CodeHandoffActionReferenceView.destination":               {string(operatoraction.DestinationQueue), string(operatoraction.DestinationApprovals), string(operatoraction.DestinationDiffs), string(operatoraction.DestinationWake)},
	"CodeHandoffReportReferenceView.status":                    {string(domain.FindingReportGenerated)},
	"OperatorActionCenterView.protocol_version":                {operatoraction.ProtocolVersion},
	"OperatorActionItemView.kind":                              {string(operatoraction.KindSteeringPending), string(operatoraction.KindApprovalPending), string(operatoraction.KindFileEditReview), string(operatoraction.KindFileEditApply), string(operatoraction.KindWakeDue)},
	"OperatorActionItemView.state":                             {"pending", "proposed", "approved", "queued"},
	"OperatorActionItemView.destination":                       {string(operatoraction.DestinationQueue), string(operatoraction.DestinationApprovals), string(operatoraction.DestinationDiffs), string(operatoraction.DestinationWake)},
	"ProviderAvailabilityView.kind":                            {modelregistry.ProviderKindLocal, modelregistry.ProviderKindAnthropicCompatible, modelregistry.ProviderKindOpenAICompatible, modelregistry.ProviderKindOllama},
	"ProviderAvailabilityView.status":                          {modelregistry.ProviderAvailable, modelregistry.ProviderNotConfigured, modelregistry.ProviderInvalidConfiguration},
	"ProviderAvailabilityView.credential_source":               {"none", "environment", "system", modelregistry.CredentialSourceChatGPT},
	"ScopeView.web_access_mode":                                {"disabled", "rules", "full"},
	"RunCreationControlRequestView.web_access_mode":            {"disabled", "rules", "full"},
	"ThreadCreationControlRequestView.web_access_mode":         {"disabled", "rules", "full"},
	"RunWebAccessControlRequestView.web_access_mode":           {"disabled", "rules", "full"},
	"RunWebAccessControlRequestView.version":                   {application.RunWebAccessControlProtocolVersion},
	"RunWebAccessControlView.version":                          {application.RunWebAccessControlProtocolVersion},
	"ScopeView.network_mode":                                   {"disabled", "allowlist"},
	"MissionView.profile":                                      {"code", "review", "learn", "script"},
	"ThreadView.protocol_version":                              {domain.ThreadProtocolVersion},
	"ThreadView.status":                                        {string(domain.ThreadActive), string(domain.ThreadArchived), string(domain.ThreadDeleted)},
	"ThreadView.composer_state":                                {"ready", "waiting_approval", "successor_required", "unavailable"},
	"ThreadView.execution_state":                               {"idle", "running", "stopping", "stop_failed", "waiting_approval", "paused", "completed", "failed", "cancelled", "unknown"},
	"TaskConfigurationView.version":                            {application.TaskConfigurationVersion},
	"TaskSkillCandidatesView.version":                          {application.TaskSkillCandidatesVersion},
	"TaskSkillCandidatesView.surface":                          {"code", "cyber"},
	"TaskSkillCandidatesView.phase":                            {"plan", "deliver"},
	"TaskSkillCandidatesView.profile":                          {"code", "learn", "review", "script"},
	"TaskConfigurationView.project_disposition":                {"absent", "applied", "rejected"},
	"TaskConfigurationView.profile":                            {"code", "learn", "review", "script"},
	"TaskConfigurationRequest.profile":                         {"code", "learn", "review", "script"},
	"ConfigurationSource.source":                               {"default", "operator", "project", "snapshot"},
	"ConfigurationSource.field":                                {"budget.max_turns", "budget.max_tokens", "budget.max_tool_calls", "budget.max_cost_usd", "budget.timeout_seconds", "read_only", "allowed_profiles", "exclude_paths", "skill_suggestions", "test_command_id", "format_command_id"},
	"ThreadCreationControlRequestView.version":                 {domain.ThreadCreationProtocolVersion},
	"ThreadCreationControlRequestView.profile":                 {string(domain.ProfileCode), string(domain.ProfileReview), string(domain.ProfileLearn), string(domain.ProfileScript)},
	"ThreadCreationControlRequestView.surface":                 {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"ThreadCreationControlRequestView.phase":                   {string(domain.ExecutionPhasePlan), string(domain.ExecutionPhaseDeliver)},
	"ThreadCreationControlRequestView.network_mode":            {"disabled", "allowlist"},
	"ThreadMessageControlRequestView.version":                  {domain.ThreadMessageProtocolVersion},
	"ThreadTurnControlRequestView.version":                     {domain.ThreadMessageProtocolVersion},
	"MCPReference.method":                                      {"resources/read", "prompts/get"},
	"ThreadPlanControlRequestView.version":                     {application.PlanDeliveryControlProtocolVersion},
	"ThreadPlanControlView.version":                            {application.PlanDeliveryControlProtocolVersion},
	"ThreadPlanControlRequestView.action":                      {"enter_plan", "enter_deliver", "confirm"},
	"ThreadPlanControlView.action":                             {"enter_plan", "enter_deliver", "confirm"},
	"ThreadPlanControlRequestView.manual_acceptance":           {"required", "on_demand"},
	"ThreadPlanControlView.manual_acceptance":                  {"required", "on_demand"},
	"ThreadPlanControlView.state":                              {"not_received", "prepared", "received", "completed", "failed", "rejected"},
	"WorkspaceFileReference.source_kind":                       {"workspace_file"},
	"ThreadMessageControlView.version":                         {domain.ThreadMessageProtocolVersion},
	"ThreadExecutionState.version":                             {application.ThreadExecutionProtocolVersion},
	"ThreadExecutionState.state":                               {"idle", "running", "stopping", "stop_failed"},
	"ThreadInterruptRequestView.version":                       {application.ThreadExecutionProtocolVersion},
	"ThreadRunRecoveryControlRequestView.version":              {domain.ThreadRunRecoveryProtocolVersion},
	"ThreadRunRecoveryControlView.version":                     {domain.ThreadRunRecoveryProtocolVersion},
	"ThreadRunRecoveryView.version":                            {domain.ThreadRunRecoveryProtocolVersion},
	"ThreadMessageView.role":                                   {"system", "user", "assistant", "tool"},
	"ThreadMessageView.provenance_version":                     {session.LegacyContextProvenanceVersion, session.ContextProvenanceVersion},
	"ThreadMessageView.source_kind":                            {session.SourceOperatorMessage, session.SourceModelResponse, session.SourceGoControl, session.SourceWorkspaceImage, session.SourceUploadedFile, session.SourceWorkspaceFile, session.SourceWorkspaceList, session.SourceWorkspaceDiff, session.SourceToolResult, session.SourceGoCommandResult},
	"ThreadMessageView.status":                                 {"pending", "committed", "cancelled"},
	"ThreadExecutionPermissionView.protocol_version":           {domain.ThreadExecutionPermissionProtocolVersion, domain.ThreadApprovalPermissionProtocolVersion},
	"ThreadExecutionPermissionView.mode":                       {string(domain.RunExecutionPermissionConservative), string(domain.RunExecutionPermissionWorkspaceAccess), string(domain.RunExecutionPermissionApproval), string(domain.RunExecutionPermissionFullAccess), string(domain.RunExecutionPermissionDebug), "ask", "auto", "full"},
	"ThreadExecutionPermissionView.approval_mode":              {"ask", "auto", "full"},
	"ThreadExecutionPermissionView.full_activation":            {"inactive", "active", "unavailable"},
	"ThreadExecutionPermissionView.approval_policy":            {string(domain.ExecutionPermissionApprovalFixedTemplates), string(domain.ExecutionPermissionApprovalOutOfScopeExactOnce), string(domain.ExecutionPermissionApprovalPerCommand), string(domain.ExecutionPermissionApprovalNone), "per_operation"},
	"ThreadExecutionPermissionView.command_scope":              {string(domain.ExecutionPermissionCommandFixedTemplates), string(domain.ExecutionPermissionCommandSandboxedWorkspace), string(domain.ExecutionPermissionCommandArbitraryStateless), string(domain.ExecutionPermissionCommandArbitraryPersistent), "per_operation"},
	"ThreadExecutionPermissionView.filesystem_scope":           {string(domain.ExecutionPermissionFilesystemWorkspaceGuarded), string(domain.ExecutionPermissionFilesystemHostFull), "per_operation"},
	"ThreadExecutionPermissionView.network_scope":              {string(domain.ExecutionPermissionNetworkDisabled), string(domain.ExecutionPermissionNetworkHost), "per_operation"},
	"ThreadExecutionPermissionView.risk_tier":                  {string(domain.ExecutionRiskMinimal), string(domain.ExecutionRiskElevated), string(domain.ExecutionRiskHigh)},
	"ThreadExecutionPermissionView.required_gate":              {string(domain.ExecutionPermissionGateConservative), string(domain.ExecutionPermissionGateWorkspaceSandbox), string(domain.ExecutionPermissionGateOperatorApproval), string(domain.ExecutionPermissionGateDangerFullAccess), string(domain.ExecutionPermissionGateDebugMaximumAccess), "operation_authority"},
	"ThreadExecutionPermissionView.policy_version":             {domain.RunExecutionPermissionPolicyVersion, domain.OperationPermissionPolicyVersion},
	"ThreadExecutionPermissionControlRequestView.mode":         {"ask", "auto", "full"},
	"ThreadExecutionPermissionControlView.current_run_effect":  {string(domain.ThreadExecutionPermissionApplied), string(domain.ThreadExecutionPermissionPausedAndApplied), string(domain.ThreadExecutionPermissionDeferred), string(domain.ThreadExecutionPermissionNoActiveRun)},
	"ThreadExecutionPermissionControlView.current_run_mode":    {string(domain.RunExecutionPermissionConservative), string(domain.RunExecutionPermissionWorkspaceAccess), string(domain.RunExecutionPermissionApproval), string(domain.RunExecutionPermissionFullAccess), string(domain.RunExecutionPermissionDebug), "ask", "auto", "full"},
	"ThreadLifecycleControlRequestView.version":                {domain.ThreadLifecycleProtocolVersion},
	"ThreadLifecycleControlView.version":                       {domain.ThreadLifecycleProtocolVersion},
	"ThreadExportView.protocol_version":                        {domain.ThreadExportProtocolVersion},
	"RunView.status":                                           runStatuses(),
	"RunModeView.protocol_version":                             {domain.RunModeProtocolVersion},
	"RunModeView.surface":                                      {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"RunModeView.phase":                                        {string(domain.ExecutionPhasePlan), string(domain.ExecutionPhaseDeliver)},
	"RunModeView.profile":                                      {"code", "review", "learn", "script"},
	"RunModeView.policy_version":                               {domain.RunModePolicyVersion},
	"RunExecutionProfileView.protocol_version":                 {domain.RunExecutionProfileProtocolVersion},
	"RunExecutionProfileView.profile":                          {string(domain.RunExecutionProfilePreview), string(domain.RunExecutionProfileDocker), string(domain.RunExecutionProfileLocal), string(domain.RunExecutionProfileSBX)},
	"RunExecutionProfileView.backend":                          {string(domain.ExecutionBackendNoop), string(domain.ExecutionBackendDocker), string(domain.ExecutionBackendLocal), string(domain.ExecutionBackendSBX)},
	"RunExecutionProfileView.approval_policy":                  {string(domain.ExecutionApprovalNone), string(domain.ExecutionApprovalAlways)},
	"RunExecutionProfileView.filesystem_scope":                 {string(domain.ExecutionFilesystemNone), string(domain.ExecutionFilesystemWorkspace)},
	"RunExecutionProfileView.network_scope":                    {string(domain.ExecutionNetworkDisabled)},
	"RunExecutionProfileView.risk_tier":                        {string(domain.ExecutionRiskMinimal), string(domain.ExecutionRiskElevated), string(domain.ExecutionRiskHigh)},
	"RunExecutionProfileView.required_gate":                    {string(domain.ExecutionGateNone), string(domain.ExecutionGateDockerProductionStart), string(domain.ExecutionGateLocalOSSandbox), string(domain.ExecutionGateSBXMicroVM)},
	"RunExecutionProfileView.policy_version":                   {domain.RunExecutionProfilePolicyVersion},
	"RunExecutionProfileControlRequestView.profile":            {string(domain.RunExecutionProfilePreview), string(domain.RunExecutionProfileDocker), string(domain.RunExecutionProfileLocal), string(domain.RunExecutionProfileSBX)},
	"RunExecutionPermissionView.protocol_version":              {domain.RunExecutionPermissionProtocolVersion, domain.RunApprovalPermissionProtocolVersion},
	"RunExecutionPermissionView.mode":                          {string(domain.RunExecutionPermissionConservative), string(domain.RunExecutionPermissionWorkspaceAccess), string(domain.RunExecutionPermissionApproval), string(domain.RunExecutionPermissionFullAccess), string(domain.RunExecutionPermissionDebug), "ask", "auto", "full"},
	"RunExecutionPermissionView.approval_mode":                 {"ask", "auto", "full"},
	"RunExecutionPermissionView.full_activation":               {"inactive", "active", "unavailable"},
	"RunExecutionPermissionView.approval_policy":               {string(domain.ExecutionPermissionApprovalFixedTemplates), string(domain.ExecutionPermissionApprovalOutOfScopeExactOnce), string(domain.ExecutionPermissionApprovalPerCommand), string(domain.ExecutionPermissionApprovalNone), "per_operation"},
	"RunExecutionPermissionView.command_scope":                 {string(domain.ExecutionPermissionCommandFixedTemplates), string(domain.ExecutionPermissionCommandSandboxedWorkspace), string(domain.ExecutionPermissionCommandArbitraryStateless), string(domain.ExecutionPermissionCommandArbitraryPersistent), "per_operation"},
	"RunExecutionPermissionView.filesystem_scope":              {string(domain.ExecutionPermissionFilesystemWorkspaceGuarded), string(domain.ExecutionPermissionFilesystemHostFull), "per_operation"},
	"RunExecutionPermissionView.network_scope":                 {string(domain.ExecutionPermissionNetworkDisabled), string(domain.ExecutionPermissionNetworkHost), "per_operation"},
	"RunExecutionPermissionView.risk_tier":                     {string(domain.ExecutionRiskMinimal), string(domain.ExecutionRiskElevated), string(domain.ExecutionRiskHigh)},
	"RunExecutionPermissionView.required_gate":                 {string(domain.ExecutionPermissionGateConservative), string(domain.ExecutionPermissionGateWorkspaceSandbox), string(domain.ExecutionPermissionGateOperatorApproval), string(domain.ExecutionPermissionGateDangerFullAccess), string(domain.ExecutionPermissionGateDebugMaximumAccess), "operation_authority"},
	"RunExecutionPermissionView.policy_version":                {domain.RunExecutionPermissionPolicyVersion, domain.OperationPermissionPolicyVersion},
	"RunExecutionPermissionControlRequestView.mode":            {"ask", "auto", "full"},
	"RunBrowserCDPPermissionView.protocol_version":             {domain.RunBrowserCDPPermissionProtocolVersion},
	"RunBrowserCDPPermissionView.mode":                         {string(domain.RunBrowserCDPPermissionRestricted), string(domain.RunBrowserCDPPermissionFullDebug)},
	"RunBrowserCDPPermissionView.risk_tier":                    {string(domain.ExecutionRiskMinimal), string(domain.ExecutionRiskHigh)},
	"RunBrowserCDPPermissionView.required_gate":                {string(domain.BrowserCDPPermissionGateRestricted), string(domain.BrowserCDPPermissionGateFullDebug)},
	"RunBrowserCDPPermissionView.policy_version":               {domain.RunBrowserCDPPermissionPolicyVersion},
	"RunBrowserCDPPermissionControlRequestView.mode":           {string(domain.RunBrowserCDPPermissionRestricted), string(domain.RunBrowserCDPPermissionFullDebug)},
	"FullCDPSessionOpenRequestView.version":                    {application.FullCDPSessionProtocolVersion},
	"FullCDPSessionCloseRequestView.version":                   {application.FullCDPSessionCloseProtocolVersion},
	"FullCDPSessionView.version":                               {application.FullCDPSessionProtocolVersion},
	"ThreadApplicationServicesView.version":                    {application.ThreadApplicationServicesProtocolVersion},
	"ThreadApplicationServiceDetailView.version":               {application.ThreadApplicationServicesProtocolVersion},
	"ThreadApplicationServiceStopView.version":                 {application.ThreadApplicationServicesProtocolVersion},
	"ThreadApplicationServiceStopRequestView.version":          {application.ThreadApplicationServicesProtocolVersion},
	"ThreadApplicationServiceView.state":                       {"prepared", "running", "stopping", "completed", "failed", "timed_out", "cancelled", "killed", "interrupted"},
	"ThreadApplicationServiceURLView.source":                   {"command_output"},
	"FullCDPPreviewView.version":                               {application.FullCDPPreviewProtocolVersion},
	"FullCDPPreviewRequestView.version":                        {application.FullCDPPreviewProtocolVersion},
	"FullCDPPreviewActionRequestView.version":                  {application.FullCDPPreviewActionProtocolVersion},
	"FullCDPPreviewActionRequestView.action":                   {"click", "type"},
	"FullCDPPreviewImage.media_type":                           {"image/png"},
	"FullCDPSessionView.state":                                 {string(application.FullCDPSessionNone), string(application.FullCDPSessionStarting), string(application.FullCDPSessionReady), string(application.FullCDPSessionClosing), string(application.FullCDPSessionClosed), string(application.FullCDPSessionFailed)},
	"FullCDPSessionView.close_reason":                          {application.FullCDPCloseOperator, application.FullCDPCloseExpired, application.FullCDPClosePermissionRevoked, application.FullCDPCloseProcessExited, application.FullCDPCloseRunTerminal, application.FullCDPCloseDesktopShutdown, application.FullCDPCloseOpenFailed},
	"FullCDPBrowserSelectionView.product":                      {string(browserruntime.BrowserProductChrome), string(browserruntime.BrowserProductEdge)},
	"FullCDPBrowserSelectionView.channel":                      {string(browserruntime.BrowserChannelStable), string(browserruntime.BrowserChannelBeta), string(browserruntime.BrowserChannelDev), string(browserruntime.BrowserChannelCanary)},
	"BrowserSafeWebReadiness.protocol_version":                 {browserruntime.BrowserSafeWebReadinessProtocolVersion},
	"BrowserSafeWebReadiness.blocking_reason":                  {browserruntime.BrowserSafeWebBlockedEvidenceMissing, browserruntime.BrowserSafeWebBlockedEvidenceVersionMismatch, browserruntime.BrowserSafeWebBlockedExecutableMismatch, browserruntime.BrowserSafeWebBlockedAcceptanceMismatch, browserruntime.BrowserSafeWebBlockedPolicyVersionMismatch, browserruntime.BrowserSafeWebBlockedAdapterMismatch, browserruntime.BrowserSafeWebBlockedPlatformMismatch, browserruntime.BrowserSafeWebBlockedEvidenceNotPassed, browserruntime.BrowserSafeWebBlockedReviewMissing, browserruntime.BrowserSafeWebBlockedReviewBindingMismatch, browserruntime.BrowserSafeWebBlockedReviewNotAccepted, browserruntime.BrowserSafeWebBlockedEvidenceExpired},
	"BrowserSafeWebReadiness.adapter":                          {browserruntime.WindowsWFPBrowserContainmentAdapterName},
	"BrowserSafeWebReadiness.policy_version":                   {browserruntime.BrowserNetworkContainmentPolicyVersion},
	"RunExecutionInteractionView.protocol_version":             {domain.RunExecutionInteractionProtocolVersion},
	"RunExecutionInteractionView.mode":                         {string(domain.RunExecutionInteractionPreview), string(domain.RunExecutionInteractionControlled), string(domain.RunExecutionInteractionDebug), string(domain.RunExecutionInteractionCyber)},
	"RunExecutionInteractionView.surface":                      {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"RunExecutionInteractionView.execution_profile":            {string(domain.RunExecutionProfilePreview), string(domain.RunExecutionProfileDocker), string(domain.RunExecutionProfileLocal), string(domain.RunExecutionProfileSBX)},
	"RunExecutionInteractionView.workspace_trust":              {string(domain.WorkspaceTrustUntrusted), string(domain.WorkspaceTrustTrusted)},
	"RunExecutionInteractionView.command_form":                 {string(domain.ExecutionCommandNone), string(domain.ExecutionCommandStructuredArgv), string(domain.ExecutionCommandUserConPTY), string(domain.ExecutionCommandContainerPTY)},
	"RunExecutionInteractionView.network_scope":                {string(domain.ExecutionNetworkDisabled)},
	"RunExecutionInteractionView.required_gate":                {string(domain.ExecutionInteractionGateNone), string(domain.ExecutionInteractionGateLocalOSSandbox), string(domain.ExecutionInteractionGateSBXMicroVM), string(domain.ExecutionInteractionGateDockerSandbox), string(domain.ExecutionInteractionGateDebugAgentLease), string(domain.ExecutionInteractionGateCyberContainerPTY)},
	"RunExecutionInteractionView.policy_version":               {domain.RunExecutionInteractionPolicyVersion},
	"RunExecutionInteractionControlRequestView.mode":           {string(domain.RunExecutionInteractionPreview), string(domain.RunExecutionInteractionControlled), string(domain.RunExecutionInteractionDebug), string(domain.RunExecutionInteractionCyber)},
	"RunExecutionInteractionControlRequestView.trust":          {string(domain.WorkspaceTrustUntrusted), string(domain.WorkspaceTrustTrusted)},
	"RunCapabilityReadinessView.protocol_version":              {application.RunCapabilityReadinessProtocolVersion},
	"CapabilityReadinessOptionView.value":                      {"ask", "auto", "full", string(domain.RunExecutionPermissionConservative), string(domain.RunExecutionPermissionWorkspaceAccess), string(domain.RunExecutionPermissionApproval), string(domain.RunExecutionPermissionFullAccess), string(domain.RunExecutionPermissionDebug), string(domain.RunExecutionProfilePreview), string(domain.RunExecutionProfileDocker), string(domain.RunExecutionProfileLocal), string(domain.RunExecutionProfileSBX), string(domain.RunExecutionInteractionControlled), string(domain.RunExecutionInteractionCyber), string(domain.RunBrowserCDPPermissionRestricted), string(domain.RunBrowserCDPPermissionFullDebug), application.StandardCodePresetValue},
	"CapabilityReadinessOptionView.blocked_by":                 {string(application.CapabilityBlockerRunNotQuiescent), string(application.CapabilityBlockerExecutionLeaseActive), string(application.CapabilityBlockerStartupGateClosed), string(application.CapabilityBlockerCapabilityUnimplemented), string(application.CapabilityBlockerSurfaceMismatch), string(application.CapabilityBlockerProfileMismatch), string(application.CapabilityBlockerPermissionMismatch), string(application.CapabilityBlockerWorkspaceUntrusted), string(application.CapabilityBlockerSandboxUnproven), string(application.CapabilityBlockerDockerUnavailable), string(application.CapabilityBlockerBackendNotReady), string(application.CapabilityBlockerSBXUnavailable)},
	"CapabilityReadinessOptionView.remediation":                {string(application.CapabilityRemediationPauseRun), string(application.CapabilityRemediationCreateNewRun), string(application.CapabilityRemediationWaitForExecutionLease), string(application.CapabilityRemediationRestartWithStartupGate), string(application.CapabilityRemediationUpgradeApplication), string(application.CapabilityRemediationSelectRequiredSurface), string(application.CapabilityRemediationSelectRequiredProfile), string(application.CapabilityRemediationSelectRequiredPermission), string(application.CapabilityRemediationTrustWorkspace), string(application.CapabilityRemediationVerifySandbox), string(application.CapabilityRemediationInstallOrStartDocker), string(application.CapabilityRemediationRetryBackendReadiness), string(application.CapabilityRemediationInstallOrStartSBX)},
	"RunCreationControlRequestView.version":                    {domain.RunCreationProtocolVersion},
	"RunCreationControlRequestView.profile":                    {string(domain.ProfileCode), string(domain.ProfileReview), string(domain.ProfileLearn), string(domain.ProfileScript)},
	"RunCreationControlRequestView.surface":                    {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"RunCreationControlRequestView.phase":                      {string(domain.ExecutionPhasePlan), string(domain.ExecutionPhaseDeliver)},
	"RunCreationControlRequestView.network_mode":               {"disabled", "allowlist"},
	"RunNetworkAuthorityControlRequestView.version":            {application.RunNetworkAuthorityControlProtocolVersion},
	"RunNetworkAuthorityControlView.version":                   {application.RunNetworkAuthorityControlProtocolVersion},
	"SessionMessageControlRequestView.version":                 {domain.SessionMessageSubmissionProtocolVersion},
	"SessionMessageControlRequestView.delivery_mode":           {string(domain.OperatorSteeringNextTurn), string(domain.OperatorSteeringCurrentTurn)},
	"SessionMessageControlView.version":                        {domain.SessionMessageSubmissionProtocolVersion},
	"SessionSteeringCancellationRequestView.version":           {domain.SessionSteeringCancellationProtocolVersion},
	"SessionSteeringCancellationView.version":                  {domain.SessionSteeringCancellationProtocolVersion},
	"SessionSteeringCancellationView.cancellation_kind":        {string(domain.OperatorSteeringCancellationOperator)},
	"ThreadQueuedMessagesView.version":                         {domain.ThreadQueuedMessagesProtocolVersion},
	"ThreadQueuedMessageView.status":                           {string(domain.OperatorSteeringPending)},
	"SessionSteeringRevisionRequestView.version":               {domain.SessionSteeringRevisionProtocolVersion},
	"SessionSteeringPromotionRequestView.version":              {domain.SessionSteeringPromotionProtocolVersion},
	"SessionSteeringPromotionView.version":                     {domain.SessionSteeringPromotionProtocolVersion},
	"SessionSteeringPromotionObservationView.version":          {domain.SessionSteeringPromotionProtocolVersion},
	"SessionSteeringPromotionObservationView.state":            {"absent", "sealed", "rejected"},
	"SessionSteeringPromotionRejectionView.version":            {domain.SessionSteeringPromotionProtocolVersion},
	"SessionSteeringRevisionView.version":                      {domain.SessionSteeringRevisionProtocolVersion},
	"SessionSteeringRevisionObservationView.version":           {domain.SessionSteeringRevisionProtocolVersion},
	"SessionSteeringRevisionObservationView.state":             {"absent", "sealed"},
	"QueueRevisionUnchangedView.version":                       {domain.QueueRevisionUnchangedProtocolVersion},
	"SessionSteeringCancellationObservationView.version":       {domain.SessionSteeringCancellationProtocolVersion},
	"SessionSteeringCancellationObservationView.state":         {"absent", "sealed"},
	"SessionSteeringCancellationObservedReceiptView.kind":      {string(domain.OperatorSteeringCancellationOperator)},
	"OperatorSteeringObservationMessageView.status":            {"pending", "committed", "cancelled"},
	"RunContextDiagnosticView.phase":                           {"generation_started", "generation_received", "generation_rejected", "generation_failed", "summary_saved"},
	"RunContextDiagnosticView.fallback_code":                   {"generation_cost_budget", "generation_protocol_repair", "generation_provider_failure", "generation_invalid_response", "generation_input_data", "generation_input_window", "generation_token_budget", "unclassified"},
	"RunLifecycleControlRequestView.version":                   {domain.RunLifecycleControlProtocolVersion},
	"RunLifecycleControlRequestView.action":                    {string(domain.RunLifecycleStart), string(domain.RunLifecyclePause), string(domain.RunLifecycleResume)},
	"RunLifecycleControlView.version":                          {domain.RunLifecycleControlProtocolVersion},
	"RunLifecycleControlView.action":                           {string(domain.RunLifecycleStart), string(domain.RunLifecyclePause), string(domain.RunLifecycleResume)},
	"RunLifecycleControlView.expected_status":                  {string(domain.RunCreated), string(domain.RunRunning), string(domain.RunPaused)},
	"RunLifecycleControlView.applied_status":                   {string(domain.RunRunning), string(domain.RunPaused)},
	"RunExecutionControlRequestView.version":                   {domain.RunExecutionHandoffProtocolVersion},
	"RunExecutionControlView.version":                          {domain.RunExecutionHandoffProtocolVersion},
	"RunExecutionControlView.status":                           {string(domain.RunExecutionHandoffCompleted), string(domain.RunExecutionHandoffFailed)},
	"RunExecutionControlView.run_status":                       runStatuses(),
	"PlanDirectionControlRequestView.version":                  {application.PlanDeliveryControlProtocolVersion},
	"PlanDirectionControlView.version":                         {application.PlanDeliveryControlProtocolVersion},
	"PlanDirectionControlRequestView.manual_acceptance":        {string(domain.PlanDeliveryManualAcceptanceRequired), string(domain.PlanDeliveryManualAcceptanceOnDemand)},
	"PlanDirectionControlView.manual_acceptance":               {string(domain.PlanDeliveryManualAcceptanceRequired), string(domain.PlanDeliveryManualAcceptanceOnDemand)},
	"PlanDeliverySelectionView.manual_acceptance":              {string(domain.PlanDeliveryManualAcceptanceRequired), string(domain.PlanDeliveryManualAcceptanceOnDemand)},
	"CodeHandoffPlanView.manual_acceptance":                    {string(domain.PlanDeliveryManualAcceptanceRequired), string(domain.PlanDeliveryManualAcceptanceOnDemand)},
	"PlanModeTransitionControlRequestView.version":             {application.PlanDeliveryControlProtocolVersion},
	"PlanModeTransitionControlView.version":                    {application.PlanDeliveryControlProtocolVersion},
	"PlanDeliveryTransitionControlRequestView.version":         {application.PlanDeliveryControlProtocolVersion},
	"PlanDeliveryTransitionControlView.version":                {application.PlanDeliveryControlProtocolVersion},
	"PlanDeliveryWorkItemControlRequestView.version":           {application.PlanDeliveryControlProtocolVersion},
	"PlanDeliveryCheckpointControlRequestView.version":         {application.PlanDeliveryControlProtocolVersion},
	"PlanDeliveryWorkItemControlView.version":                  {application.PlanDeliveryControlProtocolVersion},
	"PlanDeliveryCheckpointControlView.version":                {application.PlanDeliveryControlProtocolVersion},
	"PlanDeliveryWorkItemControlView.applied_status":           {string(domain.WorkItemInProgress), string(domain.WorkItemCompleted)},
	"ApprovalQueueView.protocol_version":                       {application.ApprovalQueueProtocolVersion},
	"ApprovalPreviewView.protocol_version":                     {application.ApprovalQueueProtocolVersion},
	"ApprovalPreviewView.effect":                               {"dry_run", "record_git_approval", "file_review_required", "fetch_public_https", "browser_sensitive_action", "mcp_server_and_tool", "command_process", "unavailable"},
	"AgentBrowserStatusView.version":                           {"agent_browser_status.v1"},
	"AgentBrowserStatusView.state":                             {"unavailable", "idle", "starting", "ready", "loading", "busy", "waiting_user", "failed", "closing", "closed", "cleanup_pending"},
	"AgentBrowserCloseRequestView.version":                     {"agent_browser_close.v1"},
	"AgentBrowserScreenshotView.mime_type":                     {"image/png"},
	"ApprovalQueueItemView.status":                             {string(approval.StatusPending), string(approval.StatusApproved), string(approval.StatusDenied)},
	"ApprovalDecisionControlRequestView.version":               {application.ApprovalControlProtocolVersion},
	"ApprovalDecisionControlRequestView.action":                {string(application.ApprovalControlApproveOnce), string(application.ApprovalControlApproveForThread), string(application.ApprovalControlApproveForRun), string(application.ApprovalControlDeny)},
	"ApprovalDecisionControlView.version":                      {application.ApprovalControlProtocolVersion},
	"ApprovalDecisionControlView.action":                       {string(application.ApprovalControlApproveOnce), string(application.ApprovalControlApproveForThread), string(application.ApprovalControlApproveForRun), string(application.ApprovalControlDeny)},
	"ApprovalDecisionControlView.status":                       {string(approval.StatusApproved), string(approval.StatusDenied)},
	"HostCommandProposalReviewView.decision":                   {string(runner.HostCommandReviewApprove), string(runner.HostCommandReviewDeny)},
	"HostCommandProposalView.protocol_version":                 {runner.HostCommandProposalProtocolVersion, runner.RiskEscalationProtocolVersion},
	"HostCommandProposalView.policy_version":                   {runner.HostCommandPolicyVersion, runner.RiskEscalationPolicyVersion},
	"HostCommandProposalView.permission_mode":                  {string(domain.RunExecutionPermissionApproval), string(domain.RunExecutionPermissionWorkspaceAccess)},
	"HostCommandProposalView.network_intent":                   {string(runner.HostNetworkIntentHost)},
	"HostCommandProposalView.state":                            {"waiting_approval", "approved", "denied", "completed", "failed", "invalidated"},
	"HostCommandProposalView.approval_status":                  {string(approval.StatusPending), string(approval.StatusApproved), string(approval.StatusDenied)},
	"HostCommandProposalView.risk_kinds":                       {string(runner.RiskEscalationNetwork), string(runner.RiskEscalationCredential), string(runner.RiskEscalationHostPath), string(runner.RiskEscalationPolicyDenial), string(runner.RiskEscalationNonWhitelistedTool), string(runner.RiskEscalationOtherHighRisk)},
	"HostCommandProposalResultView.status":                     {"completed", "failed"},
	"SupervisorCheckpointView.phase":                           {"idle", "turn_started", "turn_failed", "waiting", "run_completed", "run_failed"},
	"SupervisorCheckpointView.repair_phase":                    {"pending", "exhausted"},
	"RunExecutionLeaseView.status":                             {string(domain.RunExecutionLeaseActive), string(domain.RunExecutionLeaseReleased)},
	"OperatorSteeringMessageView.status":                       {string(domain.OperatorSteeringPending), string(domain.OperatorSteeringCommitted), string(domain.OperatorSteeringCancelled)},
	"PlanDeliveryProposalView.protocol_version":                {domain.PlanDeliveryProtocolVersion},
	"PlanDeliveryProposalView.status":                          {string(domain.PlanDeliveryProposalProposed)},
	"SessionView.status":                                       {"active", "archived"},
	"WorkItemView.status":                                      workItemStatusesOpenAPI(),
	"WorkItemView.priority":                                    {"low", "normal", "high", "critical"},
	"NoteView.category":                                        noteCategoriesOpenAPI(),
	"NoteView.visibility":                                      noteVisibilitiesOpenAPI(),
	"NoteView.status":                                          noteStatusesOpenAPI(),
	"ArtifactView.stream":                                      artifactStreams(),
	"ArtifactView.encoding":                                    {artifact.EncodingUTF8},
	"Manifest.protocol_version":                                {uievidence.ProtocolVersion},
	"SourceBinding.repository_kind":                            {"git", "non_git"},
	"CommandRecipe.protocol_version":                           {runner.CommandRuntimeProtocolVersion},
	"CommandRecipe.profile":                                    {string(runner.CommandRuntimePowerShell), string(runner.CommandRuntimeBash), string(runner.CommandRuntimeProcess)},
	"CommandRecipe.network":                                    {string(runner.CommandRuntimeNetworkDisabled)},
	"CommandRecipe.credentials":                                {string(runner.CommandRuntimeCredentialsNone)},
	"Step.kind":                                                uiEvidenceStepKinds(),
	"Attempt.protocol_version":                                 {uievidence.AttemptProtocolVersion},
	"Attempt.status":                                           {string(uievidence.StatusNotRun), string(uievidence.StatusRunning), string(uievidence.StatusPassed), string(uievidence.StatusFailed), string(uievidence.StatusCancelled), string(uievidence.StatusTimedOut), string(uievidence.StatusInterrupted)},
	"Attempt.failure_stage":                                    uiEvidenceFailureStages(),
	"StepReceipt.protocol_version":                             {uievidence.StepProtocolVersion},
	"StepReceipt.kind":                                         uiEvidenceStepKinds(),
	"StepReceipt.status":                                       {string(uievidence.StatusPassed), string(uievidence.StatusFailed), string(uievidence.StatusCancelled), string(uievidence.StatusTimedOut)},
	"StepReceipt.failure_stage":                                uiEvidenceFailureStages(),
	"ArtifactMetadata.protocol_version":                        {uievidence.ArtifactProtocolVersion},
	"ArtifactMetadata.kind":                                    uiEvidenceArtifactKinds(),
	"ArtifactMetadata.retention_policy":                        {string(uievidence.ArtifactRetentionRunHistory)},
	"BrowserIdentity.driver_protocol":                          {uievidence.DriverProtocolVersion},
	"BrowserIdentity.product":                                  {string(browserruntime.BrowserProductChrome), string(browserruntime.BrowserProductEdge)},
	"UIEvidenceBrowserSelection.product":                       {string(browserruntime.BrowserProductChrome), string(browserruntime.BrowserProductEdge)},
	"UIEvidenceBrowserSelection.channel":                       {string(browserruntime.BrowserChannelStable), string(browserruntime.BrowserChannelBeta), string(browserruntime.BrowserChannelDev), string(browserruntime.BrowserChannelCanary)},
	"Environment.theme":                                        {string(uievidence.ThemeLight), string(uievidence.ThemeDark)},
	"SupervisorToolCallView.status":                            {"pending", "completed", "denied", "failed"},
	"RunEventStreamView.version":                               {RunEventStreamVersion},
	"RunEventPollView.version":                                 {RunEventPollVersion},
	"ModelCancellationView.status":                             {string(domain.ModelCancellationPending), string(domain.ModelCancellationObserved), string(domain.ModelCancellationResolved)},
	"SpecialistModelCancellationView.status":                   {string(domain.ModelCancellationPending), string(domain.ModelCancellationObserved), string(domain.ModelCancellationResolved)},
	"AgentGraphView.protocol_version":                          {domain.AgentGraphProtocolVersion},
	"ExternalSkillProjectionView.protocol_version":             {skills.ExternalSkillProjectionProtocolVersion},
	"ExternalSkillProjectionView.surface":                      {string(domain.ExecutionSurfaceCode), string(domain.ExecutionSurfaceCyber)},
	"ExternalSkillProjectionView.profile":                      {string(domain.ProfileCode), string(domain.ProfileReview), string(domain.ProfileLearn), string(domain.ProfileScript)},
	"ExternalSkillProjectionItemView.trust_class":              {string(skills.PackageTrustOperatorInstalledUntrusted)},
	"AgentNodeView.role":                                       {string(domain.AgentRoleRoot), string(domain.AgentRoleSpecialist)},
	"AgentNodeView.status":                                     {string(domain.AgentReady), string(domain.AgentRunning), string(domain.AgentWaiting), string(domain.AgentCompleted), string(domain.AgentFailed), string(domain.AgentCancelled)},
	"DelegationReviewView.decision":                            {string(domain.SpecialistDelegationApproved), string(domain.SpecialistDelegationRejected)},
	"FanoutPlanView.status":                                    {string(domain.ReadOnlyFanoutPlanned)},
	"FanoutExecutionView.status":                               {string(domain.ReadOnlyFanoutExecutionRunning), string(domain.ReadOnlyFanoutExecutionCompleted), string(domain.ReadOnlyFanoutExecutionFailed), string(domain.ReadOnlyFanoutExecutionCancelled)},
	"FindingReportSummaryView.status":                          {string(domain.FindingReportGenerated)},
	"FindingView.severity":                                     {string(domain.FindingSeverityInfo), string(domain.FindingSeverityLow), string(domain.FindingSeverityMedium), string(domain.FindingSeverityHigh), string(domain.FindingSeverityCritical)},
	"FindingView.status":                                       {string(domain.FindingStatusDraft), string(domain.FindingStatusValidated), string(domain.FindingStatusAccepted), string(domain.FindingStatusFixed), string(domain.FindingStatusRejected)},
	"MessageView.role":                                         {"user", "assistant", "system", "tool"},
	"MessageView.provenance_version":                           {session.LegacyContextProvenanceVersion, session.ContextProvenanceVersion},
	"MessageView.source_kind":                                  {session.SourceOperatorMessage, session.SourceModelResponse, session.SourceGoControl, session.SourceWorkspaceImage, session.SourceUploadedFile, session.SourceWorkspaceFile, session.SourceWorkspaceList, session.SourceWorkspaceDiff, session.SourceToolResult, session.SourceGoCommandResult},

	"ExecutionPermissionCapabilityMatrixView.out_of_scope_policy": {string(domain.ExecutionPermissionOutOfScopeDenied), string(domain.ExecutionPermissionOutOfScopeExactOnce), string(domain.ExecutionPermissionOutOfScopeNotNeeded)},
}

var openAPIFieldMinimums = map[string]float64{
	"ApprovalDecisionControlRequestView.grant_ttl_seconds":                   1,
	"ApprovalDecisionControlRequestView.grant_max_uses":                      1,
	"BoundedCommandGrantView.ttl_seconds":                                    1,
	"BoundedCommandGrantView.max_uses":                                       1,
	"BoundedCommandGrantView.uses_remaining":                                 0,
	"BoundedCommandGrantView.use_ordinal":                                    1,
	"ThreadView.version":                                                     1,
	"ThreadRunView.ordinal":                                                  1,
	"ThreadEventView.id":                                                     1,
	"ThreadMessageView.token_estimate":                                       0,
	"ThreadRunAuditEventView.sequence":                                       1,
	"ThreadLifecycleControlRequestView.expected_version":                     1,
	"ScheduledJobScheduleRequestView.interval_seconds":                       domain.MinScheduledJobIntervalSeconds,
	"ScheduledJobCreateRequestView.max_rounds":                               1,
	"ScheduledJobCreateRequestView.max_model_calls":                          0,
	"ScheduledJobCreateRequestView.max_elapsed_seconds":                      1,
	"ScheduledJobRetryPolicy.max_attempts":                                   1,
	"ScheduledJobRetryPolicy.initial_backoff_seconds":                        1,
	"ScheduledJobRetryPolicy.max_backoff_seconds":                            1,
	"ScheduledJobTransitionRequestView.expected_revision":                    1,
	"ScheduledJobObservationRequestView.expected_revision":                   1,
	"ScheduledJobObservationRequestView.observation_consent_version":         1,
	"ScheduledJobCreateRequestView.observation_consent_version":              0,
	"ScheduledJob.observation_consent_version":                               0,
	"ScheduledJob.revision":                                                  1,
	"ScheduledJob.rounds_completed":                                          0,
	"ScheduledJob.model_calls":                                               0,
	"ScheduledJob.consecutive_unchanged":                                     0,
	"ScheduledJob.last_event_sequence":                                       0,
	"ScheduledJob.active_lease_generation":                                   0,
	"ScheduledJobRound.ordinal":                                              1,
	"ScheduledJobRound.attempt":                                              1,
	"ScheduledJobRound.claim_generation":                                     1,
	"ScheduledJobRound.event_sequence":                                       0,
	"DoctorSnapshot.schema_version":                                          1,
	"DebugQueryResult.after_sequence":                                        0,
	"DebugQueryResult.next_after_sequence":                                   0,
	"DebugQueryResult.limit":                                                 1,
	"DebugQueryResult.scanned":                                               0,
	"DebugTimelineItem.sequence":                                             1,
	"HealthView.schema_version":                                              0,
	"BudgetView.max_turns":                                                   1,
	"BudgetView.max_tokens":                                                  0,
	"BudgetView.max_tool_calls":                                              0,
	"BudgetView.max_cost_usd":                                                0,
	"BudgetView.timeout_seconds":                                             0,
	"RunModeView.revision":                                                   1,
	"RunWebAccessControlRequestView.expected_mode_revision":                  1,
	"RunNetworkAuthorityControlRequestView.expected_mode_revision":           1,
	"RunExecutionProfileView.revision":                                       1,
	"RunExecutionPermissionView.revision":                                    1,
	"RunBrowserCDPPermissionView.revision":                                   1,
	"FullCDPSessionOpenRequestView.expected_execution_permission_revision":   1,
	"FullCDPSessionOpenRequestView.expected_browser_cdp_permission_revision": 1,
	"SupervisorCheckpointView.next_turn":                                     1,
	"SupervisorCheckpointView.input_tokens":                                  0,
	"SupervisorCheckpointView.output_tokens":                                 0,
	"SupervisorCheckpointView.total_tokens":                                  0,
	"SupervisorCheckpointView.execution_millis":                              0,
	"ToolUsageView.consumed":                                                 0,
	"ToolUsageView.limit":                                                    0,
	"RunExecutionLeaseView.generation":                                       1,
	"OperatorSteeringMessageView.sequence":                                   1,
	"OperatorSteeringQueueView.pending":                                      0,
	"OperatorSteeringQueueView.prepared":                                     0,
	"OperatorSteeringQueueView.committed":                                    0,
	"OperatorSteeringQueueView.cancelled":                                    0,
	"PlanDeliveryModuleView.ordinal":                                         1,
	"PlanDeliveryDirectionView.ordinal":                                      1,
	"PlanDeliveryProposalView.mode_revision":                                 1,
	"PlanDeliveryProposalView.version":                                       1,
	"PlanDeliverySelectionItemView.ordinal":                                  1,
	"PlanDeliverySelectionItemView.module_ordinal":                           1,
	"PlanDeliverySelectionView.direction_ordinal":                            1,
	"PlanDeliverySelectionView.version":                                      1,
	"MessageView.token_estimate":                                             0,
	"EventView.sequence":                                                     1,
	"WorkItemView.version":                                                   1,
	"NoteView.version":                                                       1,
	"ArtifactView.size_bytes":                                                0,
	"SupervisorToolCallView.position":                                        0,
	"SupervisorToolCallView.model_attempt":                                   1,
	"SupervisorToolRoundView.turn":                                           1,
	"SupervisorToolRoundView.round":                                          1,
	"SupervisorToolRoundView.model_attempt":                                  1,
	"ModelCancellationRequestView.model_attempt":                             1,
	"ModelCancellationView.model_attempt":                                    1,
	"SpecialistModelCancellationView.model_attempt":                          1,
	"AgentNodeView.depth":                                                    0,
	"AgentNodeView.turn_limit":                                               0,
	"AgentNodeView.token_limit":                                              0,
	"AgentNodeView.turns_used":                                               0,
	"AgentNodeView.tokens_used":                                              0,
	"FindingReportSummaryView.finding_count":                                 0,
	"FindingReportSummaryView.evidence_count":                                0,
	"FindingView.ordinal":                                                    1,
	"FindingView.confidence":                                                 0,

	"ExternalSkillProjectionView.mode_revision":                                 1,
	"ExternalSkillProjectionView.token_budget":                                  1,
	"ExternalSkillProjectionView.token_upper_bound":                             1,
	"ExternalSkillProjectionView.item_count":                                    1,
	"ExternalSkillProjectionItemView.ordinal":                                   1,
	"ExternalSkillProjectionItemView.token_upper_bound":                         1,
	"ExternalSkillProjectionItemView.declared_tool_count":                       0,
	"ExternalSkillDeliveryView.prepared":                                        0,
	"ExternalSkillDeliveryView.committed":                                       0,
	"RunLifecycleControlView.event_sequence_start":                              1,
	"RunLifecycleControlView.event_sequence_end":                                1,
	"RunExecutionControlRequestView.max_steps":                                  1,
	"RunExecutionControlView.max_steps":                                         1,
	"RunExecutionControlView.selected_count":                                    0,
	"RunExecutionControlView.steps_completed":                                   0,
	"RunExecutionControlView.pending_count":                                     0,
	"RunExecutionControlView.prepared_count":                                    0,
	"RunExecutionControlView.committed_count":                                   0,
	"RunExecutionControlView.cancelled_count":                                   0,
	"RunExecutionControlView.completion_event_sequence":                         1,
	"PlanDirectionControlRequestView.direction":                                 1,
	"PlanDirectionControlView.direction":                                        1,
	"PlanDirectionControlView.work_item_count":                                  1,
	"ApprovalQueueItemView.version":                                             1,
	"HostCommandProposalView.timeout_milliseconds":                              1,
	"HostCommandProposalView.permission_revision":                               1,
	"HostCommandProposalView.supervisor_turn":                                   1,
	"HostCommandProposalView.mode_revision":                                     1,
	"HostCommandProposalView.interaction_revision":                              1,
	"HostCommandProposalView.execution_profile_revision":                        1,
	"HostCommandProposalView.max_output_bytes":                                  1,
	"HostCommandProposalView.active_process_limit":                              1,
	"HostCommandProposalView.process_memory_bytes":                              1,
	"HostCommandProposalView.grant_generation":                                  1,
	"HostCommandProposalView.grant_max_uses":                                    1,
	"HostCommandProposalView.grant_uses_remaining":                              0,
	"ProviderDiagnosticView.duration_ms":                                        0,
	"RunWakeScheduleRequestView.max_attempts":                                   1,
	"RunWakeScheduleRequestView.initial_delay_seconds":                          0,
	"RunWakeScheduleRequestView.base_backoff_seconds":                           domain.MinRunWakeBackoffSeconds,
	"RunWakeScheduleRequestView.max_backoff_seconds":                            domain.MinRunWakeBackoffSeconds,
	"RunWakeScheduleRequestView.max_elapsed_seconds":                            domain.MinRunWakeElapsedSeconds,
	"RunWakeIntentView.max_attempts":                                            1,
	"RunWakeIntentView.attempt_count":                                           0,
	"RunWakeExecutionRequestView.max_steps":                                     1,
	"WorkspaceExplorerEntryView.size_bytes":                                     0,
	"WorkspaceExplorerView.total_bytes":                                         0,
	"WorkspaceExplorerView.returned_bytes":                                      0,
	"WorkspaceExplorerView.redaction_count":                                     0,
	"WorkspaceSearchResultView.line":                                            0,
	"WorkspaceSearchView.scanned_entries":                                       0,
	"WorkspaceSearchView.scanned_files":                                         0,
	"WorkspaceSearchView.scanned_bytes":                                         0,
	"RepositoryStateView.staged_count":                                          0,
	"RepositoryStateView.worktree_count":                                        0,
	"RepositoryStateView.untracked_count":                                       0,
	"RepositoryStateView.conflicted_count":                                      0,
	"RepositoryStateView.redaction_count":                                       0,
	"RepositoryDiffItemView.patch_bytes":                                        0,
	"RepositoryDiffItemView.added_lines":                                        0,
	"RepositoryDiffItemView.deleted_lines":                                      0,
	"RepositoryDiffView.returned_count":                                         0,
	"RepositoryDiffView.omitted_count":                                          0,
	"RepositoryDiffView.redaction_count":                                        0,
	"RepositoryDiffView.total_patch_bytes":                                      0,
	"RepositoryCommitFilePreviewView.total_bytes":                               0,
	"RepositoryCommitFilePreviewView.returned_bytes":                            0,
	"RepositoryCommitFilePreviewView.redaction_count":                           0,
	"RepositoryFileHistoryView.scanned_commit_count":                            0,
	"RepositoryFileHistoryView.returned_entry_count":                            0,
	"RepositoryFileHistoryView.redaction_count":                                 0,
	"RepositoryCommitComparisonView.changed_file_count":                         0,
	"RepositoryCommitComparisonView.returned_change_count":                      0,
	"RepositoryCommitComparisonView.omitted_change_count":                       0,
	"RepositoryCommitComparisonView.redaction_count":                            0,
	"VerificationPlanItemCoverageDetailView.plan_item_ordinal":                  1,
	"VerificationPlanItemCoverageDetailView.associated_evidence_count":          0,
	"VerificationPlanItemCoverageDetailView.pass_count":                         0,
	"VerificationPlanItemCoverageDetailView.fail_count":                         0,
	"VerificationPlanItemCoverageDetailView.unknown_count":                      0,
	"VerificationPlanItemCoverageDetailView.latest_association_event_sequence":  0,
	"VerificationAssociationReferenceView.plan_item_ordinal":                    1,
	"VerificationAssociationReferenceView.evidence_event_sequence":              1,
	"VerificationAssociationReferenceView.association_event_sequence":           1,
	"FileEditChangeSetItemView.diff_bytes":                                      0,
	"FileEditChangeSetView.proposed_count":                                      0,
	"FileEditChangeSetView.approved_count":                                      0,
	"FileEditChangeSetView.applied_count":                                       0,
	"FileEditChangeSetView.denied_count":                                        0,
	"FileEditChangeSetView.failed_count":                                        0,
	"FileEditChangeSetView.returned_count":                                      0,
	"FileEditChangeSetView.total_diff_bytes":                                    0,
	"EvidenceAttachmentView.session_message_id":                                 1,
	"VerificationEvidenceInventoryView.pass_count":                              0,
	"VerificationEvidenceInventoryView.fail_count":                              0,
	"VerificationEvidenceInventoryView.unknown_count":                           0,
	"CodeHandoffView.mode_revision":                                             1,
	"CodeHandoffPlanView.direction_count":                                       0,
	"CodeHandoffPlanView.selected_direction":                                    0,
	"CodeHandoffPlanView.module_count":                                          0,
	"CodeHandoffPlanView.pending_count":                                         0,
	"CodeHandoffPlanView.in_progress_count":                                     0,
	"CodeHandoffPlanView.blocked_count":                                         0,
	"CodeHandoffPlanView.completed_count":                                       0,
	"CodeHandoffPlanView.cancelled_count":                                       0,
	"CodeHandoffQueueView.pending":                                              0,
	"CodeHandoffQueueView.prepared":                                             0,
	"CodeHandoffQueueView.committed":                                            0,
	"CodeHandoffQueueView.cancelled":                                            0,
	"CodeHandoffChangeSetView.proposed":                                         0,
	"CodeHandoffChangeSetView.approved":                                         0,
	"CodeHandoffChangeSetView.applied":                                          0,
	"CodeHandoffChangeSetView.denied":                                           0,
	"CodeHandoffChangeSetView.failed":                                           0,
	"CodeHandoffChangeSetView.returned_count":                                   0,
	"CodeHandoffChangeSetView.total_diff_bytes":                                 0,
	"CodeHandoffVerificationView.pass_count":                                    0,
	"CodeHandoffVerificationView.fail_count":                                    0,
	"CodeHandoffVerificationView.unknown_count":                                 0,
	"CodeHandoffVerificationView.returned_count":                                0,
	"CodeHandoffVerificationCoverageView.plan_count":                            0,
	"CodeHandoffVerificationCoverageView.plan_item_count":                       0,
	"CodeHandoffVerificationCoverageView.observed_plan_item_count":              0,
	"CodeHandoffVerificationCoverageView.unobserved_plan_item_count":            0,
	"CodeHandoffVerificationCoverageView.associated_evidence_count":             0,
	"CodeHandoffVerificationCoverageView.contradictory_item_count":              0,
	"CodeHandoffVerificationCoverageView.returned_item_count":                   0,
	"CodeHandoffVerificationCoverageItemView.ordinal":                           1,
	"CodeHandoffVerificationCoverageItemView.associated_evidence_count":         0,
	"CodeHandoffVerificationCoverageItemView.pass_count":                        0,
	"CodeHandoffVerificationCoverageItemView.fail_count":                        0,
	"CodeHandoffVerificationCoverageItemView.unknown_count":                     0,
	"CodeHandoffVerificationCoverageItemView.latest_association_event_sequence": 0,
	"CodeHandoffView.pending_action_count":                                      0,
	"CodeHandoffReportReferenceView.finding_count":                              0,
}

var openAPIFieldMaximums = map[string]float64{
	"ApprovalDecisionControlRequestView.grant_ttl_seconds":              900,
	"ApprovalDecisionControlRequestView.grant_max_uses":                 8,
	"BoundedCommandGrantView.ttl_seconds":                               900,
	"BoundedCommandGrantView.max_uses":                                  8,
	"BoundedCommandGrantView.uses_remaining":                            8,
	"BoundedCommandGrantView.use_ordinal":                               8,
	"HostCommandProposalView.grant_max_uses":                            runner.MaxRiskEscalationGrantUses,
	"HostCommandProposalView.grant_uses_remaining":                      runner.MaxRiskEscalationGrantUses,
	"ScheduledJobScheduleRequestView.interval_seconds":                  domain.MaxScheduledJobIntervalSeconds,
	"ScheduledJobCreateRequestView.max_rounds":                          domain.MaxScheduledJobRounds,
	"ScheduledJobCreateRequestView.observation_consent_version":         1,
	"ScheduledJobObservationRequestView.observation_consent_version":    1,
	"ScheduledJob.observation_consent_version":                          1,
	"ScheduledJobCreateRequestView.max_model_calls":                     domain.MaxScheduledJobModelCalls,
	"ScheduledJobCreateRequestView.max_elapsed_seconds":                 domain.MaxScheduledJobElapsedSeconds,
	"ScheduledJobRetryPolicy.max_attempts":                              domain.MaxScheduledJobAttempts,
	"ScheduledJobRetryPolicy.initial_backoff_seconds":                   domain.MaxScheduledJobBackoffSeconds,
	"ScheduledJobRetryPolicy.max_backoff_seconds":                       domain.MaxScheduledJobBackoffSeconds,
	"ScheduledJob.rounds_completed":                                     domain.MaxScheduledJobRounds,
	"ScheduledJob.model_calls":                                          domain.MaxScheduledJobModelCalls,
	"ScheduledJobRound.ordinal":                                         domain.MaxScheduledJobRounds,
	"ScheduledJobRound.attempt":                                         domain.MaxScheduledJobAttempts,
	"DebugQueryResult.limit":                                            100,
	"DebugQueryResult.scanned":                                          500,
	"FileEditChangeSetItemView.diff_bytes":                              fileedit.MaxDiffBytes,
	"FileEditChangeSetView.proposed_count":                              application.MaxFileEditChangeSetItems,
	"FileEditChangeSetView.approved_count":                              application.MaxFileEditChangeSetItems,
	"FileEditChangeSetView.applied_count":                               application.MaxFileEditChangeSetItems,
	"FileEditChangeSetView.denied_count":                                application.MaxFileEditChangeSetItems,
	"FileEditChangeSetView.failed_count":                                application.MaxFileEditChangeSetItems,
	"FileEditChangeSetView.returned_count":                              application.MaxFileEditChangeSetItems,
	"FileEditChangeSetView.total_diff_bytes":                            application.MaxFileEditChangeSetItems * fileedit.MaxDiffBytes,
	"RunExecutionControlRequestView.max_steps":                          domain.MaxRunExecutionHandoffSteps,
	"RunExecutionControlView.max_steps":                                 domain.MaxRunExecutionHandoffSteps,
	"RunExecutionControlView.selected_count":                            domain.MaxRunExecutionHandoffSteps,
	"RunExecutionControlView.steps_completed":                           domain.MaxRunExecutionHandoffSteps,
	"RunExecutionControlView.pending_count":                             domain.MaxRunExecutionHandoffSteps,
	"RunExecutionControlView.prepared_count":                            domain.MaxRunExecutionHandoffSteps,
	"RunExecutionControlView.committed_count":                           domain.MaxRunExecutionHandoffSteps,
	"RunExecutionControlView.cancelled_count":                           domain.MaxRunExecutionHandoffSteps,
	"PlanDirectionControlRequestView.direction":                         domain.PlanDeliveryDirectionCount,
	"PlanDirectionControlView.direction":                                domain.PlanDeliveryDirectionCount,
	"PlanDirectionControlView.work_item_count":                          domain.MaxPlanDeliveryModules,
	"RunWakeScheduleRequestView.max_attempts":                           domain.MaxRunWakeAttempts,
	"RunWakeScheduleRequestView.initial_delay_seconds":                  domain.MaxRunWakeInitialDelaySeconds,
	"RunWakeScheduleRequestView.base_backoff_seconds":                   domain.MaxRunWakeBackoffSeconds,
	"RunWakeScheduleRequestView.max_backoff_seconds":                    domain.MaxRunWakeBackoffSeconds,
	"RunWakeScheduleRequestView.max_elapsed_seconds":                    domain.MaxRunWakeElapsedSeconds,
	"RunWakeExecutionRequestView.max_steps":                             domain.MaxRunExecutionHandoffSteps,
	"WorkspaceExplorerView.returned_bytes":                              workspace.MaxExplorerProjectedBytes,
	"WorkspaceSearchView.scanned_entries":                               workspace.MaxSearchEntries,
	"WorkspaceSearchView.scanned_files":                                 workspace.MaxSearchFiles,
	"WorkspaceSearchView.scanned_bytes":                                 workspace.MaxSearchReadBytes,
	"RepositoryDiffItemView.patch_bytes":                                repository.MaxDiffPatchBytes,
	"RepositoryDiffView.returned_count":                                 repository.MaxDiffItems,
	"RepositoryDiffView.total_patch_bytes":                              repository.MaxDiffTotalBytes,
	"RepositoryCommitFilePreviewView.total_bytes":                       repository.MaxCommitFilePreviewBytes,
	"RepositoryCommitFilePreviewView.returned_bytes":                    repository.MaxCommitFilePreviewProjectedBytes,
	"RepositoryFileHistoryView.scanned_commit_count":                    repository.MaxFileHistoryCommitScan,
	"RepositoryFileHistoryView.returned_entry_count":                    repository.MaxFileHistoryEntries,
	"RepositoryFileHistoryView.redaction_count":                         repository.MaxFileHistoryCommitScan * repository.MaxCommitSubjectRunes,
	"RepositoryCommitComparisonView.changed_file_count":                 repository.MaxCommitOmittedFiles,
	"RepositoryCommitComparisonView.returned_change_count":              repository.MaxCommitChangedFiles,
	"RepositoryCommitComparisonView.omitted_change_count":               repository.MaxCommitOmittedFiles,
	"RepositoryCommitComparisonView.redaction_count":                    repository.MaxCommitOmittedFiles,
	"VerificationPlanItemCoverageDetailView.plan_item_ordinal":          verification.MaxPlanItems,
	"VerificationPlanItemCoverageDetailView.associated_evidence_count":  verification.MaxSafeCoverageCount,
	"VerificationPlanItemCoverageDetailView.pass_count":                 verification.MaxSafeCoverageCount,
	"VerificationPlanItemCoverageDetailView.fail_count":                 verification.MaxSafeCoverageCount,
	"VerificationPlanItemCoverageDetailView.unknown_count":              verification.MaxSafeCoverageCount,
	"VerificationAssociationReferenceView.plan_item_ordinal":            verification.MaxPlanItems,
	"VerificationEvidenceInventoryView.pass_count":                      verification.MaxInventoryItems,
	"VerificationEvidenceInventoryView.fail_count":                      verification.MaxInventoryItems,
	"VerificationEvidenceInventoryView.unknown_count":                   verification.MaxInventoryItems,
	"CodeHandoffPlanView.direction_count":                               domain.PlanDeliveryDirectionCount,
	"CodeHandoffPlanView.selected_direction":                            domain.PlanDeliveryDirectionCount,
	"CodeHandoffPlanView.module_count":                                  domain.MaxPlanDeliveryModules,
	"CodeHandoffPlanView.pending_count":                                 domain.MaxPlanDeliveryModules,
	"CodeHandoffPlanView.in_progress_count":                             domain.MaxPlanDeliveryModules,
	"CodeHandoffPlanView.blocked_count":                                 domain.MaxPlanDeliveryModules,
	"CodeHandoffPlanView.completed_count":                               domain.MaxPlanDeliveryModules,
	"CodeHandoffPlanView.cancelled_count":                               domain.MaxPlanDeliveryModules,
	"CodeHandoffChangeSetView.proposed":                                 application.MaxFileEditChangeSetItems,
	"CodeHandoffChangeSetView.approved":                                 application.MaxFileEditChangeSetItems,
	"CodeHandoffChangeSetView.applied":                                  application.MaxFileEditChangeSetItems,
	"CodeHandoffChangeSetView.denied":                                   application.MaxFileEditChangeSetItems,
	"CodeHandoffChangeSetView.failed":                                   application.MaxFileEditChangeSetItems,
	"CodeHandoffChangeSetView.returned_count":                           application.MaxFileEditChangeSetItems,
	"CodeHandoffChangeSetView.total_diff_bytes":                         application.MaxFileEditChangeSetItems * fileedit.MaxDiffBytes,
	"CodeHandoffVerificationView.pass_count":                            verification.MaxInventoryItems,
	"CodeHandoffVerificationView.fail_count":                            verification.MaxInventoryItems,
	"CodeHandoffVerificationView.unknown_count":                         verification.MaxInventoryItems,
	"CodeHandoffVerificationView.returned_count":                        application.MaxCodeHandoffVerifyReferences,
	"CodeHandoffVerificationCoverageView.plan_count":                    verification.MaxPlanInventoryItems,
	"CodeHandoffVerificationCoverageView.plan_item_count":               verification.MaxPlanInventoryItems * verification.MaxPlanItems,
	"CodeHandoffVerificationCoverageView.observed_plan_item_count":      verification.MaxPlanInventoryItems * verification.MaxPlanItems,
	"CodeHandoffVerificationCoverageView.unobserved_plan_item_count":    verification.MaxPlanInventoryItems * verification.MaxPlanItems,
	"CodeHandoffVerificationCoverageView.associated_evidence_count":     verification.MaxSafeCoverageCount,
	"CodeHandoffVerificationCoverageView.contradictory_item_count":      verification.MaxPlanInventoryItems * verification.MaxPlanItems,
	"CodeHandoffVerificationCoverageView.returned_item_count":           application.MaxCodeHandoffCoverageItemRefs,
	"CodeHandoffVerificationCoverageItemView.ordinal":                   verification.MaxPlanItems,
	"CodeHandoffVerificationCoverageItemView.associated_evidence_count": verification.MaxSafeCoverageCount,
	"CodeHandoffVerificationCoverageItemView.pass_count":                verification.MaxSafeCoverageCount,
	"CodeHandoffVerificationCoverageItemView.fail_count":                verification.MaxSafeCoverageCount,
	"CodeHandoffVerificationCoverageItemView.unknown_count":             verification.MaxSafeCoverageCount,
	"CodeHandoffView.pending_action_count":                              operatoraction.MaxItems,
}

var openAPIFieldMaxLengths = map[string]int{
	"HostCommandProposalView.network_purpose":                    runner.MaxRiskEscalationReasonRunes,
	"HostCommandProposalView.policy_reason":                      runner.MaxRiskEscalationReasonRunes,
	"HostCommandProposalView.other_risk_reason":                  runner.MaxRiskEscalationReasonRunes,
	"StandardCodePresetControlRequestView.goal":                  domain.MaxRunCreationGoalBytes,
	"StandardCodePresetControlRequestView.expected_trust_digest": 64,
	"StandardCodePresetControlView.trust_digest":                 64,
	"ModelCancellationRequestView.reason":                        domain.MaxModelCancellationReasonRunes,
	"RunExecutionProfileControlRequestView.reason":               domain.MaxRunExecutionProfileReasonRunes,
	"RunExecutionPermissionControlRequestView.reason":            domain.MaxRunExecutionPermissionReasonRunes,
	"ThreadExecutionPermissionControlRequestView.reason":         domain.MaxRunExecutionPermissionReasonRunes,
	"RunExecutionInteractionControlRequestView.reason":           domain.MaxRunExecutionInteractionReasonRunes,
	"RunCreationControlRequestView.goal":                         domain.MaxRunCreationGoalBytes,
	"RunWebAccessControlRequestView.reason":                      domain.MaxRunModeReasonRunes,
	"RunNetworkAuthorityControlRequestView.reason":               domain.MaxRunModeReasonRunes,
	"ThreadCreationControlRequestView.goal":                      domain.MaxRunCreationGoalBytes,
	"ThreadCreationControlRequestView.provider":                  128,
	"ThreadCreationControlRequestView.model":                     256,
	"ThreadModelRouteControlRequestView.provider":                128,
	"ThreadModelRouteControlRequestView.model":                   256,
	"ThreadModelRouteControlRequestView.operation_key":           domain.MaxAgentOperationKeyBytes,
	"SessionMessageControlRequestView.content":                   domain.MaxOperatorSteeringContentBytes,
	"SessionSteeringCancellationRequestView.reason":              domain.MaxOperatorSteeringReasonBytes,
	"ApprovalDecisionControlRequestView.reason":                  approval.MaxReasonRunes,
	"ProviderCredentialRequestView.secret":                       credential.MaxSecretBytes,
	"FileEditProposalSourceView.path":                            workspace.MaxExplorerPathRunes,
	"FileEditProposalSourceView.content":                         workspace.MaxExplorerProjectedBytes,
	"FileEditProposalSourceView.content_sha256":                  64,
	"FileEditProposalRequestView.source_handle":                  43,
	"FileEditProposalRequestView.proposed_text":                  fileedit.MaxContentBytes,
	"MessageView.source_ref":                                     session.MaxContextSourceRefRunes,
	"MessageView.content_sha256":                                 64,
	"SkillPackageInstallRequestView.archive_base64":              base64.StdEncoding.EncodedLen(plugins.MaxArchiveBytes),
	"ExtensionPluginImportSourceView.url":                        4096,
	"ExtensionPluginImportSourceView.commit":                     64,
	"ExtensionPluginImportSourceView.archive_path":               512,
	"UploadedDirectory.root_name":                                255,
	"UploadedDirectoryFile.path":                                 4096,
	"UploadedDirectoryFile.content_base64":                       base64.StdEncoding.EncodedLen(plugins.MaxUncompressedBytes),
	"SkillCatalogPublisherControlRequestView.name":               96,
	"SkillCatalogPublisherControlRequestView.team":               128,
	"SkillCatalogPublisherControlRequestView.public_key":         44,
	"WorkspaceExplorerView.path":                                 workspace.MaxExplorerPathRunes,
	"WorkspaceExplorerProvenanceView.source_ref":                 workspace.MaxExplorerPathRunes,
	"WorkspaceExplorerProvenanceView.content_sha256":             64,
	"WorkspaceExplorerEntryView.name":                            255,
	"WorkspaceExplorerEntryView.path":                            workspace.MaxExplorerPathRunes,
	"WorkspaceSearchResultView.path":                             workspace.MaxExplorerPathRunes,
	"WorkspaceSearchResultView.snippet":                          workspace.MaxSearchSnippetBytes,
	"RepositoryStateView.branch":                                 repository.MaxReferenceRunes,
	"RepositoryStateView.head":                                   12,
	"RepositoryChangeView.path":                                  repository.MaxPathRunes,
	"RepositoryDiffView.base_head":                               12,
	"RepositoryDiffItemView.path":                                repository.MaxPathRunes,
	"RepositoryDiffItemView.patch":                               repository.MaxDiffPatchBytes,
	"RepositoryCommitFilePreviewView.object_id":                  40,
	"RepositoryCommitFilePreviewView.hash":                       12,
	"RepositoryCommitFilePreviewView.path":                       repository.MaxPathRunes,
	"RepositoryCommitFilePreviewView.content":                    repository.MaxCommitFilePreviewProjectedBytes,
	"RepositoryCommitFilePreviewProvenanceView.source_ref":       repository.MaxPathRunes,
	"RepositoryCommitFilePreviewProvenanceView.content_sha256":   64,
	"RepositoryFileHistoryView.head":                             12,
	"RepositoryFileHistoryView.path":                             repository.MaxPathRunes,
	"RepositoryFileHistoryEntryView.object_id":                   40,
	"RepositoryFileHistoryEntryView.hash":                        12,
	"RepositoryCommitComparisonView.base_object_id":              40,
	"RepositoryCommitComparisonView.base_hash":                   12,
	"RepositoryCommitComparisonView.head_object_id":              40,
	"RepositoryCommitComparisonView.head_hash":                   12,
	"VerificationPlanItemCoverageDetailView.plan_sha256":         64,
	"VerificationPlanItemCoverageDetailView.plan_item_sha256":    64,
	"VerificationAssociationReferenceView.plan_item_sha256":      64,
	"FileEditChangeSetItemView.path":                             4096,
	"FileEditChangeSetItemView.destination_path":                 4096,
	"FileEditPreviewView.path":                                   4096,
	"FileEditPreviewView.destination_path":                       4096,
	"FileEditPreviewView.original_hash":                          64,
	"FileEditPreviewView.proposed_hash":                          64,
	"FileEditPreviewView.destination_original_hash":              64,
	"FileEditPreviewView.destination_proposed_hash":              64,
	"AgentCodeToolCapabilityView.refusal_reason":                 512,
	"EvidenceAttachmentRequestView.source_ref":                   workspace.MaxExplorerPathRunes,
	"EvidenceAttachmentRequestView.content_sha256":               64,
	"EvidenceAttachmentView.source_ref":                          workspace.MaxExplorerPathRunes,
	"EvidenceAttachmentView.content_sha256":                      64,
	"EvidenceInventoryItemView.source_ref":                       workspace.MaxExplorerPathRunes,
	"EvidenceInventoryItemView.content_sha256":                   64,
	"VerificationEvidenceRequestView.title":                      verification.MaxTitleRunes,
	"VerificationEvidenceRequestView.summary":                    verification.MaxSummaryRunes,
	"VerificationEvidenceItemView.title":                         verification.MaxTitleRunes,
	"VerificationEvidenceItemView.summary":                       verification.MaxSummaryRunes,
	"VerificationEvidenceItemView.summary_sha256":                64,
	"VerificationEvidenceControlView.title":                      verification.MaxTitleRunes,
	"VerificationEvidenceControlView.summary":                    verification.MaxSummaryRunes,
	"VerificationEvidenceControlView.summary_sha256":             64,
	"CodeHandoffVerificationCoverageItemView.plan_sha256":        64,
	"CodeHandoffVerificationCoverageItemView.item_sha256":        64,
}

func init() {
	openAPIFieldEnums["VerificationSnapshotExportView.protocol_version"] =
		[]string{application.VerificationSnapshotExportProtocolVersion}
	openAPIFieldEnums["VerificationSnapshotExportView.snapshot_protocol_version"] =
		[]string{application.VerificationSnapshotProtocolVersion}
	openAPIFieldEnums["VerificationSnapshotExportView.format"] = []string{
		application.VerificationSnapshotExportFormatJSON,
		application.VerificationSnapshotExportFormatMarkdown,
	}

	openAPIFieldMinimums["VerificationSnapshotExportView.plan_item_ordinal"] = 1
	openAPIFieldMinimums["VerificationSnapshotExportView.snapshot_high_water_event_sequence"] = 0
	openAPIFieldMinimums["VerificationSnapshotExportView.associated_evidence_count"] = 0
	openAPIFieldMinimums["VerificationSnapshotExportView.pass_count"] = 0
	openAPIFieldMinimums["VerificationSnapshotExportView.fail_count"] = 0
	openAPIFieldMinimums["VerificationSnapshotExportView.unknown_count"] = 0
	openAPIFieldMinimums["VerificationSnapshotExportView.returned_association_count"] = 0
	openAPIFieldMinimums["VerificationSnapshotExportView.content_bytes"] = 1

	openAPIFieldMaximums["VerificationSnapshotExportView.plan_item_ordinal"] =
		verification.MaxPlanItems
	openAPIFieldMaximums["VerificationSnapshotExportView.snapshot_high_water_event_sequence"] =
		float64(1<<53 - 1)
	openAPIFieldMaximums["VerificationSnapshotExportView.associated_evidence_count"] =
		verification.MaxSafeCoverageCount
	openAPIFieldMaximums["VerificationSnapshotExportView.pass_count"] =
		verification.MaxSafeCoverageCount
	openAPIFieldMaximums["VerificationSnapshotExportView.fail_count"] =
		verification.MaxSafeCoverageCount
	openAPIFieldMaximums["VerificationSnapshotExportView.unknown_count"] =
		verification.MaxSafeCoverageCount
	openAPIFieldMaximums["VerificationSnapshotExportView.returned_association_count"] =
		verification.MaxCoverageAssociations
	openAPIFieldMaximums["VerificationSnapshotExportView.content_bytes"] =
		application.MaxVerificationSnapshotExportBytes

	openAPIFieldMaxLengths["VerificationSnapshotExportView.plan_sha256"] = 64
	openAPIFieldMaxLengths["VerificationSnapshotExportView.plan_item_sha256"] = 64
	openAPIFieldMaxLengths["VerificationSnapshotExportView.content_sha256"] = 64
	openAPIFieldMaxLengths["VerificationSnapshotExportView.filename"] = 255
	openAPIFieldMaxLengths["VerificationSnapshotExportView.content"] =
		application.MaxVerificationSnapshotExportBytes

	for _, typeName := range []string{"VerificationSnapshotReceiptView",
		"VerificationSnapshotReceiptControlView"} {
		openAPIFieldEnums[typeName+".protocol_version"] =
			[]string{verification.SnapshotReceiptProtocolVersion}
		openAPIFieldEnums[typeName+".format"] = []string{
			application.VerificationSnapshotExportFormatJSON,
			application.VerificationSnapshotExportFormatMarkdown,
		}
		openAPIFieldMinimums[typeName+".plan_item_ordinal"] = 1
		openAPIFieldMaximums[typeName+".plan_item_ordinal"] = verification.MaxPlanItems
		for _, field := range []string{"snapshot_high_water_event_sequence",
			"associated_evidence_count", "pass_count", "fail_count", "unknown_count",
			"returned_association_count"} {
			openAPIFieldMinimums[typeName+"."+field] = 0
		}
		openAPIFieldMinimums[typeName+".content_bytes"] = 1
		openAPIFieldMinimums[typeName+".receipt_event_sequence"] = 1
		openAPIFieldMaximums[typeName+".snapshot_high_water_event_sequence"] = float64(1<<53 - 1)
		openAPIFieldMaximums[typeName+".associated_evidence_count"] = verification.MaxSafeCoverageCount
		openAPIFieldMaximums[typeName+".pass_count"] = verification.MaxSafeCoverageCount
		openAPIFieldMaximums[typeName+".fail_count"] = verification.MaxSafeCoverageCount
		openAPIFieldMaximums[typeName+".unknown_count"] = verification.MaxSafeCoverageCount
		openAPIFieldMaximums[typeName+".returned_association_count"] = verification.MaxCoverageAssociations
		openAPIFieldMaximums[typeName+".content_bytes"] = verification.MaxSnapshotReceiptContentBytes
		openAPIFieldMaximums[typeName+".receipt_event_sequence"] = float64(1<<53 - 1)
		openAPIFieldMaxLengths[typeName+".plan_sha256"] = 64
		openAPIFieldMaxLengths[typeName+".plan_item_sha256"] = 64
		openAPIFieldMaxLengths[typeName+".content_sha256"] = 64
	}
	openAPIFieldEnums["VerificationSnapshotReceiptRequestView.version"] =
		[]string{verification.SnapshotReceiptProtocolVersion}
	openAPIFieldEnums["VerificationSnapshotReceiptRequestView.format"] = []string{
		application.VerificationSnapshotExportFormatJSON,
		application.VerificationSnapshotExportFormatMarkdown,
	}
	openAPIFieldMinimums["VerificationSnapshotReceiptRequestView.plan_item_ordinal"] = 1
	openAPIFieldMaximums["VerificationSnapshotReceiptRequestView.plan_item_ordinal"] =
		verification.MaxPlanItems
	openAPIFieldMinimums["VerificationSnapshotReceiptRequestView.snapshot_high_water_event_sequence"] = 0
	openAPIFieldMaximums["VerificationSnapshotReceiptRequestView.snapshot_high_water_event_sequence"] =
		float64(1<<53 - 1)
	openAPIFieldMaxLengths["VerificationSnapshotReceiptRequestView.content_sha256"] = 64
	openAPIFieldEnums["VerificationSnapshotReceiptInventoryView.protocol_version"] =
		[]string{verification.SnapshotReceiptInventoryProtocolVersion}

	for _, typeName := range []string{"VerificationSnapshotReceiptReviewView",
		"VerificationSnapshotReceiptReviewControlView"} {
		openAPIFieldEnums[typeName+".protocol_version"] =
			[]string{verification.SnapshotReceiptReviewProtocolVersion}
		openAPIFieldEnums[typeName+".decision"] = []string{
			string(verification.SnapshotReceiptReviewMetadataConfirmed),
			string(verification.SnapshotReceiptReviewMetadataDisputed),
		}
		openAPIFieldMinimums[typeName+".receipt_event_sequence"] = 1
		openAPIFieldMaximums[typeName+".receipt_event_sequence"] = float64(1<<53 - 1)
		openAPIFieldMinimums[typeName+".review_event_sequence"] = 1
		openAPIFieldMaximums[typeName+".review_event_sequence"] = float64(1<<53 - 1)
		openAPIFieldMaxLengths[typeName+".receipt_content_sha256"] = 64
	}
	openAPIFieldEnums["VerificationSnapshotReceiptReviewRequestView.version"] =
		[]string{verification.SnapshotReceiptReviewProtocolVersion}
	openAPIFieldEnums["VerificationSnapshotReceiptReviewRequestView.decision"] = []string{
		string(verification.SnapshotReceiptReviewMetadataConfirmed),
		string(verification.SnapshotReceiptReviewMetadataDisputed),
	}
	openAPIFieldMinimums["VerificationSnapshotReceiptReviewRequestView.receipt_event_sequence"] = 1
	openAPIFieldMaximums["VerificationSnapshotReceiptReviewRequestView.receipt_event_sequence"] =
		float64(1<<53 - 1)
	openAPIFieldMaxLengths["VerificationSnapshotReceiptReviewRequestView.receipt_content_sha256"] = 64
	openAPIFieldEnums["VerificationSnapshotReceiptReviewInventoryView.protocol_version"] =
		[]string{verification.SnapshotReceiptReviewInventoryProtocolVersion}
	openAPIFieldEnums["CodeHandoffSnapshotReceiptReviewsView.protocol_version"] =
		[]string{verification.SnapshotReceiptReviewInventoryProtocolVersion}
	openAPIFieldEnums["CodeHandoffSnapshotReceiptReviewReferenceView.decision"] = []string{
		string(verification.SnapshotReceiptReviewMetadataConfirmed),
		string(verification.SnapshotReceiptReviewMetadataDisputed),
	}
	for _, field := range []string{"metadata_confirmed_count", "metadata_disputed_count",
		"returned_count"} {
		openAPIFieldMinimums["CodeHandoffSnapshotReceiptReviewsView."+field] = 0
		openAPIFieldMaximums["CodeHandoffSnapshotReceiptReviewsView."+field] =
			verification.MaxSnapshotReceiptReviewHistory
	}
	for _, field := range []string{"receipt_event_sequence", "review_event_sequence"} {
		openAPIFieldMinimums["CodeHandoffSnapshotReceiptReviewReferenceView."+field] = 1
		openAPIFieldMaximums["CodeHandoffSnapshotReceiptReviewReferenceView."+field] =
			float64(1<<53 - 1)
	}
	openAPIFieldMaxLengths["CodeHandoffSnapshotReceiptReviewReferenceView.receipt_content_sha256"] = 64
}

func runStatuses() []string {
	return []string{string(domain.RunCreated), string(domain.RunPreparing), string(domain.RunRunning),
		string(domain.RunWaitingApproval), string(domain.RunPaused), string(domain.RunCompleted),
		string(domain.RunFailed), string(domain.RunCancelled)}
}

func workItemStatusesOpenAPI() []string {
	return []string{string(domain.WorkItemPending), string(domain.WorkItemInProgress),
		string(domain.WorkItemBlocked), string(domain.WorkItemCompleted), string(domain.WorkItemCancelled)}
}

func noteStatusesOpenAPI() []string {
	return []string{string(domain.NoteActive), string(domain.NoteArchived)}
}

func noteCategoriesOpenAPI() []string {
	return []string{string(domain.NoteObservation), string(domain.NoteHypothesis), string(domain.NoteDecision),
		string(domain.NoteSummary), string(domain.NoteReference)}
}

func noteVisibilitiesOpenAPI() []string {
	return []string{string(domain.NoteVisibilityRun), string(domain.NoteVisibilityRoot),
		string(domain.NoteVisibilityOwner)}
}

func artifactStreams() []string {
	return []string{string(artifact.StreamStdout), string(artifact.StreamStderr)}
}

func uiEvidenceFailureStages() []string {
	return []string{string(uievidence.FailureNone), string(uievidence.FailureBuild),
		string(uievidence.FailureLaunch), string(uievidence.FailureReadiness),
		string(uievidence.FailureNavigation), string(uievidence.FailureSelector),
		string(uievidence.FailureAssertion), string(uievidence.FailureConsole),
		string(uievidence.FailureNetwork), string(uievidence.FailureCapture),
		string(uievidence.FailureCleanup)}
}

func uiEvidenceStepKinds() []string {
	return []string{string(uievidence.StepNavigate), string(uievidence.StepClick),
		string(uievidence.StepType), string(uievidence.StepAssertPresent),
		string(uievidence.StepAssertAbsent), string(uievidence.StepCapture)}
}

func uiEvidenceArtifactKinds() []string {
	return []string{string(uievidence.ArtifactScreenshot), string(uievidence.ArtifactDOM),
		string(uievidence.ArtifactAccessibility), string(uievidence.ArtifactConsole),
		string(uievidence.ArtifactNetwork), string(uievidence.ArtifactPerformance)}
}

func sortedOpenAPIPaths() []string {
	seen := map[string]struct{}{}
	paths := make([]string, 0, len(openAPIOperationSpecs()))
	for _, spec := range openAPIOperationSpecs() {
		if _, exists := seen[spec.Path]; exists {
			continue
		}
		seen[spec.Path] = struct{}{}
		paths = append(paths, spec.Path)
	}
	sort.Strings(paths)
	return paths
}
