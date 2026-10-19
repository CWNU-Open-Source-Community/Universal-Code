package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/projectprep"
)

// Uses the product's fixed runner, read-only rootfs, scratch HOME, network-none
// manifest and local Engine lifecycle. The tarball is local test input; this
// test performs no registry downloads or Docker image builds.
func TestDockerPNPMWorkspacePreparationRealDaemonOptIn(t *testing.T) {
	imageDigest := strings.TrimSpace(os.Getenv(dockerStandardCodeIntegrationImageEnv))
	if imageDigest == "" {
		t.Skip("set " + dockerStandardCodeIntegrationImageEnv + " to the fixed pnpm " + DockerStandardCodePNPMVersion + " image digest")
	}
	probe, err := NewLocalDockerReadinessProbe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if ready, err := probe.PNPMPreparationAvailable(ctx, imageDigest); err != nil || !ready {
		t.Fatalf("rebuild the Standard Code image with pnpm %s: ready=%t err=%v", DockerStandardCodePNPMVersion, ready, err)
	}
	local, ok := newLocalDockerContainerLifecycleTransport().(localDockerContainerLifecycleTransport)
	if !ok {
		t.Fatal("fixed local Docker lifecycle transport is unavailable")
	}
	root := t.TempDir()
	target := "apps/web"
	stage := projectprep.StageDirectory(target)
	seed := projectprep.PNPMSeedDirectory(target)
	guestStage := DockerStandardCodeWorkspaceTarget + "/" + stage
	archive := pnpmIntegrationPackage(t)
	digest := sha512.Sum512(archive)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(digest[:])
	lock := fmt.Sprintf("lockfileVersion: '9.0'\nsettings:\n  autoInstallPeers: true\n  excludeLinksFromLockfile: false\nimporters:\n  .: {}\n  apps/web:\n    dependencies:\n      prepared-lib:\n        specifier: 1.0.0\n        version: 1.0.0\n      local-lib:\n        specifier: workspace:*\n        version: link:../../packages/local-lib\n  packages/local-lib: {}\npackages:\n  prepared-lib@1.0.0:\n    resolution:\n      integrity: %s\nsnapshots:\n  prepared-lib@1.0.0: {}\n", integrity)
	files := map[string][]byte{
		".git":                            []byte("gitdir: " + filepath.Join(t.TempDir(), ".git", "worktrees", "pnpm-fixture") + "\n"),
		"package.json":                    []byte(`{"private":true,"scripts":{"postinstall":"node -e \"require('fs').writeFileSync('unapproved-script','ran')\""}}`),
		"pnpm-workspace.yaml":             []byte("packages:\n  - apps/*\n  - packages/*\n"),
		"pnpm-lock.yaml":                  []byte(lock),
		"apps/web/package.json":           []byte(`{"name":"web","private":true,"scripts":{"test":"node test.cjs"},"dependencies":{"prepared-lib":"1.0.0","local-lib":"workspace:*"}}`),
		"apps/web/test.cjs":               []byte(`const fs=require('node:fs'); const assert=require('node:assert/strict'); assert.equal(require('prepared-lib'),'prepared'); assert.equal(require('local-lib'),'workspace'); assert(fs.statSync(process.env.HOME).isDirectory()); for(const name of ['NPM_CONFIG_USERCONFIG','NPM_CONFIG_GLOBALCONFIG']) assert.equal(fs.readFileSync(process.env[name],'utf8'),''); fs.writeFileSync('pnpm-result.txt','offline workspace and scratch HOME verified');`),
		"packages/local-lib/package.json": []byte(`{"name":"local-lib","version":"1.0.0","main":"index.cjs"}`),
		"packages/local-lib/index.cjs":    []byte(`module.exports='workspace'`),
		stage + "/prepared-lib.tgz":       archive,
		seed + "/package.json":            []byte(`{"private":true}`),
		seed + "/pnpm-workspace.yaml":     []byte("packages: []\n"),
		seed + "/pnpm-lock.yaml":          []byte(strings.Replace(lock, "      integrity: "+integrity, "      integrity: "+integrity+"\n      tarball: file:"+guestStage+"/prepared-lib.tgz", 1)),
	}
	for name, body := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	common := []string{"--offline", "--config.ignore-pnpmfile=true", "--config.manage-package-manager-versions=false", "--reporter=append-only",
		"--store-dir=/workspace/" + projectprep.PNPMStoreDirectory(target), "--config.cache-dir=" + guestStage + "/pnpm-cache",
		"--config.state-dir=" + guestStage + "/pnpm-state", "--config.global-dir=" + guestStage + "/pnpm-global"}
	for _, phase := range []struct {
		name, cwd, toolchain string
		arguments            []string
	}{
		{"fetch", seed, DockerStandardCodeToolchainNodePrepare, append([]string{"fetch", "--frozen-lockfile", "--ignore-scripts"}, common...)},
		{"install", ".", DockerStandardCodeToolchainNodePrepare, append([]string{"install", "--frozen-lockfile", "--ignore-scripts"}, common...)},
		{"test", target, DockerStandardCodeToolchainNode, append(append([]string{}, common...), "--config.verify-deps-before-run=false", "test")},
	} {
		binding := dockerStandardCodeBindingFixture(phase.toolchain)
		binding.WorkingDirectory, binding.Arguments = phase.cwd, append([]string{DockerStandardCodePNPMCLI}, phase.arguments...)
		binding.TimeoutSeconds = 45
		manifest, err := DockerStandardCodeManifest(binding)
		if err != nil {
			t.Fatal(err)
		}
		ready, err := probe.Check(ctx, DockerRuntimeCapabilities{Enabled: true}, manifest, imageDigest)
		if err != nil || !ready.Ready || ready.NetworkMode != "disabled" {
			t.Fatalf("%s readiness: %+v %v", phase.name, ready, err)
		}
		write := standardCodeIntegrationWriteRequest(t, ctx, root, imageDigest, manifest, "pnpm-"+phase.name)
		stage, err := local.Stage(ctx, write)
		if err != nil {
			t.Fatal(err)
		}
		request, err := NewDockerContainerLifecycleRequest("pnpm-"+phase.name+"-attempt", 1, write, stage, DockerContainerLifecycleConfirmation)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupDockerLifecycleIntegrationContainer(t, local, request, write) })
		result, err := local.Run(ctx, request)
		if err != nil || result.Validate() != nil || result.Status != DockerContainerLifecycleStatusExited || result.ExitCode != 0 || !result.ContainerRemoved || !result.TargetAbsenceConfirmed {
			t.Fatalf("pnpm %s failed: %+v %v", phase.name, result, err)
		}
	}
	if result, err := os.ReadFile(filepath.Join(root, "apps", "web", "pnpm-result.txt")); err != nil || string(result) != "offline workspace and scratch HOME verified" {
		t.Fatalf("pnpm test output %q: %v", result, err)
	}
	if actual, err := os.ReadFile(filepath.Join(root, "pnpm-lock.yaml")); err != nil || string(actual) != lock {
		t.Fatal("original workspace lockfile changed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unapproved-script")); !os.IsNotExist(err) {
		t.Fatal("script-free preparation ran project postinstall", err)
	}
}

func pnpmIntegrationPackage(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, file := range []struct{ name, body string }{
		{"package/package.json", `{"name":"prepared-lib","version":"1.0.0","main":"index.cjs"}`},
		{"package/index.cjs", `module.exports='prepared'`},
	} {
		if err := archive.WriteHeader(&tar.Header{Name: file.name, Mode: 0644, Size: int64(len(file.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
