package projectprep_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

const pythonLockFixture = `lock-version = '1.0'
created-by = 'fixture'
requires-python = '>=3.10'
[[packages]]
name = 'six'
version = '1.17.0'
marker = "python_version >= '3.10'"
[[packages.wheels]]
url = 'https://files.pythonhosted.org/six-1.17.0-py2.py3-none-any.whl'
hashes = {sha256 = '4721f391ed90541fddacab5acf947aa0d3dc7d27b2e1e8eda2be8970586c3274'}
`

func TestPythonPlanBindsWheelCandidatesAndConditions(t *testing.T) {
	root := fixture(t, map[string]string{"pkg/pyproject.toml": "[project]\nrequires-python='>=3.10'\n", "pkg/pylock.toml": pythonLockFixture})
	plan, err := projectprep.Detect(context.Background(), root, "pkg")
	if err != nil || !plan.Ready || plan.Ecosystem != "python" || len(plan.Packages) != 1 || len(plan.Lockfiles) != 1 || plan.Install.ID != "install.python" || plan.Test.ID != "test.python" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if plan.Packages[0].Kind != projectprep.KindPythonWheel || len(plan.PythonSeed) == 0 || !strings.Contains(string(plan.PythonSeed), "python_version") || len(plan.Sources) != 1 || plan.Sources[0] != "https://files.pythonhosted.org" {
		t.Fatalf("incomplete Python binding: %+v", plan)
	}
	public, _ := json.Marshal(plan)
	if strings.Contains(string(public), ".whl") || strings.Contains(string(public), "python_version") {
		t.Fatalf("private installation input leaked: %s", public)
	}
	other := fixture(t, map[string]string{"pkg/pyproject.toml": "[project]\nrequires-python='>=3.10'\n", "pkg/pylock.toml": strings.ReplaceAll(pythonLockFixture, ">=3.10", ">=3.11")})
	changed, _ := projectprep.Detect(context.Background(), other, "pkg")
	if changed.Fingerprint == plan.Fingerprint || changed.CacheKey == plan.CacheKey || string(changed.PythonSeed) == string(plan.PythonSeed) {
		t.Fatal("Python conditions can reuse an old reviewed plan or environment")
	}
}

func TestPythonPlanRejectsUnsupportedAndUnsafeSources(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		mutate func(string) string
	}{
		{"version", func(s string) string { return strings.Replace(s, "lock-version = '1.0'", "lock-version = '2.0'", 1) }},
		{"missing-hash", func(s string) string { return strings.Replace(s, "sha256 =", "sha512 =", 1) }},
		{"local-wheel", func(s string) string {
			return strings.Replace(s, "url = 'https://files.pythonhosted.org/six-1.17.0-py2.py3-none-any.whl'", "path = '../outside.whl'", 1)
		}},
		{"source-build", func(s string) string { return strings.Replace(s, "[[packages.wheels]]", "[packages.sdist]", 1) }},
		{"optional-extras", func(s string) string { return "extras=['test']\n" + s }},
		{"credential-url", func(s string) string { return strings.Replace(s, "https://files.", "https://user:pass@files.", 1) }},
		{"escaping-name", func(s string) string {
			return strings.Replace(s, "[[packages.wheels]]", "[[packages.wheels]]\nname='../escape.whl'", 1)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := fixture(t, map[string]string{"pylock.toml": scenario.mutate(pythonLockFixture)})
			plan, err := projectprep.Detect(t.Context(), root, ".")
			if err != nil || plan.Ready || len(plan.Issues) == 0 {
				t.Fatalf("unsafe Python plan: %+v %v", plan, err)
			}
		})
	}
	root := fixture(t, map[string]string{"pyproject.toml": "[project]\nname='example'\n"})
	plan, err := projectprep.Detect(t.Context(), root, ".")
	if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "pylock.toml") {
		t.Fatalf("missing lock was accepted: %+v %v", plan, err)
	}
}

func TestPythonPlanRejectsMixedToolchains(t *testing.T) {
	for _, manifest := range []string{"package.json", "go.mod", "Cargo.toml"} {
		t.Run(manifest, func(t *testing.T) {
			root := fixture(t, map[string]string{"pylock.toml": pythonLockFixture, manifest: ""})
			plan, err := projectprep.Detect(t.Context(), root, ".")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "Multiple dependency toolchains") {
				t.Fatalf("mixed toolchains accepted: %+v err=%v", plan, err)
			}
		})
	}
}
