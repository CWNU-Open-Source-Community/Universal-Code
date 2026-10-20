param(
    [string]$SourcePath = "assets/branding/traverse-board-mark.png"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot

function Resolve-RepositoryFile {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $candidate = if ([System.IO.Path]::IsPathRooted($Path)) {
        $Path
    } else {
        Join-Path $repoRoot $Path
    }
    $resolved = (Resolve-Path -LiteralPath $candidate).Path
    $rootPrefix = $repoRoot.TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    ) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Brand source must stay inside the repository: $resolved"
    }
    return $resolved
}

function New-RoundedRectanglePath {
    param(
        [Parameter(Mandatory = $true)]
        [int]$Size
    )

    $radius = [single]($Size * 0.18)
    $diameter = [single]($radius * 2)
    $extent = [single]($Size - 1)
    $path = [System.Drawing.Drawing2D.GraphicsPath]::new()
    $path.AddArc(0, 0, $diameter, $diameter, 180, 90)
    $path.AddArc($extent - $diameter, 0, $diameter, $diameter, 270, 90)
    $path.AddArc($extent - $diameter, $extent - $diameter, $diameter, $diameter, 0, 90)
    $path.AddArc(0, $extent - $diameter, $diameter, $diameter, 90, 90)
    $path.CloseFigure()
    return $path
}

function New-PlatformIconBitmap {
    param(
        [Parameter(Mandatory = $true)]
        [System.Drawing.Image]$Source,

        [Parameter(Mandatory = $true)]
        [int]$Size
    )

    # Supersampling keeps the rounded alpha edge stable at favicon/taskbar sizes.
    $scale = if ($Size -le 256) { 4 } else { 2 }
    $workingSize = $Size * $scale
    $working = [System.Drawing.Bitmap]::new(
        $workingSize,
        $workingSize,
        [System.Drawing.Imaging.PixelFormat]::Format32bppArgb
    )
    $working.SetResolution(96, 96)
    $graphics = [System.Drawing.Graphics]::FromImage($working)
    $clip = $null
    try {
        $graphics.Clear([System.Drawing.Color]::Transparent)
        $graphics.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
        $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
        $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
        $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
        $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $clip = New-RoundedRectanglePath -Size $workingSize
        $graphics.SetClip($clip)
        $graphics.DrawImage($Source, 0, 0, $workingSize, $workingSize)
    } finally {
        if ($null -ne $clip) {
            $clip.Dispose()
        }
        $graphics.Dispose()
    }

    if ($scale -eq 1) {
        return $working
    }

    $output = [System.Drawing.Bitmap]::new(
        $Size,
        $Size,
        [System.Drawing.Imaging.PixelFormat]::Format32bppArgb
    )
    $output.SetResolution(96, 96)
    $graphics = [System.Drawing.Graphics]::FromImage($output)
    try {
        $graphics.Clear([System.Drawing.Color]::Transparent)
        $graphics.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
        $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
        $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
        $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
        $graphics.DrawImage($working, 0, 0, $Size, $Size)
    } finally {
        $graphics.Dispose()
        $working.Dispose()
    }
    return $output
}

function Convert-BitmapToPngBytes {
    param(
        [Parameter(Mandatory = $true)]
        [System.Drawing.Bitmap]$Bitmap
    )

    $stream = [System.IO.MemoryStream]::new()
    try {
        $Bitmap.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png)
        return ,$stream.ToArray()
    } finally {
        $stream.Dispose()
    }
}

function Convert-PngBytesToIndexed8 {
    param(
        [Parameter(Mandatory = $true)]
        [byte[]]$Bytes
    )

    # WPF provides a compact, adaptive 8-bit palette, but its format converter
    # flattens transparent pixels. Preserve its colour indices and restore the
    # rounded-corner mask explicitly in a GDI+ indexed bitmap. Keeping a single
    # binary transparency entry is sufficient at 600px (Windows scales this
    # 400% resource down to 150 logical pixels) and leaves 255 adaptive colours.
    Add-Type -AssemblyName PresentationCore
    $inputStream = [System.IO.MemoryStream]::new($Bytes, $false)
    try {
        $decoder = [System.Windows.Media.Imaging.PngBitmapDecoder]::new(
            $inputStream,
            [System.Windows.Media.Imaging.BitmapCreateOptions]::PreservePixelFormat,
            [System.Windows.Media.Imaging.BitmapCacheOption]::OnLoad
        )
        $frame = $decoder.Frames[0]
        $rgba = [System.Windows.Media.Imaging.FormatConvertedBitmap]::new(
            $frame,
            [System.Windows.Media.PixelFormats]::Bgra32,
            $null,
            0
        )
        $converted = [System.Windows.Media.Imaging.FormatConvertedBitmap]::new(
            $frame,
            [System.Windows.Media.PixelFormats]::Indexed8,
            $null,
            0
        )

        $width = $frame.PixelWidth
        $height = $frame.PixelHeight
        $pixelCount = $width * $height
        $indexedPixels = [byte[]]::new($pixelCount)
        $converted.CopyPixels($indexedPixels, $width, 0)
        $rgbaStride = $width * 4
        $rgbaPixels = [byte[]]::new($rgbaStride * $height)
        $rgba.CopyPixels($rgbaPixels, $rgbaStride, 0)

        $transparentPaletteIndex = -1
        for ($index = 0; $index -lt $pixelCount; $index++) {
            if ($rgbaPixels[($index * 4) + 3] -lt 128) {
                $transparentPaletteIndex = [int]$indexedPixels[$index]
                break
            }
        }
        if ($transparentPaletteIndex -lt 0) {
            throw "Indexed Windows icon has no transparent pixels to preserve."
        }

        $colors = $converted.Palette.Colors
        if ($colors.Count -lt 2 -or $transparentPaletteIndex -ge $colors.Count) {
            throw "WPF returned an invalid indexed palette."
        }
        $reservedColor = $colors[$transparentPaletteIndex]
        $replacementIndex = -1
        $replacementDistance = [long]::MaxValue
        for ($index = 0; $index -lt $colors.Count; $index++) {
            if ($index -eq $transparentPaletteIndex) {
                continue
            }
            $candidate = $colors[$index]
            $redDelta = [int]$candidate.R - [int]$reservedColor.R
            $greenDelta = [int]$candidate.G - [int]$reservedColor.G
            $blueDelta = [int]$candidate.B - [int]$reservedColor.B
            $distance = [long]($redDelta * $redDelta) +
                [long]($greenDelta * $greenDelta) +
                [long]($blueDelta * $blueDelta)
            if ($distance -lt $replacementDistance) {
                $replacementDistance = $distance
                $replacementIndex = $index
            }
        }
        if ($replacementIndex -lt 0) {
            throw "Indexed Windows icon does not have a replacement palette colour."
        }

        for ($index = 0; $index -lt $pixelCount; $index++) {
            if ($rgbaPixels[($index * 4) + 3] -lt 128) {
                $indexedPixels[$index] = [byte]$transparentPaletteIndex
            } elseif ([int]$indexedPixels[$index] -eq $transparentPaletteIndex) {
                # The adaptive quantizer may also use the corner's palette slot
                # for opaque artwork. Move those pixels to its nearest colour so
                # the transparent entry cannot punch holes in the artwork.
                $indexedPixels[$index] = [byte]$replacementIndex
            }
        }

        $indexed = [System.Drawing.Bitmap]::new(
            $width,
            $height,
            [System.Drawing.Imaging.PixelFormat]::Format8bppIndexed
        )
        try {
            $indexed.SetResolution(96, 96)
            $palette = $indexed.Palette
            for ($index = 0; $index -lt $palette.Entries.Count; $index++) {
                if ($index -lt $colors.Count) {
                    $color = $colors[$index]
                    $palette.Entries[$index] = [System.Drawing.Color]::FromArgb(
                        255,
                        [int]$color.R,
                        [int]$color.G,
                        [int]$color.B
                    )
                } else {
                    $palette.Entries[$index] = [System.Drawing.Color]::Black
                }
            }
            $palette.Entries[$transparentPaletteIndex] =
                [System.Drawing.Color]::FromArgb(0, 255, 255, 255)
            $indexed.Palette = $palette

            $bitmapData = $indexed.LockBits(
                [System.Drawing.Rectangle]::new(0, 0, $width, $height),
                [System.Drawing.Imaging.ImageLockMode]::WriteOnly,
                [System.Drawing.Imaging.PixelFormat]::Format8bppIndexed
            )
            try {
                for ($row = 0; $row -lt $height; $row++) {
                    $destination = [System.IntPtr]::Add(
                        $bitmapData.Scan0,
                        $row * $bitmapData.Stride
                    )
                    [System.Runtime.InteropServices.Marshal]::Copy(
                        $indexedPixels,
                        $row * $width,
                        $destination,
                        $width
                    )
                }
            } finally {
                $indexed.UnlockBits($bitmapData)
            }

            $outputStream = [System.IO.MemoryStream]::new()
            try {
                $indexed.Save($outputStream, [System.Drawing.Imaging.ImageFormat]::Png)
                return ,$outputStream.ToArray()
            } finally {
                $outputStream.Dispose()
            }
        } finally {
            $indexed.Dispose()
        }
    } finally {
        $inputStream.Dispose()
    }
}

function Write-Bytes {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [byte[]]$Bytes
    )

    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        [System.IO.Directory]::CreateDirectory($directory) | Out-Null
    }
    [System.IO.File]::WriteAllBytes($Path, $Bytes)
}

function Write-PngAsset {
    param(
        [Parameter(Mandatory = $true)]
        [System.Drawing.Image]$Source,

        [Parameter(Mandatory = $true)]
        [int]$Size,

        [Parameter(Mandatory = $true)]
        [string]$RelativePath,

        [switch]$Indexed8
    )

    $bitmap = New-PlatformIconBitmap -Source $Source -Size $Size
    try {
        $bytes = Convert-BitmapToPngBytes -Bitmap $bitmap
        if ($Indexed8) {
            $bytes = Convert-PngBytesToIndexed8 -Bytes $bytes
        }
        if ($RelativePath.Replace("\", "/").StartsWith("packaging/windows/Assets/") -and
            $bytes.Length -ge 204800) {
            throw "Packaged Windows visual asset must be smaller than 204800 bytes: $RelativePath ($($bytes.Length) bytes)"
        }
        Write-Bytes -Path (Join-Path $repoRoot $RelativePath) -Bytes $bytes
        return ,$bytes
    } finally {
        $bitmap.Dispose()
    }
}

function Write-LittleEndianUInt16 {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.BinaryWriter]$Writer,

        [Parameter(Mandatory = $true)]
        [uint16]$Value
    )

    $Writer.Write($Value)
}

function Write-LittleEndianUInt32 {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.BinaryWriter]$Writer,

        [Parameter(Mandatory = $true)]
        [uint32]$Value
    )

    $Writer.Write($Value)
}

function Write-BigEndianUInt32 {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.BinaryWriter]$Writer,

        [Parameter(Mandatory = $true)]
        [uint32]$Value
    )

    $Writer.Write([byte](($Value -shr 24) -band 0xff))
    $Writer.Write([byte](($Value -shr 16) -band 0xff))
    $Writer.Write([byte](($Value -shr 8) -band 0xff))
    $Writer.Write([byte]($Value -band 0xff))
}

function Write-Ascii {
    param(
        [Parameter(Mandatory = $true)]
        [System.IO.BinaryWriter]$Writer,

        [Parameter(Mandatory = $true)]
        [ValidateLength(4, 4)]
        [string]$Value
    )

    $Writer.Write([System.Text.Encoding]::ASCII.GetBytes($Value))
}

function New-IcoBytes {
    param(
        [Parameter(Mandatory = $true)]
        [object[]]$Entries
    )

    $stream = [System.IO.MemoryStream]::new()
    $writer = [System.IO.BinaryWriter]::new($stream)
    try {
        Write-LittleEndianUInt16 -Writer $writer -Value 0
        Write-LittleEndianUInt16 -Writer $writer -Value 1
        Write-LittleEndianUInt16 -Writer $writer -Value ([uint16]$Entries.Count)
        $offset = 6 + (16 * $Entries.Count)
        foreach ($entry in $Entries) {
            $encodedSize = if ($entry.Size -eq 256) { 0 } else { $entry.Size }
            $writer.Write([byte]$encodedSize)
            $writer.Write([byte]$encodedSize)
            $writer.Write([byte]0)
            $writer.Write([byte]0)
            Write-LittleEndianUInt16 -Writer $writer -Value 0
            Write-LittleEndianUInt16 -Writer $writer -Value 32
            Write-LittleEndianUInt32 -Writer $writer -Value ([uint32]$entry.Bytes.Length)
            Write-LittleEndianUInt32 -Writer $writer -Value ([uint32]$offset)
            $offset += $entry.Bytes.Length
        }
        foreach ($entry in $Entries) {
            $writer.Write([byte[]]$entry.Bytes)
        }
        $writer.Flush()
        return ,$stream.ToArray()
    } finally {
        $writer.Dispose()
        $stream.Dispose()
    }
}

function New-IcnsBytes {
    param(
        [Parameter(Mandatory = $true)]
        [object[]]$Entries
    )

    $tocLength = 8 + (8 * $Entries.Count)
    $totalLength = 8 + $tocLength
    foreach ($entry in $Entries) {
        $totalLength += 8 + $entry.Bytes.Length
    }

    $stream = [System.IO.MemoryStream]::new()
    $writer = [System.IO.BinaryWriter]::new($stream)
    try {
        Write-Ascii -Writer $writer -Value "icns"
        Write-BigEndianUInt32 -Writer $writer -Value ([uint32]$totalLength)
        Write-Ascii -Writer $writer -Value "TOC "
        Write-BigEndianUInt32 -Writer $writer -Value ([uint32]$tocLength)
        foreach ($entry in $Entries) {
            Write-Ascii -Writer $writer -Value $entry.Type
            Write-BigEndianUInt32 -Writer $writer -Value ([uint32](8 + $entry.Bytes.Length))
        }
        foreach ($entry in $Entries) {
            Write-Ascii -Writer $writer -Value $entry.Type
            Write-BigEndianUInt32 -Writer $writer -Value ([uint32](8 + $entry.Bytes.Length))
            $writer.Write([byte[]]$entry.Bytes)
        }
        $writer.Flush()
        return ,$stream.ToArray()
    } finally {
        $writer.Dispose()
        $stream.Dispose()
    }
}

function Write-WindowsResource {
    param(
        [Parameter(Mandatory = $true)]
        [ValidateSet("amd64", "arm64")]
        [string]$Architecture,

        [Parameter(Mandatory = $true)]
        [string]$IcoPath,

        [Parameter(Mandatory = $true)]
        [string]$RelativeOutputPath
    )

    $outputPath = Join-Path $repoRoot $RelativeOutputPath
    # Both architectures embed the same Wails DPI and non-elevating manifest.
    $manifestPath = Join-Path $repoRoot "packaging/windows/TraverseBoard.exe.manifest"
    & go run github.com/akavel/rsrc@v0.10.2 `
        -arch $Architecture `
        -ico $IcoPath `
        -manifest $manifestPath `
        -o $outputPath
    if ($LASTEXITCODE -ne 0) {
        throw "rsrc failed for $Architecture with exit code $LASTEXITCODE"
    }
}

Add-Type -AssemblyName System.Drawing
$sourceFile = Resolve-RepositoryFile -Path $SourcePath
$source = [System.Drawing.Image]::FromFile($sourceFile)
try {
    if ($source.Width -ne 1254 -or $source.Height -ne 1254) {
        throw "Approved brand master must be exactly 1254x1254; got $($source.Width)x$($source.Height)."
    }

    Write-PngAsset -Source $source -Size 512 -RelativePath "web/src/assets/traverse-board-mark.png" | Out-Null
    Write-PngAsset -Source $source -Size 32 -RelativePath "web/public/traverse-board-favicon-32.png" | Out-Null
    Write-PngAsset -Source $source -Size 180 -RelativePath "web/public/apple-touch-icon.png" | Out-Null
    Write-PngAsset -Source $source -Size 50 -RelativePath "packaging/windows/Assets/StoreLogo.png" | Out-Null
    Write-PngAsset -Source $source -Size 300 -RelativePath "packaging/windows/StoreListingIcon.png" | Out-Null
    Write-PngAsset -Source $source -Size 44 -RelativePath "packaging/windows/Assets/Square44x44Logo.png" | Out-Null
    Write-PngAsset -Source $source -Size 150 -RelativePath "packaging/windows/Assets/Square150x150Logo.png" | Out-Null
    foreach ($asset in @(
            [pscustomobject]@{ Name = "Square44x44Logo.scale-200.png"; Size = 88 },
            [pscustomobject]@{ Name = "Square44x44Logo.scale-400.png"; Size = 176 },
            [pscustomobject]@{ Name = "Square150x150Logo.scale-200.png"; Size = 300 },
            [pscustomobject]@{ Name = "Square150x150Logo.scale-400.png"; Size = 600; Indexed8 = $true },
            [pscustomobject]@{ Name = "StoreLogo.scale-125.png"; Size = 63 },
            [pscustomobject]@{ Name = "StoreLogo.scale-150.png"; Size = 75 },
            [pscustomobject]@{ Name = "StoreLogo.scale-200.png"; Size = 100 },
            [pscustomobject]@{ Name = "StoreLogo.scale-400.png"; Size = 200 }
        )) {
        $indexed8 = $asset.PSObject.Properties["Indexed8"] -and [bool]$asset.Indexed8
        Write-PngAsset -Source $source -Size $asset.Size -Indexed8:$indexed8 `
            -RelativePath "packaging/windows/Assets/$($asset.Name)" | Out-Null
    }
    foreach ($size in @(16, 20, 24, 30, 32, 36, 40, 48, 60, 64, 72, 80, 96, 256)) {
        foreach ($suffix in @("", "_altform-unplated", "_altform-lightunplated")) {
            Write-PngAsset -Source $source -Size $size `
                -RelativePath "packaging/windows/Assets/Square44x44Logo.targetsize-$size$suffix.png" | Out-Null
        }
    }

    $pngBySize = @{}
    foreach ($size in @(16, 20, 24, 30, 32, 36, 40, 48, 60, 64, 72, 80, 96, 128, 256, 512, 1024)) {
        $bitmap = New-PlatformIconBitmap -Source $source -Size $size
        try {
            $pngBySize[$size] = Convert-BitmapToPngBytes -Bitmap $bitmap
        } finally {
            $bitmap.Dispose()
        }
    }

    $icoEntries = foreach ($size in @(16, 20, 24, 30, 32, 36, 40, 48, 60, 64, 72, 80, 96, 128, 256)) {
        [pscustomobject]@{ Size = $size; Bytes = $pngBySize[$size] }
    }
    $icoPath = Join-Path $repoRoot "packaging/windows/TraverseBoard.ico"
    Write-Bytes -Path $icoPath -Bytes (New-IcoBytes -Entries $icoEntries)

    $icnsEntries = @(
        [pscustomobject]@{ Type = "icp4"; Bytes = $pngBySize[16] },
        [pscustomobject]@{ Type = "icp5"; Bytes = $pngBySize[32] },
        [pscustomobject]@{ Type = "ic07"; Bytes = $pngBySize[128] },
        [pscustomobject]@{ Type = "ic08"; Bytes = $pngBySize[256] },
        [pscustomobject]@{ Type = "ic09"; Bytes = $pngBySize[512] },
        [pscustomobject]@{ Type = "ic10"; Bytes = $pngBySize[1024] },
        [pscustomobject]@{ Type = "ic11"; Bytes = $pngBySize[32] },
        [pscustomobject]@{ Type = "ic12"; Bytes = $pngBySize[64] },
        [pscustomobject]@{ Type = "ic13"; Bytes = $pngBySize[256] },
        [pscustomobject]@{ Type = "ic14"; Bytes = $pngBySize[512] }
    )
    Write-Bytes -Path (Join-Path $repoRoot "packaging/macos/TraverseBoard.icns") `
        -Bytes (New-IcnsBytes -Entries $icnsEntries)

    Write-WindowsResource -Architecture amd64 -IcoPath $icoPath `
        -RelativeOutputPath "cmd/cyberagent-desktop/traverse_board_windows_amd64.syso"
    Write-WindowsResource -Architecture arm64 -IcoPath $icoPath `
        -RelativeOutputPath "cmd/cyberagent-desktop/traverse_board_windows_arm64.syso"
} finally {
    $source.Dispose()
}

$sourceHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceFile).Hash.ToLowerInvariant()
Write-Output "Generated Web, Windows, and macOS brand assets from $sourceHash."
