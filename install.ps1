# Install golab and golab-mcp on Windows ONLY from a release whose SHA-256
# checksum and cosign keyless signature have both been verified against the
# pinned release identity. Downloaded text is never executed as program text.
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

function Verify-Release {
    param([string] $ArtifactPath, [string] $BundlePath, [string] $ChecksumPath)
    if (-not (Get-Command cosign -ErrorAction SilentlyContinue)) {
        throw 'cosign is required to verify the release signature'
    }
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

    tar.exe -xzf $artifactPath -C $tmp
    if ($LASTEXITCODE -ne 0) { throw 'failed to extract the verified archive' }
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
        foreach ($skill in $skills) {
            $sourceSkill = Join-Path $tmp (".agents\skills\$skill")
            if (-not (Test-Path -LiteralPath (Join-Path $sourceSkill 'SKILL.md') -PathType Leaf)) {
                throw "verified archive has no skill $skill"
            }
        }
        New-Item -ItemType Directory -Force -Path $SkillsDir | Out-Null
        foreach ($skill in $skills) {
            $sourceSkill = Join-Path $tmp (".agents\skills\$skill")
            $targetSkill = Join-Path $SkillsDir $skill
            New-Item -ItemType Directory -Force -Path $targetSkill | Out-Null
            Copy-Item -LiteralPath (Join-Path $sourceSkill '*') -Destination $targetSkill -Recurse -Force
            if (-not (Test-Path -LiteralPath (Join-Path $targetSkill 'SKILL.md') -PathType Leaf)) {
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
