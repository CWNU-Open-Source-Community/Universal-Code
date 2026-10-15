package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/toolcontract"
)

// ConfigClientProtocolVersion is the manual connection codec. It is independent
// of both native source registrations and the negotiated MCP wire profile.
const ConfigClientProtocolVersion = "mcp-client.v3"

const MaxManualConfigEntries = 32

// Environment credentials are raw secrets. An OAuth envelope belongs only to
// its HTTP authentication adapter and must never be exposed to a subprocess.
const oauthCredentialEnvelopePrefix = "uc-mcp-oauth-v1:"

func (d ServerDescriptor) validateManualConfig() error {
	if d.Source.Kind != "manual" || d.NativeSource != nil || len(d.Env)+len(d.EnvCredentialRefs) > MaxManualConfigEntries || len(d.Headers) > MaxManualConfigEntries {
		return errors.New("manual MCP connection configuration source or bounds are invalid")
	}
	if d.Transport == TransportStdio {
		if len(d.Headers) != 0 || d.CredentialRef != "" || (d.Cwd != "" && (!filepath.IsAbs(d.Cwd) || !validClientText(d.Cwd, MaxClientTargetBytes, false))) {
			return errors.New("manual stdio configuration requires an absolute cwd and no HTTP fields")
		}
		seen := make(map[string]bool, len(d.Env)+len(d.EnvCredentialRefs))
		for key, value := range d.Env {
			if !validManualEnvironmentName(key) || !validManualLiteral(value) || sensitiveManualConfigName(key) {
				return errors.New("MCP literal environment must be bounded and non-sensitive; use a credential reference for secrets")
			}
			seen[manualEnvironmentKey(key)] = true
		}
		if len(seen) != len(d.Env) {
			return errors.New("MCP environment contains ambiguous case aliases")
		}
		for key, reference := range d.EnvCredentialRefs {
			normalized := manualEnvironmentKey(key)
			if !validManualEnvironmentName(key) || !validCredentialReference(reference) || seen[normalized] {
				return errors.New("MCP environment credential reference is invalid or repeats a variable")
			}
			seen[normalized] = true
		}
	} else {
		if d.Cwd != "" || len(d.Env) != 0 || len(d.EnvCredentialRefs) != 0 {
			return errors.New("manual HTTP configuration cannot carry process fields")
		}
		u, err := url.Parse(d.Target)
		if err != nil || !validRemoteEndpoint(d.Transport, d.Target) || u.RawQuery != "" || u.ForceQuery || strings.Contains(d.Target, "#") {
			return errors.New("manual remote endpoint requires TLS or loopback without query or fragment")
		}
		seen := make(map[string]bool, len(d.Headers))
		for name, value := range d.Headers {
			canonical := http.CanonicalHeaderKey(name)
			if !validManualHeaderName(name) || seen[canonical] || hostControlledMCPHeader(name) || isWebSocketHeader(name) || sensitiveManualConfigName(name) ||
				!validManualLiteral(value) {
				return errors.New("manual MCP headers must be unique, bounded and non-sensitive; authentication uses a credential reference")
			}
			seen[canonical] = true
		}
	}
	// Match the existing immutable SQLite descriptor bound before staging.
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > 65536 {
		return errors.New("manual MCP descriptor exceeds its persisted size bound")
	}
	return nil
}

func manualEnvironmentKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func validManualEnvironmentName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i, c := range name {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func validManualLiteral(value string) bool {
	if !validClientText(value, 4096, true) || redact.String(value) != value ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") || strings.HasPrefix(value, oauthCredentialEnvelopePrefix) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validManualHeaderName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func sensitiveManualConfigName(name string) bool {
	name = strings.ReplaceAll(strings.ToLower(name), "_", "-")
	for _, fragment := range []string{"authorization", "cookie", "token", "secret", "password", "passwd", "api-key", "apikey", "private-key", "credential"} {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}

// ReferencesCredential is metadata only and never resolves a store value.
func (d ServerDescriptor) ReferencesCredential(reference string) bool {
	if reference == "" {
		return false
	}
	if d.CredentialRef == reference {
		return true
	}
	for _, current := range d.EnvCredentialRefs {
		if current == reference {
			return true
		}
	}
	return false
}

// resolveManualLaunch adapts versioned descriptors to the existing transient
// launch contract. It accepts already-read secrets and never reads arbitrary
// host environment, invokes a shell or persists resolved values.
func resolveManualLaunch(d ServerDescriptor, versions []string, secretEnvironment map[string]string) (toolcontract.ResolvedLaunch, error) {
	if d.Validate() != nil || d.NativeSource != nil {
		return toolcontract.ResolvedLaunch{}, errors.New("valid manual MCP descriptor is required")
	}
	formatVersion := "1"
	if d.ProtocolVersion == ConfigClientProtocolVersion {
		formatVersion = "3"
	}
	launch := toolcontract.ResolvedLaunch{Component: toolcontract.ComponentRef{PackageID: d.ProtocolVersion, ComponentID: d.ID},
		InstanceID: d.Fingerprint(), Format: "mcp-client", FormatVersion: formatVersion,
		ProtocolVersions: transportProtocolVersions(toolcontract.Transport(d.Transport), versions), Transport: toolcontract.Transport(d.Transport)}
	if d.Transport.Remote() {
		launch.HTTP = &toolcontract.HTTPLaunch{Endpoint: d.Target, Headers: maps.Clone(d.Headers)}
		return launch, launch.Validate()
	}
	cwd := d.Cwd
	if cwd == "" {
		cwd = os.TempDir()
	}
	cwd, err := canonicalLaunchDirectory(cwd)
	if err != nil {
		return launch, err
	}
	env := map[string]string{}
	for _, entry := range minimalMCPEnvironment() {
		key, value, present := strings.Cut(entry, "=")
		if present {
			env[key] = value
		}
	}
	env, err = normalizeLaunchEnvironment(env)
	if err != nil {
		return launch, err
	}
	for name, value := range d.Env {
		env[manualEnvironmentKey(name)] = value
	}
	for name := range d.EnvCredentialRefs {
		value, found := secretEnvironment[name]
		if !found || !credential.ValidSecret(value) || strings.HasPrefix(value, oauthCredentialEnvelopePrefix) {
			return launch, errors.New("MCP environment credential is unavailable or incompatible")
		}
		env[manualEnvironmentKey(name)] = value
	}
	executable, err := resolveLaunchExecutable(d.Target, cwd, env)
	if err != nil {
		return launch, err
	}
	digest, err := launchExecutableDigest(executable)
	if err != nil {
		return launch, err
	}
	launch.Stdio = &toolcontract.StdioLaunch{Command: executable, ExecutableSHA256: digest, Args: slices.Clone(d.Arguments), Env: env, Cwd: cwd}
	return launch, launch.Validate()
}

func (m *Manager) readManualEnvironment(ctx context.Context, d ServerDescriptor) (map[string]string, error) {
	values := make(map[string]string, len(d.EnvCredentialRefs))
	for name, reference := range d.EnvCredentialRefs {
		if m.credentials == nil {
			return nil, errors.New("MCP environment credential reader is unavailable")
		}
		value, found, err := m.credentials.Get(ctx, reference)
		if err != nil || !found || !credential.ValidSecret(value) || strings.HasPrefix(value, oauthCredentialEnvelopePrefix) {
			return nil, errors.New("MCP environment credential is unavailable or incompatible")
		}
		values[name] = value
	}
	return values, nil
}

func (m *Manager) recheckManualEnvironment(ctx context.Context, d ServerDescriptor, launch toolcontract.ResolvedLaunch) error {
	if len(d.EnvCredentialRefs) == 0 {
		return nil
	}
	current, err := m.readManualEnvironment(ctx, d)
	if err != nil || launch.Stdio == nil {
		return errors.New("MCP environment credential binding is no longer current")
	}
	for name, value := range current {
		if launch.Stdio.Env[manualEnvironmentKey(name)] != value {
			return errors.New("MCP environment credential binding is no longer current")
		}
	}
	return nil
}
