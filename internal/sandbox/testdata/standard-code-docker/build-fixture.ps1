# Builds the fixed, environment-free Standard Code image without accepting an
# unpinned base image or overwriting a pre-existing tag. Every base must already
# exist in the fixed local Docker Engine and be supplied as name@sha256:digest.

$ErrorActionPreference = "Stop"
$fixtureDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
# Explicit, pre-downloaded input; the build never contacts a package registry.
# https://registry.npmjs.org/pnpm/-/pnpm-10.28.1.tgz
$pnpmTarball = [Environment]::GetEnvironmentVariable("CYBERAGENT_STANDARD_CODE_PNPM_TARBALL")
$pnpmIntegrity = "sha512-fX27yp6ZRHt8O/enMoavqva+mSUeuUmLrvp9QGiS9nuHmts6HX5of8TMwaOIxxdfuq5WeiarRNEGe1T8sNajFg=="
if ([string]::IsNullOrWhiteSpace($pnpmTarball)) {
  throw "CYBERAGENT_STANDARD_CODE_PNPM_TARBALL must name the pre-downloaded pnpm 10.28.1 tarball ($pnpmIntegrity); this script does not download"
}
$pnpmFile = Get-Item -LiteralPath $pnpmTarball -Force
if ($pnpmFile.PSIsContainer -or ($pnpmFile.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
  throw "pnpm tarball must be a regular file without reparse indirection"
}
$expectedPNPMHash = ([BitConverter]::ToString([Convert]::FromBase64String($pnpmIntegrity.Substring(7)))).Replace('-', '')
if ((Get-FileHash -LiteralPath $pnpmFile.FullName -Algorithm SHA512).Hash -ne $expectedPNPMHash) {
  throw "pnpm 10.28.1 tarball does not match its fixed SRI checksum"
}
$required = @(
  "CYBERAGENT_STANDARD_CODE_GO_IMAGE_DIGEST",
  "CYBERAGENT_STANDARD_CODE_NODE_IMAGE_DIGEST",
  "CYBERAGENT_STANDARD_CODE_PYTHON_IMAGE_DIGEST",
  "CYBERAGENT_STANDARD_CODE_RUST_IMAGE_DIGEST",
  "CYBERAGENT_STANDARD_CODE_BUN_IMAGE_DIGEST"
)

$images = @{}
foreach ($name in $required) {
  $value = [Environment]::GetEnvironmentVariable($name)
  if ([string]::IsNullOrWhiteSpace($value) -or $value -notmatch '^.+@sha256:[0-9a-f]{64}$') {
    throw "$name must be an exact name@sha256:digest reference"
  }
  docker image inspect $value | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "$name must already exist locally; this script does not pull"
  }
  $images[$name] = $value
}

$ownedID = [guid]::NewGuid().ToString("N")
$tag = "cyberagent-standard-code-fixture:issue133-$ownedID"
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("cyberagent-standard-code-$ownedID")
$contextDirectory = Join-Path $work "context"
$imageDirectory = Join-Path $work "image"
$finalTar = Join-Path ([System.IO.Path]::GetTempPath()) ("cyberagent-standard-code-$ownedID.tar")
$existingTag = docker image ls --filter "reference=$tag" --format "{{.Repository}}:{{.Tag}}"
if ($LASTEXITCODE -ne 0) {
  throw "inspect prospective fixture tag failed"
}
if (@($existingTag) -contains $tag) {
  throw "random fixture tag already exists; refusing to overwrite it"
}
if ((Test-Path -LiteralPath $work) -or (Test-Path -LiteralPath $finalTar)) {
  throw "random fixture path already exists; refusing to overwrite it"
}
$workOwned = $false
$finalTarOwned = $false
$rawTagOwned = $false
New-Item -ItemType Directory -Path $work | Out-Null
$workOwned = $true
New-Item -ItemType Directory -Path $contextDirectory | Out-Null
New-Item -ItemType Directory -Path $imageDirectory | Out-Null

try {
  Copy-Item -LiteralPath (Join-Path $fixtureDirectory "Dockerfile") -Destination $contextDirectory
  $stagedPNPM = Join-Path $contextDirectory "pnpm-10.28.1.tgz"
  Copy-Item -LiteralPath $pnpmFile.FullName -Destination $stagedPNPM
  if ((Get-FileHash -LiteralPath $stagedPNPM -Algorithm SHA512).Hash -ne $expectedPNPMHash) {
    throw "staged pnpm tarball changed before the offline build"
  }
  $previousGOOS = $env:GOOS
  $previousGOARCH = $env:GOARCH
  $previousCGO = $env:CGO_ENABLED
  try {
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags "-s -w" -o (Join-Path $contextDirectory "standard-code-runner") `
      (Join-Path $fixtureDirectory "runner")
    if ($LASTEXITCODE -ne 0) { throw "build Standard Code runner failed" }
  } finally {
    $env:GOOS = $previousGOOS
    $env:GOARCH = $previousGOARCH
    $env:CGO_ENABLED = $previousCGO
  }

  $rawTagOwned = $true
  docker build --pull=false --network=none --provenance=false --platform linux/amd64 `
    --build-arg "GO_IMAGE=$($images['CYBERAGENT_STANDARD_CODE_GO_IMAGE_DIGEST'])" `
    --build-arg "NODE_IMAGE=$($images['CYBERAGENT_STANDARD_CODE_NODE_IMAGE_DIGEST'])" `
    --build-arg "PYTHON_IMAGE=$($images['CYBERAGENT_STANDARD_CODE_PYTHON_IMAGE_DIGEST'])" `
    --build-arg "RUST_IMAGE=$($images['CYBERAGENT_STANDARD_CODE_RUST_IMAGE_DIGEST'])" `
    --build-arg "BUN_IMAGE=$($images['CYBERAGENT_STANDARD_CODE_BUN_IMAGE_DIGEST'])" `
    -t $tag $contextDirectory
  if ($LASTEXITCODE -ne 0) { throw "build fixed Standard Code image failed" }

  docker image save $tag -o (Join-Path $work "raw.tar")
  if ($LASTEXITCODE -ne 0) { throw "save fixed Standard Code image failed" }
  tar -xf (Join-Path $work "raw.tar") -C $imageDirectory
  if ($LASTEXITCODE -ne 0) { throw "extract fixed Standard Code image failed" }
  Remove-Item -LiteralPath (Join-Path $work "raw.tar") -Force

  go run (Join-Path $fixtureDirectory "strip-image-config.go") $imageDirectory
  if ($LASTEXITCODE -ne 0) { throw "strip image environment/volumes/labels failed" }
  $finalTarOwned = $true
  tar -cf $finalTar -C $imageDirectory .
  if ($LASTEXITCODE -ne 0) { throw "repack fixed Standard Code image failed" }

  # This exact tag contains the image created above and includes a random
  # ownership suffix; removing it cannot target a pre-existing user tag.
  docker image rm $tag | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "remove owned raw image tag failed" }
  $rawTagOwned = $false
  $rawTagOwned = $true
  docker image load -i $finalTar | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "load fixed Standard Code image failed" }

  $repoDigest = docker image inspect $tag --format "{{index .RepoDigests 0}}"
  if ($LASTEXITCODE -ne 0 -or $repoDigest -notmatch '@sha256:[0-9a-f]{64}$') {
    throw "fixed Standard Code image did not bind an exact local digest"
  }
  $configJSON = docker image inspect $tag --format "{{json .Config}}"
  if ($LASTEXITCODE -ne 0) { throw "inspect fixed Standard Code image config failed" }
  $config = $configJSON | ConvertFrom-Json
  if (($null -ne $config.Env -and @($config.Env).Count -ne 0) -or
      $null -ne $config.Volumes -or $null -eq $config.Labels -or
      @($config.Labels.PSObject.Properties).Count -ne 5 -or
      $config.Labels.'org.traverse-board.project-actions' -ne '1' -or
      $config.Labels.'org.traverse-board.project-preparation' -ne '1' -or
      $config.Labels.'org.traverse-board.pnpm-version' -ne '10.28.1' -or
      $config.Labels.'org.traverse-board.yarn-version' -ne '1.22.22' -or
      $config.Labels.'org.traverse-board.bun-version' -ne '1.4.3' -or
      $config.User -ne "65532:65532" -or $config.WorkingDir -ne "/workspace" -or
      @($config.Entrypoint).Count -ne 1 -or
      $config.Entrypoint[0] -ne "/traverse-board/standard-code-runner") {
    throw "fixed Standard Code image config is not exact"
  }
  $digest = ($repoDigest -split '@')[-1]
  Write-Output "CYBERAGENT_STANDARD_CODE_DOCKER_IMAGE_DIGEST=$digest"
  Write-Output "CYBERAGENT_STANDARD_CODE_DOCKER_TEST_IMAGE_DIGEST=$digest"
  # The successfully validated final tag is the requested fixture output.
  $rawTagOwned = $false
} finally {
  if ($rawTagOwned) {
    docker image rm $tag | Out-Null
  }
  if ($finalTarOwned -and (Test-Path -LiteralPath $finalTar)) {
    Remove-Item -LiteralPath $finalTar -Force
  }
  if ($workOwned -and (Test-Path -LiteralPath $work)) {
    $resolvedWork = [System.IO.Path]::GetFullPath($work)
    $expectedWork = [System.IO.Path]::GetFullPath((Join-Path ([System.IO.Path]::GetTempPath()) ("cyberagent-standard-code-$ownedID")))
    $workEntry = Get-Item -LiteralPath $resolvedWork -Force
    if ($resolvedWork -ne $expectedWork -or -not $workEntry.PSIsContainer -or
        ($workEntry.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
      throw "owned fixture cleanup path changed; preserving it"
    }
    Remove-Item -LiteralPath $resolvedWork -Recurse -Force
  }
}
