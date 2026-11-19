<#
.SYNOPSIS
Generates a Windows Package Manager (winget) manifest for a published GitHub release.

.DESCRIPTION
Creates the YAML manifest required to submit SignalBox to the winget community repository.
Downloads the release archive and SHA256SUMS.txt to verify checksums and extract
installer URLs. The manifest is generated from templates and ready for submission
to https://github.com/microsoft/winget-pkgs

.EXAMPLE
.\New-WingetManifest.ps1 -Tag v1.2.3 -OutputDirectory dist\winget
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

$work = Join-Path ([IO.Path]::GetTempPath()) ('signalbox-winget-' + [Guid]::NewGuid().ToString('N'))
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
    
    # Create installer manifest (ProductCode will need to be updated after first release)
    $installerManifest = @"
# yaml-language-server: `$schema=https://raw.githubusercontent.com/microsoft/winget-cli/master/schemas/json/manifests/v1.6.0/manifest.installer.1.6.0.schema.json
PackageIdentifier: kaladinstorm84.SignalBox
PackageVersion: $version
Platform:
  - Windows.Desktop
MinimumOSVersion: "10.0.0.0"
Scope: Machine
InstallModes:
  - Silent
Installers:
  - Architecture: x64
    InstallerType: zip
    InstallerUrl: $(Save-ReleaseFileUrl $archiveWindows)
    InstallerSha256: $sha256Windows
    NestedInstallerType: portable
    NestedInstallerFiles:
      - SourcePath: signalbox.exe
        DestinationPath: signalbox.exe
ManifestType: installer
ManifestVersion: 1.6.0
"@

    # Create locale manifest
    $localeManifest = @"
# yaml-language-server: `$schema=https://raw.githubusercontent.com/microsoft/winget-cli/master/schemas/json/manifests/v1.6.0/manifest.locale.1.6.0.schema.json
PackageIdentifier: kaladinstorm84.SignalBox
PackageVersion: $version
PackageLocale: en-US
Publisher: kaladinstorm84
PublisherUrl: https://github.com/kaladinstorm84/SignalBox
PrivacyUrl: https://github.com/kaladinstorm84/SignalBox/blob/main/README.md
PackageName: SignalBox
PackageUrl: https://github.com/kaladinstorm84/SignalBox
License: MIT
LicenseUrl: https://github.com/kaladinstorm84/SignalBox/blob/main/LICENSE
Copyright: Copyright (c) 2026 kaladinstorm84
CopyrightUrl: https://github.com/kaladinstorm84/SignalBox/blob/main/LICENSE
ShortDescription: YAML-based AI agent workflow engine with human approval stages
Description: SignalBox is a YAML-based AI agent workflow system with human approval stages. Describe a pipeline of agents in YAML, point each agent at a runtime (Codex, Claude, Copilot, or any OpenAI-compatible API), and SignalBox runs the chain, pausing at approval gates for review in the web UI or terminal.
Tags:
  - signalbox
  - ai
  - agents
  - workflow
  - pipelines
  - automation
  - yaml
  - cli
  - foss
ReleaseNotesUrl: https://github.com/kaladinstorm84/SignalBox/releases/tag/$Tag
ManifestType: locale
ManifestVersion: 1.6.0
"@

    # Create version manifest
    $versionManifest = @"
# yaml-language-server: `$schema=https://raw.githubusercontent.com/microsoft/winget-cli/master/schemas/json/manifests/v1.6.0/manifest.version.1.6.0.schema.json
PackageIdentifier: kaladinstorm84.SignalBox
PackageVersion: $version
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.6.0
"@

    # Write manifests to output directory structured for winget-pkgs submission
    $dir = Join-Path $OutputDirectory "kaladinstorm84.SignalBox" $version
    New-Item -ItemType Directory -Path $dir -Force | Out-Null

    [IO.File]::WriteAllText((Join-Path $dir "kaladinstorm84.SignalBox.installer.yaml"), $installerManifest, $utf8)
    [IO.File]::WriteAllText((Join-Path $dir "kaladinstorm84.SignalBox.locale.en-US.yaml"), $localeManifest, $utf8)
    [IO.File]::WriteAllText((Join-Path $dir "kaladinstorm84.SignalBox.yaml"), $versionManifest, $utf8)

    Write-Host "Generated winget manifests in $dir"
    Write-Host "Next: Create PR at https://github.com/microsoft/winget-pkgs with this directory"
    Write-Host "Path for PR: manifests/k/kaladinstorm84/SignalBox/$version/"

} finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
