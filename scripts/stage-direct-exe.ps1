[CmdletBinding()]
param(
    [string]$OutputDirectory = "build/desktop",
    [ValidateSet("auto", "stable", "prerelease")]
    [string]$Channel = "auto",
    [string]$SignedArtifactPath = "",
    [string]$SigningRequestPath = "",
    [string]$SigningHandoffPath = "",
    [string]$ExpectedVersion = "",
    [string]$ExpectedRevision = "",
    [string]$ExpectedSignerSubject = "",
    [string]$ExpectedSignerThumbprint = "",
    [switch]$PrepareSigningRequest,
    [switch]$VerifyOnly
)

<#
.SYNOPSIS
Stages and verifies the single direct-download TraverseBoard.exe.

.DESCRIPTION
The reproducible build is the unsigned payload used inside the Store MSIX. A
stable direct download is supplied by an external protected signing service.
This script proves that Authenticode changed only the PE checksum, certificate
directory, alignment padding, and certificate table; verifies the trusted
signature and timestamp; binds the external signing handoff to the source
revision; and records both the pre-sign payload hash and post-sign artifact
hash. Prereleases remain explicitly unsigned and untrusted.

No private key or signing command is accepted by this script. The signing
service handoff is an input, not authority to sign.
#>

$ErrorActionPreference = "Stop"
if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    throw "Direct EXE signing verification requires Windows"
}

function Get-SHA256 {
    param([string]$Path)
    return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Get-ByteSHA256 {
    param([byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        return ([System.BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace(
            "-", "").ToLowerInvariant()
    }
    finally {
        $sha.Dispose()
    }
}

function Normalize-Thumbprint {
    param([string]$Value)
    return ([regex]::Replace([string]$Value, '[^0-9A-Fa-f]', '')).ToUpperInvariant()
}

function Find-SDKTool {
    param([string]$Name)
    $kitRoot = "C:\Program Files (x86)\Windows Kits\10\bin"
    if (-not (Test-Path -LiteralPath $kitRoot -PathType Container)) { return $null }
    foreach ($sdkVersion in @(Get-ChildItem -LiteralPath $kitRoot -Directory |
            Sort-Object Name -Descending)) {
        $candidate = Join-Path $sdkVersion.FullName "x64\$Name.exe"
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    }
    return $null
}

function Get-PEAuthenticodeLayout {
    param([byte[]]$Bytes, [string]$Label)
    if ($Bytes.Length -lt 256 -or [System.BitConverter]::ToUInt16($Bytes, 0) -ne 0x5a4d) {
        throw "$Label is not a valid PE executable"
    }
    $peOffset = [int][System.BitConverter]::ToUInt32($Bytes, 0x3c)
    if ($peOffset -lt 0 -or $peOffset + 24 -gt $Bytes.Length -or
        [System.BitConverter]::ToUInt32($Bytes, $peOffset) -ne 0x00004550) {
        throw "$Label has an invalid PE header"
    }
    $optionalOffset = $peOffset + 24
    $optionalSize = [int][System.BitConverter]::ToUInt16($Bytes, $peOffset + 20)
    if ($optionalSize -lt 96 -or $optionalOffset + $optionalSize -gt $Bytes.Length) {
        throw "$Label has an invalid PE optional header"
    }
    $magic = [System.BitConverter]::ToUInt16($Bytes, $optionalOffset)
    $dataDirectoryOffset = switch ($magic) {
        0x10b { $optionalOffset + 96 }
        0x20b { $optionalOffset + 112 }
        default { throw "$Label uses an unsupported PE optional-header format" }
    }
    $checksumOffset = $optionalOffset + 64
    $securityDirectoryOffset = $dataDirectoryOffset + (4 * 8)
    if ($securityDirectoryOffset + 8 -gt $optionalOffset + $optionalSize) {
        throw "$Label has no valid Authenticode security directory"
    }
    return [ordered]@{
        checksum_offset = $checksumOffset
        security_directory_offset = $securityDirectoryOffset
        certificate_offset = [int64][System.BitConverter]::ToUInt32(
            $Bytes, $securityDirectoryOffset)
        certificate_size = [int64][System.BitConverter]::ToUInt32(
            $Bytes, $securityDirectoryOffset + 4)
    }
}

function New-SignedCms {
    $cmsType = [System.Type]::GetType(
        "System.Security.Cryptography.Pkcs.SignedCms, System.Security.Cryptography.Pkcs",
        $false)
    if ($null -eq $cmsType) {
        [void][System.Reflection.Assembly]::LoadWithPartialName("System.Security")
    }
    try {
        return New-Object System.Security.Cryptography.Pkcs.SignedCms
    }
    catch {
        throw "The platform cannot decode Authenticode PKCS#7 signatures: $($_.Exception.Message)"
    }
}

function Read-DERElement {
    param([byte[]]$Bytes, [int]$Offset, [int]$Limit)
    if ($Offset -lt 0 -or $Limit -gt $Bytes.Length -or $Offset + 2 -gt $Limit) {
        throw "RFC 3161 token contains a truncated DER element"
    }
    $tag = [int]$Bytes[$Offset]
    $cursor = $Offset + 1
    $firstLength = [int]$Bytes[$cursor]
    $cursor++
    if (($firstLength -band 0x80) -eq 0) {
        $length = [int64]$firstLength
    }
    else {
        $lengthOctets = $firstLength -band 0x7f
        if ($lengthOctets -lt 1 -or $lengthOctets -gt 4 -or
            $cursor + $lengthOctets -gt $Limit -or $Bytes[$cursor] -eq 0) {
            throw "RFC 3161 token uses an invalid DER length"
        }
        $length = [int64]0
        for ($index = 0; $index -lt $lengthOctets; $index++) {
            $length = ($length * 256) + [int]$Bytes[$cursor + $index]
        }
        if ($length -lt 128) {
            throw "RFC 3161 token uses a non-canonical DER length"
        }
        $cursor += $lengthOctets
    }
    $next = [int64]$cursor + $length
    if ($next -gt $Limit -or $next -gt [int]::MaxValue) {
        throw "RFC 3161 token contains an oversized DER element"
    }
    return [pscustomobject]@{
        tag = $tag
        content_offset = $cursor
        content_length = [int]$length
        next_offset = [int]$next
    }
}

function Get-HexRange {
    param([byte[]]$Bytes, [int]$Offset, [int]$Length)
    if ($Offset -lt 0 -or $Length -lt 0 -or $Offset + $Length -gt $Bytes.Length) {
        throw "Cannot hash or compare an out-of-range byte sequence"
    }
    if ($Length -eq 0) { return "" }
    return ([System.BitConverter]::ToString($Bytes, $Offset, $Length)).Replace(
        "-", "").ToLowerInvariant()
}

function Get-RFC3161MessageImprint {
    param([byte[]]$TSTInfo, [byte[]]$PrimarySignature)
    $top = Read-DERElement $TSTInfo 0 $TSTInfo.Length
    if ($top.tag -ne 0x30 -or $top.next_offset -ne $TSTInfo.Length) {
        throw "RFC 3161 TSTInfo must be one canonical DER sequence"
    }
    $cursor = $top.content_offset
    $limit = $top.next_offset
    $version = Read-DERElement $TSTInfo $cursor $limit
    $cursor = $version.next_offset
    $policy = Read-DERElement $TSTInfo $cursor $limit
    $cursor = $policy.next_offset
    $messageImprint = Read-DERElement $TSTInfo $cursor $limit
    $cursor = $messageImprint.next_offset
    if ($version.tag -ne 0x02 -or $policy.tag -ne 0x06 -or
        $messageImprint.tag -ne 0x30) {
        throw "RFC 3161 TSTInfo header or MessageImprint is invalid"
    }
    $imprintCursor = $messageImprint.content_offset
    $imprintLimit = $messageImprint.next_offset
    $algorithm = Read-DERElement $TSTInfo $imprintCursor $imprintLimit
    $imprintCursor = $algorithm.next_offset
    $hashedMessage = Read-DERElement $TSTInfo $imprintCursor $imprintLimit
    if ($algorithm.tag -ne 0x30 -or $hashedMessage.tag -ne 0x04 -or
        $hashedMessage.content_length -ne 32 -or
        $hashedMessage.next_offset -ne $imprintLimit) {
        throw "RFC 3161 MessageImprint must contain one SHA-256 digest"
    }
    $algorithmCursor = $algorithm.content_offset
    $algorithmLimit = $algorithm.next_offset
    $algorithmOID = Read-DERElement $TSTInfo $algorithmCursor $algorithmLimit
    $algorithmCursor = $algorithmOID.next_offset
    if ($algorithmOID.tag -ne 0x06 -or
        (Get-HexRange $TSTInfo $algorithmOID.content_offset `
            $algorithmOID.content_length) -cne "608648016503040201") {
        throw "RFC 3161 MessageImprint algorithm is not SHA-256"
    }
    if ($algorithmCursor -lt $algorithmLimit) {
        $parameters = Read-DERElement $TSTInfo $algorithmCursor $algorithmLimit
        if ($parameters.tag -ne 0x05 -or $parameters.content_length -ne 0 -or
            $parameters.next_offset -ne $algorithmLimit) {
            throw "RFC 3161 SHA-256 AlgorithmIdentifier parameters are invalid"
        }
    }
    $expectedImprint = Get-ByteSHA256 $PrimarySignature
    $actualImprint = Get-HexRange $TSTInfo $hashedMessage.content_offset `
        $hashedMessage.content_length
    if ($actualImprint -cne $expectedImprint) {
        throw "RFC 3161 timestamp does not bind the verified Authenticode signature"
    }
    $serialNumber = Read-DERElement $TSTInfo $cursor $limit
    $cursor = $serialNumber.next_offset
    $generalizedTime = Read-DERElement $TSTInfo $cursor $limit
    if ($serialNumber.tag -ne 0x02 -or $generalizedTime.tag -ne 0x18) {
        throw "RFC 3161 TSTInfo serial number or genTime is invalid"
    }
    $timeText = [System.Text.Encoding]::ASCII.GetString(
        $TSTInfo, $generalizedTime.content_offset, $generalizedTime.content_length)
    if ($timeText -notmatch '^\d{14}(?:[\.,]\d{1,7})?Z$') {
        throw "RFC 3161 genTime is not a canonical UTC timestamp"
    }
    $timeText = $timeText.Replace(',', '.')
    $timeFormat = if ($timeText.Contains('.')) {
        "yyyyMMddHHmmss.FFFFFFF'Z'"
    }
    else {
        "yyyyMMddHHmmss'Z'"
    }
    try {
        $timestampValue = [System.DateTime]::ParseExact(
            $timeText, $timeFormat,
            [System.Globalization.CultureInfo]::InvariantCulture,
            [System.Globalization.DateTimeStyles]::None)
        $timestampAt = [System.DateTimeOffset]::new(
            [System.DateTime]::SpecifyKind($timestampValue, [System.DateTimeKind]::Utc))
    }
    catch {
        throw "RFC 3161 genTime cannot be parsed"
    }
    return [ordered]@{
        timestamp_digest_algorithm = "sha256"
        timestamp_at_utc = $timestampAt.ToUniversalTime().ToString("o")
    }
}

function Assert-AuthenticodeContentProfile {
    param([object]$CMS)
    if ([string]$CMS.ContentInfo.ContentType.Value -cne
            "1.3.6.1.4.1.311.2.1.4") {
        throw "Stable direct EXE signature does not contain Authenticode indirect data"
    }
    [byte[]]$content = $CMS.ContentInfo.Content
    $top = Read-DERElement $content 0 $content.Length
    if ($top.tag -ne 0x30 -or $top.next_offset -ne $content.Length) {
        throw "Authenticode indirect data must be one canonical DER sequence"
    }
    $cursor = $top.content_offset
    $limit = $top.next_offset
    $data = Read-DERElement $content $cursor $limit
    $cursor = $data.next_offset
    $digestInfo = Read-DERElement $content $cursor $limit
    if ($data.tag -ne 0x30 -or $digestInfo.tag -ne 0x30 -or
        $digestInfo.next_offset -ne $limit) {
        throw "Authenticode indirect data has an invalid digest structure"
    }
    $digestCursor = $digestInfo.content_offset
    $digestLimit = $digestInfo.next_offset
    $algorithm = Read-DERElement $content $digestCursor $digestLimit
    $digestCursor = $algorithm.next_offset
    $fileDigest = Read-DERElement $content $digestCursor $digestLimit
    if ($algorithm.tag -ne 0x30 -or $fileDigest.tag -ne 0x04 -or
        $fileDigest.content_length -ne 32 -or
        $fileDigest.next_offset -ne $digestLimit) {
        throw "Authenticode file digest must contain one SHA-256 value"
    }
    $algorithmCursor = $algorithm.content_offset
    $algorithmLimit = $algorithm.next_offset
    $algorithmOID = Read-DERElement $content $algorithmCursor $algorithmLimit
    $algorithmCursor = $algorithmOID.next_offset
    if ($algorithmOID.tag -ne 0x06 -or
        (Get-HexRange $content $algorithmOID.content_offset `
            $algorithmOID.content_length) -cne "608648016503040201") {
        throw "Authenticode file digest algorithm is not SHA-256"
    }
    if ($algorithmCursor -lt $algorithmLimit) {
        $parameters = Read-DERElement $content $algorithmCursor $algorithmLimit
        if ($parameters.tag -ne 0x05 -or $parameters.content_length -ne 0 -or
            $parameters.next_offset -ne $algorithmLimit) {
            throw "Authenticode SHA-256 AlgorithmIdentifier parameters are invalid"
        }
    }
}

function Get-AuthenticodeCryptographicProfile {
    param(
        [string]$ArtifactPath,
        [string]$SignerThumbprint,
        [string]$TimestampThumbprint
    )
    [byte[]]$bytes = [System.IO.File]::ReadAllBytes($ArtifactPath)
    $layout = Get-PEAuthenticodeLayout -Bytes $bytes -Label "Signed artifact"
    $certificateOffset = [int64]$layout.certificate_offset
    $certificateSize = [int64]$layout.certificate_size
    if ($certificateOffset -le 0 -or $certificateSize -le 8 -or
        $certificateOffset + $certificateSize -ne $bytes.LongLength) {
        throw "Signed artifact has an invalid Authenticode certificate table"
    }
    $winCertificateLength = [int64][System.BitConverter]::ToUInt32(
        $bytes, [int]$certificateOffset)
    $alignedLength = ($winCertificateLength + 7) -band (-bnot 7)
    if ($winCertificateLength -le 8 -or $alignedLength -ne $certificateSize -or
        [System.BitConverter]::ToUInt16($bytes, [int]$certificateOffset + 4) -ne 0x0200 -or
        [System.BitConverter]::ToUInt16($bytes, [int]$certificateOffset + 6) -ne 0x0002) {
        throw "Stable direct EXE must contain exactly one PKCS#7 Authenticode signature"
    }
    [byte[]]$pkcs7 = New-Object byte[] ([int]$winCertificateLength - 8)
    [System.Array]::Copy(
        $bytes, [int]$certificateOffset + 8, $pkcs7, 0, $pkcs7.Length)
    $cms = New-SignedCms
    try {
        $cms.Decode($pkcs7)
        $cms.CheckSignature($true)
    }
    catch {
        throw "Authenticode PKCS#7 signature cannot be decoded or verified: $($_.Exception.Message)"
    }
    $signers = @($cms.SignerInfos)
    if ($signers.Count -ne 1 -or $null -eq $signers[0].Certificate -or
        (Normalize-Thumbprint $signers[0].Certificate.Thumbprint) -cne
            (Normalize-Thumbprint $SignerThumbprint) -or
        [string]$signers[0].DigestAlgorithm.Value -cne "2.16.840.1.101.3.4.2.1") {
        throw "Stable direct EXE must use one SHA-256 signature from the approved signer"
    }
    Assert-AuthenticodeContentProfile -CMS $cms
    $rfc3161 = @($signers[0].UnsignedAttributes | Where-Object {
            [string]$_.Oid.Value -ceq "1.3.6.1.4.1.311.3.3.1"
        })
    $legacyCountersignatures = @($signers[0].UnsignedAttributes | Where-Object {
            [string]$_.Oid.Value -ceq "1.2.840.113549.1.9.6"
        })
    if ($rfc3161.Count -ne 1 -or $rfc3161[0].Values.Count -ne 1 -or
        $legacyCountersignatures.Count -ne 0) {
        throw "Stable direct EXE must use exactly one RFC 3161 timestamp and no legacy countersignature"
    }
    $timestampCms = New-SignedCms
    try {
        $timestampCms.Decode($rfc3161[0].Values[0].RawData)
        $timestampCms.CheckSignature($true)
    }
    catch {
        throw "RFC 3161 timestamp token cannot be decoded or verified: $($_.Exception.Message)"
    }
    $timestampSigners = @($timestampCms.SignerInfos)
    if ([string]$timestampCms.ContentInfo.ContentType.Value -cne
            "1.2.840.113549.1.9.16.1.4" -or
        $timestampSigners.Count -ne 1 -or
        $null -eq $timestampSigners[0].Certificate -or
        (Normalize-Thumbprint $timestampSigners[0].Certificate.Thumbprint) -cne
            (Normalize-Thumbprint $TimestampThumbprint) -or
        [string]$timestampSigners[0].DigestAlgorithm.Value -cne
            "2.16.840.1.101.3.4.2.1") {
        throw "Stable direct EXE RFC 3161 token must use SHA-256 and the verified timestamp signer"
    }
    $timestampProfile = Get-RFC3161MessageImprint `
        -TSTInfo $timestampCms.ContentInfo.Content `
        -PrimarySignature $signers[0].GetSignature()
    return [ordered]@{
        signature_digest_algorithm = "sha256"
        timestamp_protocol = "rfc3161"
        timestamp_digest_algorithm = [string]$timestampProfile.timestamp_digest_algorithm
        timestamp_at_utc = [string]$timestampProfile.timestamp_at_utc
    }
}

function Assert-AuthenticodePayloadBinding {
    param([string]$PayloadPath, [string]$ArtifactPath)
    [byte[]]$payload = [System.IO.File]::ReadAllBytes($PayloadPath)
    [byte[]]$artifact = [System.IO.File]::ReadAllBytes($ArtifactPath)
    $payloadLayout = Get-PEAuthenticodeLayout -Bytes $payload -Label "Unsigned payload"
    $artifactLayout = Get-PEAuthenticodeLayout -Bytes $artifact -Label "Signed artifact"
    if ($payloadLayout.certificate_offset -ne 0 -or $payloadLayout.certificate_size -ne 0) {
        throw "The reproducible Store payload must be unsigned before the signing handoff"
    }
    $certificateOffset = [int64]$artifactLayout.certificate_offset
    $certificateSize = [int64]$artifactLayout.certificate_size
    if ($certificateOffset -lt $payload.LongLength -or
        $certificateOffset -gt $payload.LongLength + 7 -or $certificateSize -le 0 -or
        $certificateOffset + $certificateSize -ne $artifact.LongLength) {
        throw "Signed artifact certificate table is not an append-only Authenticode change"
    }
    for ($index = $payload.Length; $index -lt [int]$certificateOffset; $index++) {
        if ($artifact[$index] -ne 0) {
            throw "Signed artifact contains non-zero bytes before its Authenticode certificate table"
        }
    }
    [byte[]]$normalizedPayload = New-Object byte[] ([int]$certificateOffset)
    [byte[]]$normalizedArtifact = New-Object byte[] ([int]$certificateOffset)
    [System.Array]::Copy($payload, 0, $normalizedPayload, 0, $payload.Length)
    [System.Array]::Copy($artifact, 0, $normalizedArtifact, 0, [int]$certificateOffset)
    foreach ($layoutAndBytes in @(
            @($payloadLayout, $normalizedPayload),
            @($artifactLayout, $normalizedArtifact)
        )) {
        $layout = $layoutAndBytes[0]
        [byte[]]$bytes = $layoutAndBytes[1]
        for ($index = 0; $index -lt 4; $index++) {
            $bytes[[int]$layout.checksum_offset + $index] = 0
        }
        for ($index = 0; $index -lt 8; $index++) {
            $bytes[[int]$layout.security_directory_offset + $index] = 0
        }
    }
    if ((Get-ByteSHA256 $normalizedPayload) -cne (Get-ByteSHA256 $normalizedArtifact)) {
        throw "Signed artifact does not preserve the exact pre-sign PE payload"
    }
}

function Assert-BooleanField {
    param([object]$Document, [string]$Name)
    $property = $Document.PSObject.Properties[$Name]
    if ($null -eq $property -or $property.Value -isnot [bool]) {
        throw "Direct EXE evidence field must be a JSON boolean: $Name"
    }
}

$repositoryRoot = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$repositoryPrefix = $repositoryRoot.TrimEnd('\', '/') +
    [System.IO.Path]::DirectorySeparatorChar
$outputRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputDirectory))
if (-not $outputRoot.StartsWith($repositoryPrefix,
        [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Direct EXE output directory must remain inside the repository"
}
$payloadPath = Join-Path $outputRoot "TraverseBoard.exe"
$metadataPath = Join-Path $outputRoot "release-metadata.json"
$directPath = Join-Path $outputRoot "TraverseBoard.direct.exe"
$requestPath = Join-Path $outputRoot "direct-exe-signing-request.json"
$handoffPath = Join-Path $outputRoot "direct-exe-signing-handoff.json"
$evidencePath = Join-Path $outputRoot "direct-exe-signing.json"
$publicSumsPath = Join-Path $outputRoot "SHA256SUMS"
$sbomPath = Join-Path $outputRoot "sbom.json"
$noticePath = Join-Path $outputRoot "NOTICE"
$requiredInputs = @($payloadPath, $metadataPath)
if (-not $PrepareSigningRequest) {
    $requiredInputs += @($sbomPath, $noticePath)
}
foreach ($required in $requiredInputs) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Direct EXE staging input is missing: $required"
    }
}
$metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
$version = [string]$metadata.app_version
$revision = [string]$metadata.revision
$payloadHash = Get-SHA256 $payloadPath
if ([string]$metadata.protocol_version -cne "portable_release_metadata.v1" -or
    [string]$metadata.binary_name -cne "TraverseBoard.exe" -or
    [string]$metadata.sha256 -cne $payloadHash -or
    $revision -notmatch '^[0-9a-f]{40}$' -or
    $version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$') {
    throw "Direct EXE staging input does not match release metadata"
}
$sourceDateEpochProperty = $metadata.PSObject.Properties["source_date_epoch"]
$sourceDateEpoch = [int64]0
if ($null -eq $sourceDateEpochProperty -or
    $sourceDateEpochProperty.Value -is [string] -or
    -not [int64]::TryParse(
        [string]$sourceDateEpochProperty.Value,
        [System.Globalization.NumberStyles]::None,
        [System.Globalization.CultureInfo]::InvariantCulture,
        [ref]$sourceDateEpoch) -or
    $sourceDateEpoch -le 0 -or
    $sourceDateEpoch -gt
        [System.DateTimeOffset]::UtcNow.AddMinutes(5).ToUnixTimeSeconds()) {
    throw "Direct EXE release metadata has an invalid source_date_epoch"
}
if (-not [string]::IsNullOrWhiteSpace($ExpectedVersion) -and
    $version -cne $ExpectedVersion) {
    throw "Direct EXE release version differs from the expected version"
}
if (-not [string]::IsNullOrWhiteSpace($ExpectedRevision) -and
    $revision -cne $ExpectedRevision) {
    throw "Direct EXE release revision differs from the expected revision"
}

if ($VerifyOnly) {
    if (-not (Test-Path -LiteralPath $directPath -PathType Leaf) -or
        -not (Test-Path -LiteralPath $evidencePath -PathType Leaf)) {
        throw "Staged direct EXE or its signing evidence is missing"
    }
    $evidence = Get-Content -LiteralPath $evidencePath -Raw | ConvertFrom-Json
    foreach ($field in @("trusted_release", "prerelease", "payload_binding_verified",
            "timestamp_present", "signtool_verified")) {
        Assert-BooleanField -Document $evidence -Name $field
    }
    if ([string]$evidence.protocol_version -cne "direct_exe_signing.v1" -or
        [string]$evidence.release_version -cne $version -or
        [string]$evidence.release_revision -cne $revision -or
        [string]$evidence.payload_sha256 -cne $payloadHash -or
        [string]$evidence.public_artifact_name -cne "TraverseBoard.exe" -or
        [string]$evidence.staged_artifact_name -cne "TraverseBoard.direct.exe" -or
        [string]$evidence.artifact_sha256 -cne (Get-SHA256 $directPath) -or
        -not $evidence.payload_binding_verified) {
        throw "Direct EXE signing evidence differs from the staged artifact or payload"
    }
    $Channel = if ($evidence.trusted_release) { "stable" } else { "prerelease" }
}
elseif ($Channel -ceq "auto") {
    throw "Direct EXE creation requires an explicit stable or prerelease channel"
}

$isPrereleaseVersion = $version -match '^v[0-9]+\.[0-9]+\.[0-9]+-'
if (($Channel -ceq "stable" -and $isPrereleaseVersion) -or
    ($Channel -ceq "prerelease" -and -not $isPrereleaseVersion)) {
    throw "Direct EXE channel differs from the SemVer prerelease component"
}

if ($PrepareSigningRequest) {
    if ($VerifyOnly -or $Channel -cne "stable" -or
        -not [string]::IsNullOrWhiteSpace($SignedArtifactPath) -or
        -not [string]::IsNullOrWhiteSpace($SigningRequestPath) -or
        -not [string]::IsNullOrWhiteSpace($SigningHandoffPath) -or
        [string]::IsNullOrWhiteSpace($ExpectedSignerSubject) -or
        (Normalize-Thumbprint $ExpectedSignerThumbprint) -notmatch '^[0-9A-F]{40,64}$') {
        throw "Signing-request preparation requires only a stable version and approved signer"
    }
    $request = [ordered]@{
        protocol_version = "direct_exe_signing_request.v1"
        release_version = $version
        release_revision = $revision
        payload_name = "TraverseBoard.exe"
        payload_sha256 = $payloadHash
        public_artifact_name = "TraverseBoard.exe"
        required_signer_subject = $ExpectedSignerSubject
        required_signer_thumbprint = Normalize-Thumbprint $ExpectedSignerThumbprint
        signature_digest_algorithm = "sha256"
        timestamp_protocol = "rfc3161"
        timestamp_digest_algorithm = "sha256"
        required_payload_binding = "pe_authenticode_normalized.v1"
    }
    [System.IO.File]::WriteAllText(
        $requestPath,
        (($request | ConvertTo-Json -Depth 4) + [System.Environment]::NewLine),
        [System.Text.UTF8Encoding]::new($false))
    Write-Output "direct_exe_signing_request: $requestPath"
    Write-Output "direct_exe_signing_request_sha256: $(Get-SHA256 $requestPath)"
    Write-Output "direct_exe_payload_sha256: $payloadHash"
    return
}

if ($Channel -ceq "stable") {
    if ($VerifyOnly) {
        $candidatePath = $directPath
        $requestFile = $requestPath
        $handoffFile = $handoffPath
    }
    else {
        if ([string]::IsNullOrWhiteSpace($SignedArtifactPath) -or
            [string]::IsNullOrWhiteSpace($SigningRequestPath) -or
            [string]::IsNullOrWhiteSpace($SigningHandoffPath)) {
            throw "Stable direct EXE staging requires the signing request, signed artifact, and handoff"
        }
        $candidatePath = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $SignedArtifactPath))
        $requestFile = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $SigningRequestPath))
        $handoffFile = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $SigningHandoffPath))
        foreach ($externalInput in @($candidatePath, $requestFile, $handoffFile)) {
            if (-not $externalInput.StartsWith($repositoryPrefix,
                    [System.StringComparison]::OrdinalIgnoreCase) -or
                -not (Test-Path -LiteralPath $externalInput -PathType Leaf)) {
                throw "Signing handoff inputs must be regular files inside the repository"
            }
        }
        $request = Get-Content -LiteralPath $requestFile -Raw | ConvertFrom-Json
        $handoff = Get-Content -LiteralPath $handoffFile -Raw | ConvertFrom-Json
    }
    if ($VerifyOnly) {
        foreach ($retainedInput in @($requestFile, $handoffFile)) {
            if (-not (Test-Path -LiteralPath $retainedInput -PathType Leaf)) {
                throw "Stable verification requires the retained signing request and handoff"
            }
        }
        $request = Get-Content -LiteralPath $requestFile -Raw | ConvertFrom-Json
        $handoff = Get-Content -LiteralPath $handoffFile -Raw | ConvertFrom-Json
    }
    if ([string]::IsNullOrWhiteSpace($ExpectedSignerSubject) -or
        (Normalize-Thumbprint $ExpectedSignerThumbprint) -notmatch '^[0-9A-F]{40,64}$') {
        throw "Stable verification requires the expected signer Subject and thumbprint"
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $candidatePath
    if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or
        [string]$signature.SignatureType -cne "Authenticode" -or
        $null -eq $signature.SignerCertificate -or
        $null -eq $signature.TimeStamperCertificate) {
        throw "Stable direct EXE requires a valid Authenticode signature and trusted timestamp"
    }
    $signerSubject = [string]$signature.SignerCertificate.Subject
    $signerThumbprint = Normalize-Thumbprint $signature.SignerCertificate.Thumbprint
    $timestampSubject = [string]$signature.TimeStamperCertificate.Subject
    $timestampThumbprint = Normalize-Thumbprint $signature.TimeStamperCertificate.Thumbprint
    if ($signerSubject -cne $ExpectedSignerSubject -or
        $signerThumbprint -cne (Normalize-Thumbprint $ExpectedSignerThumbprint)) {
        throw "Stable direct EXE signer differs from the protected release identity"
    }
    $cryptographicProfile = Get-AuthenticodeCryptographicProfile `
        -ArtifactPath $candidatePath `
        -SignerThumbprint $signerThumbprint `
        -TimestampThumbprint $timestampThumbprint
    $signtool = Find-SDKTool "signtool"
    if ($null -eq $signtool) { throw "signtool.exe was not found in the Windows SDK" }
    & $signtool verify /pa /all /v $candidatePath
    if ($LASTEXITCODE -ne 0) { throw "Stable direct EXE failed signtool policy verification" }
    Assert-AuthenticodePayloadBinding -PayloadPath $payloadPath -ArtifactPath $candidatePath
    $artifactHash = Get-SHA256 $candidatePath
    if ($artifactHash -ceq $payloadHash) {
        throw "Stable direct EXE did not acquire an Authenticode signature"
    }
    if ([string]$request.protocol_version -cne "direct_exe_signing_request.v1" -or
        [string]$request.release_version -cne $version -or
        [string]$request.release_revision -cne $revision -or
        [string]$request.payload_name -cne "TraverseBoard.exe" -or
        [string]$request.payload_sha256 -cne $payloadHash -or
        [string]$request.public_artifact_name -cne "TraverseBoard.exe" -or
        [string]$request.required_signer_subject -cne $ExpectedSignerSubject -or
        (Normalize-Thumbprint ([string]$request.required_signer_thumbprint)) -cne
            (Normalize-Thumbprint $ExpectedSignerThumbprint) -or
        [string]$request.signature_digest_algorithm -cne "sha256" -or
        [string]$request.timestamp_protocol -cne "rfc3161" -or
        [string]$request.timestamp_digest_algorithm -cne "sha256" -or
        [string]$request.required_payload_binding -cne
            "pe_authenticode_normalized.v1") {
        throw "Signing request does not bind the approved stable payload policy"
    }
    $requestHash = Get-SHA256 $requestFile
    $signedAt = [System.DateTimeOffset]::MinValue
    if ([string]$handoff.protocol_version -cne "direct_exe_signing_handoff.v1" -or
        [string]$handoff.release_version -cne $version -or
        [string]$handoff.release_revision -cne $revision -or
        [string]$handoff.payload_sha256 -cne $payloadHash -or
        [string]$handoff.signed_artifact_sha256 -cne $artifactHash -or
        [string]$handoff.signing_request_sha256 -cne $requestHash -or
        [string]$handoff.signer_subject -cne $signerSubject -or
        (Normalize-Thumbprint ([string]$handoff.signer_thumbprint)) -cne $signerThumbprint -or
        [string]$handoff.signature_digest_algorithm -cne
            [string]$cryptographicProfile.signature_digest_algorithm -or
        [string]$handoff.timestamp_protocol -cne
            [string]$cryptographicProfile.timestamp_protocol -or
        [string]$handoff.timestamp_digest_algorithm -cne
            [string]$cryptographicProfile.timestamp_digest_algorithm -or
        [string]$handoff.timestamp_authority_subject -cne $timestampSubject -or
        (Normalize-Thumbprint ([string]$handoff.timestamp_authority_thumbprint)) -cne
            $timestampThumbprint -or
        [string]::IsNullOrWhiteSpace([string]$handoff.signing_service) -or
        [string]::IsNullOrWhiteSpace([string]$handoff.service_operation_id) -or
        -not [System.DateTimeOffset]::TryParseExact(
            [string]$handoff.signed_at_utc, "o",
            [System.Globalization.CultureInfo]::InvariantCulture,
            [System.Globalization.DateTimeStyles]::RoundtripKind, [ref]$signedAt) -or
        $signedAt.ToUniversalTime().ToString("o") -cne
            [string]$cryptographicProfile.timestamp_at_utc) {
        throw "External signing handoff does not bind the verified RFC 3161 artifact"
    }
    $sourceTime = [System.DateTimeOffset]::FromUnixTimeSeconds($sourceDateEpoch)
    if ($signedAt -lt $sourceTime -or
        $signedAt -gt [System.DateTimeOffset]::UtcNow.AddMinutes(5)) {
        throw "External signing handoff timestamp is outside the release interval"
    }
    $handoffHash = Get-SHA256 $handoffFile
    if (-not $VerifyOnly) {
        Copy-Item -LiteralPath $requestFile -Destination $requestPath -Force
        Copy-Item -LiteralPath $handoffFile -Destination $handoffPath -Force
        Copy-Item -LiteralPath $candidatePath -Destination $directPath -Force
        $evidence = [ordered]@{
            protocol_version = "direct_exe_signing.v1"
            release_version = $version
            release_revision = $revision
            payload_name = "TraverseBoard.exe"
            payload_sha256 = $payloadHash
            public_artifact_name = "TraverseBoard.exe"
            staged_artifact_name = "TraverseBoard.direct.exe"
            artifact_sha256 = $artifactHash
            payload_binding = "pe_authenticode_normalized.v1"
            payload_binding_verified = $true
            trusted_release = $true
            prerelease = $false
            signature_status = "Valid"
            signer_subject = $signerSubject
            signer_thumbprint = $signerThumbprint
            signature_digest_algorithm = [string]$cryptographicProfile.signature_digest_algorithm
            timestamp_present = $true
            timestamp_protocol = [string]$cryptographicProfile.timestamp_protocol
            timestamp_digest_algorithm = [string]$cryptographicProfile.timestamp_digest_algorithm
            timestamp_authority_subject = $timestampSubject
            timestamp_authority_thumbprint = $timestampThumbprint
            signtool_verified = $true
            signing_service = [string]$handoff.signing_service
            service_operation_id = [string]$handoff.service_operation_id
            signed_at_utc = $signedAt.ToUniversalTime().ToString("o")
            signing_request_name = "direct-exe-signing-request.json"
            signing_request_sha256 = $requestHash
            signing_handoff_name = "direct-exe-signing-handoff.json"
            signing_handoff_sha256 = $handoffHash
        }
    }
    elseif (-not $evidence.trusted_release -or $evidence.prerelease -or
        -not $evidence.timestamp_present -or -not $evidence.signtool_verified -or
        [string]$evidence.signature_status -cne "Valid" -or
        [string]$evidence.payload_binding -cne "pe_authenticode_normalized.v1" -or
        [string]$evidence.signer_subject -cne $signerSubject -or
        (Normalize-Thumbprint ([string]$evidence.signer_thumbprint)) -cne $signerThumbprint -or
        [string]$evidence.signature_digest_algorithm -cne
            [string]$cryptographicProfile.signature_digest_algorithm -or
        [string]$evidence.timestamp_protocol -cne
            [string]$cryptographicProfile.timestamp_protocol -or
        [string]$evidence.timestamp_digest_algorithm -cne
            [string]$cryptographicProfile.timestamp_digest_algorithm -or
        [string]$evidence.timestamp_authority_subject -cne $timestampSubject -or
        (Normalize-Thumbprint ([string]$evidence.timestamp_authority_thumbprint)) -cne
            $timestampThumbprint -or
        [string]$evidence.signing_service -cne [string]$handoff.signing_service -or
        [string]$evidence.service_operation_id -cne [string]$handoff.service_operation_id -or
        [string]$evidence.signed_at_utc -cne $signedAt.ToUniversalTime().ToString("o") -or
        [string]$evidence.signing_request_name -cne
            "direct-exe-signing-request.json" -or
        [string]$evidence.signing_request_sha256 -cne $requestHash -or
        [string]$evidence.signing_handoff_name -cne
            "direct-exe-signing-handoff.json" -or
        [string]$evidence.signing_handoff_sha256 -cne $handoffHash) {
        throw "Stable direct EXE evidence does not match the verified signing identity"
    }
}
else {
    $payloadSignature = Get-AuthenticodeSignature -LiteralPath $payloadPath
    if ($payloadSignature.Status -ne [System.Management.Automation.SignatureStatus]::NotSigned) {
        throw "Unsigned prerelease staging refuses an unverified or unexpectedly signed payload"
    }
    if (-not $VerifyOnly) {
        if (-not [string]::IsNullOrWhiteSpace($SignedArtifactPath) -or
            -not [string]::IsNullOrWhiteSpace($SigningRequestPath) -or
            -not [string]::IsNullOrWhiteSpace($SigningHandoffPath)) {
            throw "Prerelease staging cannot consume a signing handoff"
        }
        if (Test-Path -LiteralPath $requestPath -PathType Leaf) {
            Remove-Item -LiteralPath $requestPath -Force
        }
        if (Test-Path -LiteralPath $handoffPath -PathType Leaf) {
            Remove-Item -LiteralPath $handoffPath -Force
        }
        Copy-Item -LiteralPath $payloadPath -Destination $directPath -Force
        $evidence = [ordered]@{
            protocol_version = "direct_exe_signing.v1"
            release_version = $version
            release_revision = $revision
            payload_name = "TraverseBoard.exe"
            payload_sha256 = $payloadHash
            public_artifact_name = "TraverseBoard.exe"
            staged_artifact_name = "TraverseBoard.direct.exe"
            artifact_sha256 = $payloadHash
            payload_binding = "byte_identical.v1"
            payload_binding_verified = $true
            trusted_release = $false
            prerelease = $true
            signature_status = "NotSigned"
            signer_subject = ""
            signer_thumbprint = ""
            signature_digest_algorithm = ""
            timestamp_present = $false
            timestamp_protocol = ""
            timestamp_digest_algorithm = ""
            timestamp_authority_subject = ""
            timestamp_authority_thumbprint = ""
            signtool_verified = $false
            signing_service = ""
            service_operation_id = ""
            signed_at_utc = ""
            signing_request_name = ""
            signing_request_sha256 = ""
            signing_handoff_name = ""
            signing_handoff_sha256 = ""
        }
    }
    elseif ($evidence.trusted_release -or -not $evidence.prerelease -or
        $evidence.timestamp_present -or $evidence.signtool_verified -or
        [string]$evidence.signature_status -cne "NotSigned" -or
        [string]$evidence.payload_binding -cne "byte_identical.v1" -or
        -not [string]::IsNullOrEmpty([string]$evidence.signature_digest_algorithm) -or
        -not [string]::IsNullOrEmpty([string]$evidence.timestamp_digest_algorithm) -or
        -not [string]::IsNullOrEmpty([string]$evidence.signing_request_name) -or
        -not [string]::IsNullOrEmpty([string]$evidence.signing_request_sha256) -or
        -not [string]::IsNullOrEmpty([string]$evidence.signing_handoff_name) -or
        -not [string]::IsNullOrEmpty([string]$evidence.signing_handoff_sha256) -or
        (Get-SHA256 $directPath) -cne $payloadHash) {
        throw "Prerelease direct EXE evidence makes an unsupported trust claim"
    }
}

if (-not $VerifyOnly) {
    [System.IO.File]::WriteAllText(
        $evidencePath,
        (($evidence | ConvertTo-Json -Depth 5) + [System.Environment]::NewLine),
        [System.Text.UTF8Encoding]::new($false))
}
$evidenceHash = Get-SHA256 $evidencePath
$directHash = Get-SHA256 $directPath
$expectedPublicSums = @(
    "$directHash  TraverseBoard.exe",
    "$evidenceHash  direct-exe-signing.json",
    "$(Get-SHA256 $sbomPath)  sbom.json",
    "$(Get-SHA256 $noticePath)  NOTICE",
    "$(Get-SHA256 $metadataPath)  release-metadata.json"
)
if ($evidence.trusted_release) {
    $expectedPublicSums = @(
        $expectedPublicSums[0],
        $expectedPublicSums[1],
        "$(Get-SHA256 $requestPath)  direct-exe-signing-request.json",
        "$(Get-SHA256 $handoffPath)  direct-exe-signing-handoff.json"
    ) + @($expectedPublicSums[2..($expectedPublicSums.Count - 1)])
}
if ($VerifyOnly) {
    if (-not (Test-Path -LiteralPath $publicSumsPath -PathType Leaf)) {
        throw "Public SHA256SUMS is missing"
    }
    $actualPublicSums = @([System.IO.File]::ReadAllLines($publicSumsPath) |
        Where-Object { $_.Length -ne 0 })
    if ($actualPublicSums.Count -ne $expectedPublicSums.Count) {
        throw "Public SHA256SUMS entry count differs from the direct EXE contract"
    }
    for ($index = 0; $index -lt $expectedPublicSums.Count; $index++) {
        if ($actualPublicSums[$index] -cne $expectedPublicSums[$index]) {
            throw "Public SHA256SUMS differs at entry $index"
        }
    }
}
else {
    [System.IO.File]::WriteAllText(
        $publicSumsPath,
        (($expectedPublicSums -join [System.Environment]::NewLine) +
            [System.Environment]::NewLine),
        [System.Text.UTF8Encoding]::new($false))
}

Write-Output "direct_exe: $directPath"
Write-Output "direct_exe_sha256: $directHash"
Write-Output "direct_exe_payload_sha256: $payloadHash"
Write-Output "direct_exe_trusted_release: $($evidence.trusted_release)"
Write-Output "direct_exe_evidence: $evidencePath"
