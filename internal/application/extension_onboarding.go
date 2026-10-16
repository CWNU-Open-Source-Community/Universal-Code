package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
)

// RegisterMCP reuses CLI staging, including its durable descriptor replay and
// scope checks. Registration never connects, discovers, reviews or invokes.
func (s *ExtensionControlService) RegisterMCP(ctx context.Context, descriptor mcp.ServerDescriptor) (
	mcp.ServerRecord, bool, error,
) {
	// Keep in-process registration and reference confirmation in one ordering.
	if s.credentials != nil {
		s.credentials.mu.Lock()
		defer s.credentials.mu.Unlock()
	}
	if (descriptor.ProtocolVersion != mcp.ClientProtocolVersion && descriptor.ProtocolVersion != mcp.ConfigClientProtocolVersion) || descriptor.NativeSource != nil {
		return mcp.ServerRecord{}, false, apperror.New(apperror.CodeInvalidArgument,
			"manual MCP registration requires the supported descriptor protocol")
	}
	// The HTTP operator supplies launch metadata, not acquired Plugin provenance.
	descriptor.Source = mcp.Source{Kind: "manual", URI: "http-upload"}
	return s.mcp.Stage(ctx, descriptor)
}

// ImportPlugin retains the exact bounded archive as inert data. The existing
// package fingerprint is its replay identity, including after a lost response.
// No filesystem path, URL fetch or automatic capability review is accepted.
func (s *ExtensionControlService) ImportPlugin(ctx context.Context, archive []byte, expectedSHA256 string) (
	plugins.Installation, bool, error,
) {
	if len(archive) < 1 || len(archive) > plugins.MaxArchiveBytes {
		return plugins.Installation{}, false, apperror.New(apperror.CodeInvalidArgument,
			"plugin archive violates its size bound")
	}
	digest := sha256.Sum256(archive)
	actualSHA256 := hex.EncodeToString(digest[:])
	if expectedSHA256 != actualSHA256 {
		return plugins.Installation{}, false, apperror.New(apperror.CodeInvalidArgument,
			"plugin archive does not match its SHA-256 pin")
	}
	return s.plugins.Stage(ctx, archive, plugins.InstallSource{Kind: "upload",
		URI: "sha256:" + actualSHA256, SHA256: actualSHA256}, "", "http_extension_operator")
}

// ImportPluginSource acquires only the operator-selected pinned archive. It
// shares the CLI fetchers and the same inert, replayable staging operation.
type ImportPluginSourceRequest struct {
	Kind        string
	URL         string
	SHA256      string
	Commit      string
	ArchivePath string
}

type PluginImportAcquisition struct {
	Kind        string
	URL         string
	SHA256      string
	Commit      string
	ArchivePath string
}

type ImportPluginSourceResult struct {
	Installation plugins.Installation
	Replayed     bool
	Acquisition  PluginImportAcquisition
}

func (s *ExtensionControlService) ImportPluginSource(ctx context.Context, request ImportPluginSourceRequest) (
	ImportPluginSourceResult, error,
) {
	source := plugins.InstallSource{Kind: request.Kind, URI: request.URL,
		SHA256: request.SHA256, Commit: request.Commit}
	if source.Kind == "git" && source.SHA256 == "" {
		source.SHA256 = strings.Repeat("0", 64) // Filled from the exact fetched blob below.
	}
	if (source.Kind != "https" && source.Kind != "git") || source.Validate() != nil ||
		(source.Kind == "https" && request.ArchivePath != "") {
		return ImportPluginSourceResult{}, apperror.New(apperror.CodeInvalidArgument,
			"plugin import requires a pinned HTTPS URL or Git commit")
	}
	archivePath := strings.TrimSpace(request.ArchivePath)
	if source.Kind == "git" && archivePath == "" {
		archivePath = "plugin.zip"
	}
	var archive []byte
	var err error
	if source.Kind == "https" {
		archive, err = plugins.FetchPinnedHTTPS(ctx, source.URI, source.SHA256, nil)
	} else {
		archive, err = plugins.FetchPinnedGitArchive(ctx, source.URI, source.Commit, archivePath, "")
	}
	if err != nil {
		if ctx.Err() != nil {
			return ImportPluginSourceResult{}, apperror.Normalize(ctx.Err())
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ImportPluginSourceResult{}, apperror.Normalize(err)
		}
		return ImportPluginSourceResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"plugin source acquisition failed", err)
	}
	digest := sha256.Sum256(archive)
	source.SHA256 = hex.EncodeToString(digest[:])
	if request.SHA256 != "" && request.SHA256 != source.SHA256 {
		return ImportPluginSourceResult{}, apperror.New(apperror.CodeInvalidArgument,
			"plugin archive does not match its SHA-256 pin")
	}
	installation, replayed, err := s.plugins.Stage(ctx, archive, source, "", "http_extension_operator")
	if err != nil {
		return ImportPluginSourceResult{}, err
	}
	if installation.ArchiveSHA256 != source.SHA256 {
		return ImportPluginSourceResult{}, apperror.New(apperror.CodeConflict,
			"the retained plugin archive differs from the acquired archive")
	}
	return ImportPluginSourceResult{Installation: installation, Replayed: replayed,
		Acquisition: PluginImportAcquisition{Kind: source.Kind, URL: source.URI,
			SHA256: source.SHA256, Commit: source.Commit, ArchivePath: archivePath}}, nil
}
