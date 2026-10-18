package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
)

type workerToolEntry struct {
	definition toolgateway.ToolDefinition
	authority  json.RawMessage
}
type workerToolCatalogue struct {
	tools map[string]workerToolEntry
	mcp   supervisorMCPTools
}

func (c workerToolCatalogue) specs() []llm.ToolSpec {
	names := make([]string, 0, len(c.tools))
	for name := range c.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]llm.ToolSpec, 0, len(names))
	for _, name := range names {
		d := c.tools[name].definition
		specs = append(specs, llm.ToolSpec{Name: name, Description: d.Description, Parameters: d.InputSchema})
	}
	return specs
}

// The execution catalogue is built from current parent capabilities, then
// intersected with the immutable delegation profile. Each invocation binds its
// own child identity; a parent capability snapshot never acts as a child grant.
func (s *AgentRunner) workerToolCatalogue(ctx context.Context, ref domain.AgentAttemptRef) (workerToolCatalogue, error) {
	c := workerToolCatalogue{tools: map[string]workerToolEntry{}}
	w, err := readWorkerExecutionContext(ctx, s.store, ref, s.drydocks)
	if err != nil {
		return c, err
	}
	parent, err := s.store.(workerExecutionStore).GetAgentNode(ctx, w.Child.ParentID)
	if err != nil {
		return c, err
	}
	if parent.Role != domain.AgentRoleRoot || parent.RunID != ref.RunID {
		return c, errors.New("worker parent changed")
	}
	mode, err := s.store.GetRunMode(ctx, ref.RunID)
	if err != nil {
		return c, err
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, ref.RunID)
	if err != nil {
		return c, err
	}
	turn := domain.SupervisorTurn{Run: w.Run, Mission: w.Mission, Agent: parent, Mode: mode}
	add := func(d toolgateway.ToolDefinition, authority json.RawMessage) {
		if w.Child.Worker.AllowsTool(string(d.Name)) && len(authority) > 0 {
			c.tools[string(d.Name)] = workerToolEntry{d, authority}
		}
	}
	code, authority, err := s.supervisorAgentCodeCapabilities(ctx, turn, permission)
	if err != nil {
		return c, err
	}
	for _, d := range code.VisibleDefinitions() {
		add(d, authority)
	}
	semantic, err := s.workerCodeIntelCapabilities(ctx, ref, turn)
	if err != nil {
		return c, err
	}
	for _, d := range semantic.VisibleDefinitions() {
		add(d, authority)
	}
	if w.Child.Worker.AllowsTool(string(toolgateway.SkillReadTool)) {
		entry, available, err := s.workerSkillToolEntry(ctx, ref, turn)
		if err != nil {
			return c, err
		}
		if available {
			add(entry.definition, entry.authority)
		}
	}
	if w.Child.Worker.AllowsTool(string(toolgateway.HistorySearchTool)) || w.Child.Worker.AllowsTool(string(toolgateway.HistoryReadTool)) {
		historyAuthority, err := s.workerHistoryToolAuthority(ctx, ref)
		if err != nil {
			return c, err
		}
		for _, definition := range toolgateway.WorkerHistoryRecallToolDefinitions() {
			add(definition, historyAuthority)
		}
	}
	command, err := s.supervisorCommandRuntimeTools(ctx, ref.RunID, permission.Mode)
	if err != nil {
		return c, err
	}
	if supervisorCommandRuntimeAvailable(mode.Surface, mode.Phase, permission.Mode, command.Adapter) {
		add(toolgateway.CommandRuntimeDefinitionForAdapter(command.Adapter), command.Authority)
	}
	if supervisorProjectTestsAvailable(mode.Surface, mode.Phase, permission.Mode, command.Adapter) {
		if definition, ok := toolgateway.SupervisorToolDefinition(toolgateway.RunTestsTool); ok {
			add(definition, command.Authority)
		}
	}
	if s.agentBrowser != nil && s.agentBrowser.available && permission.Mode.IncludesFullAccess() {
		authority, err := s.agentBrowser.WorkerToolAuthority(ctx, ref)
		if err != nil {
			return c, err
		}
		for _, name := range []toolgateway.ToolName{toolgateway.BrowserStatusTool, toolgateway.BrowserNavigateTool, toolgateway.BrowserSnapshotTool, toolgateway.BrowserClickTool, toolgateway.BrowserTypeTool, toolgateway.BrowserScreenshotTool, toolgateway.BrowserScrollTool, toolgateway.BrowserKeyTool} {
			if d, ok := toolgateway.AgentBrowserToolDefinition(name); ok {
				add(d, authority)
			}
		}
	}
	if w.Child.Worker.AllowsTool(string(toolgateway.MCPToolCallTool)) || w.Child.Worker.AllowsTool(string(toolgateway.MCPToolSearchTool)) {
		c.mcp, err = s.supervisorMCPCapabilities(ctx, turn, permission, ref)
		if err != nil {
			return c, err
		}
		if len(c.mcp.Capabilities.Servers) > 0 {
			add(toolgateway.MCPToolDefinition(), c.mcp.Authority)
			if d, ok := toolgateway.SupervisorToolDefinition(toolgateway.MCPToolSearchTool); ok {
				add(d, c.mcp.Authority)
			}
		}
	}
	return c, nil
}

func (s *AgentRunner) bindWorkerToolAuthority(ctx context.Context, call domain.SpecialistToolCall, catalogue workerToolCatalogue) (json.RawMessage, error) {
	entry, ok := catalogue.tools[call.Name]
	if !ok {
		return nil, errors.New("worker tool is outside the currently available delegated catalogue")
	}
	name, payload := toolgateway.ToolName(call.Name), json.RawMessage(call.PayloadJSON)
	if toolgateway.IsAgentCodeTool(name) || toolgateway.IsCodeIntelTool(name) {
		e, err := s.workerCodeExecutor()
		if err != nil {
			return nil, err
		}
		return e.BindSpecialistAgentCodeAuthority(ctx, call.Ref, name, entry.authority, payload)
	}
	if toolgateway.IsCommandExecutionTool(name) {
		binder, ok := s.commandRuntime.(interface {
			BindSpecialistCommandToolAuthority(context.Context, domain.AgentAttemptRef, toolgateway.ToolName, json.RawMessage, json.RawMessage) (json.RawMessage, error)
		})
		if !ok {
			return nil, errors.New("worker command authority binding is unavailable")
		}
		return binder.BindSpecialistCommandToolAuthority(ctx, call.Ref, name, entry.authority, payload)
	}
	if name == toolgateway.MCPToolCallTool || name == toolgateway.MCPToolSearchTool {
		a, err := mcp.DecodeSupervisorCallAuthority(entry.authority)
		if err != nil {
			return nil, err
		}
		if name == toolgateway.MCPToolSearchTool {
			a.Version = mcp.SupervisorCatalogAuthorityVersion
			a.ServerID, a.DescriptorFingerprint, a.ServerGeneration = "", "", 0
		} else {
			p, _, err := toolgateway.NormalizeMCPToolPayload(payload)
			if err != nil {
				return nil, err
			}
			if !supervisorMCPToolAvailable(catalogue.mcp.Capabilities, p) {
				return nil, errors.New("worker MCP invocation does not match the current catalogue; search for its exact schema")
			}
			for _, server := range catalogue.mcp.Capabilities.Servers {
				if server.ServerID == p.ServerID {
					a.ServerID, a.DescriptorFingerprint, a.ServerGeneration = server.ServerID, server.DescriptorFingerprint, server.RegistrationGeneration
					break
				}
			}
		}
		return mcp.EncodeSupervisorCallAuthority(a)
	}
	return entry.authority, nil
}

func (s *AgentRunner) workerCodeExecutor() (*AgentCodeToolExecutor, error) {
	st, ok := s.store.(AgentCodeToolStore)
	if !ok {
		return nil, errors.New("worker workspace runtime is unavailable")
	}
	return NewAgentCodeToolExecutor(st, s.checker).WithRunWorktree(s.drydocks).WithExecutionPermissionCapabilities(s.executionCapabilities), nil
}

func (s *AgentRunner) closeWorkerResources(ref domain.AgentAttemptRef) error {
	ctx, cancel := specialistEventContext(context.Background())
	defer cancel()
	var closeErr error
	if closer, ok := s.commandRuntime.(interface {
		CloseSpecialistCommands(context.Context, domain.AgentAttemptRef) (int, error)
	}); ok {
		_, err := closer.CloseSpecialistCommands(ctx, ref)
		closeErr = errors.Join(closeErr, err)
	}
	if s.agentBrowser != nil {
		closeErr = errors.Join(closeErr, s.agentBrowser.CloseWorker(ctx, ref))
	}
	if closer, ok := s.mcpClient.(interface{ CloseWorkspaceScope(string, string) }); ok {
		closer.CloseWorkspaceScope(ref.RunID, workerMCPScopeID(ref))
	}
	closeErr = errors.Join(closeErr, s.closeWorkerCodeIntelResources(ctx, ref))
	return closeErr
}
