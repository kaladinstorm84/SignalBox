<#
.SYNOPSIS
Builds the SignalBox Chocolatey package for a published GitHub release.

.DESCRIPTION
Downloads the release's Windows archive and SHA256SUMS.txt, requires the
archive's checksum to match its published entry, fills in the package
templates next to this script, and runs choco pack. The release must already
be published because the package downloads the archive from it at install time.

.EXAMPLE
.\New-ChocolateyPackage.ps1 -Tag v1.2.3 -OutputDirectory dist\choco
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

if (-not (Get-Command choco -ErrorAction SilentlyContinue)) {
    throw 'choco was not found. Install Chocolatey first: https://chocolatey.org/install'
}

$version = $Tag.Substring(1)
$archive = "signalbox-$Tag-windows-amd64.zip"
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

$work = Join-Path ([IO.Path]::GetTempPath()) ('signalbox-choco-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $archivePath = Save-ReleaseFile $archive $work
    $sumsPath = Save-ReleaseFile 'SHA256SUMS.txt' $work

    $sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    $published = $null
    foreach ($line in Get-Content -LiteralPath $sumsPath) {
        if ($line -match '^([0-9a-fA-F]{64})\s+\*?(\S+)\s*$' -and $Matches[2] -eq $archive) {
            $published = $Matches[1].ToLowerInvariant()
        }
    }
    if (-not $published) { throw "SHA256SUMS.txt has no entry for $archive" }
    if ($published -ne $sha256) {
        throw "Checksum mismatch for ${archive}: downloaded $sha256 but SHA256SUMS.txt lists $published"
    }
    Write-Host "Verified $archive ($sha256)"

    $stage = Join-Path $work 'package'
    New-Item -ItemType Directory -Path (Join-Path $stage 'tools') -Force | Out-Null
    $values = @{
        '{{VERSION}}'    = $version
        '{{TAG}}'        = $Tag
        '{{REPOSITORY}}' = $Repository
        '{{SHA256}}'     = $sha256
    }
    foreach ($file in 'signalbox.nuspec', 'tools\chocolateyInstall.ps1') {
        $text = [IO.File]::ReadAllText((Join-Path $PSScriptRoot $file), $utf8)
        foreach ($key in $values.Keys) { $text = $text.Replace($key, $values[$key]) }
        if ($text -match '\{\{[A-Z0-9_]+\}\}') { throw "Unfilled placeholder $($Matches[0]) in $file" }
        [IO.File]::WriteAllText((Join-Path $stage $file), $text, $utf8)
    }

    New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
    $OutputDirectory = (Resolve-Path -LiteralPath $OutputDirectory).Path
    choco pack (Join-Path $stage 'signalbox.nuspec') --outputdirectory $OutputDirectory | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "choco pack failed with exit code $LASTEXITCODE" }

    $package = Join-Path $OutputDirectory "signalbox.$version.nupkg"
    if (-not (Test-Path -LiteralPath $package)) { throw "Expected package was not created: $package" }
    $package
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
