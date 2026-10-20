package dependencycache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/projectprep"
)

// This opt-in verifier installs a real hash-locked PyPI wheel into disposable
// task directories using the product's detector, broker and fixed pip program.
func TestPythonNativeOfflinePreparation(t *testing.T) {
	python, wheelFile := os.Getenv("TRAVERSE_PYTHON_TEST_EXECUTABLE"), os.Getenv("TRAVERSE_PYTHON_TEST_WHEEL")
	if python == "" || wheelFile == "" {
		t.Skip("set TRAVERSE_PYTHON_TEST_EXECUTABLE and TRAVERSE_PYTHON_TEST_WHEEL to verify real offline pip")
	}
	if !filepath.IsAbs(python) || !filepath.IsAbs(wheelFile) {
		t.Fatal("native verifier requires absolute interpreter and wheel paths")
	}
	body, err := os.ReadFile(wheelFile)
	if err != nil {
		t.Fatal(err)
	}
	const digest = "4721f391ed90541fddacab5acf947aa0d3dc7d27b2e1e8eda2be8970586c3274"
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != digest {
		t.Fatal("native fixture needs the pinned six 1.17.0 PyPI wheel")
	}
	broker, err := New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, prefix, packageFields, wheelFields, extra, want string
		tamperWheel, tamperLock, zero                         bool
	}{
		{name: "install-and-test"},
		{name: "selected-marker", packageFields: "marker = \"python_version >= '3.10'\"\n"},
		{name: "unselected-marker", packageFields: "marker = \"python_version < '3'\"\n", zero: true},
		{name: "default-group", prefix: "requires-python='>=3.10'\ndefault-groups=['default']\n", packageFields: "marker = \"'default' in dependency_groups\"\n"},
		{name: "requires-python", prefix: "requires-python = '>=99'\n", want: "does not satisfy requires-python"},
		{name: "package-requires-python", packageFields: "requires-python = '>=99'\n", want: "does not satisfy requires-python"},
		{name: "environment", prefix: "environments = [\"os_name == 'future-os'\"]\n", want: "outside this lock"},
		{name: "invalid-marker", packageFields: "marker = 'invalid marker syntax'\n", want: "InvalidMarker"},
		{name: "incompatible-wheel", wheelFields: "name = 'six-1.17.0-cp299-cp299-any.whl'\n", want: "No compatible locked wheel"},
		{name: "wheel-size", wheelFields: "size = 1\n", want: "wheel size mismatch"},
		{name: "ambiguous-package", extra: "[[packages]]\nname='six'\nversion='1.17.0'\n[[packages.wheels]]\nurl='https://files.pythonhosted.org/six-1.17.0-py2.py3-none-any.whl'\nhashes={sha256='" + digest + "'}\n", want: "Ambiguous Python lock entries"},
		{name: "wheel-changed", tamperWheel: true, want: "wheel integrity mismatch"},
		{name: "lock-changed", tamperLock: true, want: "project or lock changed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			workspace, err := os.MkdirTemp("", "python-prep-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(workspace) })
			workspace, err = filepath.EvalSymlinks(workspace)
			if err != nil {
				t.Fatal(err)
			}
			prefix := scenario.prefix
			if prefix == "" {
				prefix = "requires-python = '>=3.10'\n"
			}
			lock := prefix + "lock-version = '1.0'\ncreated-by = 'native-fixture'\n[[packages]]\nname = 'six'\nversion = '1.17.0'\n" + scenario.packageFields + "[[packages.wheels]]\n" + scenario.wheelFields + "url = 'https://files.pythonhosted.org/six-1.17.0-py2.py3-none-any.whl'\nhashes = {sha256 = '" + digest + "'}\n" + scenario.extra
			for name, text := range map[string]string{"pyproject.toml": "[project]\nname='native-python-test'\nversion='0.1.0'\n", "pylock.toml": lock, "test_dependency.py": "import unittest, six\nclass DependencyTest(unittest.TestCase):\n def test_dependency(self):\n  self.assertEqual(six.__version__, '1.17.0')\n  self.assertIn('.traverse-preparation', six.__file__)\n"} {
				if err := os.WriteFile(filepath.Join(workspace, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := projectprep.Detect(t.Context(), workspace, ".")
			if err != nil || !plan.Ready {
				t.Fatalf("plan %+v %v", plan, err)
			}
			for _, pkg := range plan.Packages {
				key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
				if err := os.WriteFile(filepath.Join(broker.root, hex.EncodeToString(key[:])+".blob"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			stats, err := broker.Prepare(t.Context(), plan, nil, workspace, nil, func(context.Context) error { return nil })
			if err != nil || stats.FetchedPackages != 0 || stats.CachedPackages != len(plan.Packages) {
				t.Fatalf("offline broker stats=%+v err=%v", stats, err)
			}
			if scenario.tamperWheel {
				if err := os.WriteFile(filepath.Join(workspace, filepath.FromSlash(projectprep.PythonWheelDirectory(".")), plan.Packages[0].ID), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.tamperLock {
				if err := os.WriteFile(filepath.Join(workspace, "pylock.toml"), []byte(lock+"\n# changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			seedSum := sha256.Sum256(plan.PythonSeed)
			common := []string{workspace, filepath.Join(workspace, filepath.FromSlash(projectprep.PythonPlanFile("."))), hex.EncodeToString(seedSum[:])}
			install := append(projectprep.PythonPreparationProgram(false), common...)
			install = append(install, filepath.Join(workspace, filepath.FromSlash(projectprep.PythonWheelDirectory("."))), filepath.Join(workspace, filepath.FromSlash(projectprep.PythonSiteDirectory("."))))
			run := func(arguments []string) (string, error) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, python, arguments...)
				cmd.Dir = workspace
				cmd.Env = append(os.Environ(), "PIP_INDEX_URL=http://127.0.0.1:9/", "PIP_EXTRA_INDEX_URL=", "PIP_NO_INDEX=1", "PIP_DISABLE_PIP_VERSION_CHECK=1")
				output, err := cmd.CombinedOutput()
				return string(output), err
			}
			output, err := run(install)
			if scenario.want != "" {
				if err == nil || !strings.Contains(output, scenario.want) {
					t.Fatalf("expected %q: err=%v output=%s", scenario.want, err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("real offline pip failed: %v\n%s", err, output)
			}
			t.Logf("real pip output:\n%s", output)
			if scenario.zero {
				if !strings.Contains(output, "selected packages: 0") {
					t.Fatal("false marker still installed a package")
				}
				return
			}
			test := append(projectprep.PythonPreparationProgram(true), common...)
			test = append(test, filepath.Join(workspace, filepath.FromSlash(projectprep.PythonSiteDirectory("."))), "unittest", "discover", "-v")
			output, err = run(test)
			if err != nil || !strings.Contains(output, "OK") {
				t.Fatalf("prepared Python test failed: %v\n%s", err, output)
			}
			t.Logf("prepared test output:\n%s", output)
			output, err = run(install)
			if err != nil || !strings.Contains(output, "Reusing matching") {
				t.Fatalf("matching environment did not reuse: %v\n%s", err, output)
			}
		})
	}
}
