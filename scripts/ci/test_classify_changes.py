from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
from unittest.mock import patch

from classify_changes import CHECKS, ClassificationError, changed_paths, classify_paths, main
import run_go_checks as runner


@patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": ""})
class ClassifyPathsTests(unittest.TestCase):
    def enabled(self, paths, affected=()):
        return {name for name, enabled in classify_paths(paths, affected).items() if enabled}

    def test_frontend_and_documentation_do_not_start_backend_or_native_matrix(self):
        for paths in ([b"web/src/v2/styles.css"], [b"web/package-lock.json"],
                      [b"web/public/traverse-board-favicon-32.png", b"docs/architecture.md"]):
            with self.subTest(paths=paths):
                self.assertEqual(self.enabled(paths), {"web", "journeys"})
        self.assertEqual(self.enabled([b"README.md", b"docs/ci.md"]), set())
        self.assertEqual(self.enabled([b"docs/openapi.json"]), {"backend", "web", "journeys"})

    def test_go_dependencies_select_store_without_unrelated_platforms(self):
        selected = self.enabled(
            [b"internal/toolgateway/registry.go"],
            ["project/internal/toolgateway", "project/internal/store",
             "project/internal/application", "project/internal/app",
             "project/internal/httpapi", "project/internal/desktop"],
        )
        self.assertEqual(selected, {"backend", "store", "journeys"})

    def test_documentation_screenshots_do_not_expand_frontend_checks(self):
        for directory in ("docs/acceptance", "docs/evidence", "docs/images"):
            for suffix in (".png", ".jpg", ".jpeg", ".webp", ".PNG"):
                screenshot = f"{directory}/browser-viewer/before{suffix}".encode()
                with self.subTest(screenshot=screenshot):
                    self.assertEqual(self.enabled([screenshot]), set())
                    for paths in ([screenshot, b"web/src/v2/components/agent-browser.tsx"],
                                  [b"web/src/v2/components/agent-browser.tsx", screenshot]):
                        self.assertEqual(self.enabled(paths), {"web", "journeys"})

    def test_screenshot_documentation_keeps_other_input_checks(self):
        screenshot = b"docs/acceptance/browser-viewer/after.png"
        for paths, affected in (
                ([b"assets/branding/traverse-board-mark.png"], ()),
                ([b"web/public/traverse-board-favicon-32.png"], ()),
                ([b"internal/workspace/testdata/reference.png"], ("project/internal/workspace",)),
                ([b"internal/domain/run.go"], ("project/internal/domain", "project/internal/store")),
                ([b"docs/openapi.json"], ()),
                ([b"go.mod"], ()),
                ([b"scripts/ci/classify_changes.py"], ())):
            with self.subTest(paths=paths):
                self.assertEqual(self.enabled([screenshot, *paths], affected), self.enabled(paths, affected))
        for path in (b"docs/acceptance/verify.py", b"docs/evidence/trace.json",
                     b"docs/images/browser.svg", b"docs/images/browser.png.sh",
                     b"docs/acceptance-runtime/before.png", b"unknown-assets/before.png"):
            with self.subTest(path=path):
                self.assertEqual(self.enabled([path, screenshot]), set(CHECKS))

    def test_product_journeys_follow_backend_consumers_without_native_matrix(self):
        checks = self.enabled([b"internal/llm/model_catalog.go"],
                              ["project/internal/llm", "project/internal/application"])
        self.assertEqual(checks, {"backend", "journeys"})
        checks = self.enabled([b"internal/app/api_command.go"], ["project/internal/app"])
        self.assertEqual(checks, {"backend", "native", "journeys"})
        self.assertEqual(self.enabled([b"cmd/workbenchjourney/main.go"]),
                         {"backend", "journeys"})
        self.assertEqual(self.enabled([b"internal/analyzer/vectors_test.go"]), {"backend"})

    def test_native_lsp_and_browser_runtime_inputs_are_selected(self):
        self.assertEqual(self.enabled([b"internal/codeintel/runtime.go"]),
                         {"backend", "lsp"})
        checks = self.enabled([b"internal/runner/process_windows.go"],
                              ["project/internal/runner", "project/internal/browserruntime"])
        self.assertTrue({"backend", "native", "ui"} <= checks)
        self.assertNotIn("packaging", checks)
        checks = self.enabled([b"internal/domain/run.go"], [
            "project/internal/domain", "project/internal/store", "project/internal/runner",
        ])
        self.assertTrue({"race", "store", "native"} <= checks)
        for path in (b"internal/store/command_runtime_jobs.go",
                     b"internal/store/command_operation_approval.go",
                     b"internal/application/run_capability_readiness.go",
                     b"internal/toolgateway/command_runtime.go"):
            self.assertIn("native", self.enabled([path]), path)

    def test_mixed_changes_keep_both_lanes_and_tagged_embedding_gets_native_checks(self):
        self.assertEqual(self.enabled([b"web/src/App.tsx", b"internal/llm/openai.go"]),
                         {"backend", "web", "journeys"})
        self.assertEqual(self.enabled([b"web/assets_desktop.go"]), {"web", "journeys", "native"})
        self.assertEqual(self.enabled([b"scripts/build-desktop.ps1"]), {"native", "packaging"})
        self.assertEqual(self.enabled([b"analyzers/src/lib.rs"]), {"rust", "native"})

    def test_global_inputs_and_unknown_paths_get_full_checks(self):
        for path in (b"go.mod", b"go.sum", b"scripts/ci/classify_changes.py",
                     b".github/workflows/ci.yml", b"new-toolchain.toml",
                     b"protocols/registry.json", b"README.md\nunknown=true"):
            with self.subTest(path=path):
                self.assertEqual(self.enabled([path]), set(CHECKS))

    def test_empty_diff_is_an_error(self):
        with self.assertRaises(ClassificationError):
            classify_paths([])

    def test_schedule_and_manual_run_full_without_a_diff(self):
        with patch("run_go_checks.collect_packages", return_value=[]) as collect, \
                patch("run_go_checks.select_plan", return_value={"full": True}) as select:
            for event in ("schedule", "workflow_dispatch"):
                output = StringIO()
                with redirect_stdout(output), redirect_stderr(StringIO()):
                    self.assertEqual(main(["--event", event, "--repo", "."]), 0)
                values = dict(line.split("=", 1) for line in output.getvalue().splitlines())
                self.assertTrue(all(json.loads(values["checks"]).values()))
                self.assertEqual(values["full"], "true")
            self.assertEqual(collect.call_count, 2)
            self.assertTrue(select.call_args.kwargs["full"])

    def test_failed_diff_emits_no_selection_output(self):
        output, errors = StringIO(), StringIO()
        with redirect_stdout(output), redirect_stderr(errors):
            result = main(["--event", "pull_request", "--repo", ".",
                           "--base", "bad", "--head", "also-bad"])
        self.assertEqual(result, 1)
        self.assertEqual(output.getvalue(), "")


@patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": ""})
class GoPlanClassificationTests(unittest.TestCase):
    def classify(self, path):
        root = Path("fixture-repo").absolute()
        module = "cyberagent-workbench"
        records = []
        for directory, imports in (
                ("internal/agentpackages", []), ("internal/mcp", []), ("internal/plugins", []),
                ("internal/application", []), ("internal/app", ["internal/application"]),
                ("internal/httpapi", ["internal/application"]), ("internal/store", [])):
            records.append({"ImportPath": module + "/" + directory,
                            "Dir": str(root / directory), "Module": {"Path": module, "Dir": str(root)},
                            "Imports": [module + "/" + imported for imported in imports]})
        output = StringIO()
        with patch("classify_changes.changed_paths", return_value=[path]), \
                patch("run_go_checks.collect_packages", return_value=records), \
                redirect_stdout(output), redirect_stderr(StringIO()):
            self.assertEqual(main(["--event", "pull_request", "--repo", str(root)]), 0)
        values = dict(line.split("=", 1) for line in output.getvalue().splitlines())
        checks, plan = json.loads(values["checks"]), json.loads(values["go_plan"])
        runner.validate_plan(plan)
        return {key for key, selected in checks.items() if selected}, plan

    def test_indirect_dto_change_keeps_openapi_golden_without_full_httpapi_suite(self):
        checks, plan = self.classify(b"internal/application/github_review_service.go")
        self.assertEqual(checks, {"backend", "journeys"})
        self.assertIn("cyberagent-workbench/internal/httpapi", plan["compile"])
        self.assertNotIn("cyberagent-workbench/internal/httpapi", plan["test"])
        self.assertEqual(plan["integrations"], [{"package": "cyberagent-workbench/internal/httpapi",
                         "run": "^(TestOpenAPIGoldenDocumentMatchesGoDTOs)$"}])

    def test_snapshot_only_runs_targeted_go_golden_and_web_api_check(self):
        checks, plan = self.classify(b"docs/openapi.json")
        self.assertEqual(checks, {"backend", "web", "journeys"})
        self.assertFalse(plan["full"])
        self.assertEqual(plan["affected"], ["cyberagent-workbench/internal/httpapi"])
        self.assertEqual(plan["test"], [])
        self.assertEqual(plan["integrations"], [{"package": "cyberagent-workbench/internal/httpapi",
                         "run": "^(TestOpenAPIGoldenDocumentMatchesGoDTOs)$"}])

    def test_shared_launch_fixture_reaches_mcp_but_ordinary_fixture_stays_local(self):
        for directory, consumers in (("launch-handoff", ["internal/agentpackages", "internal/mcp"]),
                                     ("ordinary", ["internal/agentpackages"])):
            with self.subTest(directory=directory):
                checks, plan = self.classify(f"internal/agentpackages/testdata/{directory}/mcp.json".encode())
                self.assertEqual(checks, {"backend"})
                self.assertFalse(plan["full"])
                self.assertEqual(plan["test"], ["cyberagent-workbench/" + p for p in consumers])
                self.assertEqual(plan["production_affected"], [])

    def test_skill_creator_shared_subtree_activates_store_shards_but_pins_remain_local(self):
        checks, plan = self.classify(
            b"internal/agentpackages/testdata/upstream/anthropic-skill-creator/skills/skill-creator/SKILL.md")
        self.assertEqual(checks, {"backend", "store", "journeys"})
        self.assertTrue(plan["store_affected"])
        self.assertNotIn("cyberagent-workbench/internal/store", plan["test"] + plan["compile"])
        self.assertEqual(plan["production_affected"], [])
        self.assertEqual(plan["integrations"], [{"package": "cyberagent-workbench/internal/application",
                         "run": "^(TestPortableSkillUpstreamImportReadResourceAndRestart)$"}])
        checks, plan = self.classify(b"internal/agentpackages/testdata/upstream/pins.json")
        self.assertEqual(checks, {"backend"})
        self.assertEqual(plan["test"], ["cyberagent-workbench/internal/agentpackages"])


@patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": ""})
class GitDiffTests(unittest.TestCase):
    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.repo = Path(self.temporary_directory.name)
        self.git("init", "-q")
        self.git("config", "user.name", "CI test")
        self.git("config", "user.email", "ci@example.invalid")

    def tearDown(self):
        self.temporary_directory.cleanup()

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), *args], check=True,
                              stdout=subprocess.PIPE, text=True).stdout.strip()

    def commit_file(self, name, content):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        self.git("add", "--", name)
        self.git("commit", "-qm", name)
        return self.git("rev-parse", "HEAD")

    @unittest.skipIf(os.name == "nt", "Windows filenames cannot contain newlines")
    def test_diff_is_nul_safe_for_newline_path(self):
        base = self.commit_file("README.md", "base\n")
        head = self.commit_file("docs/bad\nname.md", "content\n")
        self.assertEqual(changed_paths(self.repo, base, head), [b"docs/bad\nname.md"])

    def test_rename_and_delete_keep_original_input_paths(self):
        base = self.commit_file("internal/old/file.go", "package old\n")
        self.git("mv", "internal/old/file.go", "README.en.md")
        self.git("commit", "-qm", "rename")
        paths = changed_paths(self.repo, base, self.git("rev-parse", "HEAD"))
        self.assertEqual(paths, [b"README.en.md", b"internal/old/file.go"])
        self.assertTrue(classify_paths(paths)["backend"])

    def test_main_push_uses_its_diff_instead_of_forcing_full_ci(self):
        base = self.commit_file("README.md", "base\n")
        head = self.commit_file("web/src/styles.css", "body {}\n")
        output = StringIO()
        with redirect_stdout(output), redirect_stderr(StringIO()):
            self.assertEqual(main(["--event", "push", "--repo", str(self.repo),
                                   "--base", base, "--head", head]), 0)
        values = dict(line.split("=", 1) for line in output.getvalue().splitlines())
        self.assertEqual(values["web"], "true")
        self.assertEqual(values["backend"], "false")
        self.assertEqual(values["full"], "false")


class WorkflowGateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.root = Path(__file__).resolve().parents[2]
        cls.ci = (cls.root / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        cls.release = (cls.root / ".github/workflows/release-desktop.yml").read_text(encoding="utf-8")

    def test_all_builders_use_the_exact_module_toolchain(self):
        module = (self.root / "go.mod").read_text(encoding="utf-8")
        self.assertRegex(module, r"(?m)^go [0-9]+\.[0-9]+\.[0-9]+$")
        for workflow in (self.ci, self.release):
            builders = re.findall(r"uses: actions/setup-go@", workflow)
            self.assertTrue(builders)
            self.assertNotRegex(workflow, r"(?m)^\s*go-version:")
            self.assertEqual(len(builders), len(re.findall(
                r"(?m)^\s*go-version-file: go\.mod$", workflow)))

    def run_gate(self, job, needs):
        start = self.ci.index(f"  {job}:\n")
        block = self.ci[start:]
        match = re.search(r"python - <<'PY'\n(.*?)          PY", block, re.S)
        self.assertIsNotNone(match)
        code = "\n".join(line[10:] for line in match.group(1).splitlines())
        env = dict(os.environ, NEEDS_JSON=json.dumps(needs))
        return subprocess.run([sys.executable, "-c", code], env=env,
                              capture_output=True, text=True).returncode

    def test_partial_gate_requires_selected_jobs_and_allows_unselected_skips(self):
        needs = {job: {"result": "skipped"}
                 for job in ("store-tests", "go-race", "go-audit")}
        checks = dict.fromkeys(CHECKS, False)
        needs["classify"] = {"result": "success", "outputs": {"checks": json.dumps(checks)}}
        needs["go-checks"] = {"result": "success"}
        self.assertEqual(self.run_gate("go", needs), 0)
        checks["store"] = True
        needs["classify"]["outputs"]["checks"] = json.dumps(checks)
        self.assertNotEqual(self.run_gate("go", needs), 0)
        needs["store-tests"]["result"] = "success"
        self.assertEqual(self.run_gate("go", needs), 0)
        needs["classify"]["result"] = "failure"
        self.assertNotEqual(self.run_gate("go", needs), 0)

    def test_full_gate_rejects_failed_cancelled_or_skipped_platform(self):
        needs = {job: {"result": "success"} for job in (
            "classify", "go", "code-intel-lsp", "web", "rust",
            "desktop-macos", "desktop", "ui-evidence-windows",
        )}
        self.assertEqual(self.run_gate("full-ci", needs), 0)
        for result in ("skipped", "failure", "cancelled"):
            needs["desktop"]["result"] = result
            self.assertNotEqual(self.run_gate("full-ci", needs), 0)
        full_header = self.ci.split("  full-ci:\n", 1)[1].split("    runs-on:", 1)[0]
        self.assertIn("needs.classify.outputs.full == 'true'", full_header)

    def test_release_only_packages_on_relevant_inputs_and_requires_full_ci(self):
        trigger = self.release.split("permissions:", 1)[0]
        for ordinary in ("README.md", "go.mod", "go.sum", "web/package-lock.json",
                         "protocols/registry.json", "web/public/**"):
            self.assertNotIn(f"      - '{ordinary}'", trigger)
        self.assertIn("      - 'scripts/build-desktop.ps1'", trigger)
        self.assertIn('Full CI verification', self.release)
        self.assertIn("/attempts/", self.release)


if __name__ == "__main__":
    unittest.main()
