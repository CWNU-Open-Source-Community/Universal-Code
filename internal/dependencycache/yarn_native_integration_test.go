package dependencycache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/projectprep"
)

// Runs only this test's generated project, using an explicitly supplied Yarn
// CLI. Product execution continues to require the fixed Docker toolchain.
func TestYarnNativeOfflineMirror(t *testing.T) {
	node, cli := os.Getenv("TRAVERSE_YARN_TEST_NODE"), os.Getenv("TRAVERSE_YARN_TEST_CLI")
	if node == "" || cli == "" {
		t.Skip("set TRAVERSE_YARN_TEST_NODE and TRAVERSE_YARN_TEST_CLI for native Yarn verification")
	}
	for _, tool := range []string{node, cli} {
		if info, err := os.Stat(tool); !filepath.IsAbs(tool) || err != nil || !info.Mode().IsRegular() {
			t.Fatalf("explicit absolute tool required: %q %v", tool, err)
		}
	}
	tools := t.TempDir()
	networkLog, preload := filepath.Join(tools, "network-attempts"), filepath.Join(tools, "deny-network.cjs")
	block := fmt.Sprintf(`const fs=require('node:fs');
const deny=()=>{fs.appendFileSync(%q,'attempt\n');throw new Error('YARN_TEST_NETWORK_DENIED')};
const net=require('node:net');net.connect=net.createConnection=net.Socket.prototype.connect=deny;
require('node:tls').connect=deny;require('node:dns').lookup=deny;
for(const name of ['node:http','node:https']){const m=require(name);m.request=m.get=deny}
globalThis.fetch=deny;require('node:module').syncBuiltinESMExports();
`, networkLog)
	if err := os.WriteFile(preload, []byte(block), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(directory string, arguments ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, node, append([]string{"--require", preload, cli}, arguments...)...)
		command.Dir = directory
		command.Env = append(os.Environ(), "CI=true", "NODE_OPTIONS=", "YARN_IGNORE_PATH=1", "PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := command.CombinedOutput()
		t.Logf("yarn %v:\n%s", arguments, output)
		if err != nil {
			t.Fatalf("native Yarn failed: %v", err)
		}
		return strings.TrimSpace(string(output))
	}
	if version := run(tools, "--no-default-rc", "--version"); version != projectprep.YarnVersion {
		t.Fatalf("wrong Yarn version %q", version)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, file := range []struct{ name, body string }{
		{"package/package.json", `{"name":"mirror-example","version":"1.0.0","main":"index.cjs","bin":{"mirror-example":"bin.cjs"},"scripts":{"postinstall":"node -e \"require('fs').writeFileSync('install-ran','bad')\""}}`},
		{"package/index.cjs", "module.exports='mirror-ok';\n"},
		{"package/bin.cjs", "#!/usr/bin/env node\nconsole.log('bin-ok');\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0755, Size: int64(len(file.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		t.Fatal(err)
	}
	body := archive.Bytes()
	hash := sha512.Sum512(body)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(hash[:])
	files := map[string]string{
		"package.json":      `{"name":"offline-example","version":"1.0.0","private":true,"packageManager":"yarn@1.22.22","dependencies":{"mirror-example":"1.0.0"},"scripts":{"test":"node test.cjs && mirror-example","postinstall":"node -e \"require('fs').writeFileSync('install-ran','bad')\""}}`,
		"yarn.lock":         "# yarn lockfile v1\n\nmirror-example@1.0.0:\n  version \"1.0.0\"\n  resolved \"https://registry.npmjs.org/mirror-example/-/mirror-example-1.0.0.tgz\"\n  integrity " + integrity + "\n",
		"test.cjs":          "require('node:assert/strict').equal(require('mirror-example'),'mirror-ok');console.log('dependency-import-ok');\n",
		".yarnrc":           "yarn-path \"./untrusted-cli.cjs\"\nregistry \"https://invalid.example\"\n",
		"untrusted-cli.cjs": "require('fs').writeFileSync('untrusted-cli-ran','bad');throw Error('project yarn-path executed');\n",
	}
	requests := 0
	broker, firstRoot := brokerFixture(t, func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != "https://registry.npmjs.org/mirror-example/-/mirror-example-1.0.0.tgz" {
			t.Fatal("unexpected artifact", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	for trial := 0; trial < 2; trial++ {
		root := firstRoot
		if trial == 1 {
			var err error
			root, err = filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
		}
		project := filepath.Join(root, "中文 project")
		if err := os.MkdirAll(project, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(project, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := projectprep.Detect(t.Context(), root, "中文 project")
		if err != nil || !plan.Ready {
			t.Fatal(plan, err)
		}
		sources := plan.Sources
		if trial == 1 {
			sources = nil
		}
		stats, err := broker.Prepare(t.Context(), plan, sources, root, nil, func(context.Context) error { return nil })
		if err != nil || requests != 1 || (trial == 1 && (stats.Bytes != 0 || stats.CachedPackages != 1)) {
			t.Fatal(stats, requests, err)
		}
		stage := filepath.Join(root, projectprep.StageDirectory(plan.Target))
		args := []string{"--no-default-rc", "--use-yarnrc", filepath.Join(stage, "yarnrc"), "--cache-folder", filepath.Join(stage, "yarn-cache"), "--offline", "--non-interactive", "--no-progress"}
		run(project, append(append([]string{"install"}, args...), "--frozen-lockfile", "--ignore-scripts")...)
		result := run(project, append(append([]string{}, args...), "run", "test")...)
		if !strings.Contains(result, "dependency-import-ok") || !strings.Contains(result, "bin-ok") {
			t.Fatal("dependency or executable link did not work", result)
		}
		for _, name := range []string{"install-ran", "node_modules/mirror-example/install-ran", "untrusted-cli-ran"} {
			if _, err := os.Stat(filepath.Join(project, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unapproved code ran: %s %v", name, err)
			}
		}
		for _, name := range []string{"package.json", "yarn.lock", ".yarnrc"} {
			data, err := os.ReadFile(filepath.Join(project, name))
			if err != nil || string(data) != files[name] {
				t.Fatal("project source changed", name, err)
			}
		}
	}
	if _, err := os.Stat(networkLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Yarn attempted networking", err)
	}
}
