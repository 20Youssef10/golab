<#
.SYNOPSIS
Install golab and golab-mcp on Windows from a verified signed release.
.DESCRIPTION
Uses only PowerShell/.NET built-ins: Invoke-WebRequest to download over HTTPS,
Get-FileHash for the checksum gate, Expand-Archive for the signed .zip release
asset and a small .NET GZipStream tar reader for the flat skills bundle. No
package manager; curl.exe and tar.exe are not needed. Downloads are verified with
SHA-256 and a cosign keyless signature pinned to the release workflow identity.
Neither curl.exe nor tar.exe is needed. Downloaded text is never executed as
program text.
.NOTES
Requires cosign >= v2.6.5. Older cosign versions cannot read the
--new-bundle-format sigstore bundles published since v0.3.0 and fail with a
misleading "signature verification failed"; this installer bootstraps the
pinned v2.6.5 static binary when cosign is missing or too old.
Upgrade: go install github.com/sigstore/cosign/v2/cmd/cosign@latest
         https://docs.sigstore.dev/cosign/installation/
#>
[CmdletBinding()]
param(
    [string] $Version,
    [string] $InstallDir = "$env:LOCALAPPDATA\golab\bin",
    [string] $SkillsDir = "$env:LOCALAPPDATA\golab\skills",
    [switch] $NoSkills,
    [switch] $VerifyOnly,
    [string] $Artifact,
    [string] $Bundle,
    [string] $Checksums
)

$ErrorActionPreference = 'Stop'
$owner = '20Youssef10'
$repo = 'golab'
$issuer = 'https://token.actions.githubusercontent.com'
$identity = '^https://github\.com/20Youssef10/golab/\.github/workflows/release\.yml@refs/tags/v'
$skills = @('golab-operations', 'colab-mcp')
$minCosign = [version]'2.6.5'
$cosignUpgrade = 'go install github.com/sigstore/cosign/v2/cmd/cosign@latest (https://docs.sigstore.dev/cosign/installation/)'
$COSIGN_VERSION = 'v2.6.5'
$COSIGN_BASE = "https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION"

# REAL digests from the published
# https://github.com/sigstore/cosign/releases/download/v2.6.5/cosign_checksums.txt
# (SHA-256 of that file:
# 4037b3c8d222e970cf0c030147471f44743626323cb0904820e59869e03968ef).
# Do NOT edit these without re-fetching that file. Windows ARM64 runs the x64
# static binary through Windows' x64 emulation; no arm64 asset is published.
$COSIGN_CHECKSUMS_DIGEST = '4037b3c8d222e970cf0c030147471f44743626323cb0904820e59869e03968ef'
$COSIGN_PINNED = @{
    'cosign-windows-amd64.exe' = '19ec346d0050f59ffad9694da97ef9704be34ec60ddc41c5f1d16d8619385825'
}

$script:cosignBin = ''

try { Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction Stop } catch { }

# Save-Url is HTTPS-only: plaintext downloads and HTTP redirects are refused.
function Save-Url {
    param([string] $Uri, [string] $Path)
    if (-not $Uri.StartsWith('https://', [System.StringComparison]::Ordinal)) {
        throw "refusing non-HTTPS download: $Uri"
    }
    Invoke-WebRequest -Uri $Uri -OutFile $Path -UseBasicParsing -TimeoutSec 300
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "download failed: $Uri" }
}

# Bootstrap-Cosign verifies the pinned release's checksums file against its
# own real digest, checks the listed binary digest against the pin, and only
# then uses the downloaded verifier. Any mismatch is a hard failure.
function Bootstrap-Cosign {
    param([string] $TempDir)
    $asset = 'cosign-windows-amd64.exe'
    $checksumsPath = Join-Path $TempDir 'cosign_checksums.txt'
    Save-Url -Uri "$COSIGN_BASE/cosign_checksums.txt" -Path $checksumsPath
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $checksumsPath).Hash.ToLowerInvariant()
    if ($actual -ne $COSIGN_CHECKSUMS_DIGEST) {
        throw "cosign checksums.txt digest mismatch: got $actual, want pinned $COSIGN_CHECKSUMS_DIGEST"
    }
    $expected = ''
    foreach ($line in Get-Content -LiteralPath $checksumsPath) {
        $parts = $line.Trim() -split '\s+'
        if ($parts.Length -ge 2 -and $parts[1] -eq $asset) {
            $expected = $parts[0].ToLowerInvariant()
            break
        }
    }
    if ([string]::IsNullOrEmpty($expected)) { throw "no checksum entry for $asset in cosign checksums.txt" }
    $pinned = $COSIGN_PINNED[$asset]
    if ([string]::IsNullOrEmpty($pinned)) { throw "no pinned cosign digest for $asset" }
    if ($expected -ne $pinned) {
        throw "published cosign checksum changed for $asset: $expected != pinned $pinned"
    }
    $bin = Join-Path $TempDir 'cosign.exe'
    Save-Url -Uri "$COSIGN_BASE/$asset" -Path $bin
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $bin).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "cosign bootstrap checksum mismatch for $asset: got $actual, want $expected"
    }
    $script:cosignBin = $bin
    Write-Host "install: bootstrapped verified cosign $COSIGN_VERSION from $COSIGN_BASE"
}

# Reject an old cosign BEFORE verification: v0.3.0 bundles use
# --new-bundle-format, which older binaries misreport as a bad signature.
# Missing/old cosign is replaced by the verified pinned bootstrap binary.
function Assert-CosignVersion {
    param([string] $TempDir)
    $found = ''
    $local = Get-Command cosign.exe -ErrorAction SilentlyContinue
    if ($local) {
        $raw = & $local.Source version --output json 2>$null
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($raw -join ''))) {
            $raw = & $local.Source version 2>$null
        }
        $text = $raw -join "`n"
        # Matches JSON ("GitVersion": "v2.6.5") and plain `GitVersion: v2.6.5`.
        $match = [regex]::Match($text, 'GitVersion"?\s*:?\s*"?v?(?<ver>\d+\.\d+(?:\.\d+)?)', 'IgnoreCase')
        if ($match.Success) { $found = [version]$match.Groups['ver'].Value }
        if ($found -and $found -ge $minCosign) {
            $script:cosignBin = $local.Source
            return
        }
    }
    # No viable local verifier: bootstrap the pinned release instead.
    Bootstrap-Cosign -TempDir $TempDir
    $raw = & $script:cosignBin version --output json 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($raw -join ''))) {
        $raw = & $script:cosignBin version 2>$null
    }
    $text = $raw -join "`n"
    $match = [regex]::Match($text, 'GitVersion"?\s*:?\s*"?v?(?<ver>\d+\.\d+(?:\.\d+)?)', 'IgnoreCase')
    if (-not $match.Success) {
        throw "could not parse 'cosign version' output; cosign >= v2.6.5 is required; upgrade: $cosignUpgrade"
    }
    $found = [version]$match.Groups['ver'].Value
    if ($found -lt $minCosign) {
        throw "cosign v$found is too old: v2.6.5 or newer is required to read the v0.3.0 sigstore bundle; upgrade: $cosignUpgrade"
    }
}

function Verify-Release {
    param([string] $ArtifactPath, [string] $BundlePath, [string] $ChecksumPath, [string] $TempDir)
    Assert-CosignVersion -TempDir $TempDir
    if (-not (Test-Path -LiteralPath $ArtifactPath -PathType Leaf)) { throw "artifact not found: $ArtifactPath" }
    if (-not (Test-Path -LiteralPath $BundlePath -PathType Leaf)) { throw "cosign bundle not found: $BundlePath" }
    if (-not (Test-Path -LiteralPath $ChecksumPath -PathType Leaf)) { throw "checksums file not found: $ChecksumPath" }

    $name = Split-Path -Leaf $ArtifactPath
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $ArtifactPath).Hash.ToLowerInvariant()
    $expected = ''
    foreach ($line in Get-Content -LiteralPath $ChecksumPath) {
        $parts = $line.Trim() -split '\s+'
        if ($parts.Length -ge 2 -and ($parts[1] -eq $name -or $parts[1] -eq "*$name")) {
            $expected = $parts[0].ToLowerInvariant().TrimStart('s','h','a','2','5','6',':')
            break
        }
    }
    if ([string]::IsNullOrEmpty($expected)) { throw "no checksum entry for $name" }
    if ($actual -ne $expected) { throw "checksum mismatch for $name" }

    # cosign verify-blob pins BOTH the OIDC issuer and the workflow identity.
    & $script:cosignBin verify-blob $ArtifactPath --bundle $BundlePath `
        --certificate-oidc-issuer $issuer --certificate-identity-regexp $identity
    if ($LASTEXITCODE -ne 0) { throw "cosign signature verification failed for $name" }
    Write-Host "install: verified checksum and cosign signature for $name"
}

# Expand-TarGz extracts ONLY the two allow-listed skill trees from the signed
# flat bundle using .NET GZipStream, with no external tar.exe. It rejects
# traversal, absolute paths, links and special members before writing them.
function Read-Exactly {
    param([System.IO.Stream] $Stream, [byte[]] $Buffer, [int] $Count)
    $total = 0
    while ($total -lt $Count) {
        $n = $Stream.Read($Buffer, $total, $Count - $total)
        if ($n -eq 0) { throw 'truncated skills bundle' }
        $total += $n
    }
}

function Expand-TarGz {
    param([string] $ArchivePath, [string] $Destination, [string[]] $AllowedPrefixes)
    $fullDest = [System.IO.Path]::GetFullPath($Destination)
    $stream = [System.IO.File]::OpenRead($ArchivePath)
    try {
        $gzip = [System.IO.Compression.GZipStream]::new($stream, [System.IO.Compression.CompressionMode]::Decompress)
        try {
            $header = New-Object byte[] 512
            while ($true) {
                Read-Exactly -Stream $gzip -Buffer $header -Count 512
                $isZero = $true
                foreach ($byte in $header) { if ($byte -ne 0) { $isZero = $false; break } }
                if ($isZero) { break }
                $name = [System.Text.Encoding]::UTF8.GetString($header, 0, 100).TrimEnd([char]0)
                if ([string]::IsNullOrEmpty($name)) { continue }
                $sizeField = [System.Text.Encoding]::ASCII.GetString($header, 124, 12).Trim([char]0, ' ')
                $size = 0
                if ($sizeField -match '^[0-7]+$') { $size = [Convert]::ToInt64($sizeField, 8) }
                $type = [char]$header[156]

                $allowed = $false
                foreach ($prefix in $AllowedPrefixes) {
                    if ($name -eq $prefix -or $name.StartsWith($prefix + '/')) { $allowed = $true; break }
                }
                if (-not $allowed) { throw "unexpected member in skills bundle: $name" }
                if ($name -match '(^|/)\.\.(/|$)' -or $name.StartsWith('/') -or $name -match '^[A-Za-z]:') {
                    throw "unsafe member in skills bundle: $name"
                }
                if ($type -eq '1' -or $type -eq '2' -or ($type -ne '0' -and $type -ne '5' -and $type -ne [char]0)) {
                    throw "unsafe member in skills bundle: $name"
                }
                $relative = $name.Replace('/', [System.IO.Path]::DirectorySeparatorChar)
                $target = [System.IO.Path]::GetFullPath((Join-Path $fullDest $relative))
                if (-not ($target -eq $fullDest -or $target.StartsWith($fullDest + [System.IO.Path]::DirectorySeparatorChar))) {
                    throw "unsafe member in skills bundle: $name"
                }
                if ($type -eq '5') {
                    New-Item -ItemType Directory -Force -Path $target | Out-Null
                    continue
                }
                $data = New-Object byte[] ([int]$size)
                Read-Exactly -Stream $gzip -Buffer $data -Count ([int]$size)
                $pad = (512 - ([int]$size % 512)) % 512
                if ($pad -gt 0) {
                    $drop = New-Object byte[] $pad
                    Read-Exactly -Stream $gzip -Buffer $drop -Count $pad
                }
                $parent = [System.IO.Path]::GetDirectoryName($target)
                New-Item -ItemType Directory -Force -Path $parent | Out-Null
                [System.IO.File]::WriteAllBytes($target, $data)
            }
        } finally { $gzip.Dispose() }
    } finally { $stream.Dispose() }
}

if ($VerifyOnly) {
    if ([string]::IsNullOrEmpty($Artifact) -or [string]::IsNullOrEmpty($Bundle) -or [string]::IsNullOrEmpty($Checksums)) {
        throw '-VerifyOnly requires -Artifact, -Bundle and -Checksums'
    }
    Verify-Release -ArtifactPath $Artifact -BundlePath $Bundle -ChecksumPath $Checksums -TempDir ([System.IO.Path]::GetTempPath())
    exit 0
}

$arch = $env:PROCESSOR_ARCHITECTURE
switch -Regex ($arch) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    'x86'   { $arch = '386' }
    default { throw "unsupported architecture: $arch" }
}

if ([string]::IsNullOrEmpty($Version)) {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$owner/$repo/releases/latest" -TimeoutSec 300
    $tag = $release.tag_name
} else {
    $tag = "v$($Version.TrimStart('v'))"
}
if ([string]::IsNullOrEmpty($tag)) { throw 'could not read tag_name from the latest release' }
$ver = $tag.TrimStart('v')
# The signed winget .zip repack carries the same binaries and skills bundle as
# the tar.gz, and Expand-Archive can read it without curl.exe or tar.exe.
$asset = "golab_${ver}_windows_${arch}.zip"
$base = "https://github.com/$owner/$repo/releases/download/$tag"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("golab-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $artifactPath = Join-Path $tmp $asset
    $bundlePath = "$artifactPath.sigstore.json"
    $checksumPath = Join-Path $tmp 'checksums.txt'
    Save-Url -Uri "$base/$asset" -Path $artifactPath
    Save-Url -Uri "$base/checksums.txt" -Path $checksumPath
    Save-Url -Uri "$base/$asset.sigstore.json" -Path $bundlePath
    Verify-Release -ArtifactPath $artifactPath -BundlePath $bundlePath -ChecksumPath $checksumPath -TempDir $tmp

    # Validate the signed zip's flat allowlist before extracting anything.
    $zip = [System.IO.Compression.ZipFile]::OpenRead($artifactPath)
    try {
        foreach ($entry in $zip.Entries) {
            if ($entry.FullName -notin @('golab.exe', 'golab-mcp.exe', 'golab-skills.tar.gz')) {
                throw "unexpected member in archive: $($entry.FullName)"
            }
            if ($entry.FullName -match '(^|/)\.\.' -or [System.IO.Path]::IsPathRooted($entry.FullName)) {
                throw "unsafe member in archive: $($entry.FullName)"
            }
        }
    } finally { $zip.Dispose() }
    Expand-Archive -LiteralPath $artifactPath -DestinationPath $tmp -Force

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    foreach ($binary in @('golab.exe', 'golab-mcp.exe')) {
        $source = Join-Path $tmp $binary
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "verified archive has no $binary" }
        Copy-Item -LiteralPath $source -Destination (Join-Path $InstallDir $binary) -Force
        $installed = Join-Path $InstallDir $binary
        if (-not (Test-Path -LiteralPath $installed -PathType Leaf)) { throw "failed to install $binary" }
        # Smoke test the installed bytes (no downloaded text is executed).
        & $installed --help 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "$binary --help failed after install" }
    }
    Write-Host "install: installed golab and golab-mcp $ver to $InstallDir"

    if (-not $NoSkills) {
        $skillsBundle = Join-Path $tmp 'golab-skills.tar.gz'
        if (-not (Test-Path -LiteralPath $skillsBundle -PathType Leaf)) {
            throw 'verified archive has no golab-skills.tar.gz'
        }
        New-Item -ItemType Directory -Force -Path $SkillsDir | Out-Null
        Expand-TarGz -ArchivePath $skillsBundle -Destination $SkillsDir -AllowedPrefixes $skills
        foreach ($skill in $skills) {
            if (-not (Test-Path -LiteralPath (Join-Path $SkillsDir "$skill\SKILL.md") -PathType Leaf)) {
                throw "failed to install skill $skill"
            }
        }
        Write-Host "install: installed skills $($skills -join ', ') to $SkillsDir"
    } else {
        Write-Host 'install: skipped skills (-NoSkills)'
    }
} finally {
    Remove-Item -Recurse -Force -LiteralPath $tmp -ErrorAction SilentlyContinue
}
