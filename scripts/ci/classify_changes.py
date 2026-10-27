#!/usr/bin/env python3
"""Choose PR/push checks from changed inputs and the Go import graph."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Sequence

_COMMIT_RE = re.compile(r"[0-9a-f]{40}")
CHECKS = ("full", "backend", "store", "web", "journeys", "lsp", "rust", "native",
          "ui", "race", "audit", "packaging")
_FULL_PATHS = {"go.mod", "go.sum", ".github/workflows/ci.yml"}
_DOCUMENTATION_IMAGE_PREFIXES = ("docs/acceptance/", "docs/evidence/", "docs/images/")
_DOCUMENTATION_IMAGE_SUFFIXES = (".png", ".jpg", ".jpeg", ".webp")
_PACKAGING_PREFIXES = (
    "packaging/", "assets/branding/", "scripts/build-desktop",
    "scripts/release-desktop", "scripts/package-", "scripts/verify-",
    "scripts/stage-direct-exe", "scripts/finalize-windows-release",
    "scripts/check-windows-compat", "scripts/check-macos-compat",
    "scripts/macos-release", "scripts/test_macos_release",
    "scripts/windows-visual-assets", "scripts/generate-brand-assets",
    "scripts/smoke-desktop", "scripts/standard-code-packaged",
    "scripts/test-standard-code-packaged", "scripts/standard-code-product",
)
_NATIVE_APPLICATION_PREFIXES = (
    "internal/application/command_", "internal/application/local_command",
    "internal/application/windows_", "internal/application/runtime_",
    "internal/application/execution_", "internal/application/host_",
    "internal/application/run_capability_readiness.go",
    "internal/app/command_", "internal/app/commands", "internal/app/execution_",
    "internal/app/api_", "internal/app/app.go",
    "internal/store/command_", "internal/store/execution_",
    "internal/toolgateway/command_runtime",
)
_RACE_PREFIXES = (
    "internal/application/command_", "internal/application/approval",
    "internal/application/execution_", "internal/application/permission",
    "internal/application/workspace_", "internal/application/run_",
    "internal/httpapi/execution_", "internal/httpapi/approval",
)


class ClassificationError(RuntimeError):
    """The change set could not be read."""


def classify_paths(paths: Sequence[bytes], affected: Sequence[str] = ()) -> dict[str, bool]:
    if not paths:
        raise ClassificationError("the change set is empty")
    checks = dict.fromkeys(CHECKS, False)
    for raw_path in paths:
        path = raw_path.decode("utf-8", "surrogateescape")
        if path in _FULL_PATHS or path.startswith("scripts/ci/"):
            return dict.fromkeys(CHECKS, True)
        if path.startswith(("internal/", "cmd/")):
            checks["backend"] = True
            if path.startswith(("internal/desktop/", "cmd/cyberagent-desktop/", "cmd/cyberagent/")):
                checks["native"] = True
            if path.startswith(("internal/httpapi/", "internal/webui/")):
                checks["web"] = True
            if path.startswith(_NATIVE_APPLICATION_PREFIXES):
                checks["native"] = True
            if path.startswith(_RACE_PREFIXES):
                checks["race"] = True
            if path.startswith(("internal/releasegate/", "internal/packagede2e/",
                                "cmd/releasegate/", "cmd/releasegen/", "cmd/packagede2e/")):
                checks["packaging"] = checks["native"] = True
        elif path.startswith("web/"):
            checks["web"] = True
            # The desktop-tagged embedding adapter is absent from default go list.
            if path.endswith(".go"):
                checks["native"] = True
        elif path == "docs/openapi.json":
            checks["backend"] = checks["web"] = True
        elif path.startswith("analyzers/") or path == "scripts/build-embedded-wasi.sh":
            checks["rust"] = checks["native"] = True
        elif path.startswith(_PACKAGING_PREFIXES) or path == ".github/workflows/release-desktop.yml":
            checks["packaging"] = checks["native"] = True
        elif path.startswith(("scripts/prepare-windows-fixed-go",
                              "scripts/tests/prepare-windows-fixed-go")):
            checks["native"] = True
        elif (path.endswith(".md") or path.startswith("docs/convergence/")
              or (path.startswith(_DOCUMENTATION_IMAGE_PREFIXES)
                  and path.lower().endswith(_DOCUMENTATION_IMAGE_SUFFIXES))
              or path in {".gitignore", ".gitattributes", "LICENSE", "LICENSE.md"}
              or path.startswith((".github/ISSUE_TEMPLATE/", ".github/PULL_REQUEST_TEMPLATE"))):
            # Acceptance screenshots are documentation, not runtime assets.
            # Generated registry/inventory and documentation contracts are
            # checked in the always-on Go checks job.
            pass
        else:
            # New build inputs and unclassified tooling receive full coverage.
            return dict.fromkeys(CHECKS, True)

    def touches(*packages: str) -> bool:
        return any(name.endswith("/internal/" + package)
                   for name in affected for package in packages)

    checks["store"] = touches("store")
    checks["rust"] |= touches("analyzer")
    # These are the runtime inputs exercised by the real LSP tests. A generic
    # toolgateway import in workspace does not make every registry edit an LSP change.
    checks["lsp"] = any(path.startswith((
        b"internal/codeintel/", b"internal/workspace/", b"internal/repository/",
        b"internal/apperror/", b"internal/redact/",
    )) for path in paths)
    checks["native"] |= touches("runner", "sandbox", "credential", "commandruntimeadapter")
    checks["native"] |= checks["rust"]
    checks["ui"] = touches("browserruntime", "uievidence", "sandbox")
    # Product journeys exercise the built renderer against Go and SQLite.
    # Keep them separate from the Windows browser-runtime evidence matrix.
    checks["journeys"] = checks["web"] or touches("app", "application", "httpapi", "webui") or any(
        path.startswith(b"cmd/workbenchjourney/") for path in paths)
    checks["race"] |= touches("domain", "executionauth", "policy", "runmutation", "workspace")
    return checks


def _git(repo: Path, *args: str) -> bytes:
    try:
        return subprocess.run(
            ["git", "-C", str(repo), *args], check=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        ).stdout
    except (OSError, subprocess.CalledProcessError) as exc:
        detail = (exc.stderr.decode("utf-8", "replace").strip()
                  if isinstance(exc, subprocess.CalledProcessError) else str(exc))
        raise ClassificationError(detail or f"git {' '.join(args)} failed") from exc


def changed_paths(repo: Path, base: str, head: str) -> list[bytes]:
    """Include both sides of renames; preserve spaces and newlines in filenames."""
    for label, revision in (("base", base), ("head", head)):
        if _COMMIT_RE.fullmatch(revision) is None:
            raise ClassificationError(f"{label} revision is invalid")
        _git(repo, "cat-file", "-e", f"{revision}^{{commit}}")
    output = _git(repo, "diff", "--no-renames", "--name-only", "-z", base, head, "--")
    if not output or not output.endswith(b"\0"):
        raise ClassificationError("the change set is empty or malformed")
    paths = output[:-1].split(b"\0")
    if any(not path or path.startswith(b"/") for path in paths):
        raise ClassificationError("git returned an invalid repository path")
    return paths


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--event", required=True)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--base", default="")
    parser.add_argument("--head", default="")
    args = parser.parse_args(argv)
    try:
        from run_go_checks import collect_packages, select_plan

        full = args.event in {"schedule", "workflow_dispatch"}
        if args.event not in {"pull_request", "push", "schedule", "workflow_dispatch"}:
            raise ClassificationError(f"unsupported event: {args.event}")
        paths = [] if full else changed_paths(args.repo, args.base, args.head)
        checks = dict.fromkeys(CHECKS, True) if full else classify_paths(paths)
        go_plan = {}
        if checks["backend"]:
            packages = collect_packages(args.repo)
            go_paths = [path for path in paths if path.startswith((b"internal/", b"cmd/"))
                        or path == b"docs/openapi.json"]
            go_plan = select_plan(packages, go_paths, full=checks["full"])
            if go_plan["full"]:
                checks = dict.fromkeys(CHECKS, True)
            else:
                checks = classify_paths(paths, go_plan["affected"])
                checks["store"] = go_plan["store_affected"]
        for key, value in checks.items():
            print(f"{key}={str(value).lower()}")
        print("checks=" + json.dumps(checks, separators=(",", ":")))
        print("go_plan=" + json.dumps(go_plan, separators=(",", ":")))
        print("Selected checks: " + ", ".join(key for key, value in checks.items() if value),
              file=sys.stderr)
        if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
            with Path(summary).open("a", encoding="utf-8") as output:
                output.write("### CI selection\n\n")
                output.write("Full run\n\n" if checks["full"] else "Affected checks\n\n")
                output.write("| Check | Selected |\n| --- | --- |\n")
                for key, value in checks.items():
                    output.write(f"| {key} | {'yes' if value else 'no'} |\n")
                if go_plan:
                    output.write(f"\nGo: {len(go_plan['test'])} packages with full tests, "
                                 f"{len(go_plan['compile'])} with compilation checks, "
                                 f"{len(go_plan['integrations'])} integration selections.\n")
    except (ClassificationError, RuntimeError, OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"change classification failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
