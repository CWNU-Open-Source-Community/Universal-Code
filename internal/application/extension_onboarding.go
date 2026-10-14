package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

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
