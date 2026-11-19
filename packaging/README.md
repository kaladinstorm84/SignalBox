# Package Manager Scripts Reference

This directory contains PowerShell scripts that generate manifests for different package managers.

## Scripts

### New-ChocolateyPackage.ps1

Builds a Chocolatey package from a published GitHub release.

**Usage:**
```powershell
.\packaging\chocolatey\New-ChocolateyPackage.ps1 -Tag v1.2.3 -OutputDirectory dist\choco
```

**Parameters:**
- `-Tag` (required): Release tag in format `vMAJOR.MINOR.PATCH`
- `-OutputDirectory` (required): Directory where the `.nupkg` file is created
- `-Repository` (optional): GitHub repository in format `owner/repo` (default: `kaladinstorm84/SignalBox`)

**What it does:**
1. Downloads the Windows x64 ZIP archive and `SHA256SUMS.txt` from the release
2. Verifies the checksum matches the published entry
3. Generates the `.nuspec` metadata file with version and checksums
4. Runs `choco pack` to create the `.nupkg` package
5. Tests the package install/uninstall on the runner

**Prerequisites:**
- Chocolatey must be installed: `choco --version`

**Output:**
- `signalbox.X.Y.Z.nupkg` — Ready to submit to Chocolatey or install locally

---

### New-WingetManifest.ps1

Generates Windows Package Manager (winget) manifests from a published release.

**Usage:**
```powershell
.\packaging\winget\New-WingetManifest.ps1 -Tag v1.2.3 -OutputDirectory dist\winget
```

**Parameters:**
- `-Tag` (required): Release tag in format `vMAJOR.MINOR.PATCH`
- `-OutputDirectory` (required): Directory where manifests are created
- `-Repository` (optional): GitHub repository in format `owner/repo` (default: `kaladinstorm84/SignalBox`)

**What it does:**
1. Downloads the Windows x64 ZIP archive and `SHA256SUMS.txt` from the release
2. Verifies the checksum matches the published entry
3. Generates three YAML manifest files:
   - `kaladinstorm84.SignalBox.installer.yaml` — Installer details
   - `kaladinstorm84.SignalBox.locale.en-US.yaml` — Localization and metadata
   - `kaladinstorm84.SignalBox.yaml` — Version information

**Prerequisites:**
- PowerShell 5.1+ (Windows default)

**Output:**
- `kaladinstorm84.SignalBox/X.Y.Z/` — Directory ready for PR submission to [winget-pkgs](https://github.com/microsoft/winget-pkgs)

**Next steps:**
1. Download the generated directory
2. Fork https://github.com/microsoft/winget-pkgs
3. Create PR with structure: `manifests/k/kaladinstorm84/SignalBox/X.Y.Z/`

See [Package Managers](../help/package-managers.md) for detailed instructions.

---

### New-ScoopManifest.ps1

Generates a Scoop bucket manifest from a published release.

**Usage:**
```powershell
.\packaging\scoop\New-ScoopManifest.ps1 -Tag v1.2.3 -OutputDirectory dist\scoop
```

**Parameters:**
- `-Tag` (required): Release tag in format `vMAJOR.MINOR.PATCH`
- `-OutputDirectory` (required): Directory where the manifest is created
- `-Repository` (optional): GitHub repository in format `owner/repo` (default: `kaladinstorm84/SignalBox`)

**What it does:**
1. Downloads the Windows x64 ZIP archive and `SHA256SUMS.txt` from the release
2. Verifies the checksum matches the published entry
3. Generates a JSON manifest with:
   - Installer URL and SHA256 hash
   - Binary name and description
   - Auto-update configuration for future releases

**Prerequisites:**
- PowerShell 5.1+ (Windows default)

**Output:**
- `signalbox.json` — Manifest ready for PR submission to [ScoopInstaller/Main-Bucket](https://github.com/ScoopInstaller/Main-Bucket)

**Next steps:**
1. Download the generated `signalbox.json`
2. If repository meets Scoop criteria (500+ stars, 150+ forks):
   - Fork https://github.com/ScoopInstaller/Main-Bucket
   - Create PR with file at `bucket/signalbox.json`
3. Otherwise:
   - Create custom bucket repository
   - Add manifest to `bucket/signalbox.json`
   - Users install with: `scoop bucket add signalbox https://your-bucket-url`

See [Package Managers](../help/package-managers.md) for detailed instructions.

---

## Common Workflow

The release workflow automatically runs these scripts for stable releases (non-prerelease):

1. **publish** job creates the GitHub release with archives and checksums
2. **chocolatey** job runs and automatically submits to Chocolatey
3. **winget-manifest** job generates manifests (uploaded as artifact)
4. **scoop-manifest** job generates manifest (uploaded as artifact)

For manual testing or offline releases:

```powershell
# Test locally (after a release is published)
.\packaging\chocolatey\New-ChocolateyPackage.ps1 -Tag v1.2.3 -OutputDirectory dist
.\packaging\winget\New-WingetManifest.ps1 -Tag v1.2.3 -OutputDirectory dist
.\packaging\scoop\New-ScoopManifest.ps1 -Tag v1.2.3 -OutputDirectory dist
```

---

## Troubleshooting

### "Could not download" errors

The scripts retry up to 6 times with 10-second delays. This handles transient GitHub/network issues. If errors persist:
- Verify the tag format is correct: `v1.2.3`
- Confirm the release is published (not still in draft)
- Check that the Windows x64 ZIP exists in the release

### JSON/YAML validation errors

Manifests should be valid per schema. To test locally:

```powershell
# winget (requires winget installed)
winget validate manifests/k/kaladinstorm84/SignalBox/1.2.3/kaladinstorm84.SignalBox.yaml

# Scoop (validate JSON)
ConvertFrom-Json (Get-Content signalbox.json -Raw)
```

### Chocolatey package not created

Ensure:
- Chocolatey is installed: `choco --version`
- You have write permissions to the output directory
- The downloaded ZIP checksum is verified before packing

---

## See Also

- [Releasing.md](../help/releasing.md) — Complete release process
- [Package Managers](../help/package-managers.md) — Submission and installation instructions
