package codeintel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/workspaceidentity"
)

const (
	ToolWorkspaceSymbols = "code_workspace_symbols"
	ToolDocumentSymbols  = "code_document_symbols"
	ToolDefinition       = "code_definition"
	ToolReferences       = "code_references"
	ToolImplementation   = "code_implementation"
	ToolHover            = "code_hover"
	ToolSignatureHelp    = "code_signature_help"
	ToolDiagnostics      = "code_diagnostics"
	ToolCallHierarchy    = "code_call_hierarchy"
	ToolTypeHierarchy    = "code_type_hierarchy"
)

type Request struct {
	// ExpectedDocumentSHA256 binds automatic post-edit feedback to the applied
	// bytes. It is an internal constraint, never a model-provided authority.
	ExpectedDocumentSHA256 string                       `json:"-"`
	PathPolicy             workspaceidentity.PathPolicy `json:"-"`
	Tool                   string                       `json:"tool"`
	WorkspaceID            string                       `json:"workspace_id"`
	WorkspaceRoot          string                       `json:"-"`
	ServerID               string                       `json:"server_id"`
	ServerGeneration       string                       `json:"server_generation"`
	CapabilityFingerprint  string                       `json:"capability_fingerprint"`
	Path                   string                       `json:"path,omitempty"`
	Query                  string                       `json:"query,omitempty"`
	Position               Position                     `json:"position,omitempty"`
	Direction              string                       `json:"direction,omitempty"`
	IncludeDeclaration     bool                         `json:"include_declaration,omitempty"`
	Limit                  int                          `json:"limit"`
	Cursor                 string                       `json:"cursor,omitempty"`
}

func (r Request) Validate() error {
	if r.ExpectedDocumentSHA256 != "" && (r.Tool != ToolDiagnostics || !validDigest(r.ExpectedDocumentSHA256)) {
		return apperror.New(apperror.CodeInvalidArgument, "expected document hash requires a diagnostics request")
	}
	if err := r.PathPolicy.Validate(); err != nil {
		return err
	}
	if r.Path != "" {
		if err := r.PathPolicy.Check(r.Path); err != nil {
			return err
		}
	}
	if !validTool(r.Tool) || !validIdentity(r.WorkspaceID) ||
		!validIdentity(r.ServerID) || !validDigest(r.ServerGeneration) ||
		!validDigest(r.CapabilityFingerprint) || strings.TrimSpace(r.WorkspaceRoot) == "" ||
		r.Limit < 1 || r.Limit > MaxResultItems || len(r.Cursor) > 8192 ||
		!utf8.ValidString(r.Cursor) || strings.ContainsRune(r.Cursor, 0) {
		return apperror.New(apperror.CodeInvalidArgument,
			"code-intel request identity, capability binding, or page is invalid")
	}
	if r.Query != strings.TrimSpace(r.Query) || !utf8.ValidString(r.Query) ||
		utf8.RuneCountInString(r.Query) > 256 || strings.ContainsRune(r.Query, 0) {
		return apperror.New(apperror.CodeInvalidArgument,
			"code-intel query must be bounded normalized UTF-8")
	}
	documentTool := r.Tool != ToolWorkspaceSymbols
	if documentTool {
		if r.Path == "" || r.Path != strings.TrimSpace(r.Path) ||
			utf8.RuneCountInString(r.Path) > 512 {
			return apperror.New(apperror.CodeInvalidArgument,
				"code-intel document path is required")
		}
	} else if r.Path != "" {
		return apperror.New(apperror.CodeInvalidArgument,
			"workspace symbol query cannot carry a document path")
	}
	positionTool := r.Tool == ToolDefinition || r.Tool == ToolReferences ||
		r.Tool == ToolImplementation || r.Tool == ToolHover || r.Tool == ToolSignatureHelp ||
		r.Tool == ToolCallHierarchy || r.Tool == ToolTypeHierarchy
	if positionTool && r.Position.Validate() != nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"code-intel position is invalid")
	}
	if !positionTool && r.Position != (Position{}) {
		return apperror.New(apperror.CodeInvalidArgument,
			"code-intel tool does not accept a position")
	}
	switch r.Tool {
	case ToolCallHierarchy:
		if r.Direction != "incoming" && r.Direction != "outgoing" && r.Direction != "both" {
			return apperror.New(apperror.CodeInvalidArgument,
				"call hierarchy direction must be incoming, outgoing, or both")
		}
	case ToolTypeHierarchy:
		if r.Direction != "supertypes" && r.Direction != "subtypes" && r.Direction != "both" {
			return apperror.New(apperror.CodeInvalidArgument,
				"type hierarchy direction must be supertypes, subtypes, or both")
		}
	default:
		if r.Direction != "" {
			return apperror.New(apperror.CodeInvalidArgument,
				"code-intel tool does not accept a hierarchy direction")
		}
	}
	if r.Tool != ToolReferences && r.IncludeDeclaration {
		return apperror.New(apperror.CodeInvalidArgument,
			"only references accepts include_declaration")
	}
	return nil
}

func validTool(value string) bool {
	switch value {
	case ToolWorkspaceSymbols, ToolDocumentSymbols, ToolDefinition, ToolReferences,
		ToolImplementation, ToolHover, ToolSignatureHelp, ToolDiagnostics,
		ToolCallHierarchy, ToolTypeHierarchy:
		return true
	default:
		return false
	}
}

func (m *Manager) Execute(ctx context.Context, request Request) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	key := serverKey(request.WorkspaceID, request.ServerID)
	var current *client
	var err error
	if request.ExpectedDocumentSHA256 != "" {
		current, err = m.ensureClientGeneration(ctx, key, request.WorkspaceRoot, request.ServerGeneration)
	} else {
		current, err = m.ensureClient(ctx, key, request.WorkspaceRoot)
	}
	if err != nil {
		return Result{}, err
	}
	snapshot := cloneSnapshot(current.snapshot)
	if snapshot.Generation != request.ServerGeneration ||
		snapshot.CapabilityFingerprint != request.CapabilityFingerprint {
		return Result{}, apperror.New(apperror.CodeConflict,
			"code-intel server generation or capability fingerprint is stale")
	}
	if !snapshotSupportsTool(snapshot, request.Tool) {
		return Result{}, apperror.New(apperror.CodeFailedPrecondition,
			"language server did not negotiate the requested semantic capability")
	}

	before, err := captureWorkspaceBinding(ctx, request.WorkspaceRoot, request.WorkspaceID)
	if err != nil {
		return Result{}, err
	}
	var document *documentBinding
	if request.Path != "" {
		opened, openErr := current.ensureDocument(ctx, request.Path)
		if openErr != nil {
			return Result{}, openErr
		}
		document = &opened
		if request.ExpectedDocumentSHA256 != "" && document.SHA256 != request.ExpectedDocumentSHA256 {
			return Result{}, apperror.New(apperror.CodeConflict, "document no longer matches the applied version")
		}
	}
	queryFingerprint := requestFingerprint(request, before, document, snapshot)
	requestTimeout := time.Duration(current.descriptor.RequestTimeoutMillis) * time.Millisecond
	queryCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	items, edges, content, warnings, partial, queryErr := current.query(queryCtx, request, document)
	contextErr := queryCtx.Err()
	cancel()
	if queryErr != nil {
		if contextErr != nil || transportFailure(current.transport, queryErr) {
			m.invalidate(key, current, queryErr)
		}
		if errors.Is(contextErr, context.DeadlineExceeded) {
			return Result{}, apperror.New(apperror.CodeDeadlineExceeded,
				"language server request exceeded its reviewed timeout")
		}
		if errors.Is(contextErr, context.Canceled) || errors.Is(queryErr, context.Canceled) {
			return Result{}, apperror.Normalize(context.Canceled)
		}
		return Result{}, apperror.Wrap(apperror.CodeUnavailable,
			"language server semantic query failed", queryErr)
	}

	after, err := captureWorkspaceBinding(ctx, request.WorkspaceRoot, request.WorkspaceID)
	if err != nil {
		return Result{}, err
	}
	if !sameWorkspaceBinding(before, after) {
		return Result{}, apperror.New(apperror.CodeConflict,
			"Workspace commit, branch, dirty state, or root changed during the semantic query")
	}
	if document != nil {
		observed, err := captureDocumentBinding(request.WorkspaceRoot, request.Path,
			document.Version)
		if err != nil || observed.SHA256 != document.SHA256 || observed.URI != document.URI {
			return Result{}, apperror.New(apperror.CodeConflict,
				"document changed during the semantic query")
		}
	}
	m.mu.Lock()
	stillCurrent := m.clients[key] == current && !m.closing && !m.quarantined
	m.mu.Unlock()
	if !stillCurrent {
		return Result{}, apperror.New(apperror.CodeConflict,
			"language server configuration changed during the semantic query")
	}

	sortEvidence(items, edges)
	items, edges = filterProjectEvidence(items, edges, request.PathPolicy)
	paged, pagedEdges, page, err := paginateEvidence(items, edges, request.Cursor,
		request.Limit, queryFingerprint)
	if err != nil {
		return Result{}, err
	}
	state := EvidenceCurrent
	if partial || page.Truncated || len(warnings) > 0 {
		state = EvidencePartial
	}
	result := Result{ProtocolVersion: ProtocolVersion, Tool: request.Tool, State: state,
		EvidenceLevel: "semantic_language_server",
		Provenance:    newProvenance(before, document, snapshot, queryFingerprint),
		Items:         paged, Edges: pagedEdges, Content: content, Page: page,
		Warnings: boundedWarnings(warnings)}
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	if len(raw) > MaxResultBytes {
		return Result{}, apperror.New(apperror.CodeResourceExhausted,
			"semantic result exceeds the bounded result envelope")
	}
	return result, nil
}

func (c *client) ensureDocument(ctx context.Context, path string) (documentBinding, error) {
	if c == nil || c.transport == nil {
		return documentBinding{}, errors.New("language server client is unavailable")
	}
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	c.mu.Lock()
	previous, exists := c.documents[path]
	openCount := len(c.documents)
	c.mu.Unlock()
	if !exists && openCount >= MaxOpenDocuments {
		return documentBinding{}, apperror.New(apperror.CodeResourceExhausted,
			"language server open-document limit was reached")
	}
	version := 1
	if exists {
		version = previous.Version
	}
	observed, err := captureDocumentBinding(c.workspace.Root, path, version)
	if err != nil {
		return documentBinding{}, err
	}
	languageID, supported := c.descriptor.LanguageForPath(path)
	if !supported {
		return documentBinding{}, apperror.New(apperror.CodeFailedPrecondition,
			"reviewed language server is not configured for this document extension")
	}
	if exists && previous.SHA256 == observed.SHA256 && previous.URI == observed.URI {
		return previous, nil
	}
	if exists {
		observed.Version = previous.Version + 1
	}
	c.mu.Lock()
	c.documents[path] = observed
	delete(c.diagnostics, observed.URI)
	c.mu.Unlock()
	rollback := func() {
		c.mu.Lock()
		if exists {
			c.documents[path] = previous
		} else {
			delete(c.documents, path)
		}
		delete(c.diagnostics, observed.URI)
		c.mu.Unlock()
	}
	if exists {
		if err := c.transport.notify(ctx, "textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": observed.URI, "version": observed.Version},
			"contentChanges": []map[string]string{{"text": observed.Text}},
		}); err != nil {
			rollback()
			return documentBinding{}, err
		}
	} else {
		if err := c.transport.notify(ctx, "textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{"uri": observed.URI, "languageId": languageID,
				"version": observed.Version, "text": observed.Text},
		}); err != nil {
			rollback()
			return documentBinding{}, err
		}
	}
	return observed, nil
}

func (c *client) query(ctx context.Context, request Request, document *documentBinding) (
	[]EvidenceItem, []GraphEdge, string, []string, bool, error,
) {
	switch request.Tool {
	case ToolWorkspaceSymbols:
		var raw json.RawMessage
		if err := c.transport.request(ctx, "workspace/symbol",
			map[string]string{"query": request.Query}, &raw); err != nil {
			return nil, nil, "", nil, false, err
		}
		items, warnings := parseSymbols(c.workspace.Root, raw, "")
		return items, nil, "", warnings, len(warnings) > 0, nil
	case ToolDocumentSymbols:
		var raw json.RawMessage
		if err := c.transport.request(ctx, "textDocument/documentSymbol",
			textDocumentParams(document), &raw); err != nil {
			return nil, nil, "", nil, false, err
		}
		items, warnings := parseSymbols(c.workspace.Root, raw, document.Path)
		return items, nil, "", warnings, len(warnings) > 0, nil
	case ToolDefinition, ToolImplementation, ToolReferences:
		return c.queryLocations(ctx, request, document)
	case ToolHover:
		var raw json.RawMessage
		if err := c.transport.request(ctx, "textDocument/hover",
			positionParams(document, request.Position), &raw); err != nil {
			return nil, nil, "", nil, false, err
		}
		content, item, warnings := parseHover(c.workspace.Root, raw, document.Path)
		items := []EvidenceItem{}
		if item != nil {
			items = append(items, *item)
		}
		return items, nil, content, warnings, len(warnings) > 0, nil
	case ToolSignatureHelp:
		var raw json.RawMessage
		if err := c.transport.request(ctx, "textDocument/signatureHelp",
			positionParams(document, request.Position), &raw); err != nil {
			return nil, nil, "", nil, false, err
		}
		items, content, warnings := parseSignatureHelp(c.workspace.Root, raw, document.Path)
		return items, nil, content, warnings, len(warnings) > 0, nil
	case ToolDiagnostics:
		return c.queryDiagnostics(ctx, document, request.ExpectedDocumentSHA256 != "")
	case ToolCallHierarchy:
		return c.queryCallHierarchy(ctx, request, document)
	case ToolTypeHierarchy:
		return c.queryTypeHierarchy(ctx, request, document)
	default:
		return nil, nil, "", nil, false, errors.New("unsupported semantic tool")
	}
}

func (c *client) queryLocations(ctx context.Context, request Request,
	document *documentBinding,
) ([]EvidenceItem, []GraphEdge, string, []string, bool, error) {
	method := "textDocument/definition"
	params := positionParams(document, request.Position)
	switch request.Tool {
	case ToolImplementation:
		method = "textDocument/implementation"
	case ToolReferences:
		method = "textDocument/references"
		params["context"] = map[string]bool{"includeDeclaration": request.IncludeDeclaration}
	}
	var raw json.RawMessage
	if err := c.transport.request(ctx, method, params, &raw); err != nil {
		return nil, nil, "", nil, false, err
	}
	items, warnings := parseLocations(c.workspace.Root, raw, request.Tool)
	return items, nil, "", warnings, len(warnings) > 0, nil
}

// ErrDiagnosticsPending means no response can yet be bound to this document
// version. It must not be presented as a successful empty diagnostic list.
var ErrDiagnosticsPending = errors.New("diagnostics for the current document version are pending")

func (c *client) queryDiagnostics(ctx context.Context, document *documentBinding, requireVersion bool) (
	[]EvidenceItem, []GraphEdge, string, []string, bool, error,
) {
	var raw json.RawMessage
	err := c.transport.request(ctx, "textDocument/diagnostic", map[string]any{
		"textDocument": map[string]string{"uri": document.URI}}, &raw)
	if err == nil {
		if requireVersion {
			var report struct {
				Kind  string          `json:"kind"`
				Items json.RawMessage `json:"items"`
			}
			if json.Unmarshal(raw, &report) != nil || (report.Kind != "" && report.Kind != "full") || len(report.Items) == 0 || string(report.Items) == "null" {
				return nil, nil, "", nil, false, ErrDiagnosticsPending
			}
		}
		items, warnings := parseDiagnostics(c.workspace.Root, raw, document.Path, document.URI)
		return items, nil, "", warnings, len(warnings) > 0, nil
	}
	if ctx.Err() != nil || transportFailure(c.transport, err) {
		return nil, nil, "", nil, false, err
	}
	// Push-only servers do not advertise a pull diagnostic provider. Give the
	// bounded publishDiagnostics notification one short scheduling window.
	select {
	case <-ctx.Done():
		return nil, nil, "", nil, false, ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}
	c.mu.Lock()
	published := append(json.RawMessage(nil), c.diagnostics[document.URI]...)
	c.mu.Unlock()
	if len(published) == 0 {
		if requireVersion {
			return nil, nil, "", nil, false, ErrDiagnosticsPending
		}
		return []EvidenceItem{}, nil, "", []string{
			"server supports document sync but returned neither pull nor published diagnostics"}, true, nil
	}
	if requireVersion {
		var header struct {
			Version *int `json:"version"`
		}
		if json.Unmarshal(published, &header) != nil || header.Version == nil || *header.Version != document.Version {
			return nil, nil, "", nil, false, ErrDiagnosticsPending
		}
	}
	items, warnings := parseDiagnostics(c.workspace.Root, published, document.Path, document.URI)
	warnings = append(warnings, "diagnostics came from bounded publishDiagnostics fallback")
	return items, nil, "", warnings, true, nil
}

func textDocumentParams(document *documentBinding) map[string]any {
	return map[string]any{"textDocument": map[string]string{"uri": document.URI}}
}

func positionParams(document *documentBinding, position Position) map[string]any {
	return map[string]any{"textDocument": map[string]string{"uri": document.URI},
		"position": position}
}

func requestFingerprint(request Request, workspace workspaceBinding,
	document *documentBinding, snapshot CapabilitySnapshot,
) string {
	parts := []string{ProtocolVersion, request.Tool, request.WorkspaceID, request.ServerID,
		request.ServerGeneration, request.CapabilityFingerprint, request.Path, request.Query,
		fmt.Sprint(request.Position.Line), fmt.Sprint(request.Position.Character), request.Direction,
		fmt.Sprint(request.IncludeDeclaration), fmt.Sprint(request.Limit),
		workspace.RootFingerprint, workspace.Commit, workspace.Branch,
		fmt.Sprint(workspace.Dirty), workspace.DirtyDigest, snapshot.DescriptorFingerprint}
	if document != nil {
		parts = append(parts, document.URI, document.SHA256, fmt.Sprint(document.Version))
	}
	if len(request.PathPolicy.ExcludePaths) != 0 {
		patterns := append([]string(nil), request.PathPolicy.ExcludePaths...)
		sort.Strings(patterns)
		parts = append(parts, "project-exclusions", strings.Join(patterns, "\n"))
	}
	return digestStrings(parts...)
}

func sameWorkspaceBinding(left, right workspaceBinding) bool {
	return left.WorkspaceID == right.WorkspaceID && left.RootFingerprint == right.RootFingerprint &&
		left.RepositoryAvailable == right.RepositoryAvailable && left.Commit == right.Commit &&
		left.Branch == right.Branch && left.Dirty == right.Dirty &&
		left.DirtyDigest == right.DirtyDigest && samePlatformPath(left.Root, right.Root)
}

func snapshotSupportsTool(snapshot CapabilitySnapshot, tool string) bool {
	for _, candidate := range snapshot.ModelVisibleTools {
		if candidate == tool {
			return true
		}
	}
	return false
}

func transportFailure(transport *transport, err error) bool {
	if transport == nil {
		return true
	}
	select {
	case <-transport.done:
		return true
	default:
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "lsp response contains trailing") ||
		strings.Contains(message, "decode lsp") || strings.Contains(message, "transport") ||
		strings.Contains(message, "semantic result limit")
}

type evidenceCursor struct {
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
	Offset      int    `json:"offset"`
}

func paginateEvidence(items []EvidenceItem, edges []GraphEdge, cursor string, limit int,
	queryFingerprint string,
) ([]EvidenceItem, []GraphEdge, Page, error) {
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].ID
	}
	fingerprint := digestStrings(queryFingerprint, strings.Join(ids, "\x00"))
	offset := 0
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(decoded) > 4096 {
			return nil, nil, Page{}, apperror.New(apperror.CodeInvalidArgument,
				"code-intel cursor is invalid")
		}
		var value evidenceCursor
		decoder := json.NewDecoder(bytes.NewReader(decoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, Page{}, apperror.New(apperror.CodeInvalidArgument,
				"code-intel cursor is invalid")
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) ||
			value.Version != ProtocolVersion || value.Fingerprint != fingerprint ||
			value.Offset <= 0 || value.Offset >= len(items) {
			return nil, nil, Page{}, apperror.New(apperror.CodeConflict,
				"code-intel cursor is stale or does not match the current evidence set")
		}
		offset = value.Offset
	}
	end := min(len(items), offset+limit)
	pageItems := append([]EvidenceItem(nil), items[offset:end]...)
	present := make(map[string]struct{}, len(pageItems))
	for _, item := range pageItems {
		present[item.ID] = struct{}{}
	}
	pageEdges := make([]GraphEdge, 0)
	for _, edge := range edges {
		_, from := present[edge.From]
		_, to := present[edge.To]
		if from && to {
			pageEdges = append(pageEdges, edge)
		}
	}
	page := Page{Limit: limit, Returned: len(pageItems), Total: len(items),
		Truncated: end < len(items)}
	if end < len(items) {
		raw, _ := json.Marshal(evidenceCursor{Version: ProtocolVersion,
			Fingerprint: fingerprint, Offset: end})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return pageItems, pageEdges, page, nil
}

func sortEvidence(items []EvidenceItem, edges []GraphEdge) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Path != items[j].Path {
			return items[i].Path < items[j].Path
		}
		if items[i].Range != nil && items[j].Range != nil &&
			items[i].Range.Start.Line != items[j].Range.Start.Line {
			return items[i].Range.Start.Line < items[j].Range.Start.Line
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Relationship < edges[j].Relationship
	})
}

func boundedWarnings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, min(len(values), 16))
	seen := make(map[string]struct{})
	for _, value := range values {
		value, _ = sanitizeText(value, 512, false)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == 16 {
			break
		}
	}
	return result
}

func filterProjectEvidence(items []EvidenceItem, edges []GraphEdge, policy workspaceidentity.PathPolicy) ([]EvidenceItem, []GraphEdge) {
	if len(policy.ExcludePaths) == 0 {
		return items, edges
	}
	visible := make(map[string]bool, len(items))
	filtered := make([]EvidenceItem, 0, len(items))
	for _, item := range items {
		if item.Path != "" && policy.Excludes(item.Path) {
			continue
		}
		visible[item.ID] = true
		filtered = append(filtered, item)
	}
	filteredEdges := make([]GraphEdge, 0, len(edges))
	for _, edge := range edges {
		if visible[edge.From] && visible[edge.To] {
			filteredEdges = append(filteredEdges, edge)
		}
	}
	return filtered, filteredEdges
}
