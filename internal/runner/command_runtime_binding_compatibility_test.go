package runner

import (
	"strings"
	"testing"
)

func TestCommandRuntimeOrdinaryLaunchBindingCompatibility(t *testing.T) {
	spec := CommandRuntimeResolvedSpec{
		Spec: CommandRuntimeSpec{
			Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
			Executable: "/toolchains/go", Arguments: []string{"version"},
			WorkingDirectory: ".", Environment: []CommandRuntimeEnvironment{},
			StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 1000,
			Output:              CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network:             CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
			Purpose: "ordinary command",
		},
		ExecutablePath: "/toolchains/go", ExecutableSHA256: strings.Repeat("a", 64),
		CanonicalArgv: []string{"version"}, EnvironmentSHA256: strings.Repeat("b", 64),
		WorkspaceRootSHA256: strings.Repeat("c", 64), ExecutablePinned: true,
	}
	// These are the pre-invocation-path bindings. Empty optional fields must
	// preserve ordinary stored intents and their authorization fingerprints.
	const fingerprint = "9355aa7ad9de91f7fa3e7bcb585bd98f4d1c103365a3ead53b3efca8cee91f7b"
	const intent = `{"version":"command-runtime.v2","policy_version":"command-runtime-policy.v2","profile":"process","executable_path":"/toolchains/go","executable_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","argv":["version"],"working_directory":".","environment":[],"environment_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","stdin_policy":"closed","initial_stdin_bytes":0,"initial_stdin_sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","close_initial_stdin":true,"timeout_milliseconds":1000,"output":{"inline_bytes":4096,"artifact_bytes":4096},"network":"disabled","credentials":"none","purpose":"ordinary command"}`
	if actual := CommandRuntimeSpecFingerprint(spec); actual != fingerprint {
		t.Fatalf("ordinary fingerprint changed: %s", actual)
	}
	if actual := commandRuntimeIntentJSON(spec); actual != intent {
		t.Fatalf("ordinary stored intent changed: %s", actual)
	}
	spec.Spec.Executable = "/aliases/go"
	if CommandRuntimeSpecFingerprint(spec) != fingerprint || commandRuntimeIntentJSON(spec) != intent {
		t.Fatal("a non-Python alias changed its historical launch binding")
	}
}
