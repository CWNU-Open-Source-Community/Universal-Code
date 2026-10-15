package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"cyberagent-workbench/internal/toolcontract"
)

// ResolveLaunch resolves only the declaration and the host's explicit context.
// It never reads ambient environment, invokes a shell, creates directories, or
// grants authority. The caller must authorize the returned frozen configuration.
func ResolveLaunch(declaration toolcontract.LaunchDeclaration, host toolcontract.LaunchContext) (toolcontract.ResolvedLaunch, error) {
	if declaration.Validate() != nil || host.Validate() != nil {
		return toolcontract.ResolvedLaunch{}, errors.New("invalid MCP declaration or host launch context")
	}
	// Native ecosystems require their own reviewed rules, not this portable codec.
	switch declaration.Format {
	case "agent-plugins":
		if declaration.FormatVersion != "1.0.0" {
			return toolcontract.ResolvedLaunch{}, errors.New("unsupported Agent Plugins launch version")
		}
	default:
		return toolcontract.ResolvedLaunch{}, errors.New("unsupported MCP launch declaration format")
	}
	root, err := canonicalLaunchDirectory(host.InstallRoot)
	if err != nil {
		return toolcontract.ResolvedLaunch{}, err
	}
	data, err := canonicalLaunchDirectory(host.DataRoot)
	if err != nil {
		return toolcontract.ResolvedLaunch{}, err
	}
	base, err := normalizeLaunchEnvironment(host.BaseEnv)
	if err != nil {
		return toolcontract.ResolvedLaunch{}, err
	}
	versions := slices.Clone(declaration.ProtocolVersions)
	if len(versions) == 0 {
		versions = transportProtocolVersions(declaration.Transport, host.ProtocolVersions)
	}
	for _, version := range versions {
		if !transportSupportsProtocol(declaration.Transport, version) || !slices.Contains(host.ProtocolVersions, version) {
			return toolcontract.ResolvedLaunch{}, errors.New("MCP profile is unsupported or outside the host's permitted versions")
		}
	}
	resolved := toolcontract.ResolvedLaunch{Component: declaration.Component, InstanceID: host.InstanceID,
		Format: declaration.Format, FormatVersion: declaration.FormatVersion, ProtocolVersions: versions, Transport: declaration.Transport}
	switch declaration.Transport {
	case toolcontract.TransportStdio:
		local := *declaration.Stdio
		local.Env, err = normalizeLaunchEnvironment(local.Env)
		if err != nil {
			return toolcontract.ResolvedLaunch{}, err
		}
		for key, value := range local.Env {
			if key == "PLUGIN_ROOT" || key == "PLUGIN_DATA" {
				return toolcontract.ResolvedLaunch{}, errors.New("Agent Plugins configuration cannot supply reserved environment variables")
			}
			value, err = expandAgentPluginValue(value, root, data)
			if err != nil {
				return toolcontract.ResolvedLaunch{}, err
			}
			base[key] = value
		}
		base["PLUGIN_ROOT"], base["PLUGIN_DATA"] = root, data
		local.Env = base
		// os/exec would otherwise silently inherit SystemRoot on Windows.
		if runtime.GOOS == "windows" && local.Env["SYSTEMROOT"] == "" {
			return toolcontract.ResolvedLaunch{}, errors.New("Windows stdio launch requires host-supplied SystemRoot")
		}
		local.Cwd, err = resolveAgentPluginCwd(local.Cwd, root, data)
		if err != nil {
			return toolcontract.ResolvedLaunch{}, err
		}
		// Portable commands never expand placeholders and ./ always means root,
		// even when cwd selects the separately managed data directory.
		local.Command, err = resolveAgentPluginCommand(local.Command, root, local.Env)
		if err != nil {
			return toolcontract.ResolvedLaunch{}, err
		}
		local.ExecutableSHA256, err = launchExecutableDigest(local.Command)
		if err != nil {
			return toolcontract.ResolvedLaunch{}, err
		}
		local.Args = slices.Clone(local.Args)
		for i, arg := range local.Args {
			local.Args[i], err = expandAgentPluginValue(arg, root, data)
			if err != nil {
				return toolcontract.ResolvedLaunch{}, err
			}
		}
		resolved.Stdio = &local
	case toolcontract.TransportStreamableHTTP, toolcontract.TransportSSE, toolcontract.TransportWebSocket:
		remote := *declaration.HTTP
		if !validRemoteEndpoint(TransportKind(declaration.Transport), remote.Endpoint) {
			return toolcontract.ResolvedLaunch{}, errors.New("Agent Plugins HTTP endpoint is invalid")
		}
		remote.Headers = make(map[string]string, len(declaration.HTTP.Headers))
		for name, value := range declaration.HTTP.Headers {
			remote.Headers[http.CanonicalHeaderKey(name)] = value
		}
		if remote.Credential != nil {
			credential := *remote.Credential
			remote.Credential = &credential
		}
		resolved.HTTP = &remote
	}
	if err := resolved.Validate(); err != nil {
		return toolcontract.ResolvedLaunch{}, err
	}
	return resolved, nil
}

// SSE and WebSocket retain the legacy JSON-RPC transport binding. The 2026
// profile defines bindings for stdio and Streamable HTTP only.
func transportSupportsProtocol(transport toolcontract.Transport, version string) bool {
	return supportedClientProtocol(version) &&
		(version != preferredClientProtocolVersion || transport == toolcontract.TransportStdio || transport == toolcontract.TransportStreamableHTTP)
}

func transportProtocolVersions(transport toolcontract.Transport, versions []string) []string {
	result := make([]string, 0, len(versions))
	for _, version := range versions {
		if transportSupportsProtocol(transport, version) {
			result = append(result, version)
		}
	}
	return result
}

func expandAgentPluginValue(value, root, data string) (string, error) {
	// strings.Replacer consumes only the original input. Replacement text is not
	// rescanned, and unrecognized/case-varied/malformed placeholders stay literal.
	result := strings.NewReplacer("${PLUGIN_ROOT}", root, "${PLUGIN_DATA}", data).Replace(value)
	if len(result) > 65536 {
		return "", errors.New("expanded MCP launch field exceeds its limit")
	}
	return result, nil
}

func resolveAgentPluginCwd(value, root, data string) (string, error) {
	boundary := root
	switch {
	case value == "":
		return root, nil
	case strings.HasPrefix(value, "./"):
	case value == "${PLUGIN_ROOT}", strings.HasPrefix(value, "${PLUGIN_ROOT}/"):
	case value == "${PLUGIN_DATA}", strings.HasPrefix(value, "${PLUGIN_DATA}/"):
		boundary = data
	default:
		return "", errors.New("Agent Plugins cwd has an unsupported form")
	}
	expanded, err := expandAgentPluginValue(value, root, data)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(value, "./") {
		expanded = filepath.Join(root, expanded)
	}
	if !launchPathWithin(boundary, expanded) {
		return "", errors.New("Agent Plugins cwd escapes its declared root")
	}
	resolved, err := canonicalLaunchDirectory(expanded)
	if err != nil {
		return "", err
	}
	if !launchPathWithin(boundary, resolved) {
		return "", errors.New("Agent Plugins cwd resolves outside its declared root")
	}
	return resolved, nil
}

func resolveAgentPluginCommand(command, root string, env map[string]string) (string, error) {
	packaged := strings.HasPrefix(command, "./")
	if packaged {
		command = filepath.Join(root, command)
		if !launchPathWithin(root, command) {
			return "", errors.New("Agent Plugins command escapes its plugin root")
		}
	} else if command == "." || command == ".." || strings.ContainsAny(command, "/\\:") {
		return "", errors.New("Agent Plugins command must be a bare name or ./ package path")
	}
	resolved, err := resolveLaunchExecutable(command, root, env)
	if err != nil {
		return "", err
	}
	if packaged && !launchPathWithin(root, resolved) {
		return "", errors.New("Agent Plugins command resolves outside its plugin root")
	}
	return resolved, nil
}

func launchPathWithin(root, value string) bool {
	relative, err := filepath.Rel(root, value)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validRemoteEndpoint(kind TransportKind, value string) bool {
	if !kind.Remote() {
		return false
	}
	if kind == TransportWebSocket {
		u, err := url.Parse(value)
		if err != nil {
			return false
		}
		switch u.Scheme {
		case "wss":
			u.Scheme = "https"
		case "ws":
			u.Scheme = "http"
		default:
			return false
		}
		value = u.String()
	}
	return agentPluginEndpoint(value)
}

func agentPluginEndpoint(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || strings.Contains(value, "#") || u.Opaque != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(u.Hostname())
	return err == nil && ip.Unmap().IsLoopback()
}

func normalizeLaunchEnvironment(env map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(env))
	for key, value := range env {
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if _, exists := result[key]; exists {
			return nil, errors.New("MCP environment contains ambiguous case aliases")
		}
		result[key] = value
	}
	return result, nil
}

func canonicalLaunchDirectory(value string) (string, error) {
	value, err := canonicalLaunchPath(value)
	if err != nil {
		return "", errors.New("MCP launch directory is unavailable")
	}
	info, err := os.Stat(value)
	if err != nil || !info.IsDir() || !filepath.IsAbs(value) {
		return "", errors.New("MCP launch directory is invalid")
	}
	return filepath.Clean(value), nil
}

func resolveLaunchExecutable(command, cwd string, env map[string]string) (string, error) {
	candidates := []string{command}
	if !filepath.IsAbs(command) {
		if strings.ContainsAny(command, "/\\") {
			candidates = []string{filepath.Join(cwd, command)}
		} else {
			candidates = nil
			for _, dir := range filepath.SplitList(env["PATH"]) {
				// Never pick an implicit executable from cwd or an ambient PATH.
				if !filepath.IsAbs(dir) {
					continue
				}
				candidates = append(candidates, filepath.Join(dir, command))
			}
		}
	}
	for _, candidate := range candidates {
		paths := []string{candidate}
		if runtime.GOOS == "windows" && filepath.Ext(candidate) == "" {
			paths = nil
			extensions := env["PATHEXT"]
			if extensions == "" {
				extensions = ".COM;.EXE;.BAT;.CMD"
			}
			for _, ext := range strings.Split(extensions, ";") {
				if slices.Contains([]string{".exe", ".com", ".cmd", ".bat"}, strings.ToLower(ext)) {
					paths = append(paths, candidate+ext)
				}
			}
		}
		for _, path := range paths {
			if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(path), ".exe") && !strings.EqualFold(filepath.Ext(path), ".com") {
				if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
					return "", errors.New("MCP executable requires an interpreter; implicit Windows script launch is unsupported")
				}
				continue // shell scripts require an explicitly declared interpreter
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0111 == 0) {
				continue
			}
			canonical, err := canonicalLaunchPath(path)
			if err == nil {
				return filepath.Clean(canonical), nil
			}
		}
	}
	return "", errors.New("MCP executable was not found in the explicit launch context")
}

func launchExecutableDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("MCP executable is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512*1024*1024 {
		return "", errors.New("MCP executable is not a bounded regular file")
	}
	hash := sha256.New()
	if n, err := io.Copy(hash, io.LimitReader(file, 512*1024*1024+1)); err != nil || n > 512*1024*1024 {
		return "", errors.New("MCP executable could not be fingerprinted")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func cloneResolvedLaunch(value toolcontract.ResolvedLaunch) toolcontract.ResolvedLaunch {
	value.ProtocolVersions = slices.Clone(value.ProtocolVersions)
	if value.Stdio != nil {
		local := *value.Stdio
		local.Args, local.Env = slices.Clone(local.Args), maps.Clone(local.Env)
		value.Stdio = &local
	}
	if value.HTTP != nil {
		remote := *value.HTTP
		remote.Headers = maps.Clone(remote.Headers)
		if remote.Credential != nil {
			credential := *remote.Credential
			remote.Credential = &credential
		}
		value.HTTP = &remote
	}
	return value
}

func launchEnvironmentEntries(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	sort.Strings(entries)
	return entries
}
