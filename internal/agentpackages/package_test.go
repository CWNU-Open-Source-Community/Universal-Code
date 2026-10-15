package agentpackages_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/agentpackages"
	"cyberagent-workbench/internal/toolcontract"
)

func pinnedSource() toolcontract.SourceRef {
	return toolcontract.SourceRef{URI: "https://github.com/agentplugins/agent-plugins-example", Revision: "5f3f5084a821aefa792e79500dd8f0462ab83473"}
}

func TestPublicUnmodifiedPluginToActivationAndResource(t *testing.T) {
	ctx := context.Background()
	dir := "testdata/upstream/agent-plugins-example"
	pkg, err := agentpackages.OpenDirectory(ctx, dir, "host-installation-1", pinnedSource())
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	if pkg.Format() != agentpackages.FormatAgentPlugin || pkg.Name() != "agent-plugins-example" || pkg.Version() != "1.0.0" || pkg.Source() != pinnedSource() || pkg.Manifest().Validate() != nil {
		t.Fatal("package format, author version or host provenance lost")
	}
	refs := pkg.Skills()
	if len(refs) != 1 || refs[0].Validate() != nil {
		t.Fatal("missing valid component identity")
	}
	ref := refs[0]
	name, description, err := pkg.SkillSummary(ref)
	if err != nil || name != "migrate-agent-plugin" || description == "" {
		t.Fatal("metadata not available")
	}
	frontmatter, err := pkg.Frontmatter(ref)
	if err != nil {
		t.Fatal(err)
	}
	first := bytes.Clone(frontmatter)
	frontmatter[0] = 'X'
	again, _ := pkg.Frontmatter(ref)
	if !bytes.Equal(first, again) {
		t.Fatal("frontmatter was not detached")
	}
	instructions, err := pkg.Instructions(ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pkg.Read(ctx, instructions, 100000)
	want, readErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(instructions.Path)))
	if err != nil || readErr != nil || !bytes.Equal(raw, want) {
		t.Fatal("activation did not retain original SKILL.md")
	}
	resourcePath := "references/migration-guide.md"
	want, err = os.ReadFile(filepath.Join(dir, "skills", name, filepath.FromSlash(resourcePath)))
	if err != nil {
		t.Fatal(err)
	}
	resource, err := pkg.Resource(ref, resourcePath, hash(want))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pkg.Read(ctx, resource, 100000)
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatal("on-demand resource bytes changed")
	}
	wrong := resource
	wrong.Component.PackageID = "other-installation"
	if _, err := pkg.Read(ctx, wrong, 100000); err == nil {
		t.Fatal("cross-package component read accepted")
	}
	wrong = resource
	wrong.Path = "plugin.json"
	if _, err := pkg.Read(ctx, wrong, 100000); err == nil {
		t.Fatal("skill reference read outside its component")
	}
	if _, err := pkg.Resource(ref, "../../plugin.json", hash(nil)); err == nil {
		t.Fatal("resource traversal accepted")
	}
}

func TestPublicLargeStandaloneSkillAndEmptyAssets(t *testing.T) {
	dir := "testdata/upstream/anthropic-skill-creator/skills/skill-creator"
	source := toolcontract.SourceRef{URI: "https://github.com/anthropics/skills/tree/8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4/skills/skill-creator", Revision: "8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4"}
	pkg, err := agentpackages.OpenDirectory(context.Background(), dir, "standalone-installation", source)
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	if pkg.Format() != agentpackages.FormatAgentSkill || pkg.Version() != "" || len(pkg.Skills()) != 1 {
		t.Fatal("standalone skill invented a version or lost its format")
	}
	ref := pkg.Skills()[0]
	instructions, _ := pkg.Instructions(ref)
	raw, err := pkg.Read(context.Background(), instructions, 100000)
	if err != nil || len(raw) != 33168 {
		t.Fatal("real instructions rejected by legacy token/size rules")
	}
	empty, err := pkg.Resource(ref, "scripts/__init__.py", hash(nil))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pkg.Read(context.Background(), empty, 1)
	if err != nil || len(raw) != 0 {
		t.Fatal("empty upstream file lost")
	}
}

func writePackage(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, raw := range files {
		file := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func standardFiles() map[string][]byte {
	return map[string][]byte{
		"plugin.json":                   []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"test-plugin"}`),
		"skills/good/SKILL.md":          []byte("---\r\nname: good\r\ndescription: Demonstrate loading.\r\n---\r\nUse the resource when needed.\r\n"),
		"skills/good/assets/binary.bin": {0, 255, 3},
	}
}

func TestPublicLaunchBindingFailureIsolationAndNoExpansion(t *testing.T) {
	files := standardFiles()
	files["mcp.json"] = []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{
"stdio":{"type":"stdio","command":"./bin/tool","args":["${PLUGIN_ROOT}/entry","${UNKNOWN}"],"cwd":"${PLUGIN_DATA}","env":{"TOKEN":"test-secret"}},
"http":{"type":"streamable-http","url":"https://example.invalid/mcp","headers":{"Authorization":"test-secret"}},
"legacy-sse":{"type":"sse","url":"https://example.invalid/events"},
"bad":{"type":"stdio","command":"tool","env":null}
}}`)
	dir := writePackage(t, files)
	pkg, err := agentpackages.OpenDirectory(context.Background(), dir, "host-installation-2", pinnedSource())
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	launches := pkg.Launches()
	if len(launches) != 3 || len(pkg.Skills()) != 1 || len(pkg.Diagnostics()) != 1 {
		t.Fatal("component failures leaked to siblings")
	}
	for _, launch := range launches {
		if err := launch.Validate(); err != nil {
			t.Fatal(err)
		}
		if launch.Format != agentpackages.FormatAgentPlugin || launch.FormatVersion != "1.0.0" || len(launch.ProtocolVersions) != 0 {
			t.Fatal("wire protocol confused with package schema")
		}
		key, content, found := pkg.ServerSource(launch.Component)
		if !found || key == "" || content.Path != "mcp.json" || content.SHA256 != hash(files["mcp.json"]) {
			t.Fatal("source component identity lost")
		}
		if launch.Stdio != nil {
			if launch.Stdio.Command != "./bin/tool" || launch.Stdio.Args[0] != "${PLUGIN_ROOT}/entry" || launch.Stdio.Cwd != "${PLUGIN_DATA}" || launch.Stdio.ExecutableSHA256 != "" {
				t.Fatal("A resolved a launch")
			}
			launch.Stdio.Env["TOKEN"] = "mutated"
			launch.Stdio.Args[0] = "mutated"
		}
		if launch.HTTP != nil && launch.HTTP.Headers != nil {
			launch.HTTP.Headers["Authorization"] = "mutated"
		}
	}
	for _, launch := range pkg.Launches() {
		if launch.Stdio != nil && (launch.Stdio.Env["TOKEN"] != "test-secret" || launch.Stdio.Args[0] != "${PLUGIN_ROOT}/entry") {
			t.Fatal("caller mutated stored stdio declaration")
		}
		if launch.HTTP != nil && launch.Transport == toolcontract.TransportStreamableHTTP && launch.HTTP.Headers["Authorization"] != "test-secret" {
			t.Fatal("caller mutated stored HTTP declaration")
		}
	}
	for _, diagnostic := range pkg.Diagnostics() {
		if diagnostic.Validate() != nil || strings.Contains(diagnostic.Message, "test-secret") {
			t.Fatal("invalid or secret-bearing diagnostic")
		}
	}
	if strings.Contains(fmt.Sprintf("%v %#v", pkg, pkg), "test-secret") {
		t.Fatal("package formatting leaked launch data")
	}
}

func TestPublicContentDigestDriftAndBounds(t *testing.T) {
	files := standardFiles()
	dir := writePackage(t, files)
	pkg, err := agentpackages.OpenDirectory(context.Background(), dir, "digest-installation", pinnedSource())
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	ref := pkg.Skills()[0]
	content, _ := pkg.Resource(ref, "assets/binary.bin", hash(files["skills/good/assets/binary.bin"]))
	raw, err := pkg.Read(context.Background(), content, 3)
	if err != nil || !bytes.Equal(raw, []byte{0, 255, 3}) {
		t.Fatal("binary resource changed")
	}
	if _, err := pkg.Read(context.Background(), content, 2); err == nil {
		t.Fatal("read limit ignored")
	}
	if err := os.WriteFile(filepath.Join(dir, "skills/good/assets/binary.bin"), []byte{0, 255, 4}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.Read(context.Background(), content, 3); err == nil {
		t.Fatal("same-size resource revision drift ignored")
	}
	instructions, _ := pkg.Instructions(ref)
	instructions.SHA256 = hash([]byte("replacement"))
	if _, err := pkg.Read(context.Background(), instructions, 100000); err == nil {
		t.Fatal("instruction digest was caller-replaceable")
	}
}

func TestPublicLegacySelectionAndNoMalformedStandardFallback(t *testing.T) {
	for _, test := range []struct{ name, body, format string }{
		{"manifest.json", `{"protocol":"skill.v1"}`, agentpackages.FormatLegacySkill},
		{"plugin.json", `{"protocol":"plugin.v1"}`, agentpackages.FormatLegacyPlugin},
	} {
		dir := writePackage(t, map[string][]byte{test.name: []byte(test.body), "SKILL.md": []byte("old content")})
		format, err := agentpackages.DetectDirectory(context.Background(), dir)
		if err != nil || format != test.format {
			t.Fatal("legacy format no longer routed")
		}
		if _, err := agentpackages.OpenDirectory(context.Background(), dir, "legacy", pinnedSource()); !errors.Is(err, agentpackages.ErrLegacyFormat) {
			t.Fatal("legacy codec was replaced or bypassed")
		}
	}
	files := standardFiles()
	files["plugin.json"] = []byte(`{"$schema":"unknown","protocol":"plugin.v1","name":"looks-legacy"}`)
	dir := writePackage(t, files)
	_, err := agentpackages.OpenDirectory(context.Background(), dir, "bad-standard", pinnedSource())
	if err == nil || errors.Is(err, agentpackages.ErrLegacyFormat) {
		t.Fatal("malformed explicit standard downgraded to legacy")
	}
}

func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
