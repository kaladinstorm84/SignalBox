# Distribution to winget and Scoop

This document covers how to submit SignalBox to the **Windows Package Manager (winget)** and **Scoop** package managers.

## Overview

After publishing a stable release on GitHub, the release workflow automatically generates manifests for both package managers:

- **winget**: Microsoft's official Windows Package Manager (built into Windows 11, available on Windows 10)
- **Scoop**: A lightweight command-line package manager for Windows, popular with developers

Both require manual PR submission to their repositories. Manifests are generated and uploaded as artifacts in the release workflow.

## Windows Package Manager (winget)

### Why winget?

- Built into Windows 11; available on Windows 10
- Official Microsoft package manager
- Simple install: `winget install signalbox`

### Workflow

The release workflow generates three YAML manifest files in the `winget-manifest` artifact:

1. `kaladinstorm84.SignalBox.installer.yaml` — Installer details (URL, hash, type)
2. `kaladinstorm84.SignalBox.locale.en-US.yaml` — Localization, description, metadata
3. `kaladinstorm84.SignalBox.yaml` — Version manifest

### Submission Steps

1. Download the `winget-manifest` artifact from the GitHub release workflow run
2. Fork https://github.com/microsoft/winget-pkgs
3. Create a new branch from `master`:
   ```bash
   git checkout -b signalbox/v1.2.3
   ```
4. Create the directory structure:
   ```bash
   mkdir -p manifests/k/kaladinstorm84/SignalBox/1.2.3
   ```
5. Copy the generated manifest files into this directory:
   ```bash
   cp <downloaded-manifests>/* manifests/k/kaladinstorm84/SignalBox/1.2.3/
   ```
6. Validate your manifest locally (if winget CLI is installed):
   ```bash
   winget validate manifests/k/kaladinstorm84/SignalBox/1.2.3/kaladinstorm84.SignalBox.yaml
   ```
7. Create a PR with title `New package: SignalBox v1.2.3` or `Update package: SignalBox to v1.2.3`

### Validation and Approval

The winget team's automated checks will:
- Validate YAML syntax and schema compliance
- Verify the installer URL is accessible
- Confirm the SHA256 hash matches the downloaded file
- Run basic install/uninstall tests

Manual review may also occur to ensure the application meets quality standards. Expect 1-2 weeks for approval and publication.

### winget Install Command

Once approved, users can install with:

```bash
winget install kaladinstorm84.SignalBox
# or with explicit version
winget install kaladinstorm84.SignalBox --version 1.2.3
```

## Scoop

### Why Scoop?

- Popular with developers and power users
- Simple JSON manifest format
- Automatic updates and version tracking
- Lightweight and Unix-like philosophy (even on Windows)

### Requirements for Main Bucket

Scoop's main bucket requires:
- 500+ GitHub stars
- 150+ GitHub forks
- Latest stable version
- Full version (not trial)
- Fairly standard install
- Non-GUI tool (SignalBox qualifies as CLI with embedded web UI)

If you don't meet these requirements yet, you can still:
- Create a custom Scoop bucket under your repository
- Push to community bucket once criteria are met

### Workflow

The release workflow generates a JSON manifest in the `scoop-manifest` artifact:

```json
{
  "version": "1.2.3",
  "description": "YAML-based AI agent workflow engine...",
  "url": "https://github.com/kaladinstorm84/SignalBox/releases/download/v1.2.3/...",
  "hash": "sha256:...",
  "bin": "signalbox.exe",
  "checkver": { ... },
  "autoupdate": { ... }
}
```

The manifest includes auto-update configuration so Scoop can automatically create new manifests when new releases are published.

### Submission Steps (Main Bucket)

1. Download the `scoop-manifest` artifact (file: `signalbox.json`)
2. Fork https://github.com/ScoopInstaller/Main-Bucket
3. Create a new branch:
   ```bash
   git checkout -b signalbox
   ```
4. Copy the manifest to the bucket directory:
   ```bash
   cp signalbox.json bucket/signalbox.json
   ```
5. Test locally (requires Scoop to be installed):
   ```bash
   scoop install ./bucket/signalbox.json
   scoop uninstall signalbox
   ```
6. Create a PR with title `Add signalbox` or `Update signalbox to v1.2.3`

### Custom Bucket (Alternative)

If your repository doesn't meet main bucket criteria, create a custom bucket:

1. Create a new repository: `https://github.com/kaladinstorm84/scoop-signalbox`
2. Add the manifest at `bucket/signalbox.json`
3. Users install with:
   ```bash
   scoop bucket add signalbox https://github.com/kaladinstorm84/scoop-signalbox
   scoop install signalbox
   ```

### Validation and Approval

The Scoop team's automated checks will:
- Validate JSON schema
- Verify the installer URL is accessible
- Confirm the SHA256 hash matches
- Run basic install/uninstall tests

Approval is typically faster than winget (3-5 days). Expect human review for first-time submissions.

### Scoop Install Command

Once approved, users can install with:

```bash
# Install from main bucket
scoop install signalbox

# or with explicit version
scoop install signalbox@1.2.3

# Check for updates
scoop update signalbox
```

## Local Testing Before Submission

Before submitting to either package manager, test locally:

### winget (Windows 11 or winget installed on Windows 10)

```bash
# Validate manifest syntax
winget validate <manifest-directory>

# Install from local path (requires URL to be accessible)
winget install --manifest <manifest-yaml-path>
```

### Scoop

```bash
# Validate JSON
scoop checkver .\signalbox.json

# Install from local path
scoop install .\signalbox.json

# Verify executable runs
signalbox version

# Uninstall
scoop uninstall signalbox
```

## Troubleshooting

### winget submission fails validation

- Ensure the Windows x64 ZIP archive is accessible at the URL specified in the manifest
- Verify the SHA256 hash matches the published `SHA256SUMS.txt`
- Check that the YAML syntax is valid (use an online YAML validator)
- Ensure the description and tags are appropriate

### Scoop submission fails

- Verify the JSON is valid using an online JSON validator
- Ensure the URL to the Windows x64 ZIP is correct and accessible
- Check that SHA256 hash extraction regex correctly finds the hash in `SHA256SUMS.txt`
- Confirm auto-update `regex` and `url` patterns are correct for future releases

### Auto-update not working in Scoop

The auto-update section uses regex to extract the version from GitHub tags and to find the hash. If you change release naming or checksum file format in the future, the regex patterns may need updating.

Current patterns expect:
- Tags in format: `v1.2.3` (with leading `v`)
- Checksums file: `SHA256SUMS.txt` with format `<hash>  <filename>`

## Release Checklist

Before tagging a release, ensure:

- [ ] All changes committed and pushed
- [ ] Tests pass locally and in CI
- [ ] Version number updated in code (if applicable)
- [ ] Tag with format `v1.2.3` (vMAJOR.MINOR.PATCH)
- [ ] Push tag: `git push origin v1.2.3`

After release workflow completes:

- [ ] Download `winget-manifest` artifact
- [ ] Download `scoop-manifest` artifact
- [ ] Submit to winget-pkgs if criteria met (or custom bucket if not)
- [ ] Submit to Scoop main bucket if criteria met (or custom bucket if not)
- [ ] Link PRs in the GitHub release notes for visibility

## See Also

- [Windows Package Manager Docs](https://learn.microsoft.com/en-us/windows/package-manager/)
- [Scoop Documentation](https://scoop.sh)
- [Scoop Bucket Criteria](https://github.com/ScoopInstaller/scoop/wiki/Criteria-for-including-apps-in-the-main-bucket)
