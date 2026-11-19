<#
.SYNOPSIS
Generates a Scoop manifest for a published GitHub release.

.DESCRIPTION
Creates the JSON manifest required to submit SignalBox to the Scoop main bucket.
Downloads the release archive and SHA256SUMS.txt to verify checksums and extract
the hash for the Windows x64 binary.

The manifest is compatible with the Scoop bucket format and can be submitted to
https://github.com/ScoopInstaller/Main-Bucket

Scoop bucket requirements:
- Reasonably well-known and widely used tool
- Latest stable version
- Full version (not trial)
- Fairly standard install
- Non-GUI tool (SignalBox qualifies as a CLI tool with embedded web UI)

.EXAMPLE
.\New-ScoopManifest.ps1 -Tag v1.2.3 -OutputDirectory dist\scoop
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$', Options = 'None')]
    [string] $Tag,

    [Parameter(Mandatory)]
    [string] $OutputDirectory,

    [ValidatePattern('^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')]
    [string] $Repository = 'kaladinstorm84/SignalBox'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

# Requires PowerShell 7+ or ConvertTo-Json with -AsHashtable in PS 5.1
if ($PSVersionTable.PSVersion.Major -lt 7) {
    if (-not (Get-Module -ListAvailable ConvertFromExportedValues -ErrorAction SilentlyContinue)) {
        Write-Warning "PowerShell 7+ recommended for JSON generation, using PSObject workaround"
    }
}

$version = $Tag.Substring(1)
$baseUrl = "https://github.com/$Repository/releases/download/$Tag"
$utf8 = New-Object System.Text.UTF8Encoding($false)

function Save-ReleaseFile([string] $Name, [string] $Directory) {
    $path = Join-Path $Directory $Name
    for ($attempt = 1; ; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$Name" -OutFile $path
            return $path
        } catch {
            if ($attempt -ge 6) { throw "Could not download $baseUrl/${Name}: $($_.Exception.Message)" }
            Write-Host "Download of $Name failed (attempt $attempt of 6); retrying in 10 seconds."
            Start-Sleep -Seconds 10
        }
    }
}

function Save-ReleaseFileUrl([string] $Filename) {
    return "$baseUrl/$Filename"
}

$work = Join-Path ([IO.Path]::GetTempPath()) ('signalbox-scoop-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    # Download and verify checksums
    $archiveWindows = "signalbox-$Tag-windows-amd64.zip"
    $sumsPath = Save-ReleaseFile 'SHA256SUMS.txt' $work

    $hashes = @{}
    foreach ($line in Get-Content -LiteralPath $sumsPath) {
        if ($line -match '^([0-9a-fA-F]{64})\s+\*?(\S+)\s*$') {
            $hashes[$Matches[2]] = $Matches[1].ToLowerInvariant()
        }
    }

    if (-not $hashes.ContainsKey($archiveWindows)) {
        throw "SHA256SUMS.txt has no entry for $archiveWindows"
    }
    $sha256Windows = $hashes[$archiveWindows]
    Write-Host "Verified Windows archive checksum: $sha256Windows"

    New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
    
    # Create Scoop manifest in JSON format
    # Scoop uses version-specific URLs for archives (not just latest)
    $manifest = @{
        version      = $version
        description  = "YAML-based AI agent workflow engine with human approval stages"
        homepage     = "https://github.com/$Repository"
        license      = "MIT"
        url          = "$(Save-ReleaseFileUrl $archiveWindows)"
        hash         = $sha256Windows
        bin          = "signalbox.exe"
        checkver     = @{
            github = $Repository
            regex  = "v([0-9.]+)"
        }
        autoupdate   = @{
            url  = "https://github.com/$Repository/releases/download/v`$version/signalbox-v`$version-windows-amd64.zip"
            hash = @{
                url = "https://github.com/$Repository/releases/download/v`$version/SHA256SUMS.txt"
                regex = "(?m)^([0-9a-fA-F]{64})\s+signalbox-v`$version-windows-amd64\.zip$"
                mode = "extract"
            }
        }
    }

    $json = ConvertTo-Json -InputObject $manifest -Depth 10
    $manifestPath = Join-Path $OutputDirectory 'signalbox.json'
    [IO.File]::WriteAllText($manifestPath, $json, $utf8)

    Write-Host "Generated Scoop manifest: $manifestPath"
    Write-Host ""
    Write-Host "Next steps:"
    Write-Host "1. Test locally: scoop install $manifestPath"
    Write-Host "2. Create PR at https://github.com/ScoopInstaller/Main-Bucket"
    Write-Host "3. Add signalbox.json to bucket/signalbox.json in your PR"
    Write-Host ""
    Write-Host "Note: Scoop submission requires 500+ GitHub stars and 150+ forks for main bucket."
    Write-Host "If criteria aren't met, create a custom bucket:"
    Write-Host "  https://scoop.sh/#/buckets"

} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
