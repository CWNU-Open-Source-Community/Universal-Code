package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/session"
	workspacepolicy "cyberagent-workbench/internal/workspace"
)

const CodeIntelConfigurationProtocol = "code-intel-configuration.v1"

type CodeIntelControlStore interface {
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
}

type CodeIntelConfigurationRequest struct {
	ServerID              string
	Name                  string
	WorkspaceID           string
	Languages             []codeintel.Language
	Executable            string
	Arguments             []string
	ExecutableSHA256      string
	InitializationOptions json.RawMessage
	RequestTimeoutMillis  int64
}

type CodeIntelConfiguration struct {
	ServerID              string
	ServerName            string
	WorkspaceID           string
	Languages             []codeintel.Language
	ExecutableSHA256      string
	DescriptorFingerprint string
	ReviewState           string
	Source                codeintel.Source
	ReviewedBy            string
	ReviewedAt            time.Time
}

type CodeIntelConfigurationReviewRequest struct {
	WorkspaceID                   string
	ExpectedDescriptorFingerprint string
	ReviewedBy                    string
}

type CodeIntelConfigurationTestRequest struct {
	WorkspaceID                   string
	ExpectedDescriptorFingerprint string
	Tool                          string
	Path                          string
	Query                         string
}

type CodeIntelConfigurationTest struct {
	Configuration CodeIntelConfiguration
	Server        codeintel.CapabilitySnapshot
	Result        codeintel.Result
}

// CodeIntelControlService owns explicit staging/review of the existing operator
// configuration contract. Drafts are process-local; only reviewed descriptors
// enter the managed file and the stable manager used by model tools.
type CodeIntelControlService struct {
	mu      sync.Mutex
	store   CodeIntelControlStore
	manager *codeintel.Manager
	path    string
	digest  string
	drafts  map[string]codeintel.ServerDescriptor
	reviews map[string]string
}

func OpenCodeIntelControlService(store CodeIntelControlStore, manager *codeintel.Manager,
	path string,
) (*CodeIntelControlService, error) {
	if store == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("code-intel control requires a registered Workspace store and managed path")
	}
	digest := ""
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("managed code-intel configuration must be a real file")
		}
		_, digest, err = codeintel.LoadConfig(path)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if manager == nil {
		var err error
		if digest != "" {
			manager, _, err = codeintel.NewManagerFromConfig(path)
		} else {
			manager, err = codeintel.NewEmptyManager()
		}
		if err != nil {
			return nil, err
		}
	}
	return &CodeIntelControlService{store: store, manager: manager, path: path,
		digest: digest, drafts: make(map[string]codeintel.ServerDescriptor), reviews: make(map[string]string)}, nil
}

func (s *CodeIntelControlService) Manager() *codeintel.Manager { return s.manager }

func codeIntelConfigurationKey(workspaceID, serverID string) string {
	return workspaceID + "\x00" + serverID
}

func configurationMetadata(d codeintel.ServerDescriptor, pending bool) CodeIntelConfiguration {
	raw, _ := json.Marshal(d.Languages)
	var languages []codeintel.Language
	_ = json.Unmarshal(raw, &languages)
	result := CodeIntelConfiguration{ServerID: d.ID, ServerName: d.Name, WorkspaceID: d.WorkspaceID,
		Languages: languages, ExecutableSHA256: d.ExecutableSHA256, DescriptorFingerprint: d.Fingerprint(),
		ReviewState: "reviewed", Source: d.Source, ReviewedBy: d.ReviewedBy, ReviewedAt: d.ReviewedAt}
	if pending {
		result.ReviewState = "pending_review"
		result.ReviewedBy = ""
		result.ReviewedAt = time.Time{}
	}
	return result
}

func (s *CodeIntelControlService) Configurations(ctx context.Context, workspaceID string) ([]CodeIntelConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []CodeIntelConfiguration{}
	for _, d := range s.manager.Descriptors() {
		if workspaceID == "" || d.WorkspaceID == workspaceID {
			result = append(result, configurationMetadata(d, false))
		}
	}
	for _, d := range s.drafts {
		if workspaceID == "" || d.WorkspaceID == workspaceID {
			result = append(result, configurationMetadata(d, true))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WorkspaceID != result[j].WorkspaceID {
			return result[i].WorkspaceID < result[j].WorkspaceID
		}
		if result[i].ServerID != result[j].ServerID {
			return result[i].ServerID < result[j].ServerID
		}
		return result[i].ReviewState < result[j].ReviewState
	})
	return result, nil
}

func (s *CodeIntelControlService) Stage(ctx context.Context, r CodeIntelConfigurationRequest) (CodeIntelConfiguration, error) {
	if _, err := s.store.GetWorkspaceInfo(ctx, r.WorkspaceID); err != nil {
		return CodeIntelConfiguration{}, err
	}
	d := codeintel.ServerDescriptor{ProtocolVersion: codeintel.ProtocolVersion, ID: r.ServerID, Name: r.Name,
		WorkspaceID: r.WorkspaceID, Languages: r.Languages, Executable: r.Executable, Arguments: r.Arguments,
		ExecutableSHA256: r.ExecutableSHA256, InitializationOptions: r.InitializationOptions,
		RequestTimeoutMillis: r.RequestTimeoutMillis, ReviewedBy: "pending-review", ReviewedAt: time.Unix(0, 0).UTC()}
	_, prepared, _, err := codeintel.PrepareConfig(codeintel.Config{ProtocolVersion: codeintel.ConfigProtocolVersion,
		Servers: []codeintel.ServerDescriptor{d}}, filepath.Base(s.path))
	if err != nil {
		return CodeIntelConfiguration{}, apperror.New(apperror.CodeInvalidArgument, "LSP configuration identity, language, executable hash, arguments, or options are invalid")
	}
	d = prepared.Servers[0]
	s.mu.Lock()
	defer s.mu.Unlock()
	key := codeIntelConfigurationKey(r.WorkspaceID, r.ServerID)
	if len(s.drafts) >= codeintel.MaxServers {
		if _, exists := s.drafts[key]; !exists {
			return CodeIntelConfiguration{}, apperror.New(apperror.CodeResourceExhausted, "too many pending LSP configurations")
		}
	}
	s.drafts[key] = d
	return configurationMetadata(d, true), nil
}

func (s *CodeIntelControlService) Review(ctx context.Context, serverID string, r CodeIntelConfigurationReviewRequest) (CodeIntelConfiguration, error) {
	if _, err := s.store.GetWorkspaceInfo(ctx, r.WorkspaceID); err != nil {
		return CodeIntelConfiguration{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := codeIntelConfigurationKey(r.WorkspaceID, serverID)
	d, found := s.drafts[key]
	if !found || d.Fingerprint() != r.ExpectedDescriptorFingerprint {
		for _, current := range s.manager.Descriptors() {
			if codeIntelConfigurationKey(current.WorkspaceID, current.ID) == key && s.reviews[key+"\x00"+r.ExpectedDescriptorFingerprint] == current.Fingerprint() {
				return configurationMetadata(current, false), nil
			}
		}
		return CodeIntelConfiguration{}, apperror.New(apperror.CodeConflict, "LSP configuration review is missing or stale")
	}
	qualification, err := codeintel.NewManager([]codeintel.ServerDescriptor{d})
	if err != nil {
		return CodeIntelConfiguration{}, err
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, r.WorkspaceID)
	if err != nil {
		_ = qualification.Close(context.Background())
		return CodeIntelConfiguration{}, err
	}
	checks := qualification.Qualify(ctx, workspace.ID, workspace.RootPath)
	_ = qualification.Close(context.Background())
	if err := ctx.Err(); err != nil {
		return CodeIntelConfiguration{}, err
	}
	if len(checks) != 1 || !checks[0].Eligible {
		return CodeIntelConfiguration{}, apperror.New(apperror.CodeFailedPrecondition, "LSP executable is unavailable or its SHA-256 no longer matches the reviewed input")
	}
	d.ReviewedBy = r.ReviewedBy
	d.ReviewedAt = time.Now().UTC()
	descriptors := s.manager.Descriptors()
	replaced := false
	for i, current := range descriptors {
		if codeIntelConfigurationKey(current.WorkspaceID, current.ID) == key {
			descriptors[i] = d
			replaced = true
		}
	}
	if !replaced {
		descriptors = append(descriptors, d)
	}
	raw, prepared, digest, err := codeintel.PrepareConfig(codeintel.Config{ProtocolVersion: codeintel.ConfigProtocolVersion, Servers: descriptors}, filepath.Base(s.path))
	if err != nil {
		return CodeIntelConfiguration{}, apperror.New(apperror.CodeInvalidArgument, "reviewed LSP configuration exceeds its contract")
	}
	err = s.manager.ReplaceConfiguration(ctx, prepared.Servers, func() error {
		if err := s.persist(raw); err != nil {
			return err
		}
		s.digest = digest
		return nil
	})
	if err != nil {
		return CodeIntelConfiguration{}, err
	}
	s.digest = digest
	delete(s.drafts, key)
	for _, current := range prepared.Servers {
		if codeIntelConfigurationKey(current.WorkspaceID, current.ID) == key {
			s.reviews[key+"\x00"+r.ExpectedDescriptorFingerprint] = current.Fingerprint()
			return configurationMetadata(current, false), nil
		}
	}
	return CodeIntelConfiguration{}, apperror.New(apperror.CodeInternal, "reviewed LSP configuration readback failed")
}

func (s *CodeIntelControlService) persist(raw []byte) error {
	// Managed configuration is outside repository discovery. Reject directory and
	// file redirection, and compare the current digest before replacing it.
	parent := filepath.Dir(s.path)
	for directory := parent; ; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return apperror.New(apperror.CodeFailedPrecondition, "managed LSP configuration directory is unavailable or redirected")
		}
		if next := filepath.Dir(directory); next == directory {
			break
		}
	}
	lock, err := acquireCodeIntelConfigurationLock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if s.digest != "" {
		_, observed, err := codeintel.LoadConfig(s.path)
		if err != nil || observed != s.digest {
			return apperror.New(apperror.CodeConflict, "managed LSP configuration changed outside this process")
		}
	} else if _, err := os.Lstat(s.path); !errors.Is(err, os.ErrNotExist) {
		return apperror.New(apperror.CodeConflict, "managed LSP configuration was created outside this process")
	}
	file, err := os.CreateTemp(parent, ".code-intel-review-*.json")
	if err != nil {
		return apperror.New(apperror.CodeUnavailable, "managed LSP configuration could not be written")
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, s.path)
	}
	if err != nil {
		return apperror.New(apperror.CodeUnavailable, "managed LSP configuration publication failed")
	}
	return nil
}

func (s *CodeIntelControlService) Test(ctx context.Context, serverID string, r CodeIntelConfigurationTestRequest) (CodeIntelConfigurationTest, error) {
	if r.Tool != codeintel.ToolDocumentSymbols && r.Tool != codeintel.ToolWorkspaceSymbols {
		return CodeIntelConfigurationTest{}, apperror.New(apperror.CodeInvalidArgument, "LSP setup probe supports document or workspace symbols only")
	}
	if r.WorkspaceID == "" || r.ExpectedDescriptorFingerprint == "" || (r.Tool == codeintel.ToolDocumentSymbols && r.Path == "") {
		return CodeIntelConfigurationTest{}, apperror.New(apperror.CodeInvalidArgument, "LSP setup probe requires a Workspace, reviewed fingerprint, and query input")
	}
	workspace, err := s.store.GetWorkspaceInfo(ctx, r.WorkspaceID)
	if err != nil {
		return CodeIntelConfigurationTest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CodeIntelConfigurationTest{}, err
	}
	var descriptor *codeintel.ServerDescriptor
	for _, current := range s.manager.Descriptors() {
		if current.WorkspaceID == r.WorkspaceID && current.ID == serverID {
			copyValue := current
			descriptor = &copyValue
			break
		}
	}
	if descriptor == nil {
		return CodeIntelConfigurationTest{}, apperror.New(apperror.CodeFailedPrecondition, "LSP configuration requires explicit review before testing")
	}
	if descriptor.Fingerprint() != r.ExpectedDescriptorFingerprint {
		return CodeIntelConfigurationTest{}, apperror.New(apperror.CodeConflict, "reviewed LSP descriptor changed before testing")
	}
	request := codeintel.Request{Tool: r.Tool, WorkspaceID: workspace.ID, WorkspaceRoot: workspace.RootPath,
		ServerID: serverID, ServerGeneration: strings.Repeat("0", 64), CapabilityFingerprint: strings.Repeat("0", 64), Path: r.Path, Query: r.Query, Limit: 20}
	if err := request.Validate(); err != nil {
		return CodeIntelConfigurationTest{}, err
	}
	if r.Tool == codeintel.ToolDocumentSymbols {
		if _, _, err := workspacepolicy.AgentCodeReadMutationSource(workspace.RootPath, r.Path); err != nil {
			return CodeIntelConfigurationTest{}, err
		}
	}
	eligible := false
	for _, check := range s.manager.Qualify(ctx, workspace.ID, workspace.RootPath) {
		if check.ServerID == serverID {
			eligible = check.Eligible
		}
	}
	if err := ctx.Err(); err != nil {
		return CodeIntelConfigurationTest{}, err
	}
	if !eligible {
		return CodeIntelConfigurationTest{}, apperror.New(apperror.CodeFailedPrecondition, "LSP executable or Workspace qualification no longer matches its review")
	}
	snapshot, err := s.manager.InitializeServer(ctx, workspace.ID, workspace.RootPath, serverID)
	if err != nil {
		return CodeIntelConfigurationTest{}, err
	}
	result, err := s.manager.Execute(ctx, codeintel.Request{Tool: r.Tool, WorkspaceID: workspace.ID, WorkspaceRoot: workspace.RootPath,
		ServerID: serverID, ServerGeneration: snapshot.Generation, CapabilityFingerprint: snapshot.CapabilityFingerprint,
		Path: r.Path, Query: r.Query, Limit: 20})
	if err != nil {
		return CodeIntelConfigurationTest{}, err
	}
	return CodeIntelConfigurationTest{Configuration: configurationMetadata(*descriptor, false), Server: snapshot, Result: result}, nil
}
