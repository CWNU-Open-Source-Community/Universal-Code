package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

type mcpSearchInvocation struct {
	Version               string `json:"version"`
	Method                string `json:"method,omitempty"`
	ServerID              string `json:"server_id"`
	ToolName              string `json:"tool_name"`
	CapabilityFingerprint string `json:"capability_fingerprint"`
}

type mcpSearchOperation struct {
	Kind        string              `json:"kind"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	ServerName  string              `json:"server_name"`
	InputSchema json.RawMessage     `json:"input_schema"`
	Invocation  mcpSearchInvocation `json:"invocation"`
}

type mcpSearchPage struct {
	CatalogRevision string                        `json:"catalog_revision"`
	TotalMatches    int                           `json:"total_matches"`
	Offset          int                           `json:"offset"`
	Operations      []mcpSearchOperation          `json:"operations"`
	NextRequest     *toolgateway.MCPSearchPayload `json:"next_request,omitempty"`
}

func (e *MCPClientToolExecutor) SearchMCP(ctx context.Context, scope toolgateway.MCPExecutionScope,
	request toolgateway.MCPSearchPayload,
) (json.RawMessage, error) {
	if e == nil || e.client == nil || e.store == nil {
		return nil, errors.New("MCP catalog runtime is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if err := e.recheckMCPSearchScope(ctx, scope); err != nil {
		return nil, err
	}
	if scope.RequestedBy == "specialist_runner" {
		if err := e.checkWorkerMCPSearch(ctx, scope, request); err != nil {
			return nil, err
		}
	}
	catalog, err := e.client.Capabilities(ctx, scope.RunID, scope.WorkspaceID)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	page, err := searchMCPCatalog(catalog, request)
	if err != nil {
		return nil, err
	}
	if err := e.recheckMCPSearchScope(ctx, scope); err != nil {
		return nil, err
	}
	if scope.RequestedBy == "specialist_runner" {
		if err := e.checkWorkerMCPSearch(ctx, scope, request); err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (e *MCPClientToolExecutor) checkWorkerMCPSearch(ctx context.Context, scope toolgateway.MCPExecutionScope, request toolgateway.MCPSearchPayload) error {
	st, ok := e.store.(workerToolSourceStore)
	if !ok {
		return errors.New("worker MCP search source is unavailable")
	}
	ref := domain.AgentAttemptRef{RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID}
	if _, err := readWorkerExecutionContext(ctx, e.store, ref, e.drydocks); err != nil {
		return err
	}
	c, err := st.GetSpecialistToolCall(ctx, ref, scope.SpecialistToolCallID)
	if err != nil {
		return err
	}
	a, err := mcp.DecodeSupervisorCallAuthority(json.RawMessage(c.AuthorityJSON))
	if err != nil {
		return err
	}
	if a.Version != mcp.SupervisorCatalogAuthorityVersion || a.MissionID != scope.MissionID || a.WorkspaceID != scope.WorkspaceID || a.PermissionMode != scope.PermissionMode || a.PermissionSnapshotID != scope.PermissionSnapshotID || a.PermissionRevision != scope.PermissionRevision || a.PermissionGeneration != scope.PermissionGeneration || a.PermissionRuntimeEpoch != scope.PermissionRuntimeEpoch || a.RunAuthorizationFence != scope.RunAuthorizationFence {
		return errors.New("worker MCP search authority changed")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	canonical, err := toolgateway.NormalizeSupervisorToolPayload(toolgateway.MCPToolSearchTool, raw)
	if err != nil {
		return err
	}
	_, err = st.ValidateSpecialistToolSource(ctx, domain.SpecialistToolSource{Ref: ref, CallID: c.CallID, SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID, Name: string(toolgateway.MCPToolSearchTool), PayloadJSON: string(canonical), AuthorityJSON: c.AuthorityJSON, ChargeID: scope.InvocationID, LeaseID: scope.LeaseID, LeaseGeneration: scope.LeaseGeneration, RequireStarted: true})
	return err
}

func (e *MCPClientToolExecutor) recheckMCPSearchScope(ctx context.Context, scope toolgateway.MCPExecutionScope) error {
	if err := e.recheckExecutionScope(ctx, scope); err != nil {
		return err
	}
	store, ok := e.store.(interface {
		GetAgentNode(context.Context, string) (domain.AgentNode, error)
		GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	})
	if !ok {
		return errors.New("MCP catalog actor and mode reader is unavailable")
	}
	actor, err := store.GetAgentNode(ctx, scope.AgentID)
	if err != nil {
		return apperror.Normalize(err)
	}
	mode, err := store.GetRunMode(ctx, scope.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if actor.RunID != scope.RunID || actor.Role != scope.Role || actor.Status != domain.AgentRunning ||
		actor.ActiveAttemptID != scope.AgentAttemptID || scope.AgentAttemptID == "" ||
		mode.Surface != scope.Surface || mode.Phase != scope.Phase || mode.MissionID != scope.MissionID {
		return apperror.New(apperror.CodeConflict, "MCP catalog actor, attempt or execution mode changed")
	}
	return nil
}

func searchMCPCatalog(catalog mcp.ScopedCapabilities, request toolgateway.MCPSearchPayload) (json.RawMessage, error) {
	if catalog.ProtocolVersion != mcp.ClientProtocolVersion {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "MCP catalog protocol is unavailable")
	}
	if request.CatalogRevision != "" && request.CatalogRevision != catalog.Generation {
		return nil, apperror.New(apperror.CodeConflict, "MCP catalog changed; restart the search at offset 0 without catalog_revision")
	}
	type match struct {
		operation mcpSearchOperation
		score     int
	}
	terms := strings.FieldsFunc(strings.ToLower(request.Query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	matches := []match{}
	for _, server := range catalog.Servers {
		if request.ServerID != "" && request.ServerID != server.ServerID {
			continue
		}
		add := func(kind, name, description, target, method, toolName string, schema json.RawMessage) {
			score := mcpSearchScore(terms, name, server.ServerID+" "+server.Name, description+" "+target)
			if request.Query != "" && strings.EqualFold(request.Query, name) {
				score += 20
			}
			if request.Query != "" && score == 0 {
				return
			}
			version := toolgateway.MCPClientToolProtocolVersion
			if method != "" {
				version = toolgateway.MCPContentToolProtocolVersion
			}
			matches = append(matches, match{mcpSearchOperation{Kind: kind, Name: name,
				Description: description, ServerName: server.Name, InputSchema: schema,
				Invocation: mcpSearchInvocation{Version: version, Method: method, ServerID: server.ServerID,
					ToolName: toolName, CapabilityFingerprint: server.CapabilityFingerprint}}, score})
		}
		for _, tool := range server.Tools {
			add("tool", tool.Name, tool.Description, "", "", tool.Name, tool.InputSchema)
		}
		// Use the same authoritative schema builder as content invocation.
		for _, resource := range server.Resources {
			choices := (mcp.ScopedServerCapability{Resources: []mcp.RemoteResource{resource}}).ContentChoices()
			add("resource", resource.Name, resource.Description, resource.URI, "resources/read", "resources.read", choices[0].InputSchema)
		}
		for _, template := range server.ResourceTemplates {
			choices := (mcp.ScopedServerCapability{ResourceTemplates: []mcp.RemoteResourceTemplate{template}}).ContentChoices()
			if len(choices) != 0 {
				add("resource_template", template.Name, template.Description, template.URITemplate, "resources/read", "resources.read", choices[0].InputSchema)
			}
		}
		for _, prompt := range server.Prompts {
			choices := (mcp.ScopedServerCapability{Prompts: []mcp.RemotePrompt{prompt}}).ContentChoices()
			add("prompt", prompt.Name, prompt.Description, "", "prompts/get", "prompts.get", choices[0].InputSchema)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	page := mcpSearchPage{CatalogRevision: catalog.Generation, TotalMatches: len(matches),
		Offset: request.Offset, Operations: []mcpSearchOperation{}}
	encoded, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	for i := request.Offset; i < len(matches) && len(page.Operations) < request.Limit; i++ {
		page.Operations = append(page.Operations, matches[i].operation)
		page.NextRequest = nil
		if i+1 < len(matches) {
			next := request
			next.Offset, next.CatalogRevision = i+1, catalog.Generation
			page.NextRequest = &next
		}
		candidate, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		if len(candidate) > toolgateway.MaxResultStdoutBytes {
			if len(page.Operations) == 1 {
				operation := matches[i].operation
				return nil, apperror.New(apperror.CodeResourceExhausted,
					fmt.Sprintf("MCP catalog entry %s/%s exceeds the %d-byte search page limit; the server must reduce this entry's schema or description", operation.Invocation.ServerID, operation.Name, toolgateway.MaxResultStdoutBytes))
			}
			// The last complete page already points at this exact next entry.
			break
		}
		encoded = candidate
	}
	return encoded, nil
}

func mcpSearchScore(terms []string, name, server, description string) int {
	name, server, description = strings.ToLower(name), strings.ToLower(server), strings.ToLower(description)
	score := 0
	for _, term := range terms {
		if term == name {
			score += 10
		} else if strings.Contains(name, term) {
			score += 5
		}
		if strings.Contains(server, term) {
			score += 3
		}
		if strings.Contains(description, term) {
			score++
		}
	}
	return score
}
