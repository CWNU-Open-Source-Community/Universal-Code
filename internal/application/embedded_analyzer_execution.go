package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cyberagent-workbench/internal/analyzer"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/session"
)

const (
	EmbeddedAnalyzerExecutionProtocolVersion = "embedded_analyzer_operator_request.v1"
	EmbeddedAnalyzerExecutionConfirmation    = "RUN-EMBEDDED-ANALYZER"
	embeddedAnalyzerCapabilityLifetime       = 2 * time.Minute
)

type EmbeddedAnalyzerExecutionStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	RegisterAnalyzerExecutionCapability(context.Context,
		analyzer.AnalyzerExecutionCapability) (analyzer.AnalyzerExecutionCapability, bool, error)
	ConsumeAnalyzerExecutionCapability(context.Context, string, string, []byte,
		analyzer.InvocationCandidate, time.Time) (analyzer.AnalyzerExecutionConsumption, error)
	CommitAnalyzerExecution(context.Context, analyzer.AnalyzerExecutionCommitRequest) (
		analyzer.AnalyzerExecutionRecord, artifact.Descriptor, bool, error)
}

type EmbeddedAnalyzerExecutionRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	RunID           string `json:"run_id"`
	Text            string `json:"text,omitempty"`
	File            string `json:"file,omitempty"`
	MediaType       string `json:"media_type"`
	RequestedBy     string `json:"requested_by"`
	Confirmation    string `json:"confirmation"`
}

type EmbeddedAnalyzerExecutionResult struct {
	Record   analyzer.AnalyzerExecutionRecord `json:"record"`
	Artifact artifact.Descriptor              `json:"artifact"`
	Result   analyzer.Result                  `json:"result"`
	Replayed bool                             `json:"replayed"`
}

type EmbeddedAnalyzerExecutionService struct {
	store EmbeddedAnalyzerExecutionStore
}

func NewEmbeddedAnalyzerExecutionService(store EmbeddedAnalyzerExecutionStore) *EmbeddedAnalyzerExecutionService {
	return &EmbeddedAnalyzerExecutionService{store: store}
}

func (s *EmbeddedAnalyzerExecutionService) Execute(ctx context.Context,
	request EmbeddedAnalyzerExecutionRequest,
) (EmbeddedAnalyzerExecutionResult, error) {
	if s == nil || s.store == nil || ctx == nil {
		return EmbeddedAnalyzerExecutionResult{}, errors.New("embedded analyzer execution service is unavailable")
	}
	request.ProtocolVersion = strings.TrimSpace(request.ProtocolVersion)
	request.RunID = strings.TrimSpace(request.RunID)
	request.File = strings.TrimSpace(request.File)
	request.MediaType = strings.TrimSpace(request.MediaType)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.Confirmation = strings.TrimSpace(request.Confirmation)
	if request.ProtocolVersion != EmbeddedAnalyzerExecutionProtocolVersion || request.RunID == "" ||
		request.RequestedBy == "" || request.Confirmation != EmbeddedAnalyzerExecutionConfirmation ||
		(request.Text == "") == (request.File == "") {
		return EmbeddedAnalyzerExecutionResult{}, errors.New("embedded analyzer request or explicit confirmation is invalid")
	}
	if request.MediaType == "" {
		request.MediaType = "text/plain"
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return EmbeddedAnalyzerExecutionResult{}, err
	}
	if run.Terminal() || run.SessionID == "" {
		return EmbeddedAnalyzerExecutionResult{}, errors.New("embedded analyzer requires an active Run with a Session")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return EmbeddedAnalyzerExecutionResult{}, err
	}
	workspace, err := s.store.GetWorkspaceByID(ctx, mission.WorkspaceID)
	if err != nil {
		return EmbeddedAnalyzerExecutionResult{}, err
	}
	content := []byte(request.Text)
	if request.File != "" {
		content, err = readEmbeddedAnalyzerWorkspaceFile(workspace.RootPath, request.File)
		if err != nil {
			return EmbeddedAnalyzerExecutionResult{}, err
		}
	}
	if len(content) == 0 || len(content) > analyzer.MaxDecodedInputBytes {
		return EmbeddedAnalyzerExecutionResult{}, fmt.Errorf("analyzer input must be between 1 and %d bytes",
			analyzer.MaxDecodedInputBytes)
	}

	requestID := idgen.New("analyzer-request")
	protocolRequest := analyzer.Request{
		ProtocolVersion: analyzer.RequestProtocolVersion,
		RequestID:       requestID,
		Analyzer:        analyzer.FixtureAnalyzerName,
		Input: analyzer.Input{MediaType: request.MediaType,
			ContentBase64: base64.StdEncoding.EncodeToString(content)},
		Limits: analyzer.Limits{MaxInputBytes: analyzer.MaxDecodedInputBytes,
			MaxOutputBytes: 4096, TimeoutMilliseconds: 5000},
		MetadataOnly: true,
	}
	rawRequest, err := json.Marshal(protocolRequest)
	if err != nil {
		return EmbeddedAnalyzerExecutionResult{}, err
	}
	candidate, code := analyzer.BuildInvocationCandidate(rawRequest)
	if code != "" {
		return EmbeddedAnalyzerExecutionResult{}, fmt.Errorf("build embedded analyzer candidate: %s", code)
	}
	bearer := make([]byte, analyzer.AnalyzerExecutionCapabilityTokenBytes)
	if _, err := io.ReadFull(rand.Reader, bearer); err != nil {
		return EmbeddedAnalyzerExecutionResult{}, fmt.Errorf("generate analyzer capability: %w", err)
	}
	issuedAt := time.Now().UTC()
	capability, code := analyzer.BuildAnalyzerExecutionCapability(idgen.New("analyzer-capability"),
		run.ID, workspace.ID, candidate, bearer, issuedAt,
		issuedAt.Add(embeddedAnalyzerCapabilityLifetime))
	if code != "" {
		return EmbeddedAnalyzerExecutionResult{}, fmt.Errorf("build analyzer capability: %s", code)
	}
	if _, replayed, err := s.store.RegisterAnalyzerExecutionCapability(ctx, capability); err != nil {
		return EmbeddedAnalyzerExecutionResult{}, err
	} else if replayed {
		return EmbeddedAnalyzerExecutionResult{}, errors.New("new analyzer capability unexpectedly replayed")
	}
	consumption, err := s.store.ConsumeAnalyzerExecutionCapability(ctx, capability.ID,
		idgen.New("analyzer-consumption"), bearer, candidate, time.Now().UTC())
	for index := range bearer {
		bearer[index] = 0
	}
	if err != nil {
		return EmbeddedAnalyzerExecutionResult{}, err
	}
	executed, code := analyzer.ExecuteEmbeddedWASI(ctx, rawRequest)
	if code != "" {
		return EmbeddedAnalyzerExecutionResult{}, fmt.Errorf("embedded analyzer execution failed: %s", code)
	}
	commit := analyzer.AnalyzerExecutionCommitRequest{
		ID: idgen.New("analyzer-execution"), RunID: run.ID, SessionID: run.SessionID,
		WorkspaceID: workspace.ID, CapabilityID: capability.ID,
		ConsumptionID: consumption.ID, RequestedBy: request.RequestedBy,
		Candidate: candidate, Execution: executed.Execution, RawResult: executed.RawResult,
		CreatedAt: time.Now().UTC(),
	}
	record, descriptor, replayed, err := s.store.CommitAnalyzerExecution(ctx, commit)
	if err != nil {
		return EmbeddedAnalyzerExecutionResult{}, err
	}
	decoded, code := analyzer.DecodeResult(executed.RawResult)
	if code != "" {
		return EmbeddedAnalyzerExecutionResult{}, fmt.Errorf("decode committed analyzer result: %s", code)
	}
	return EmbeddedAnalyzerExecutionResult{Record: record, Artifact: descriptor,
		Result: decoded, Replayed: replayed}, nil
}

func readEmbeddedAnalyzerWorkspaceFile(root, relative string) ([]byte, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(relative) == "" ||
		filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" ||
		strings.ContainsRune(relative, '\x00') {
		return nil, errors.New("analyzer file must be a non-empty workspace-relative path")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	rootInfo, err := os.Stat(rootReal)
	if err != nil || !rootInfo.IsDir() {
		return nil, errors.New("analyzer workspace root is unavailable")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("analyzer file escapes the workspace")
	}
	confinedRoot, err := os.OpenRoot(rootReal)
	if err != nil {
		return nil, fmt.Errorf("open analyzer workspace root: %w", err)
	}
	defer confinedRoot.Close()
	before, err := confinedRoot.Stat(clean)
	if err != nil {
		return nil, fmt.Errorf("stat analyzer input within workspace: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("analyzer file must be regular")
	}
	file, err := rootfs.OpenReadOnly(confinedRoot, clean)
	if err != nil {
		return nil, fmt.Errorf("open analyzer input within workspace: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > analyzer.MaxDecodedInputBytes {
		return nil, fmt.Errorf("analyzer file must be regular and between 1 and %d bytes",
			analyzer.MaxDecodedInputBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, analyzer.MaxDecodedInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) < 1 || len(content) > analyzer.MaxDecodedInputBytes {
		return nil, errors.New("analyzer file changed outside the accepted size bound")
	}
	after, err := confinedRoot.Stat(clean)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) ||
		after.Size() != info.Size() || int64(len(content)) != info.Size() ||
		!after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("analyzer file changed while it was being read")
	}
	return content, nil
}
