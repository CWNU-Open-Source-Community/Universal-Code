package codeintel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
)

type Manager struct {
	mu          sync.Mutex
	descriptors map[string]ServerDescriptor
	records     map[string]CapabilitySnapshot
	clients     map[string]*client
	starts      map[string]int64
	bootID      string
	runtimeHome string
	closing     bool
	quarantined bool
}

type client struct {
	mu          sync.Mutex
	documentMu  sync.Mutex
	key         string
	descriptor  ServerDescriptor
	workspace   workspaceBinding
	transport   *transport
	snapshot    CapabilitySnapshot
	documents   map[string]documentBinding
	diagnostics map[string]json.RawMessage
	serverLog   *boundedBuffer
	stopping    bool
}

type initializeResult struct {
	Capabilities map[string]json.RawMessage `json:"capabilities"`
	ServerInfo   *struct {
		Name    string `json:"name"`
		Version string `json:"version,omitempty"`
	} `json:"serverInfo,omitempty"`
}

func NewManager(descriptors []ServerDescriptor) (*Manager, error) {
	if len(descriptors) == 0 || len(descriptors) > MaxServers {
		return nil, errors.New("code-intel manager requires one to 32 reviewed servers")
	}
	return newManager(descriptors)
}

// NewEmptyManager reserves one stable Go-owned runtime for explicit operator
// configuration. An empty manager starts no process and contributes no tools.
func NewEmptyManager() (*Manager, error) { return newManager(nil) }

func newManager(descriptors []ServerDescriptor) (*Manager, error) {
	bootRaw := make([]byte, 32)
	if _, err := rand.Read(bootRaw); err != nil {
		return nil, fmt.Errorf("create code-intel boot generation: %w", err)
	}
	runtimeHome, err := os.MkdirTemp("", "prayu-code-intel-")
	if err != nil {
		return nil, fmt.Errorf("create code-intel runtime home: %w", err)
	}
	manager := &Manager{descriptors: make(map[string]ServerDescriptor, len(descriptors)),
		records: make(map[string]CapabilitySnapshot, len(descriptors)),
		clients: make(map[string]*client), starts: make(map[string]int64),
		bootID: hex.EncodeToString(bootRaw), runtimeHome: runtimeHome}
	for _, descriptor := range descriptors {
		if err := descriptor.Validate(); err != nil {
			_ = os.RemoveAll(runtimeHome)
			return nil, err
		}
		key := serverKey(descriptor.WorkspaceID, descriptor.ID)
		if _, exists := manager.descriptors[key]; exists {
			_ = os.RemoveAll(runtimeHome)
			return nil, errors.New("code-intel manager received duplicate server identity")
		}
		manager.descriptors[key] = descriptor
		manager.records[key] = configuredSnapshot(descriptor)
	}
	return manager, nil
}

// Descriptors is for the process-owned configuration service. Public adapters
// must project metadata rather than exposing launch arguments or host paths.
func (m *Manager) Descriptors() []ServerDescriptor {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ServerDescriptor, 0, len(m.descriptors))
	for _, descriptor := range m.descriptors {
		raw, _ := json.Marshal(descriptor)
		var copyValue ServerDescriptor
		_ = json.Unmarshal(raw, &copyValue)
		result = append(result, copyValue)
	}
	sort.Slice(result, func(i, j int) bool {
		return serverKey(result[i].WorkspaceID, result[i].ID) < serverKey(result[j].WorkspaceID, result[j].ID)
	})
	return result
}

// ReplaceConfiguration serializes configuration publication with process
// ownership. Publication failure leaves current descriptors and running clients
// intact. After publication, old clients are closed and reaped before descriptor
// replacement. Incomplete cleanup quarantines the runtime; the saved review can
// be recovered on restart, but this process cannot start or query either set.
// It never initializes the replacement configuration.
func (m *Manager) ReplaceConfiguration(ctx context.Context, descriptors []ServerDescriptor,
	persist func() error,
) error {
	if len(descriptors) == 0 || len(descriptors) > MaxServers || persist == nil {
		return apperror.New(apperror.CodeInvalidArgument, "reviewed code-intel configuration is invalid")
	}
	seen := make(map[string]bool)
	for _, descriptor := range descriptors {
		if err := descriptor.Validate(); err != nil || seen[serverKey(descriptor.WorkspaceID, descriptor.ID)] {
			return apperror.New(apperror.CodeInvalidArgument, "reviewed code-intel descriptors are invalid")
		}
		seen[serverKey(descriptor.WorkspaceID, descriptor.ID)] = true
	}
	if ctx == nil {
		return apperror.New(apperror.CodeInvalidArgument, "code-intel configuration requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.quarantined {
		return apperror.New(apperror.CodeFailedPrecondition, "previous owned LSP cleanup is incomplete; restart to recover the saved review")
	}
	if m.closing {
		return apperror.New(apperror.CodeFailedPrecondition, "code-intel manager is shutting down")
	}
	if err := persist(); err != nil {
		return err
	}
	for key, current := range m.clients {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), MaximumShutdownGracePeriod)
		_ = current.close(cleanupCtx)
		cancel()
		reapCtx, reapCancel := context.WithTimeout(context.Background(), MaximumShutdownGracePeriod)
		err := current.transport.process.Wait(reapCtx)
		reapCancel()
		if err != nil {
			m.quarantined = true
			for recordKey, descriptor := range m.descriptors {
				record := configuredSnapshot(descriptor)
				record.Health = HealthUnavailable
				record.LastError = "previous owned LSP cleanup is incomplete; restart to recover the saved review"
				m.records[recordKey] = record
			}
			return apperror.New(apperror.CodeUnavailable, "previous language server cleanup is incomplete")
		}
		delete(m.clients, key)
		record := m.records[key]
		record.Health = HealthStopped
		m.records[key] = record
	}
	m.descriptors = make(map[string]ServerDescriptor, len(descriptors))
	m.records = make(map[string]CapabilitySnapshot, len(descriptors))
	for _, descriptor := range descriptors {
		key := serverKey(descriptor.WorkspaceID, descriptor.ID)
		m.descriptors[key] = descriptor
		m.records[key] = configuredSnapshot(descriptor)
	}
	return nil
}

// InitializeServer starts only the reviewed server explicitly selected by the
// operator. It does not initialize other configured servers in the Workspace.
func (m *Manager) InitializeServer(ctx context.Context, workspaceID, root, serverID string) (CapabilitySnapshot, error) {
	current, err := m.ensureClient(ctx, serverKey(workspaceID, serverID), root)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	return cloneSnapshot(current.snapshot), nil
}

// RefreshInitializedServer follows normal workspace-generation invalidation,
// but cannot cold-start a replacement configuration that was never offered.
func (m *Manager) RefreshInitializedServer(ctx context.Context, workspaceID, root, serverID, generation string) (CapabilitySnapshot, error) {
	if !validDigest(generation) {
		return CapabilitySnapshot{}, apperror.New(apperror.CodeInvalidArgument, "initialized server generation is required")
	}
	current, err := m.ensureClientGeneration(ctx, serverKey(workspaceID, serverID), root, generation)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	return cloneSnapshot(current.snapshot), nil
}

func NewManagerFromConfig(path string) (*Manager, string, error) {
	config, digest, err := LoadConfig(path)
	if err != nil {
		return nil, "", err
	}
	manager, err := NewManager(config.Servers)
	return manager, digest, err
}

func configuredSnapshot(descriptor ServerDescriptor) CapabilitySnapshot {
	languages := make([]string, 0, len(descriptor.Languages))
	for _, language := range descriptor.Languages {
		languages = append(languages, language.ID)
	}
	sort.Strings(languages)
	return CapabilitySnapshot{ProtocolVersion: ProtocolVersion,
		ServerID: descriptor.ID, ServerName: descriptor.Name,
		WorkspaceID: descriptor.WorkspaceID, Languages: languages,
		Source: descriptor.Source, DescriptorFingerprint: descriptor.Fingerprint(),
		Health: HealthConfigured, ModelVisibleTools: []string{}, ProcessOwned: true,
		ReadOnly: true, NetworkAccessGranted: false, CredentialsGranted: false,
		ShellProfileLoaded: false}
}

func serverKey(workspaceID, serverID string) string { return workspaceID + "\x00" + serverID }

func (m *Manager) Qualify(ctx context.Context, workspaceID, root string) []Qualification {
	if m == nil {
		return []Qualification{}
	}
	m.mu.Lock()
	quarantined := m.quarantined
	descriptors := make([]ServerDescriptor, 0)
	for _, descriptor := range m.descriptors {
		if descriptor.WorkspaceID == workspaceID {
			descriptors = append(descriptors, descriptor)
		}
	}
	m.mu.Unlock()
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	result := make([]Qualification, 0, len(descriptors))
	for _, descriptor := range descriptors {
		qualification := Qualification{ProtocolVersion: ProtocolVersion,
			ServerID: descriptor.ID, WorkspaceID: descriptor.WorkspaceID,
			Health: HealthUnavailable, DescriptorFingerprint: descriptor.Fingerprint(),
			Reviewed:     descriptor.ReviewedBy != "" && !descriptor.ReviewedAt.IsZero(),
			ProcessOwned: true, MinimalEnvironment: true, NetworkAccessGranted: false,
			CredentialsGranted: false, ShellProfileLoaded: false}
		if quarantined {
			qualification.Reason = "previous owned LSP cleanup is incomplete; restart to recover the saved review"
			result = append(result, qualification)
			continue
		}
		if _, err := captureWorkspaceBinding(ctx, root, workspaceID); err != nil {
			qualification.Reason, _ = sanitizeText(err.Error(), 2048, false)
			result = append(result, qualification)
			continue
		}
		observed, available, err := executableDigest(descriptor.Executable)
		qualification.ExecutableHashMatched = available && err == nil &&
			observed == descriptor.ExecutableSHA256
		if err != nil {
			qualification.Reason, _ = sanitizeText(err.Error(), 2048, false)
		} else if !qualification.ExecutableHashMatched {
			qualification.Reason = "reviewed LSP executable hash no longer matches"
		} else {
			qualification.Eligible = true
			qualification.Health = HealthConfigured
		}
		result = append(result, qualification)
	}
	return result
}

// Capabilities starts and initializes each eligible server for the exact
// Workspace. Servers that fail remain visible as bounded qualification records
// but contribute no model tool definitions.
func (m *Manager) Capabilities(ctx context.Context, workspaceID, root string) []CapabilitySnapshot {
	if m == nil {
		return []CapabilitySnapshot{}
	}
	m.mu.Lock()
	keys := make([]string, 0)
	for key, descriptor := range m.descriptors {
		if descriptor.WorkspaceID == workspaceID {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	sort.Strings(keys)
	result := make([]CapabilitySnapshot, 0, len(keys))
	for _, key := range keys {
		current, err := m.ensureClient(ctx, key, root)
		if err != nil {
			m.mu.Lock()
			record := m.records[key]
			m.mu.Unlock()
			result = append(result, record)
			continue
		}
		result = append(result, cloneSnapshot(current.snapshot))
	}
	return result
}

func (m *Manager) Inventory() []CapabilitySnapshot {
	if m == nil {
		return []CapabilitySnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]CapabilitySnapshot, 0, len(m.records))
	for _, record := range m.records {
		result = append(result, cloneSnapshot(record))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WorkspaceID == result[j].WorkspaceID {
			return result[i].ServerID < result[j].ServerID
		}
		return result[i].WorkspaceID < result[j].WorkspaceID
	})
	return result
}

func cloneSnapshot(value CapabilitySnapshot) CapabilitySnapshot {
	value.Languages = append([]string(nil), value.Languages...)
	value.ModelVisibleTools = append([]string(nil), value.ModelVisibleTools...)
	if value.QualifiedAt != nil {
		copyTime := *value.QualifiedAt
		value.QualifiedAt = &copyTime
	}
	return value
}

func (m *Manager) ensureClient(ctx context.Context, key, root string) (*client, error) {
	return m.ensureClientGeneration(ctx, key, root, "")
}

func (m *Manager) ensureClientGeneration(ctx context.Context, key, root, expectedGeneration string) (*client, error) {
	if m == nil {
		return nil, errors.New("code-intel manager is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "code-intel initialization requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.quarantined {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "previous owned LSP cleanup is incomplete; restart to recover the saved review")
	}
	if m.closing {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"code-intel manager is shutting down")
	}
	if expectedGeneration != "" && (m.clients[key] == nil || m.clients[key].snapshot.Generation != expectedGeneration) {
		return nil, apperror.New(apperror.CodeConflict, "initialized language server generation changed")
	}
	descriptor, exists := m.descriptors[key]
	if !exists {
		return nil, apperror.New(apperror.CodeNotFound,
			"reviewed language server was not found")
	}
	workspace, err := captureWorkspaceBinding(ctx, root, descriptor.WorkspaceID)
	if err != nil {
		m.recordFailureLocked(key, HealthUnavailable, err)
		return nil, err
	}
	if existing := m.clients[key]; existing != nil {
		select {
		case <-existing.transport.done:
			delete(m.clients, key)
		default:
			if sameWorkspaceBinding(existing.workspace, workspace) {
				return existing, nil
			}
			delete(m.clients, key)
			go func(current *client) {
				shutdownCtx, cancel := context.WithTimeout(context.Background(),
					MaximumShutdownGracePeriod)
				_ = current.close(shutdownCtx)
				cancel()
			}(existing)
		}
	}
	observedDigest, available, err := executableDigest(descriptor.Executable)
	if err != nil || !available || observedDigest != descriptor.ExecutableSHA256 {
		if err == nil {
			err = errors.New("reviewed LSP executable hash no longer matches")
		}
		m.recordFailureLocked(key, HealthUnavailable, err)
		return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
			"language server qualification failed", err)
	}
	m.starts[key]++
	generation := digestStrings(ProtocolVersion, m.bootID, key,
		fmt.Sprint(m.starts[key]), workspace.RootFingerprint)
	starting := configuredSnapshot(descriptor)
	starting.Health = HealthStarting
	starting.Generation = generation
	m.records[key] = starting

	current := &client{key: key, descriptor: descriptor, workspace: workspace,
		documents: make(map[string]documentBinding), diagnostics: make(map[string]json.RawMessage),
		serverLog: newBoundedBuffer(MaxLogBytes)}
	transport, err := newTransport(descriptor, workspace.Root, m.runtimeHome,
		current.handleNotification)
	if err != nil {
		m.recordFailureLocked(key, HealthUnavailable, err)
		return nil, apperror.Wrap(apperror.CodeUnavailable,
			"language server could not start", err)
	}
	current.transport = transport
	initializeCtx, cancel := context.WithTimeout(ctx,
		time.Duration(descriptor.RequestTimeoutMillis)*time.Millisecond)
	initialize, err := current.initialize(initializeCtx, generation)
	cancel()
	if err != nil {
		_ = transport.process.Kill()
		waitCtx, waitCancel := context.WithTimeout(context.Background(),
			MaximumShutdownGracePeriod)
		_ = transport.process.Wait(waitCtx)
		waitCancel()
		m.recordFailureLocked(key, healthForError(err), err)
		return nil, apperror.Wrap(apperror.CodeUnavailable,
			"language server initialization failed", err)
	}
	current.snapshot = initialize
	m.clients[key] = current
	m.records[key] = cloneSnapshot(initialize)
	go m.watchClient(key, current)
	return current, nil
}

func (c *client) initialize(ctx context.Context, generation string) (CapabilitySnapshot, error) {
	rootURI, err := workspaceURI(c.workspace.Root)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	initializationOptions := any(nil)
	if len(c.descriptor.InitializationOptions) != 0 {
		if err := json.Unmarshal(c.descriptor.InitializationOptions, &initializationOptions); err != nil {
			return CapabilitySnapshot{}, err
		}
	}
	params := map[string]any{
		"processId":  nil,
		"clientInfo": map[string]string{"name": "Universal Code", "version": ProtocolVersion},
		"rootUri":    rootURI,
		"workspaceFolders": []map[string]string{{"uri": rootURI,
			"name": filepath.Base(c.workspace.Root)}},
		"initializationOptions": initializationOptions,
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"workspace": map[string]any{"workspaceFolders": true,
				"symbol": map[string]any{"dynamicRegistration": false}},
			"textDocument": map[string]any{
				"synchronization": map[string]any{"dynamicRegistration": false,
					"didSave": false},
				"documentSymbol": map[string]any{"dynamicRegistration": false,
					"hierarchicalDocumentSymbolSupport": true},
				"definition": map[string]any{"dynamicRegistration": false,
					"linkSupport": true},
				"references": map[string]any{"dynamicRegistration": false},
				"implementation": map[string]any{"dynamicRegistration": false,
					"linkSupport": true},
				"hover": map[string]any{"dynamicRegistration": false,
					"contentFormat": []string{"markdown", "plaintext"}},
				"signatureHelp": map[string]any{"dynamicRegistration": false,
					"signatureInformation": map[string]any{"documentationFormat": []string{"markdown", "plaintext"}}},
				"diagnostic": map[string]any{"dynamicRegistration": false,
					"relatedDocumentSupport": false},
				"callHierarchy": map[string]any{"dynamicRegistration": false},
				"typeHierarchy": map[string]any{"dynamicRegistration": false},
			},
			"window": map[string]any{"workDoneProgress": false},
		},
		"trace": "off",
	}
	var response initializeResult
	if err := c.transport.request(ctx, "initialize", params, &response); err != nil {
		return CapabilitySnapshot{}, err
	}
	capabilities := parseCapabilities(response.Capabilities)
	if !capabilities.Any() {
		return CapabilitySnapshot{}, errors.New(
			"language server negotiated no supported read-only semantic capability")
	}
	if err := c.transport.notify(ctx, "initialized", map[string]any{}); err != nil {
		return CapabilitySnapshot{}, err
	}
	serverName := c.descriptor.Name
	serverVersion := "unknown"
	if response.ServerInfo != nil {
		if safe, _ := sanitizeText(response.ServerInfo.Name, 256, false); safe != "" {
			serverName = safe
		}
		if safe, _ := sanitizeText(response.ServerInfo.Version, 256, false); safe != "" {
			serverVersion = safe
		}
	}
	now := time.Now().UTC()
	snapshot := configuredSnapshot(c.descriptor)
	snapshot.ServerName = serverName
	snapshot.ServerVersion = serverVersion
	snapshot.Capabilities = capabilities
	snapshot.ModelVisibleTools = capabilities.ToolNames()
	snapshot.CapabilityFingerprint = capabilityFingerprint(serverName, serverVersion, capabilities)
	snapshot.Generation = generation
	snapshot.Health = HealthHealthy
	snapshot.QualifiedAt = &now
	if err := snapshot.Validate(); err != nil {
		return CapabilitySnapshot{}, err
	}
	return snapshot, nil
}

func parseCapabilities(raw map[string]json.RawMessage) Capabilities {
	return Capabilities{
		WorkspaceSymbols: capabilityEnabled(raw["workspaceSymbolProvider"]),
		DocumentSymbols:  capabilityEnabled(raw["documentSymbolProvider"]),
		Definition:       capabilityEnabled(raw["definitionProvider"]),
		References:       capabilityEnabled(raw["referencesProvider"]),
		Implementation:   capabilityEnabled(raw["implementationProvider"]),
		Hover:            capabilityEnabled(raw["hoverProvider"]),
		SignatureHelp:    capabilityEnabled(raw["signatureHelpProvider"]),
		Diagnostics: capabilityEnabled(raw["diagnosticProvider"]) ||
			capabilityEnabled(raw["textDocumentSync"]),
		CallHierarchy: capabilityEnabled(raw["callHierarchyProvider"]),
		TypeHierarchy: capabilityEnabled(raw["typeHierarchyProvider"]),
	}
}

func capabilityEnabled(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "false" && trimmed != "null" && trimmed != "0"
}

func (c *client) handleNotification(method string, params json.RawMessage) {
	if c == nil {
		return
	}
	switch method {
	case "textDocument/publishDiagnostics":
		if len(params) == 0 || len(params) > MaxResultBytes || !json.Valid(params) {
			return
		}
		var header struct {
			URI     string `json:"uri"`
			Version *int   `json:"version,omitempty"`
		}
		if json.Unmarshal(params, &header) != nil || header.URI == "" {
			return
		}
		_, canonicalURI, err := workspaceRelativeURI(c.workspace.Root, header.URI)
		if err != nil {
			return
		}
		c.mu.Lock()
		var opened *documentBinding
		for _, document := range c.documents {
			if document.URI == canonicalURI {
				copyValue := document
				opened = &copyValue
				break
			}
		}
		if opened != nil && (header.Version == nil || *header.Version == opened.Version) {
			c.diagnostics[canonicalURI] = append(json.RawMessage(nil), params...)
		}
		c.mu.Unlock()
	case "window/logMessage", "window/showMessage", "$/logTrace":
		var value struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &value) == nil && value.Message != "" {
			_, _ = c.serverLog.Write([]byte(value.Message + "\n"))
		}
	}
}

func (m *Manager) watchClient(key string, current *client) {
	<-current.transport.done
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients[key] != current {
		return
	}
	delete(m.clients, key)
	if m.quarantined {
		return
	}
	current.mu.Lock()
	stopping := current.stopping
	current.mu.Unlock()
	if m.closing || stopping {
		record := current.snapshot
		record.Health = HealthStopped
		record.LastError = ""
		m.records[key] = record
		return
	}
	m.recordFailureLocked(key, HealthCrashed, current.transport.failure())
}

func (m *Manager) recordFailureLocked(key string, health HealthStatus, err error) {
	record, exists := m.records[key]
	if !exists {
		return
	}
	record.Health = health
	if err != nil {
		record.LastError, _ = sanitizeText(err.Error(), 2048, false)
	}
	m.records[key] = record
}

func healthForError(err error) HealthStatus {
	if err == nil {
		return HealthUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return HealthTimedOut
	}
	// The bounded stdout drain can close the pipe after process exit, so the
	// reader may observe os.ErrClosed instead of EOF. Both indicate a crashed LSP.
	if errors.Is(err, os.ErrClosed) {
		return HealthCrashed
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "exited") || strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "unexpected eof") || strings.Contains(message, "read lsp message: eof") {
		return HealthCrashed
	}
	if strings.Contains(message, "json-rpc") || strings.Contains(message, "content-length") ||
		strings.Contains(message, "decode lsp") || strings.Contains(message, "protocol") {
		return HealthProtocolErr
	}
	return HealthUnavailable
}

func (c *client) close(ctx context.Context) error {
	if c == nil || c.transport == nil {
		return nil
	}
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	c.mu.Lock()
	c.stopping = true
	documents := make([]documentBinding, 0, len(c.documents))
	for _, document := range c.documents {
		documents = append(documents, document)
	}
	c.mu.Unlock()
	for _, document := range documents {
		closeCtx, cancel := context.WithTimeout(ctx, time.Second)
		_ = c.transport.notify(closeCtx, "textDocument/didClose",
			map[string]any{"textDocument": map[string]string{"uri": document.URI}})
		cancel()
	}
	return c.transport.closeGracefully(ctx)
}

func (m *Manager) invalidate(key string, current *client, err error) {
	if m == nil || current == nil {
		return
	}
	m.mu.Lock()
	if m.clients[key] == current {
		delete(m.clients, key)
		m.recordFailureLocked(key, healthForError(err), err)
	}
	m.mu.Unlock()
	_ = current.transport.process.Kill()
	waitCtx, cancel := context.WithTimeout(context.Background(), MaximumShutdownGracePeriod)
	_ = current.transport.process.Wait(waitCtx)
	cancel()
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil
	}
	m.closing = true
	clients := make([]*client, 0, len(m.clients))
	for _, current := range m.clients {
		clients = append(clients, current)
	}
	m.clients = make(map[string]*client)
	m.mu.Unlock()
	var result error
	for _, current := range clients {
		if err := current.close(ctx); err != nil {
			result = errors.Join(result, err)
		}
	}
	if m.runtimeHome != "" {
		if err := os.RemoveAll(m.runtimeHome); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
