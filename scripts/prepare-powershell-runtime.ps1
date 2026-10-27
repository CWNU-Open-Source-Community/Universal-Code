[CmdletBinding()]
param([string]$OutputDirectory = "build/desktop")

# App-local runtime only: no system install, PATH edits, elevation or profiles.
$ErrorActionPreference = "Stop"
$repositoryRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$outputRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
$prefix = $repositoryRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
if (-not $outputRoot.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Runtime output must stay inside the repository"
}
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw "Windows runtime preparation requires Windows" }
$current = $repositoryRoot
foreach ($part in $outputRoot.Substring($prefix.Length).Split([char[]]"\/")) {
    $current = Join-Path $current $part
    if ((Test-Path -LiteralPath $current) -and ((Get-Item -Force -LiteralPath $current).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Runtime output cannot traverse a reparse point"
    }
}
[IO.Directory]::CreateDirectory($outputRoot) | Out-Null
$destination = Join-Path $outputRoot "runtimes/powershell7"
if (Test-Path -LiteralPath (Join-Path $outputRoot "runtimes")) {
    if ((Get-Item -Force -LiteralPath (Join-Path $outputRoot "runtimes")).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Runtime parent cannot be a reparse point" }
}
if (Test-Path -LiteralPath $destination) { throw "Runtime already exists; keep it or explicitly remove it before preparing another version" }
$version = "7.6.8"
$expectedHash = "B9F074F923C499B51EC99D313DA8E9AE90D17A9F3ADF00C73D6189F34D908528"
$source = "https://github.com/PowerShell/PowerShell/releases/download/v$version/PowerShell-$version-win-x64.zip"
$stage = Join-Path $outputRoot (".powershell-" + [guid]::NewGuid().ToString("N"))
[IO.Directory]::CreateDirectory($stage) | Out-Null
try {
    $archive = Join-Path $stage "powershell.zip"
    # SHA pinned to Microsoft's release, checked before any executable is loaded.
    Invoke-WebRequest -UseBasicParsing -Uri $source -OutFile $archive
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -cne $expectedHash) { throw "PowerShell archive SHA-256 mismatch" }
    $expanded = Join-Path $stage "expanded"
    Expand-Archive -LiteralPath $archive -DestinationPath $expanded
    foreach ($required in @("pwsh.exe", "LICENSE.txt", "ThirdPartyNotices.txt")) {
        if (-not (Test-Path -LiteralPath (Join-Path $expanded $required) -PathType Leaf)) { throw "PowerShell package is missing $required" }
    }
    $signature = Get-AuthenticodeSignature -LiteralPath (Join-Path $expanded "pwsh.exe")
    if ($signature.Status -ne "Valid" -or $signature.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation(?:,|$)') { throw "PowerShell executable lacks a valid Microsoft signature" }
    [ordered]@{ name="PowerShell"; version=$version; architecture="x64"; source=$source; archive_sha256=$expectedHash.ToLowerInvariant(); license="MIT" } |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $expanded "runtime-source.json") -Encoding UTF8
    [IO.Directory]::CreateDirectory((Split-Path -Parent $destination)) | Out-Null
    Move-Item -LiteralPath $expanded -Destination $destination
    Write-Output "Prepared Microsoft PowerShell $version (x64): $destination"
}
finally {
    # The fresh, absolute staging path is strictly within the checked output root.
    $resolvedStage = [IO.Path]::GetFullPath($stage)
    if (-not $resolvedStage.StartsWith($outputRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "Refusing out-of-tree cleanup" }
    Remove-Item -LiteralPath $resolvedStage -Recurse -Force
}
