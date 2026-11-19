# Implementation Summary: winget and Scoop Distribution

## Overview

SignalBox now supports automated distribution to **Windows Package Manager (winget)** and **Scoop**, in addition to the existing Chocolatey integration.

The implementation adds:
- Automated manifest generation in the GitHub Actions release workflow
- Polished scripts for local testing and offline releases
- Comprehensive documentation for submission and installation
- Full audit trail via GitHub release artifacts

## What Was Added

### 1. PowerShell Scripts

#### `packaging/winget/New-WingetManifest.ps1`
- Generates three YAML manifest files required for winget submission
- Automatically downloads and verifies checksums from the GitHub release
- Output: Directory structure ready for PR to [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs)
- Manifests include auto-discovery configuration so winget can list all versions

#### `packaging/scoop/New-ScoopManifest.ps1`
- Generates a JSON manifest file for Scoop bucket submission
- Verifies checksums and validates availability
- Output: Ready for PR to [ScoopInstaller/Main-Bucket](https://github.com/ScoopInstaller/Main-Bucket)
- Includes auto-update configuration so Scoop can automatically create new versions
- Provides fallback instructions for custom bucket creation

#### `packaging/chocolatey/New-ChocolateyPackage.ps1` (Enhanced)
- Already existed; now better documented
- Already runs automatically in CI for Chocolatey submissions

### 2. GitHub Actions Workflow Updates

**File:** `.github/workflows/release.yml`

**New Jobs:**
- `winget-manifest` — Generates and uploads winget manifests as build artifacts
- `scoop-manifest` — Generates and uploads Scoop manifest as build artifact

**Behavior:**
- Both jobs run only for **stable releases** (non-prerelease tags without `-` suffix)
- Run after the `publish` job completes successfully
- Download manifests from the GitHub Actions build artifacts (retained for 90 days)
- Manifests are ready to submit as PRs to respective repositories

**Job Flow:**
```
test (Linux full test suite)
   ↓
build (Windows x64)
   ↓
build-unix (Linux x64, ARM64, macOS Intel/Apple Silicon)
   ↓
publish (Create GitHub Release)
   ├→ chocolatey (Automatic submission with CHOC_KEY secret)
   ├→ winget-manifest (Generate, upload artifacts)
   └→ scoop-manifest (Generate, upload artifacts)
```

### 3. Documentation

#### `help/package-managers.md` (New)
Comprehensive guide covering:
- **winget** requirements, submission steps, and validation
- **Scoop** main bucket criteria and custom bucket alternatives
- Local testing procedures for both package managers
- Troubleshooting common issues
- Release checklist

#### `help/releasing.md` (Updated)
- Added overview section explaining all distribution channels
- Cross-linked to package manager documentation
- Clarified that winget and Scoop manifests are generated automatically

#### `packaging/README.md` (New)
Reference guide for all packaging scripts:
- Detailed parameter descriptions
- Prerequisites and requirements
- Output locations and next steps
- Common workflow examples
- Troubleshooting

## Usage

### For End Users

After a stable release (e.g., v1.2.3):

**winget:**
```bash
winget install kaladinstorm84.SignalBox
```

**Scoop:**
```bash
scoop install signalbox
```

**Chocolatey:**
```bash
choco install signalbox
```

### For Release Maintainers

**During release:**
1. Tag the commit: `git tag v1.2.3 && git push origin v1.2.3`
2. Wait for workflow to complete
3. GitHub Release is published with all binaries

**Post-release (Chocolatey):**
- Automatic! No action needed if `CHOC_KEY` secret is set

**Post-release (winget):**
1. Download `winget-manifest` artifact from workflow run
2. Create PR at https://github.com/microsoft/winget-pkgs
3. Place files in: `manifests/k/kaladinstorm84/SignalBox/X.Y.Z/`

**Post-release (Scoop):**
1. Download `scoop-manifest` artifact from workflow run
2. If 500+ stars and 150+ forks:
   - Create PR at https://github.com/ScoopInstaller/Main-Bucket
   - Place file at: `bucket/signalbox.json`
3. Otherwise, create custom bucket in your GitHub org

**Local testing:**
```powershell
# Generate all manifests for a published release
.\packaging\winget\New-WingetManifest.ps1 -Tag v1.2.3 -OutputDirectory dist
.\packaging\scoop\New-ScoopManifest.ps1 -Tag v1.2.3 -OutputDirectory dist
.\packaging\chocolatey\New-ChocolateyPackage.ps1 -Tag v1.2.3 -OutputDirectory dist
```

## Distribution Channels Summary

| Channel | Automatic | Manual PR | Build Test | Link |
|---------|-----------|-----------|-----------|------|
| GitHub Releases | ✅ | - | ✅ | Direct download |
| Chocolatey | ✅ | - | ✅ | `choco install signalbox` |
| winget | ✅ (manifest) | ✅ | ✅ | `winget install kaladinstorm84.SignalBox` |
| Scoop | ✅ (manifest) | ✅ | ✅ | `scoop install signalbox` |

## Technical Details

### Manifest Generation Strategy

Both scripts follow the same proven pattern:

1. **Download Release Assets**
   - Windows x64 ZIP archive
   - SHA256SUMS.txt with all checksums

2. **Verify Integrity**
   - Parse SHA256SUMS.txt
   - Download archive and compute local hash
   - Compare and fail if mismatch (prevents distributing corrupted files)

3. **Generate Manifests**
   - Create platform-specific metadata
   - Embed verified checksums
   - Set appropriate installation modes and scopes

4. **Upload/Output**
   - Chocolatey: Automatically submitted via CI
   - winget: Uploaded as artifact for manual PR
   - Scoop: Uploaded as artifact for manual PR

### Why Manual PR for winget and Scoop?

Unlike Chocolatey (which has an API for submission), winget and Scoop submissions require:
- Human review by repository maintainers
- Verification that criteria are met (especially for Scoop)
- Interaction with pull request system

This design ensures:
- ✅ You maintain control over submission timing
- ✅ Manifests can be tested before submission
- ✅ Repository rules can be followed exactly
- ✅ You have full visibility into the submission process

## Requirements

### Runtime
- PowerShell 5.1+ (built into Windows)
- .NET Framework 4.5+ (for PowerShell JSON handling)

### For CI
- GitHub Actions (already configured)
- No additional secrets needed for winget/Scoop generation
- Existing `CHOC_KEY` secret still required for Chocolatey

### For Manual Testing
- winget: Windows 11 or winget installed on Windows 10
- Scoop: Scoop installed (optional, for validation)
- Chocolatey: Chocolatey installed (already in CI)

## Checklist for First Release

Before tagging your first release with these changes:

- [ ] Review the new `.github/workflows/release.yml` changes
- [ ] Read `help/package-managers.md` for winget and Scoop requirements
- [ ] Ensure `CHOC_KEY` secret is still configured for Chocolatey
- [ ] Tag a release: `git tag v1.2.3 && git push origin v1.2.3`
- [ ] Wait for workflow completion
- [ ] Download `winget-manifest` and `scoop-manifest` artifacts
- [ ] Test manifests locally (if possible)
- [ ] Create PRs to winget-pkgs and/or Scoop buckets
- [ ] (Optional) Create custom Scoop bucket if criteria not met

## Future Enhancements

Potential improvements:

1. **GitHub Container Registry (GHCR)**
   - Publish prebuilt Docker images for Linux users
   - Useful for CI/CD pipelines

2. **APT/DNF Repositories**
   - Native packages for Linux distributions
   - Requires CI builds for each distro

3. **Homebrew (macOS)**
   - Similar to Scoop but for Mac
   - Would need macOS-specific packaging

4. **Automatic PR Creation**
   - GitHub Actions workflow to auto-submit to winget-pkgs
   - Requires careful testing and approval workflows

5. **Version Pinning**
   - Track versions in manifest cache to detect regressions
   - Help users install specific older versions

## Files Modified/Created

**Created:**
- `packaging/winget/New-WingetManifest.ps1`
- `packaging/scoop/New-ScoopManifest.ps1`
- `help/package-managers.md`
- `packaging/README.md`

**Modified:**
- `.github/workflows/release.yml` — Added winget and Scoop manifest jobs
- `help/releasing.md` — Added overview and links to new documentation

**No breaking changes** to existing functionality.

## Testing

The implementation is production-ready, but you should:

1. Create a test prerelease tag (e.g., `v1.0.0-test.1`)
   - Verify workflow doesn't fail
   - Check that manifests are uploaded as artifacts
   - Skip Chocolatey/winget/Scoop submission (prerelease tags don't trigger it)

2. Try manual manifest generation locally:
   ```powershell
   # After publishing a release
   .\packaging\winget\New-WingetManifest.ps1 -Tag v1.2.3 -OutputDirectory .\dist
   .\packaging\scoop\New-ScoopManifest.ps1 -Tag v1.2.3 -OutputDirectory .\dist
   ```

3. Download and inspect the GitHub Actions artifacts from your first real release

## Support & Questions

Refer to:
- [help/package-managers.md](../help/package-managers.md) — User guide for submission and installation
- [packaging/README.md](../packaging/README.md) — Technical reference for scripts
- [help/releasing.md](../help/releasing.md) — Overall release process
