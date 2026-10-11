[CmdletBinding()]
param(
    [string]$OutputDirectory = "build/desktop",
    [string]$ExpectedVersion = "",
    [string]$ExpectedRevision = "",
    [ValidateRange(5, 60)][int]$StartupTimeoutSeconds = 20,
    [string]$OutputPath = "",
    [string]$ProductReportPath = "",
    [string]$SecurityReportPath = "",
    [string]$AggregateOutputPath = "",
    [switch]$RequireReleaseGate
)

<#
.SYNOPSIS
Prepares the fixed Standard Code fixtures and exercises the exact portable ZIP.

.DESCRIPTION
Materializes the hash-bound Go, Node.js, Python, and Rust repositories, proves
their fail/repair/pass oracle with locally installed toolchains, extracts the
verified portable ZIP, and exercises default and safe operator-preview startup
against an isolated CYBERAGENT_HOME.

This is a packaged bootstrap for issue #140. It deliberately reports
needs_full_matrix until both independent product and security reports are
supplied. When both reports are present, the Go-owned aggregate gate strictly
revalidates their self hashes and binds them to this exact candidate. It never
labels an unexecuted case as pass or skip.
#>

$ErrorActionPreference = "Stop"
if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    throw "Standard Code packaged E2E requires Windows"
}
if (-not ("StandardCodePackagedE2ENative" -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;

public static class StandardCodePackagedE2ENative
{
    private delegate bool EnumWindowsProc(IntPtr window, IntPtr parameter);

    [DllImport("user32.dll")]
    private static extern bool EnumWindows(EnumWindowsProc callback, IntPtr parameter);

    [DllImport("user32.dll")]
    private static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern int GetWindowTextLength(IntPtr window);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(IntPtr window, StringBuilder name, int count);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern IntPtr SendMessageTimeout(IntPtr window, uint message,
        IntPtr wordParameter, IntPtr longParameter, uint flags, uint timeout,
        out UIntPtr result);

    public static IntPtr[] FindApplicationWindows(int expectedProcessId)
    {
        List<IntPtr> result = new List<IntPtr>();
        EnumWindows(delegate(IntPtr window, IntPtr parameter) {
            uint processId;
            GetWindowThreadProcessId(window, out processId);
            if (processId != (uint)expectedProcessId) { return true; }
            StringBuilder name = new StringBuilder(256);
            // desktopWindowsOptions pins this class. An error dialog or
            // another process's window is not a successfully started shell.
            if (GetClassName(window, name, name.Capacity) > 0 && name.ToString() == "CyberAgentWorkbench") {
                result.Add(window);
            }
            return true;
        }, IntPtr.Zero);
        return result.ToArray();
    }

    public static bool IsApplicationWindowResponsive(IntPtr window, int expectedProcessId)
    {
        uint processId;
        GetWindowThreadProcessId(window, out processId);
        if (processId != (uint)expectedProcessId) { return false; }
        UIntPtr result;
        // WM_NULL observes the native message loop without performing an action.
        // SMTO_BLOCK | SMTO_ABORTIFHUNG keeps a stalled shell probe bounded.
        return SendMessageTimeout(window, 0, IntPtr.Zero, IntPtr.Zero, 3, 250, out result) != IntPtr.Zero;
    }

    [DllImport("user32.dll", SetLastError = true)]
    public static extern bool PostMessage(IntPtr window, uint message,
        IntPtr wordParameter, IntPtr longParameter);

    public static IntPtr[] FindTopLevelWindows(int expectedProcessId)
    {
        List<IntPtr> result = new List<IntPtr>();
        EnumWindows(delegate(IntPtr window, IntPtr parameter) {
            uint processId;
            GetWindowThreadProcessId(window, out processId);
            if (processId == (uint)expectedProcessId && GetWindowTextLength(window) > 0) {
                result.Add(window);
            }
            return true;
        }, IntPtr.Zero);
        return result.ToArray();
    }
}
'@ -ErrorAction Stop | Out-Null
}

$repositoryRoot = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$repositoryPrefix = $repositoryRoot.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
$outputRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
if (-not $outputRoot.StartsWith($repositoryPrefix,
        [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Packaged E2E release input must remain inside the repository"
}
$outputPrefix = $outputRoot.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
$productReportProvided = -not [string]::IsNullOrWhiteSpace($ProductReportPath)
$securityReportProvided = -not [string]::IsNullOrWhiteSpace($SecurityReportPath)
if ($productReportProvided -ne $securityReportProvided) {
    throw "Product and security reports must be supplied together"
}
if ($RequireReleaseGate -and -not $productReportProvided) {
    throw "The complete release gate requires product and security reports"
}
if (-not $productReportProvided -and -not [string]::IsNullOrWhiteSpace($AggregateOutputPath)) {
    throw "AggregateOutputPath requires product and security reports"
}
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Join-Path $outputRoot "standard-code-packaged-e2e.json"
}
$output = if ([System.IO.Path]::IsPathRooted($OutputPath)) {
    [System.IO.Path]::GetFullPath($OutputPath)
} else {
    [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputPath))
}
if (-not $output.StartsWith($repositoryPrefix,
        [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Packaged E2E evidence must remain inside the repository"
}
if (Test-Path -LiteralPath $output) {
    throw "Packaged E2E evidence output must not already exist"
}

function Resolve-GateEvidenceInput {
    param([Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label)
    $resolved = if ([System.IO.Path]::IsPathRooted($Path)) {
        [System.IO.Path]::GetFullPath($Path)
    } else {
        [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $Path))
    }
    if (-not $resolved.StartsWith($outputPrefix,
            [System.StringComparison]::OrdinalIgnoreCase) -or
        -not (Test-Path -LiteralPath $resolved -PathType Leaf)) {
        throw "$Label must be an existing file below the release output directory"
    }
    $item = Get-Item -LiteralPath $resolved -Force
    if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        throw "$Label must not be a reparse point"
    }
    return $resolved
}

$productEvidence = $null
$securityEvidence = $null
$aggregateOutput = $null
if ($productReportProvided) {
    $productEvidence = Resolve-GateEvidenceInput -Path $ProductReportPath `
        -Label "Product evidence"
    $securityEvidence = Resolve-GateEvidenceInput -Path $SecurityReportPath `
        -Label "Security evidence"
    if ([string]::IsNullOrWhiteSpace($AggregateOutputPath)) {
        $AggregateOutputPath = Join-Path $outputRoot "standard-code-release-gate.json"
    }
    $aggregateOutput = if ([System.IO.Path]::IsPathRooted($AggregateOutputPath)) {
        [System.IO.Path]::GetFullPath($AggregateOutputPath)
    } else {
        [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $AggregateOutputPath))
    }
    if (-not $aggregateOutput.StartsWith($outputPrefix,
            [System.StringComparison]::OrdinalIgnoreCase) -or
        (Test-Path -LiteralPath $aggregateOutput)) {
        throw "Aggregate release gate output must be a new file below the release output directory"
    }
}

function Assert-E2ECondition {
    param([bool]$Condition, [string]$Code)
    if (-not $Condition) { throw "Packaged E2E condition failed: $Code" }
}

function Get-SHA256 {
    param([string]$Path)
    return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

$results = [System.Collections.Generic.List[object]]::new()
function Add-E2EResult {
    param([string]$ID, [string]$Status, [object]$Facts)
    Assert-E2ECondition ($Status -in @("pass", "fail")) "invalid_result_status"
    [void]$results.Add([pscustomobject][ordered]@{
        id = $ID
        status = $Status
        facts = $Facts
    })
}

function Get-SafeFailureCode {
    param([string]$Message)
    $condition = [regex]::Match($Message,
        '^Packaged E2E condition failed: (?<code>[a-z0-9_]+)$')
    if ($condition.Success) { return $condition.Groups['code'].Value }
    $known = @{
        "Packaged candidate exited during startup" = "candidate_startup_exit"
        "Packaged candidate exited after store startup" = "candidate_post_store_exit"
        "Packaged candidate store startup timed out" = "candidate_startup_timeout"
        "Packaged candidate application window timed out" = "candidate_window_timeout"
        "Packaged candidate application window remained unresponsive" = "candidate_window_unresponsive"
        "TCP listener inspection is unavailable" = "listener_inspection_unavailable"
        "Fixture Git inspection failed" = "fixture_git_inspection_failed"
        "Sentinel evidence file remained unreadable" = "sentinel_evidence_unreadable"
    }
    if ($known.ContainsKey($Message)) { return [string]$known[$Message] }
    return "unexpected_harness_error"
}

function Invoke-GitValue {
    param([string]$Root, [string[]]$Arguments)
    $value = & git -C $Root -c core.autocrlf=false -c core.filemode=false @Arguments 2>$null
    if ($LASTEXITCODE -ne 0) { throw "Fixture Git inspection failed" }
    return (($value -join "`n").Trim())
}

function Get-FixtureState {
    param([string]$Root, [object]$Expected)
    $head = Invoke-GitValue -Root $Root -Arguments @("rev-parse", "HEAD")
    $tree = Invoke-GitValue -Root $Root -Arguments @("show", "-s", "--format=%T", "HEAD")
    $status = Invoke-GitValue -Root $Root -Arguments @(
        "status", "--porcelain=v1", "--untracked-files=all"
    )
    return [pscustomobject][ordered]@{
        id = [string]$Expected.id
        head_matches = $head -ceq [string]$Expected.head
        tree_matches = $tree -ceq [string]$Expected.tree
        clean = [string]::IsNullOrEmpty($status)
    }
}

function Start-PackagedCandidate {
    param([string[]]$Arguments)
    $parameters = @{
        FilePath = $script:binary
        WorkingDirectory = $script:extractRoot
        WindowStyle = "Hidden"
        PassThru = $true
    }
    if ($Arguments.Count -gt 0) { $parameters.ArgumentList = $Arguments }
    $process = Start-Process @parameters
    [void]$script:startedCandidates.Add([pscustomobject]@{
        id = $process.Id
        started_at = $process.StartTime.ToUniversalTime()
    })
    $script:activeCandidate = $process
    return $process
}

function Wait-CandidateReady {
    param([System.Diagnostics.Process]$Process)
    $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $storeObserved = $false
    $windowObserved = $false
    do {
        $Process.Refresh()
        if ($Process.HasExited) { throw "Packaged candidate exited during startup" }
        $storeObserved = (Test-Path -LiteralPath $script:database -PathType Leaf) -and
            [int64](Get-Item -LiteralPath $script:database).Length -gt 0
        $windows = @([StandardCodePackagedE2ENative]::FindApplicationWindows($Process.Id))
        $windowObserved = $windowObserved -or $windows.Count -gt 0
        $responsive = @($windows | Where-Object {
            [StandardCodePackagedE2ENative]::IsApplicationWindowResponsive($_, $Process.Id)
        }).Count -gt 0
        if ($storeObserved -and $responsive) {
            Start-Sleep -Milliseconds 750
            $Process.Refresh()
            if ($Process.HasExited) { throw "Packaged candidate exited after store startup" }
            $stillResponsive = @(
                [StandardCodePackagedE2ENative]::FindApplicationWindows($Process.Id) | Where-Object {
                    [StandardCodePackagedE2ENative]::IsApplicationWindowResponsive($_, $Process.Id)
                }
            ).Count -gt 0
            if ($stillResponsive -and (Test-Path -LiteralPath $script:database -PathType Leaf) -and
                [int64](Get-Item -LiteralPath $script:database).Length -gt 0) {
                return [pscustomobject][ordered]@{
                    process_alive = $true
                    store_present = $true
                    store_nonempty = $true
                    native_window_ready = $true
                    native_window_responsive = $true
                }
            }
        }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $storeObserved) { throw "Packaged candidate store startup timed out" }
    if (-not $windowObserved) { throw "Packaged candidate application window timed out" }
    throw "Packaged candidate application window remained unresponsive"
}

function Get-SafeCandidateExitCode {
    if ($null -eq $script:activeCandidate) { return $null }
    try {
        $script:activeCandidate.Refresh()
        if ($script:activeCandidate.HasExited) { return [int]$script:activeCandidate.ExitCode }
    } catch { }
    return $null
}

function Close-PackagedCandidate {
    param([System.Diagnostics.Process]$Process)
    $windowDeadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
    $windowReady = $false
    $windowHandles = @()
    while ([DateTime]::UtcNow -lt $windowDeadline) {
        $Process.Refresh()
        if ($Process.HasExited) { break }
        $windowHandles = @(
            [StandardCodePackagedE2ENative]::FindTopLevelWindows($Process.Id)
        )
        if ($windowHandles.Count -gt 0) {
            $windowReady = $true
            break
        }
        Start-Sleep -Milliseconds 100
    }
    $requested = $false
    foreach ($windowHandle in $windowHandles) {
        if ([StandardCodePackagedE2ENative]::PostMessage(
                $windowHandle, 0x0112, [IntPtr]::new(0xF060), [IntPtr]::Zero)) {
            $requested = $true
        }
        if ([StandardCodePackagedE2ENative]::PostMessage(
                $windowHandle, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero)) {
            $requested = $true
        }
    }
    $exited = $requested -and $Process.WaitForExit(10000)
    $exitCode = $null
    if ($exited) {
        $Process.Refresh()
        $exitCode = $Process.ExitCode
    }
    $forceCleanupUsed = $false
    if (-not $exited) {
        $Process.Refresh()
        if (-not $Process.HasExited) {
            $Process.Kill($true)
            [void]$Process.WaitForExit(5000)
            $forceCleanupUsed = $true
        }
    }
    $Process.Refresh()
    $stopped = $Process.HasExited
    return [pscustomobject][ordered]@{
        window_ready = $windowReady
        close_requested = $requested
        graceful_exit = $exited
        force_cleanup_used = $forceCleanupUsed
        stopped = $stopped
        exit_code = $exitCode
    }
}

function Get-ListenerCount {
    param([int]$ProcessID)
    if ($null -eq (Get-Command Get-NetTCPConnection -ErrorAction SilentlyContinue)) {
        throw "TCP listener inspection is unavailable"
    }
    return @(
        Get-NetTCPConnection -State Listen -OwningProcess $ProcessID -ErrorAction SilentlyContinue
    ).Count
}

function Test-ContainsBytePattern {
    param([byte[]]$Bytes, [byte[]]$Pattern)
    if ($Pattern.Length -eq 0 -or $Bytes.Length -lt $Pattern.Length) { return $false }
    for ($offset = 0; $offset -le $Bytes.Length - $Pattern.Length; $offset++) {
        $matches = $true
        for ($index = 0; $index -lt $Pattern.Length; $index++) {
            if ($Bytes[$offset + $index] -ne $Pattern[$index]) {
                $matches = $false
                break
            }
        }
        if ($matches) { return $true }
    }
    return $false
}

function Test-SentinelPersisted {
    param([string[]]$Roots, [string[]]$Sentinels)
    $patterns = [System.Collections.Generic.List[byte[]]]::new()
    foreach ($sentinel in $Sentinels) {
        $patterns.Add([System.Text.Encoding]::UTF8.GetBytes($sentinel))
        $patterns.Add([System.Text.Encoding]::Unicode.GetBytes($sentinel))
    }
    foreach ($root in $Roots) {
        if (-not (Test-Path -LiteralPath $root -PathType Container)) { continue }
        :rescan for ($scan = 1; $scan -le 5; $scan++) {
            foreach ($file in Get-ChildItem -LiteralPath $root -File -Recurse -Force) {
                $webview2Data = $file.FullName.StartsWith(
                    (Join-Path $Roots[0] "webview2") + [System.IO.Path]::DirectorySeparatorChar,
                    [System.StringComparison]::OrdinalIgnoreCase)
                $bytes = $null
                for ($attempt = 1; $attempt -le 5; $attempt++) {
                    try {
                        $bytes = [System.IO.File]::ReadAllBytes($file.FullName)
                        break
                    } catch {
                        $readError = $_.Exception
                        while ($null -ne $readError.InnerException) {
                            $readError = $readError.InnerException
                        }
                        if ($webview2Data -and $scan -lt 5 -and
                            ($readError -is [System.IO.FileNotFoundException] -or
                             $readError -is [System.IO.DirectoryNotFoundException])) {
                            # WebView2 can remove/rotate cache files after its host exits.
                            # Re-enumerate the root so replacement files are also scanned.
                            continue rescan
                        }
                        if ($attempt -eq 5) {
                            $failure = [System.IO.IOException]::new("Sentinel evidence file remained unreadable")
                            # Fixed categories and numeric error facts only: no local
                            # path, file bytes, exception message or sentinel value.
                            $failure.Data["sentinel_evidence_read"] = [pscustomobject][ordered]@{
                                root_index = [array]::IndexOf($Roots, $root)
                                webview2_data = $webview2Data
                                exception_type = $readError.GetType().FullName
                                hresult = [int]$readError.HResult
                                file_attributes = [int]$file.Attributes
                            }
                            throw $failure
                        }
                        Start-Sleep -Milliseconds 200
                    }
                }
                foreach ($pattern in $patterns) {
                    if (Test-ContainsBytePattern -Bytes $bytes -Pattern $pattern) { return $true }
                }
            }
            break
        }
    }
    return $false
}

function Test-OwnedCandidateRunning {
    param([object]$Candidate)
    $remaining = Get-Process -Id $Candidate.id -ErrorAction SilentlyContinue
    if ($null -eq $remaining) { return $false }
    try {
        return $remaining.StartTime.ToUniversalTime() -eq $Candidate.started_at -and
            $remaining.Path -and $remaining.Path.Equals(
                $script:binary, [System.StringComparison]::OrdinalIgnoreCase)
    } catch {
        return $false
    } finally {
        $remaining.Dispose()
    }
}

function Stop-OwnedCandidates {
    foreach ($candidate in $script:startedCandidates) {
        $remaining = Get-Process -Id $candidate.id -ErrorAction SilentlyContinue
        if ($null -eq $remaining) { continue }
        try {
            $sameStart = $remaining.StartTime.ToUniversalTime() -eq $candidate.started_at
            $sameBinary = $remaining.Path -and $remaining.Path.Equals(
                $script:binary, [System.StringComparison]::OrdinalIgnoreCase)
            if ($sameStart -and $sameBinary) {
                $remaining.Kill($true)
                [void]$remaining.WaitForExit(5000)
            }
        } catch {
            # A process that exited while being inspected needs no wider cleanup.
        } finally {
            $remaining.Dispose()
        }
    }
}

$temporaryRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot ".tmp"))
[System.IO.Directory]::CreateDirectory($temporaryRoot) | Out-Null
$temporaryInfo = Get-Item -LiteralPath $temporaryRoot -Force
Assert-E2ECondition ($temporaryInfo.PSIsContainer -and
    -not ($temporaryInfo.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) `
    "unsafe_temporary_root"
$runID = [guid]::NewGuid().ToString("N")
$harnessRoot = Join-Path $temporaryRoot ("standard-code-packaged-e2e-" + $runID)
[System.IO.Directory]::CreateDirectory($harnessRoot) | Out-Null
$script:extractRoot = Join-Path $harnessRoot "package"
$script:isolatedHome = Join-Path $harnessRoot "home"
$repositoriesRoot = Join-Path $harnessRoot "repositories"
$fixtureReportPath = Join-Path $harnessRoot "fixture-set.json"
[System.IO.Directory]::CreateDirectory($script:extractRoot) | Out-Null
[System.IO.Directory]::CreateDirectory($script:isolatedHome) | Out-Null
$script:database = Join-Path $script:isolatedHome "cyberagent.db"
$script:binary = $null
$script:startedCandidates = [System.Collections.Generic.List[object]]::new()
$script:activeCandidate = $null

$manifest = $null
$metadata = $null
$fixtureReport = $null
$zipHash = ""
$binaryHash = ""
$bootstrapError = $null
$bootstrapPhase = "candidate_provenance"
$environmentNames = @(
    "CYBERAGENT_HOME", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
    "AZURE_CLIENT_SECRET", "GOOGLE_APPLICATION_CREDENTIALS", "SSH_AUTH_SOCK",
    "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"
)
$previousEnvironment = @{}
foreach ($name in $environmentNames) {
    $previousEnvironment[$name] = [System.Environment]::GetEnvironmentVariable($name, "Process")
}
$sentinelStem = "traverse-issue140-" + [guid]::NewGuid().ToString("N")
$sentinels = @(
    $sentinelStem + "-aws", $sentinelStem + "-azure", $sentinelStem + "-gcp",
    $sentinelStem + "-ssh", $sentinelStem + "-proxy"
)

try {
    $manifestPath = Join-Path $outputRoot "portable-zip-manifest.json"
    $metadataPath = Join-Path $outputRoot "release-metadata.json"
    Assert-E2ECondition (Test-Path -LiteralPath $manifestPath -PathType Leaf) `
        "portable_manifest_missing"
    Assert-E2ECondition (Test-Path -LiteralPath $metadataPath -PathType Leaf) `
        "release_metadata_missing"
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
    Assert-E2ECondition ($manifest.protocol_version -ceq "portable_zip_manifest.v1") `
        "portable_manifest_protocol"
    Assert-E2ECondition ($metadata.protocol_version -ceq "portable_release_metadata.v1") `
        "release_metadata_protocol"
    Assert-E2ECondition ($manifest.version -ceq $metadata.app_version -and
        $manifest.revision -ceq $metadata.revision -and -not [bool]$metadata.modified -and
        [bool]$metadata.reproducibility_checked -and [bool]$metadata.reproducible) `
        "candidate_provenance"
    if (-not [string]::IsNullOrWhiteSpace($ExpectedVersion)) {
        Assert-E2ECondition ($manifest.version -ceq $ExpectedVersion) "candidate_version"
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedRevision)) {
        Assert-E2ECondition ($manifest.revision -ceq $ExpectedRevision) "candidate_revision"
    }
    Add-E2EResult "candidate_provenance" "pass" ([pscustomobject][ordered]@{
        manifest_bound = $true
        clean_revision = $true
        reproducible = $true
    })

    $bootstrapPhase = "fixture_oracle"
    & go run ./cmd/packagede2e --output $repositoriesRoot `
        --report $fixtureReportPath --verify-toolchains | Out-Null
    Assert-E2ECondition ($LASTEXITCODE -eq 0) "fixture_oracle_command"
    $fixtureReport = Get-Content -LiteralPath $fixtureReportPath -Raw | ConvertFrom-Json
    $repositoryReports = @($fixtureReport.repositories)
    Assert-E2ECondition ($fixtureReport.protocol_version -ceq "standard_code_fixture_set.v1" -and
        [bool]$fixtureReport.oracle_verified -and [bool]$fixtureReport.all_attack_cases_bound -and
        [int]$fixtureReport.repository_count -eq 4 -and
        [int]$fixtureReport.attack_case_count -eq 39 -and
        @($repositoryReports | Where-Object {
            -not [bool]$_.clean -or -not [bool]$_.baseline_failure_observed -or
            -not [bool]$_.repair_pass_verified
        }).Count -eq 0) "fixture_oracle_report"
    Add-E2EResult "fixed_repository_oracle" "pass" ([pscustomobject][ordered]@{
        repository_count = 4
        attack_case_count = 39
        baseline_failures_observed = 4
        repair_passes_verified = 4
    })

    $bootstrapPhase = "package_extraction"
    $zipName = [string]$manifest.zip_name
    Assert-E2ECondition ([System.IO.Path]::GetFileName($zipName) -ceq $zipName) `
        "portable_zip_name"
    $zipPath = Join-Path $outputRoot $zipName
    Assert-E2ECondition (Test-Path -LiteralPath $zipPath -PathType Leaf) "portable_zip_missing"
    $zipHash = Get-SHA256 $zipPath
    Assert-E2ECondition ($zipHash -ceq [string]$manifest.zip_sha256) "portable_zip_hash"
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [System.IO.Compression.ZipFile]::ExtractToDirectory($zipPath, $script:extractRoot)
    $script:binary = Join-Path $script:extractRoot "TraverseBoard.exe"
    Assert-E2ECondition (Test-Path -LiteralPath $script:binary -PathType Leaf) `
        "packaged_binary_missing"
    $binaryHash = Get-SHA256 $script:binary
    Assert-E2ECondition ($binaryHash -ceq [string]$manifest.binary_sha256) `
        "packaged_binary_hash"
    Add-E2EResult "exact_package_extraction" "pass" ([pscustomobject][ordered]@{
        zip_hash_matches = $true
        binary_hash_matches = $true
    })

    $bootstrapPhase = "isolated_environment"
    [System.Environment]::SetEnvironmentVariable("CYBERAGENT_HOME", $script:isolatedHome, "Process")
    [System.Environment]::SetEnvironmentVariable("AWS_ACCESS_KEY_ID", $sentinels[0], "Process")
    [System.Environment]::SetEnvironmentVariable("AWS_SECRET_ACCESS_KEY", $sentinels[0], "Process")
    [System.Environment]::SetEnvironmentVariable("AZURE_CLIENT_SECRET", $sentinels[1], "Process")
    [System.Environment]::SetEnvironmentVariable("GOOGLE_APPLICATION_CREDENTIALS", $sentinels[2], "Process")
    [System.Environment]::SetEnvironmentVariable("SSH_AUTH_SOCK", $sentinels[3], "Process")
    foreach ($proxyName in @("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY")) {
        [System.Environment]::SetEnvironmentVariable($proxyName, $sentinels[4], "Process")
    }

    $bootstrapPhase = "default_launch"
    $default = Start-PackagedCandidate -Arguments @()
    $bootstrapPhase = "default_readiness"
    $defaultReady = Wait-CandidateReady -Process $default
    $bootstrapPhase = "default_isolation"
    $listenerCount = Get-ListenerCount -ProcessID $default.Id
    Assert-E2ECondition ($defaultReady.store_nonempty -and $listenerCount -eq 0) `
        "default_start_isolation"
    $bootstrapPhase = "default_close"
    $defaultClose = Close-PackagedCandidate -Process $default
    $defaultPassed = $defaultClose.stopped
    $default.Dispose()
    Add-E2EResult "packaged_default_start" `
        $(if ($defaultPassed) { "pass" } else { "fail" }) ([pscustomobject][ordered]@{
        store_created = $true
        process_alive = $true
        native_window_ready = $defaultReady.native_window_ready
        native_window_responsive = $defaultReady.native_window_responsive
        host_tcp_listener_count = $listenerCount
        window_discovered = $defaultClose.window_ready
        close_requested = $defaultClose.close_requested
        graceful_exit_observed = $defaultClose.graceful_exit
        owned_force_cleanup_used = $defaultClose.force_cleanup_used
        process_stopped = $defaultClose.stopped
        exit_code = $defaultClose.exit_code
    })
    Assert-E2ECondition $defaultPassed "default_owned_cleanup"

    $bootstrapPhase = "preview_launch"
    $preview = Start-PackagedCandidate -Arguments @("--operator-preview")
    $bootstrapPhase = "preview_readiness"
    $previewReady = Wait-CandidateReady -Process $preview
    $bootstrapPhase = "preview_isolation"
    $previewListenerCount = Get-ListenerCount -ProcessID $preview.Id
    Assert-E2ECondition ($previewReady.store_nonempty -and $previewListenerCount -eq 0) `
        "operator_preview_start"
    $bootstrapPhase = "preview_kill"
    $preview.Kill($true)
    $preview.WaitForExit()
    $preview.Dispose()
    $storeBytesAfterKill = [int64](Get-Item -LiteralPath $script:database).Length

    $bootstrapPhase = "reopen_launch"
    $reopened = Start-PackagedCandidate -Arguments @("--operator-preview")
    $bootstrapPhase = "reopen_readiness"
    $reopenReady = Wait-CandidateReady -Process $reopened
    $bootstrapPhase = "reopen_isolation"
    $reopenListenerCount = Get-ListenerCount -ProcessID $reopened.Id
    $bootstrapPhase = "reopen_close"
    $reopenClose = Close-PackagedCandidate -Process $reopened
    Assert-E2ECondition ($reopenReady.store_nonempty -and $storeBytesAfterKill -gt 0 -and
        $reopenListenerCount -eq 0 -and $reopenClose.stopped) "operator_preview_reopen"
    $reopened.Dispose()
    Add-E2EResult "packaged_operator_preview_kill_reopen" "pass" `
        ([pscustomobject][ordered]@{
            ready_before_kill = $true
            native_window_ready_before_kill = $previewReady.native_window_ready
            native_window_responsive_before_kill = $previewReady.native_window_responsive
            store_retained = $true
            reopened = $true
            native_window_ready_after_reopen = $reopenReady.native_window_ready
            native_window_responsive_after_reopen = $reopenReady.native_window_responsive
            host_tcp_listener_count = $previewListenerCount + $reopenListenerCount
            graceful_exit_after_reopen = $reopenClose.graceful_exit
            owned_force_cleanup_after_reopen = $reopenClose.force_cleanup_used
            process_stopped_after_reopen = $reopenClose.stopped
        })

    $bootstrapPhase = "fixture_immutability"
    $fixtureStates = foreach ($repositoryReport in $repositoryReports) {
        Get-FixtureState -Root (Join-Path $repositoriesRoot $repositoryReport.id) `
            -Expected $repositoryReport
    }
    $fixturesUntouched = @($fixtureStates | Where-Object {
        -not $_.head_matches -or -not $_.tree_matches -or -not $_.clean
    }).Count -eq 0
    Assert-E2ECondition $fixturesUntouched "fixture_mutation"
    Add-E2EResult "fixed_repositories_immutable" "pass" ([pscustomobject][ordered]@{
        repositories_checked = 4
        exact_heads = $true
        exact_trees = $true
        clean_worktrees = $true
    })

    $bootstrapPhase = "sentinel_non_persistence"
    $sentinelPersisted = Test-SentinelPersisted `
        -Roots @($script:isolatedHome, $script:extractRoot, $repositoriesRoot) `
        -Sentinels $sentinels
    Assert-E2ECondition (-not $sentinelPersisted) "sentinel_persisted"
    Add-E2EResult "credential_sentinel_non_persistence" "pass" `
        ([pscustomobject][ordered]@{
            sentinel_classes = 5
            persisted = $false
            values_redacted = $true
        })

    $bootstrapPhase = "candidate_cleanup"
    $liveOwned = @($script:startedCandidates | Where-Object {
        Test-OwnedCandidateRunning -Candidate $_
    }).Count
    Assert-E2ECondition ($liveOwned -eq 0) "candidate_process_leak"
    Add-E2EResult "candidate_process_cleanup" "pass" ([pscustomobject][ordered]@{
        candidate_processes_started = $script:startedCandidates.Count
        candidate_processes_remaining = 0
    })
} catch {
    $bootstrapError = Get-SafeFailureCode -Message $_.Exception.Message
    Add-E2EResult "packaged_bootstrap_completion" "fail" ([pscustomobject][ordered]@{
        failure_code = $bootstrapError
        phase = $bootstrapPhase
        launch_ordinal = $script:startedCandidates.Count
        candidate_exit_code = Get-SafeCandidateExitCode
        sentinel_evidence_read = $_.Exception.Data["sentinel_evidence_read"]
        detail_redacted = $true
    })
} finally {
    foreach ($name in $environmentNames) {
        [System.Environment]::SetEnvironmentVariable(
            $name, $previousEnvironment[$name], "Process")
    }
    if ($null -ne $script:binary) { Stop-OwnedCandidates }
}

$cleanupPassed = $false
try {
    $resolvedHarness = [System.IO.Path]::GetFullPath($harnessRoot)
    $temporaryPrefix = $temporaryRoot.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    Assert-E2ECondition ($resolvedHarness.StartsWith($temporaryPrefix,
            [System.StringComparison]::OrdinalIgnoreCase) -and
        [System.IO.Path]::GetFileName($resolvedHarness).StartsWith(
            "standard-code-packaged-e2e-", [System.StringComparison]::Ordinal)) `
        "unsafe_cleanup_root"
    for ($attempt = 1; $attempt -le 5; $attempt++) {
        try {
            if (Test-Path -LiteralPath $resolvedHarness) {
                Remove-Item -LiteralPath $resolvedHarness -Recurse -Force -ErrorAction Stop
            }
            $cleanupPassed = -not (Test-Path -LiteralPath $resolvedHarness)
            if ($cleanupPassed) { break }
        } catch {
            if ($attempt -eq 5) { throw }
            Start-Sleep -Milliseconds 400
        }
    }
    Assert-E2ECondition $cleanupPassed "harness_cleanup_incomplete"
    Add-E2EResult "owned_harness_cleanup" "pass" ([pscustomobject][ordered]@{
        owned_directory_removed = $true
    })
} catch {
    $bootstrapError = "harness_cleanup_failed"
    Add-E2EResult "owned_harness_cleanup" "fail" ([pscustomobject][ordered]@{
        failure_code = "harness_cleanup_failed"
        phase = "harness_cleanup"
        detail_redacted = $true
    })
}

$bootstrapFailed = $null -ne $bootstrapError -or
    @($results | Where-Object { $_.status -ne "pass" }).Count -gt 0
$fixtureEvidence = if ($null -ne $fixtureReport) {
    [pscustomobject][ordered]@{
        protocol_version = [string]$fixtureReport.protocol_version
        manifest_sha256 = [string]$fixtureReport.manifest_sha256
        attack_matrix_sha256 = [string]$fixtureReport.attack_matrix_sha256
        repository_count = [int]$fixtureReport.repository_count
        attack_case_count = [int]$fixtureReport.attack_case_count
        oracle_verified = [bool]$fixtureReport.oracle_verified
        all_attack_cases_bound = [bool]$fixtureReport.all_attack_cases_bound
    }
} else { $null }
$candidateEvidence = if ($null -ne $manifest -and $null -ne $metadata) {
    [pscustomobject][ordered]@{
        version = [string]$manifest.version
        revision = [string]$manifest.revision
        zip_sha256 = $zipHash
        binary_sha256 = $binaryHash
        source_date_epoch = [int64]$metadata.source_date_epoch
    }
} else { $null }
$report = [pscustomobject][ordered]@{
    protocol_version = "standard_code_packaged_e2e.v1"
    generated_at = [DateTime]::UtcNow.ToString("o")
    issue = 140
    bootstrap_status = $(if ($bootstrapFailed) { "fail" } else { "pass" })
    release_gate_status = $(if ($bootstrapFailed) { "fail" } else { "needs_full_matrix" })
    candidate = $candidateEvidence
    fixture_set = $fixtureEvidence
    results = @($results)
    attack_matrix = [pscustomobject][ordered]@{
        required_case_count = 39
        prepared_case_count = $(if ($null -ne $fixtureReport) {
            [int]$fixtureReport.attack_case_count
        } else { 0 })
        evidenced_case_count = 0
        remaining_required_case_count = 39
        status = "needs_full_matrix"
        failure_policy = "fail_closed_no_waiver"
        unexecuted_cases_are_not_pass_or_skip = $true
    }
}
[System.IO.Directory]::CreateDirectory((Split-Path -Parent $output)) | Out-Null
$json = $report | ConvertTo-Json -Depth 8
$reportBytes = [System.Text.UTF8Encoding]::new($false).GetBytes($json + "`n")
$reportStream = [System.IO.File]::Open($output, [System.IO.FileMode]::CreateNew,
    [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
try {
    $reportStream.Write($reportBytes, 0, $reportBytes.Length)
    $reportStream.Flush($true)
} finally {
    $reportStream.Dispose()
}

Write-Output "standard_code_packaged_e2e_written: $output"
Write-Output "standard_code_packaged_e2e_bootstrap: $(if ($bootstrapFailed) { 'fail' } else { 'pass' })"
Write-Output "standard_code_packaged_e2e_release_gate: $(if ($bootstrapFailed) { 'fail' } else { 'needs_full_matrix' })"
foreach ($result in $results) {
    if ($result.status -ne "fail") { continue }
    # Only report the fixed case identifier and already-sanitized failure code.
    # Raw exceptions and synthetic credential values remain out of CI logs.
    $failureCode = [string]$result.facts.failure_code
    if ([string]::IsNullOrWhiteSpace($failureCode)) { $failureCode = "case_failed" }
    Write-Output "standard_code_packaged_e2e_failure: $($result.id) / $failureCode"
}
if ($bootstrapFailed) { throw "Standard Code packaged E2E bootstrap failed" }

if ($productReportProvided) {
    $candidateBinary = Join-Path $outputRoot "TraverseBoard.exe"
    $candidateArchive = Join-Path $outputRoot ([string]$manifest.zip_name)
    Push-Location $repositoryRoot
    try {
        & go run ./cmd/releasegate `
            --binary $candidateBinary `
            --archive $candidateArchive `
            --portable-manifest (Join-Path $outputRoot "portable-zip-manifest.json") `
            --release-metadata (Join-Path $outputRoot "release-metadata.json") `
            --bootstrap $output `
            --product $productEvidence `
            --security $securityEvidence `
            --expected-revision ([string]$manifest.revision) `
            --report $aggregateOutput
        if ($LASTEXITCODE -ne 0) { throw "Standard Code aggregate release gate failed" }
    }
    finally { Pop-Location }
    Write-Output "standard_code_release_gate_written: $aggregateOutput"
    Write-Output "standard_code_release_gate: passed"
}
