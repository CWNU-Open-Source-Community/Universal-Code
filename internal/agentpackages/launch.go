package agentpackages

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"cyberagent-workbench/internal/toolcontract"
)

func bindLaunch(server serverDescription, ref toolcontract.ComponentRef) (toolcontract.LaunchDeclaration, error) {
	declaration := toolcontract.LaunchDeclaration{Component: ref, Format: FormatAgentPlugin, FormatVersion: "1.0.0"}
	switch server.transport {
	case "stdio":
		var fields struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			Cwd     string            `json:"cwd"`
		}
		if json.Unmarshal(server.declaration, &fields) != nil {
			return declaration, errors.New("launch_declaration_invalid")
		}
		declaration.Transport = toolcontract.TransportStdio
		declaration.Stdio = &toolcontract.StdioLaunch{Command: fields.Command, Args: fields.Args, Env: fields.Env, Cwd: fields.Cwd}
	case "streamable-http", "sse":
		var fields struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		if json.Unmarshal(server.declaration, &fields) != nil {
			return declaration, errors.New("launch_declaration_invalid")
		}
		declaration.Transport = toolcontract.TransportStreamableHTTP
		if server.transport == "sse" {
			declaration.Transport = toolcontract.TransportSSE
		}
		declaration.HTTP = &toolcontract.HTTPLaunch{Endpoint: fields.URL, Headers: fields.Headers}
	default:
		return declaration, errors.New("launch_transport_unsupported")
	}
	if declaration.Validate() != nil {
		return declaration, errors.New("launch_host_bounds_exceeded")
	}
	return declaration, nil
}

func cloneLaunch(value toolcontract.LaunchDeclaration) toolcontract.LaunchDeclaration {
	value.ProtocolVersions = slices.Clone(value.ProtocolVersions)
	if value.Stdio != nil {
		stdio := *value.Stdio
		stdio.Args = slices.Clone(stdio.Args)
		stdio.Env = maps.Clone(stdio.Env)
		value.Stdio = &stdio
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
