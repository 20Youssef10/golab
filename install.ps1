<#
.SYNOPSIS
Install golab and golab-mcp on Windows from a verified signed release.
.DESCRIPTION
Downloads are verified with SHA-256 and a cosign keyless signature pinned to
the release workflow identity. Downloaded text is never executed as program
text.
.NOTES
Requires cosign >= v2.6.5. Older cosign versions cannot read the
--new-bundle-format sigstore bundles published since v0.3.0 and fail with a
misleading "signature verification failed"; this installer rejects them
before verification with an actionable upgrade hint.
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

# Reject an old cosign BEFORE verification: v0.3.0 bundles use
# --new-bundle-format, which older binaries misreport as a bad signature.
function Assert-CosignVersion {
    if (-not (Get-Command cosign -ErrorAction SilentlyContinue)) {
        throw "cosign >= v2.6.5 is required to verify the release signature; upgrade: $cosignUpgrade"
    }
    $raw = & cosign version --output json 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($raw -join ''))) {
        $raw = & cosign version 2>$null
    }
    $text = $raw -join "`n"
    # Matches both the JSON form ("GitVersion": "v2.6.5") and the plain
    # `GitVersion: v2.6.5` line printed by older cosign releases.
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
    param([string] $ArtifactPath, [string] $BundlePath, [string] $ChecksumPath)
    Assert-CosignVersion
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

    & cosign verify-blob $ArtifactPath --bundle $BundlePath `
        --certificate-oidc-issuer $issuer --certificate-identity-regexp $identity
    if ($LASTEXITCODE -ne 0) { throw "cosign signature verification failed for $name" }
    Write-Host "install: verified checksum and cosign signature for $name"
}

if ($VerifyOnly) {
    if ([string]::IsNullOrEmpty($Artifact) -or [string]::IsNullOrEmpty($Bundle) -or [string]::IsNullOrEmpty($Checksums)) {
        throw '-VerifyOnly requires -Artifact, -Bundle and -Checksums'
    }
    Verify-Release -ArtifactPath $Artifact -BundlePath $Bundle -ChecksumPath $Checksums
    exit 0
}

if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) { throw 'curl.exe is required' }
if (-not (Get-Command tar.exe -ErrorAction SilentlyContinue)) { throw 'tar.exe is required (Windows 10+)' }

$arch = $env:PROCESSOR_ARCHITECTURE
switch -Regex ($arch) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    'x86'   { $arch = '386' }
    default { throw "unsupported architecture: $arch" }
}

if ([string]::IsNullOrEmpty($Version)) {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$owner/$repo/releases/latest"
    $tag = $release.tag_name
} else {
    $tag = "v$($Version.TrimStart('v'))"
}
if ([string]::IsNullOrEmpty($tag)) { throw 'could not read tag_name from the latest release' }
$ver = $tag.TrimStart('v')
$asset = "golab_${ver}_windows_${arch}.tar.gz"
$base = "https://github.com/$owner/$repo/releases/download/$tag"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("golab-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $artifactPath = Join-Path $tmp $asset
    $bundlePath = "$artifactPath.sigstore.json"
    $checksumPath = Join-Path $tmp 'checksums.txt'
    curl.exe -fsSL -o $artifactPath "$base/$asset"
    curl.exe -fsSL -o $checksumPath "$base/checksums.txt"
    curl.exe -fsSL -o $bundlePath "$base/$asset.sigstore.json"
    if ($LASTEXITCODE -ne 0) { throw 'failed to download release verification material' }
    Verify-Release -ArtifactPath $artifactPath -BundlePath $bundlePath -ChecksumPath $checksumPath

    # Extract ONLY known flat members from the verified outer archive; no
    # arbitrary nested path is ever written to disk.
    tar.exe -xzf $artifactPath -C $tmp golab.exe golab-mcp.exe
    if ($LASTEXITCODE -ne 0) { throw 'failed to extract the verified binaries' }
    tar.exe -xzf $artifactPath -C $tmp golab-skills.tar.gz
    if ($LASTEXITCODE -ne 0) { throw 'failed to extract the verified skills bundle' }
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
        $bundlePathSkills = Join-Path $tmp 'golab-skills.tar.gz'
        if (-not (Test-Path -LiteralPath $bundlePathSkills -PathType Leaf)) {
            throw 'verified archive has no golab-skills.tar.gz'
        }
        # Defence in depth: the bundle is inside signed bytes, but still refuse
        # to extract anything outside the two known skill trees.
        foreach ($member in (& tar.exe -tzf $bundlePathSkills)) {
            if ($member -notmatch '^(golab-operations|colab-mcp)(/|$)') {
                throw "unexpected member in skills bundle: $member"
            }
        }
        if ($LASTEXITCODE -ne 0) { throw 'failed to list the skills bundle' }
        New-Item -ItemType Directory -Force -Path $SkillsDir | Out-Null
        & tar.exe -xzf $bundlePathSkills -C $SkillsDir golab-operations colab-mcp
        if ($LASTEXITCODE -ne 0) { throw 'failed to extract the skills bundle' }
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
