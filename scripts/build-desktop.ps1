[CmdletBinding()]
param(
    [string]$OutputDirectory = "build/desktop",
    [string]$Version = "v1.0.0",
    [switch]$SkipFrontend,
    [switch]$BundlePowerShell7,
    [switch]$VerifyReproducible
)

$ErrorActionPreference = "Stop"
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$outputRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
$repositoryFull = [System.IO.Path]::GetFullPath($repositoryRoot)
$binaryPath = Join-Path $outputRoot "TraverseBoard.exe"
$reproBinaryPath = Join-Path $outputRoot "TraverseBoard.repro.exe"
$metadataPath = Join-Path $outputRoot "release-metadata.json"
$guideName = "LOCAL-TEST-GUIDE.txt"
$guideSourcePath = Join-Path $repositoryRoot "packaging/windows/$guideName"
$guidePath = Join-Path $outputRoot $guideName
$goSumPath = Join-Path $repositoryRoot "go.sum"
$nodeLockPath = Join-Path $repositoryRoot "web/package-lock.json"
$cargoLockPath = Join-Path $repositoryRoot "analyzers/Cargo.lock"
$rustToolchainPath = Join-Path $repositoryRoot "analyzers/rust-toolchain.toml"
$embeddedAnalyzerPath = Join-Path $repositoryRoot "internal/analyzer/embedded/cyberagent-analyzer-fixture.wasm"

if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    throw "Desktop portable build currently supports only Windows"
}
if (-not $outputRoot.StartsWith($repositoryFull + [System.IO.Path]::DirectorySeparatorChar,
        [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Desktop output directory must remain inside the repository"
}
if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$') {
    throw "Desktop release version is invalid"
}
if (-not (Test-Path -LiteralPath $guideSourcePath -PathType Leaf)) {
    throw "Desktop local test guide is required"
}
foreach ($provenanceInput in @($goSumPath, $nodeLockPath, $cargoLockPath,
        $rustToolchainPath, $embeddedAnalyzerPath)) {
    if (-not (Test-Path -LiteralPath $provenanceInput -PathType Leaf)) {
        throw "Desktop release provenance input is missing: $provenanceInput"
    }
}

function Invoke-Checked {
    param([scriptblock]$Command, [string]$Failure)
    & $Command
    if ($LASTEXITCODE -ne 0) { throw $Failure }
}

function Assert-NoOutputReparsePoint {
    param([string]$Root, [string]$Candidate)
    $separators = [char[]]@([System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar)
    $rootPrefix = $Root.TrimEnd($separators) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $Candidate.StartsWith($rootPrefix,
            [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Desktop output directory must remain inside the repository"
    }
    $relative = $Candidate.Substring($rootPrefix.Length)
    $current = $Root
    foreach ($component in [regex]::Split($relative, '[\\/]')) {
        if ([string]::IsNullOrWhiteSpace($component)) { continue }
        $current = Join-Path $current $component
        if (-not (Test-Path -LiteralPath $current)) { break }
        $item = Get-Item -Force -LiteralPath $current
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Desktop output directory cannot traverse a reparse point"
        }
    }
}

Assert-NoOutputReparsePoint -Root $repositoryFull -Candidate $outputRoot

# A newer Go release can still be affected by an advisory fixed in our pinned
# branch. Use the same exact compiler as CI and release packaging.
$goDirective = [regex]::Match(
    [IO.File]::ReadAllText((Join-Path $repositoryRoot 'go.mod')),
    '(?m)^go (?<version>[0-9]+\.[0-9]+\.[0-9]+)\r?$')
if (-not $goDirective.Success) { throw 'Pinned Go toolchain is unavailable in go.mod.' }
$expectedGoVersion = 'go' + $goDirective.Groups['version'].Value
$goVersion = ([string](& go -C $repositoryRoot env GOVERSION)).Trim()
if ($LASTEXITCODE -ne 0 -or $goVersion -cne $expectedGoVersion) {
    throw "Desktop build requires $expectedGoVersion from go.mod. Set GOTOOLCHAIN=$expectedGoVersion."
}

if (-not $SkipFrontend) {
    Push-Location (Join-Path $repositoryRoot "web")
    try {
        Invoke-Checked { npm ci } "npm ci failed"
        Invoke-Checked { npm run check:api } "frontend API contract check failed"
        Invoke-Checked { npm test } "frontend tests failed"
        Invoke-Checked { npm run build } "frontend production build failed"
    }
    finally {
        Pop-Location
    }
}

Push-Location $repositoryRoot
try {
    Invoke-Checked {
        go test ./internal/desktop -count=1 `
            -run '^TestWindowsControlPlaneConsumesValidatedLocalSandboxReadiness$'
    } "Desktop Local Sandbox readiness test failed"
    Invoke-Checked {
        go test ./internal/desktop ./internal/webui -count=1 `
            -skip '^TestWindowsControlPlaneConsumesValidatedLocalSandboxReadiness$'
    } "Desktop Go boundary tests failed"
    Invoke-Checked { go test -count=1 -tags "desktop,wv2runtime.error" ./cmd/cyberagent-desktop ./web } "Wails adapter tests failed"

    $revision = (& git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $revision -notmatch '^[0-9a-f]{40}$') {
        throw "Git release revision is unavailable"
    }
    $sourceDateEpoch = (& git show -s --format=%ct HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $sourceDateEpoch -notmatch '^[1-9][0-9]*$') {
        throw "Git source date epoch is unavailable"
    }
    # Compare normalized content instead of relying on status' stat cache. On
    # Windows, a just-regenerated LF/CRLF-normalized file can transiently appear
    # modified in `git status` even when `git diff` proves its blob is unchanged.
    & git diff --quiet HEAD --
    $diffExit = $LASTEXITCODE
    if ($diffExit -notin @(0, 1)) { throw "Git tracked worktree state is unavailable" }
    $untracked = @(& git ls-files --others --exclude-standard)
    if ($LASTEXITCODE -ne 0) { throw "Git untracked worktree state is unavailable" }
    $modified = if ($diffExit -eq 1 -or $untracked.Count -gt 0) { "true" } else { "false" }
    $cgoEnabled = (& go env CGO_ENABLED).Trim()
    if ($LASTEXITCODE -ne 0 -or $cgoEnabled -notmatch '^[01]$') {
        throw "Go CGO build metadata is invalid"
    }
    $nodeVersion = (& node --version).Trim()
    if ($LASTEXITCODE -ne 0 -or $nodeVersion -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+') {
        throw "Node version build metadata is invalid"
    }
    $npmVersion = (& npm --version).Trim()
    if ($LASTEXITCODE -ne 0 -or $npmVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+') {
        throw "npm version build metadata is invalid"
    }
    $rustToolchainText = [System.IO.File]::ReadAllText($rustToolchainPath)
    $rustToolchainMatch = [regex]::Match($rustToolchainText,
        '(?m)^channel\s*=\s*"(?<version>[0-9]+\.[0-9]+\.[0-9]+)"\s*$')
    if (-not $rustToolchainMatch.Success) {
        throw "Pinned Rust toolchain metadata is invalid"
    }
    $rustVersion = $rustToolchainMatch.Groups['version'].Value
    $targetOS = (& go env GOOS).Trim()
    if ($LASTEXITCODE -ne 0 -or $targetOS -ne "windows") {
        throw "Desktop portable build requires GOOS=windows"
    }
    $targetArch = (& go env GOARCH).Trim()
    if ($LASTEXITCODE -ne 0 -or $targetArch -notin @("amd64", "arm64")) {
        throw "Go Windows build metadata is invalid"
    }

    New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
    # Current end-user builds launch TraverseBoard.exe directly. Remove stale
    # generated shortcuts so an incremental build cannot accidentally publish
    # either the pre-brand or current-brand developer launcher.
    foreach ($staleLauncher in @(
            "Start-Prayu-Operator-Preview.cmd",
            "Start-Traverse-Board.cmd"
        )) {
        $staleLauncherPath = Join-Path $outputRoot $staleLauncher
        if (Test-Path -LiteralPath $staleLauncherPath -PathType Leaf) {
            Remove-Item -LiteralPath $staleLauncherPath -Force
        }
    }
    foreach ($staleReleaseBinding in @(
            "TraverseBoard.direct.exe",
            "direct-exe-signing-request.json",
            "direct-exe-signing-handoff.json",
            "direct-exe-signing.json",
            "windows-release-completion.json"
        )) {
        $staleReleaseBindingPath = Join-Path $outputRoot $staleReleaseBinding
        if (Test-Path -LiteralPath $staleReleaseBindingPath -PathType Leaf) {
            Remove-Item -LiteralPath $staleReleaseBindingPath -Force
        }
    }
    Copy-Item -LiteralPath $guideSourcePath -Destination $guidePath -Force
    $ldflags = @(
        "-s", "-w", "-H=windowsgui",
        "-X=cyberagent-workbench/internal/buildinfo.Version=$Version",
        "-X=cyberagent-workbench/internal/buildinfo.Revision=$revision",
        "-X=cyberagent-workbench/internal/buildinfo.SourceDateEpoch=$sourceDateEpoch",
        "-X=cyberagent-workbench/internal/buildinfo.Modified=$modified",
        "-X=cyberagent-workbench/internal/buildinfo.CGOEnabled=$cgoEnabled"
    ) -join " "
    $previousSourceDateEpoch = $env:SOURCE_DATE_EPOCH
    $env:SOURCE_DATE_EPOCH = $sourceDateEpoch
    try {
        Invoke-Checked {
            go build -tags "desktop,production,wv2runtime.error" -trimpath -ldflags $ldflags `
                -o $binaryPath ./cmd/cyberagent-desktop
        } "Desktop production build failed"
        $reproducible = $false
        if ($VerifyReproducible) {
            Invoke-Checked {
                go build -tags "desktop,production,wv2runtime.error" -trimpath -ldflags $ldflags `
                    -o $reproBinaryPath ./cmd/cyberagent-desktop
            } "Desktop reproducibility build failed"
            $firstHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaryPath).Hash
            $secondHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $reproBinaryPath).Hash
            if ($firstHash -ne $secondHash) {
                throw "Desktop reproducibility check failed: consecutive binary hashes differ"
            }
            $reproducible = $true
        }
    }
    finally {
        $env:SOURCE_DATE_EPOCH = $previousSourceDateEpoch
        if (Test-Path -LiteralPath $reproBinaryPath -PathType Leaf) {
            Remove-Item -LiteralPath $reproBinaryPath -Force
        }
    }

    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaryPath).Hash.ToLowerInvariant()
    $guideHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $guidePath).Hash.ToLowerInvariant()
    $metadata = [ordered]@{
        protocol_version = "portable_release_metadata.v1"
        app_version = $Version
        revision = $revision
        source_date_epoch = [int64]$sourceDateEpoch
        modified = [System.Convert]::ToBoolean($modified)
        go_version = $goVersion
        node_version = $nodeVersion
        npm_version = $npmVersion
        rust_version = $rustVersion
        go_sum_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $goSumPath).Hash.ToLowerInvariant()
        node_lock_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $nodeLockPath).Hash.ToLowerInvariant()
        cargo_lock_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $cargoLockPath).Hash.ToLowerInvariant()
        embedded_analyzer_sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $embeddedAnalyzerPath).Hash.ToLowerInvariant()
        target_os = $targetOS
        target_arch = $targetArch
        cgo_enabled = $cgoEnabled
        trimpath = $true
        binary_name = "TraverseBoard.exe"
        sha256 = $hash
        operator_preview_included = $false
        operator_preview_launcher_name = ""
        operator_preview_launcher_sha256 = ""
        local_test_guide_name = $guideName
        local_test_guide_sha256 = $guideHash
        default_ui_language = "zh-CN"
        reproducibility_checked = [bool]$VerifyReproducible
        reproducible = $reproducible
        installer_included = $false
        registry_writes = $false
        startup_task = $false
        auto_update_enabled = $false
        manual_windows_10_matrix_required = $true
    }
    $metadataJSON = ($metadata | ConvertTo-Json -Depth 4) + "`n"
    [System.IO.File]::WriteAllText($metadataPath, $metadataJSON,
        [System.Text.UTF8Encoding]::new($false))

    & (Join-Path $PSScriptRoot "check-windows-compat.ps1") `
        -BinaryPath $binaryPath -MetadataPath $metadataPath
    if ($LASTEXITCODE -ne 0) { throw "Windows portable compatibility checklist failed" }
}
finally {
    Pop-Location
}

Write-Output "desktop_binary: $binaryPath"
Write-Output "desktop_sha256: $hash"
Write-Output "release_metadata: $metadataPath"
Write-Output "desktop_launch_contract: direct TraverseBoard.exe with no launcher script"
Write-Output "local_test_guide: $guidePath"
Write-Output "desktop_reproducible: $reproducible"
Write-Output "desktop_installer_included: false"
Write-Output "desktop_registry_writes: false"
Write-Output "desktop_profile_control_default: true"
Write-Output "desktop_safe_view_flag: --safe-view"

if ($BundlePowerShell7) {
    & (Join-Path $PSScriptRoot "prepare-powershell-runtime.ps1") -OutputDirectory $OutputDirectory
    Write-Output "desktop_powershell_runtime: runtimes/powershell7 (keep this directory beside the EXE)"
}
