package runner

import "cyberagent-workbench/internal/commandruntimeadapter"

// The immutable adapter family distinguishes a native binary digest from a
// pinned guest-template/path binding. Historical jobs retain the empty kind.
func CommandRuntimeExecutableIdentityKind(adapter commandruntimeadapter.Identity) string {
	if adapter.Kind == commandruntimeadapter.KindSandboxedWorkspace && (adapter.Backend == "docker_sandboxes" ||
		(adapter.Backend == "docker_standard_code" && adapter.BackendIdentity == "docker-standard-code-guest.v1")) {
		return CommandRuntimeExecutableTemplatePathSHA256
	}
	return ""
}
