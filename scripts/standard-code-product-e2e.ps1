[CmdletBinding()]
param(
    [ValidateSet("Prepare", "InjectConcurrentEdit", "Collect")]
    [string]$Mode = "Prepare",
    [string]$OutputDirectory = "build/desktop",
    [string]$ExpectedRevision = "",
    [string]$SessionDirectory = "",
    [string]$SessionFile = "",
    [string]$RunbookPath = "",
    [string]$ReportPath = ""
)

<#
.SYNOPSIS
Prepares and collects the independent issue #182 packaged product slice.

.DESCRIPTION
Prepare verifies the exact portable candidate, materializes and proves the
fixed four-language oracle under a Unicode/space/long path, seeds owned dirty
and untracked source work, and starts the extracted EXE with zero arguments and
an isolated CYBERAGENT_HOME. The Desktop remains open for the real operator
workflow.

InjectConcurrentEdit is run after Workspace Trust has captured the Go source
state. It changes the same tracked source file outside the Drydock so the final
receipt must preserve concurrent user work.

Collect is run only after the candidate exits. It validates the candidate-bound
runbook and evidence files against durable SQLite, Drydock, Command Runtime,
Thread, Handoff, Checkpoint, Artifact, and delivery facts. It never upgrades an
unavailable backend to Full Access and never turns missing evidence into pass.
#>

$ErrorActionPreference = "Stop"
if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    throw "The issue #182 product workflow requires Windows"
}

$repositoryRoot = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$repositoryPrefix = $repositoryRoot.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
$outputRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
if (-not $outputRoot.StartsWith($repositoryPrefix,
        [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Portable candidate input must remain inside the repository"
}

function Get-SHA256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Assert-RegularFile {
    param([Parameter(Mandatory = $true)][string]$Path, [string]$Label = "file")
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label is missing: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        throw "$Label must not be a reparse point: $Path"
    }
}

function Resolve-SessionFile {
    if ([string]::IsNullOrWhiteSpace($SessionFile)) {
        throw "-SessionFile is required for $Mode"
    }
    $resolved = [System.IO.Path]::GetFullPath($SessionFile)
    Assert-RegularFile -Path $resolved -Label "product session"
    return $resolved
}

function Read-ProductSession {
    $path = Resolve-SessionFile
    $value = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    if ([string]$value.protocol_version -cne "standard_code_product_session.v1" -or
        [string]$value.candidate_sha256 -notmatch '^[0-9a-f]{64}$' -or
        [string]$value.revision -notmatch '^[0-9a-f]{40}$') {
        throw "Product session identity is invalid"
    }
    return [pscustomobject]@{ Path = $path; Value = $value }
}

function Write-JSONExclusive {
    param([Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][object]$Value)
    if (Test-Path -LiteralPath $Path) { throw "Output already exists: $Path" }
    $content = ($Value | ConvertTo-Json -Depth 12) + "`n"
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::CreateNew,
        [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    try {
        $bytes = [System.Text.UTF8Encoding]::new($false).GetBytes($content)
        $stream.Write($bytes, 0, $bytes.Length)
        $stream.Flush($true)
    }
    finally { $stream.Dispose() }
}

if ($Mode -eq "Prepare") {
    foreach ($name in @("portable-zip-manifest.json", "release-metadata.json",
            "TraverseBoard.exe")) {
        Assert-RegularFile -Path (Join-Path $outputRoot $name) -Label "portable candidate input"
    }
    $manifestPath = Join-Path $outputRoot "portable-zip-manifest.json"
    $metadataPath = Join-Path $outputRoot "release-metadata.json"
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
    if ([string]$manifest.protocol_version -cne "portable_zip_manifest.v1" -or
        [string]$metadata.protocol_version -cne "portable_release_metadata.v1" -or
        [string]$manifest.revision -cne [string]$metadata.revision -or
        [bool]$metadata.modified -or -not [bool]$metadata.reproducible) {
        throw "Portable candidate provenance is not clean and reproducible"
    }
    if (-not [string]::IsNullOrWhiteSpace($ExpectedRevision) -and
        [string]$manifest.revision -cne $ExpectedRevision) {
        throw "Portable candidate revision differs from -ExpectedRevision"
    }
    $zipPath = Join-Path $outputRoot ([string]$manifest.zip_name)
    Assert-RegularFile -Path $zipPath -Label "portable ZIP"
    & (Join-Path $PSScriptRoot "verify-portable-zip.ps1") `
        -OutputDirectory $OutputDirectory -ExpectedVersion ([string]$manifest.version) `
        -ExpectedRevision ([string]$manifest.revision)
    if ($LASTEXITCODE -ne 0) { throw "Portable ZIP verification failed" }

    if ([string]::IsNullOrWhiteSpace($SessionDirectory)) {
        $SessionDirectory = Join-Path ([System.IO.Path]::GetTempPath()) `
            ("Traverse Board 产品证据 " + [guid]::NewGuid().ToString("N"))
    }
    $sessionRoot = [System.IO.Path]::GetFullPath($SessionDirectory)
    if (Test-Path -LiteralPath $sessionRoot) {
        throw "Session directory must not already exist: $sessionRoot"
    }
    [System.IO.Directory]::CreateDirectory($sessionRoot) | Out-Null
    $sessionInfo = Get-Item -LiteralPath $sessionRoot -Force
    if ($sessionInfo.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        throw "Session directory must not be a reparse point"
    }

    $repositoriesRoot = Join-Path $sessionRoot `
        "用户 工作区/非常 长的路径 用于 Windows 中文 空格 与 长路径 产品验证/固定 四语言 仓库"
    $fixtureReportPath = Join-Path $sessionRoot "fixture-set.json"
    Push-Location $repositoryRoot
    try {
        & go run ./cmd/packagede2e --output $repositoriesRoot `
            --report $fixtureReportPath --verify-toolchains
        if ($LASTEXITCODE -ne 0) { throw "Four-language fixture oracle failed" }
    }
    finally { Pop-Location }

    $packageRoot = Join-Path $sessionRoot "exact portable package"
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [System.IO.Compression.ZipFile]::ExtractToDirectory($zipPath, $packageRoot)
    $extractedBinary = Join-Path $packageRoot "TraverseBoard.exe"
    Assert-RegularFile -Path $extractedBinary -Label "extracted candidate"
    $candidateSHA = Get-SHA256 $extractedBinary
    if ($candidateSHA -cne [string]$manifest.binary_sha256 -or
        $candidateSHA -cne (Get-SHA256 (Join-Path $outputRoot "TraverseBoard.exe"))) {
        throw "Extracted EXE does not match the exact portable candidate"
    }

    $goRoot = Join-Path $repositoriesRoot "go"
    $concurrentPath = Join-Path $goRoot "README.md"
    $concurrentBaselineSHA = Get-SHA256 $concurrentPath
    [System.IO.File]::AppendAllText($concurrentPath,
        "`r`n用户保留的未提交修改：Workspace Trust 前。`r`n",
        [System.Text.UTF8Encoding]::new($false))
    $untrackedPath = Join-Path $goRoot "edge/user-untracked.bin"
    [System.IO.File]::WriteAllBytes($untrackedPath, [byte[]](0, 255, 13, 10, 128, 1, 2, 3))

    $isolatedHome = Join-Path $sessionRoot "isolated home"
    $evidenceRoot = Join-Path $sessionRoot "evidence"
    [System.IO.Directory]::CreateDirectory($isolatedHome) | Out-Null
    foreach ($relative in @("launch", "surfaces", "fallback", "continuity",
            "windows_10", "windows_11")) {
        [System.IO.Directory]::CreateDirectory((Join-Path $evidenceRoot $relative)) | Out-Null
    }

    $previousHome = [System.Environment]::GetEnvironmentVariable("CYBERAGENT_HOME", "Process")
    try {
        [System.Environment]::SetEnvironmentVariable("CYBERAGENT_HOME", $isolatedHome, "Process")
        $process = Start-Process -FilePath $extractedBinary -WorkingDirectory $packageRoot `
            -PassThru
    }
    finally {
        [System.Environment]::SetEnvironmentVariable("CYBERAGENT_HOME", $previousHome, "Process")
    }
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $database = Join-Path $isolatedHome "cyberagent.db"
    while ([DateTime]::UtcNow -lt $deadline -and
        -not (Test-Path -LiteralPath $database -PathType Leaf)) {
        $process.Refresh()
        if ($process.HasExited) { throw "Zero-argument packaged candidate exited during startup" }
        Start-Sleep -Milliseconds 250
    }
    Assert-RegularFile -Path $database -Label "candidate database"
    $process.Refresh()
    if ($process.HasExited) { throw "Zero-argument packaged candidate exited after startup" }

    $launchRecordPath = Join-Path $evidenceRoot "launch/default.json"
    $launchRecord = [pscustomobject][ordered]@{
        protocol_version = "standard_code_product_launch.v1"
        candidate_sha256 = $candidateSHA
        executable_name = "TraverseBoard.exe"
        executable_sha256 = $candidateSHA
        arguments = @()
        process_id = [int]$process.Id
        started_at = $process.StartTime.ToUniversalTime().ToString("o")
    }
    Write-JSONExclusive -Path $launchRecordPath -Value $launchRecord

    $sessionPath = Join-Path $sessionRoot "product-session.json"
    $session = [pscustomobject][ordered]@{
        protocol_version = "standard_code_product_session.v1"
        revision = [string]$manifest.revision
        candidate_sha256 = $candidateSHA
        zip_sha256 = Get-SHA256 $zipPath
        output_root = $outputRoot
        zip_path = $zipPath
        manifest_path = $manifestPath
        metadata_path = $metadataPath
        fixture_report_path = $fixtureReportPath
        repositories_root = $repositoriesRoot
        extracted_binary = $extractedBinary
        home = $isolatedHome
        evidence_root = $evidenceRoot
        launch_record_path = $launchRecordPath
        launch_record_sha256 = Get-SHA256 $launchRecordPath
        process_id = [int]$process.Id
        process_started_at = $process.StartTime.ToUniversalTime().ToString("o")
        concurrent_relative_path = "README.md"
        concurrent_baseline_sha256 = $concurrentBaselineSHA
        concurrent_expected_sha256 = ""
        untracked_relative_path = "edge/user-untracked.bin"
        untracked_sha256 = Get-SHA256 $untrackedPath
    }
    Write-JSONExclusive -Path $sessionPath -Value $session
    Write-Output "standard_code_product_session: $sessionPath"
    Write-Output "candidate_process_id: $($process.Id)"
    Write-Output "candidate_sha256: $candidateSHA"
    Write-Output "launch_record: $launchRecordPath"
    Write-Output "launch_record_sha256: $($session.launch_record_sha256)"
    Write-Output "next: finish Workspace Trust, then run InjectConcurrentEdit with -SessionFile"
    return
}

$loaded = Read-ProductSession
$sessionPath = $loaded.Path
$session = $loaded.Value
Assert-RegularFile -Path ([string]$session.extracted_binary) -Label "extracted candidate"
if ((Get-SHA256 ([string]$session.extracted_binary)) -cne [string]$session.candidate_sha256) {
    throw "Extracted candidate hash drifted"
}

if ($Mode -eq "InjectConcurrentEdit") {
    if (-not [string]::IsNullOrWhiteSpace([string]$session.concurrent_expected_sha256)) {
        throw "Concurrent edit was already injected"
    }
    $target = Join-Path (Join-Path ([string]$session.repositories_root) "go") `
        ([string]$session.concurrent_relative_path)
    Assert-RegularFile -Path $target -Label "concurrent source target"
    [System.IO.File]::AppendAllText($target,
        "`r`n用户并发编辑：Workspace Trust 后，Drydock 交付不得覆盖。`r`n",
        [System.Text.UTF8Encoding]::new($false))
    $session.concurrent_expected_sha256 = Get-SHA256 $target
    $content = ($session | ConvertTo-Json -Depth 12) + "`n"
    [System.IO.File]::WriteAllText($sessionPath, $content,
        [System.Text.UTF8Encoding]::new($false))
    Write-Output "concurrent_edit_path: $target"
    Write-Output "concurrent_baseline_sha256: $($session.concurrent_baseline_sha256)"
    Write-Output "concurrent_expected_sha256: $($session.concurrent_expected_sha256)"
    return
}

if ([string]::IsNullOrWhiteSpace([string]$session.concurrent_expected_sha256)) {
    throw "InjectConcurrentEdit has not been completed"
}
try {
    $candidateProcess = Get-Process -Id ([int]$session.process_id) -ErrorAction Stop
    if ($candidateProcess.StartTime.ToUniversalTime().ToString("o") -ceq
        [string]$session.process_started_at) {
        throw "Stop the packaged candidate before Collect"
    }
}
catch [Microsoft.PowerShell.Commands.ProcessCommandException] {
    # Expected: the exact candidate process has exited.
}
if ([string]::IsNullOrWhiteSpace($RunbookPath) -or
    [string]::IsNullOrWhiteSpace($ReportPath)) {
    throw "Collect requires -RunbookPath and -ReportPath"
}
$runbook = [System.IO.Path]::GetFullPath($RunbookPath)
$report = [System.IO.Path]::GetFullPath($ReportPath)
Assert-RegularFile -Path $runbook -Label "product runbook"
if (Test-Path -LiteralPath $report) { throw "Product report must not already exist: $report" }
if (-not (Test-Path -LiteralPath ([System.IO.Path]::GetDirectoryName($report)) -PathType Container)) {
    throw "Product report parent directory does not exist"
}

Push-Location $repositoryRoot
try {
    & go run ./cmd/producte2e `
        --binary (Join-Path ([string]$session.output_root) "TraverseBoard.exe") `
        --zip ([string]$session.zip_path) `
        --portable-manifest ([string]$session.manifest_path) `
        --release-metadata ([string]$session.metadata_path) `
        --fixture-report ([string]$session.fixture_report_path) `
        --home ([string]$session.home) `
        --evidence-root ([string]$session.evidence_root) `
        --runbook $runbook --report $report `
        --expected-revision ([string]$session.revision)
    if ($LASTEXITCODE -ne 0) { throw "Issue #182 product evidence collection failed" }
}
finally { Pop-Location }
Write-Output "standard_code_product_e2e_written: $report"
