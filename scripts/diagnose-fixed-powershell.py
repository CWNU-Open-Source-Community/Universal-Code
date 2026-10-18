"""Temporary Windows fixed-command diagnosis; never a production gate result."""

import json
import os
from pathlib import Path
import subprocess
import tempfile


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise RuntimeError(f"diagnostic source no longer matches: {old!r}")
    return text.replace(old, new, 1)


def marker(name):
    return "[Console]::Error.WriteLine(('fixed_phase=" + name + " ticks={0}' -f [DateTime]::UtcNow.Ticks)); "


def overlay(repo, directory, mode):
    source_path = repo / "internal/runner/controlled_command.go"
    test_path = repo / "internal/runner/controlled_execution_windows_test.go"
    source = source_path.read_text(encoding="utf-8")
    test = test_path.read_text(encoding="utf-8")
    test = replace_once(test, "native := process.(*windowsCommandRuntimeProcess)", "native := process.(*windowsCommandRuntimeProcess)\n\t\t\tstopDiagnostic := startFixedPowerShellDiagnostic(t, native)\n\t\t\tdefer stopDiagnostic()")
    if mode != "original":
        source = replace_once(source, "param([string]$RelativePathHex) if", "param([string]$RelativePathHex) " + marker("entry") + "if")
        source = replace_once(source, "$RelativePath = [Text.Encoding]::UTF8.GetString($Bytes); Get-ChildItem", "$RelativePath = [Text.Encoding]::UTF8.GetString($Bytes); " + marker("decoded") + "Get-ChildItem")
        if mode == "pipeline":
            source = replace_once(source, "| ConvertTo-Json -Compress }`", "| ConvertTo-Json -Compress; " + marker("completed") + "}`")
        else:
            source = replace_once(source, "Get-ChildItem -LiteralPath $RelativePath -Force | Select-Object Name,Length,Attributes | ConvertTo-Json -Compress }`", "$Items = @(Get-ChildItem -LiteralPath $RelativePath -Force); " + marker("enumerated") + "$Rows = @($Items | Select-Object Name,Length,Attributes); " + marker("projected") + "$Rows | ConvertTo-Json -Compress; " + marker("completed") + "}`")
        test = replace_once(test, "result.stderr.err != nil || len(result.stderr.value) != 0 {", "result.stderr.err != nil {")
        test = replace_once(test, "result := waitWindowsProfileTestProcess(process, time.Duration(plan.TimeoutMilliseconds)*time.Millisecond)", "result := waitWindowsProfileTestProcess(process, time.Duration(plan.TimeoutMilliseconds)*time.Millisecond)\n\t\t\tt.Logf(\"diagnostic-only phases: %s\", result.stderr.value)")
    replacements = {}
    for path, contents in [(source_path, source), (test_path, test)]:
        target = directory / path.name
        target.write_text(contents, encoding="utf-8", newline="\n")
        replacements[str(path)] = str(target)
    path = directory / "overlay.json"
    path.write_text(json.dumps({"Replace": replacements}), encoding="utf-8")
    return path


def main():
    if os.name != "nt":
        raise RuntimeError("Windows-only diagnostic")
    repo = Path(__file__).resolve().parents[1]
    results = []
    for mode in ["original", "pipeline", "sequential"]:
        with tempfile.TemporaryDirectory(prefix="fixed-powershell-") as temporary:
            path = overlay(repo, Path(temporary), mode)
            print(json.dumps({"diagnostic_only": True, "mode": mode, "budget_ms": 60000}), flush=True)
            result = subprocess.run(["go", "test", "-overlay", str(path), "-v", "-count=1", "-timeout=3m", "./internal/runner", "-run", "^TestWindowsFixedCommandRuntimeUsesRestrictedNativeProcess$/^powershell-workspace-list$"], cwd=repo, check=False)
            results.append({"mode": mode, "exit_code": result.returncode})
    print(json.dumps({"diagnostic_only": True, "results": results}), flush=True)


if __name__ == "__main__":
    main()
