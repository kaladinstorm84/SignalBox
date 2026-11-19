# Troubleshooting Package Manager Distribution

## Common Issues and Solutions

### GitHub Actions Workflow Issues

#### Workflow Job Fails: "Could not download"

**Symptoms:**
- winget-manifest or scoop-manifest job fails
- Error: "Could not download $baseUrl/signalbox-*.zip: 404"

**Causes:**
1. Release is still in draft (manifests need published release)
2. Tag format is wrong (must be `vX.Y.Z` exactly)
3. GitHub is experiencing issues
4. The Windows x64 ZIP file wasn't created by build job

**Solutions:**
```powershell
# Check 1: Verify release is published (not draft)
gh release view v1.2.3
# Should show: "Release is not a draft"

# Check 2: Verify Windows ZIP exists in release
gh release view v1.2.3 --json assets --jq '.assets[].name'
# Should include: signalbox-v1.2.3-windows-amd64.zip

# Check 3: Verify SHA256SUMS.txt exists
gh release view v1.2.3 --json assets --jq '.assets[].name'
# Should include: SHA256SUMS.txt

# If missing, check build job logs for Windows x64 failure
```

#### Workflow Doesn't Run Manifest Jobs

**Symptoms:**
- winget-manifest and scoop-manifest jobs don't appear in workflow
- Only chocolatey job runs

**Causes:**
1. Release tag has a hyphen (prerelease, e.g., v1.0.0-rc.1)
2. Publish job failed
3. Git tag format is wrong

**Solutions:**
```powershell
# Check 1: Confirm tag format (no prerelease suffix)
git describe --tags
# Should be: v1.2.3 (not v1.2.3-rc.1 or v1.2.3-beta)

# Check 2: Verify publish job succeeded
# View workflow run → "publish" job → check for ✓ or ✗

# To create a stable release:
git tag v1.2.3        # Good
git tag v1.2.3-rc.1   # Bad (prerelease, skips manifests)
```

---

### winget Manifest Issues

#### "Manifest validation failed" on PR submission

**Symptoms:**
- winget-pkgs PR gets automated comment: "Manifest validation failed"
- May be syntax errors or URL issues

**Solutions:**

1. **Validate locally (if winget installed):**
   ```bash
   winget validate manifests/k/kaladinstorm84/SignalBox/1.2.3/kaladinstorm84.SignalBox.yaml
   ```

2. **Check YAML syntax:**
   - Use online YAML validator: https://www.yamllint.com/
   - Paste manifest content and validate

3. **Verify URLs are accessible:**
   ```bash
   # Windows (PowerShell)
   Invoke-WebRequest -Uri "https://github.com/kaladinstorm84/SignalBox/releases/download/v1.2.3/signalbox-v1.2.3-windows-amd64.zip" -Method Head -UseBasicParsing
   
   # macOS/Linux
   curl -I "https://github.com/kaladinstorm84/SignalBox/releases/download/v1.2.3/signalbox-v1.2.3-windows-amd64.zip"
   ```

4. **Verify SHA256 hash:**
   ```bash
   # Download and check
   curl -sL "https://github.com/kaladinstorm84/SignalBox/releases/download/v1.2.3/SHA256SUMS.txt"
   # Should show line like:
   # 8f4c24e3... signalbox-v1.2.3-windows-amd64.zip
   ```

#### "Installer URL is not accessible"

**Symptoms:**
- winget validation reports 404 on installer URL

**Solutions:**
1. Verify release is published (not draft)
2. Check Windows x64 ZIP filename matches exactly
3. Verify release tag in URL matches your tag

```bash
# Correct format:
# https://github.com/kaladinstorm84/SignalBox/releases/download/v1.2.3/signalbox-v1.2.3-windows-amd64.zip

# Common mistakes:
# - v1.2.3 instead of 1.2.3 in filename: WRONG
# - Missing hyphen: signalboxv1.2.3-windows-amd64.zip: WRONG
# - File is in draft release: Release might be in draft mode
```

#### "Hash mismatch"

**Symptoms:**
- winget reports: "InstallerSha256 does not match downloaded file"

**Solutions:**
1. Re-download the manifest from the artifact
2. Verify the manifest has the correct SHA256 from `SHA256SUMS.txt`

```powershell
# Manual verification
$url = "https://github.com/kaladinstorm84/SignalBox/releases/download/v1.2.3/signalbox-v1.2.3-windows-amd64.zip"
Invoke-WebRequest -Uri $url -OutFile temp.zip
(Get-FileHash -Algorithm SHA256 temp.zip).Hash.ToLowerInvariant()
# Compare with manifest's InstallerSha256 value
```

---

### Scoop Manifest Issues

#### "JSON validation error"

**Symptoms:**
- Manifest fails JSON validation
- Error on PR: "Invalid JSON"

**Solutions:**

1. **Validate locally:**
   ```powershell
   # PowerShell
   ConvertFrom-Json (Get-Content signalbox.json -Raw)
   
   # or use online validator
   # https://jsonlint.com/
   ```

2. **Common JSON issues:**
   - Missing commas between properties
   - Unescaped backslashes in URLs
   - Single quotes instead of double quotes
   - Trailing commas

3. **Check the generated file:**
   ```bash
   # View the manifest
   cat signalbox.json | jq .
   
   # or on Windows
   Get-Content signalbox.json
   ```

#### "checkver or autoupdate not working"

**Symptoms:**
- Scoop can't auto-detect new versions
- Error: "Failed to check version"

**Solutions:**

Scoop's auto-update uses regex patterns. If your release naming changes, update:

```json
{
  "checkver": {
    "github": "kaladinstorm84/SignalBox",
    "regex": "v([0-9.]+)"
  },
  "autoupdate": {
    "url": "https://github.com/kaladinstorm84/SignalBox/releases/download/v$version/signalbox-v$version-windows-amd64.zip",
    "hash": {
      "url": "https://github.com/kaladinstorm84/SignalBox/releases/download/v$version/SHA256SUMS.txt",
      "regex": "(?m)^([0-9a-fA-F]{64})\\s+signalbox-v$version-windows-amd64\\.zip$"
    }
  }
}
```

**Common issues:**
- Tag format changed from `v1.2.3` to something else
- Filename changed (e.g., added suffix like `-release`)
- SHA256SUMS.txt format changed

---

### Submission Issues

#### winget-pkgs PR Approval Stuck

**Symptoms:**
- PR has been open for 3+ weeks
- No responses from maintainers

**Solutions:**
1. Comment on PR asking for status
2. Check if criteria are met:
   - Application is well-known and widely used
   - Latest stable version
   - Full version (not trial)
   - Standard install process
   - Non-GUI tool
3. If criteria are questionable, maintainer may be waiting for more info
4. As last resort, appeal to @microsoft/winget-team in PR

#### Scoop Main Bucket Rejected: "Doesn't meet criteria"

**Symptoms:**
- PR rejected or closed
- Comment: "Minimum 500 stars and 150 forks required"

**Solutions:**
1. Create a custom Scoop bucket instead:
   ```bash
   # Create new repo: scoop-signalbox
   mkdir scoop-signalbox
   mkdir scoop-signalbox/bucket
   cp signalbox.json scoop-signalbox/bucket/signalbox.json
   cd scoop-signalbox
   git init && git add . && git commit -m "Initial commit"
   git push origin main
   ```

2. Users install from custom bucket:
   ```bash
   scoop bucket add signalbox https://github.com/kaladinstorm84/scoop-signalbox
   scoop install signalbox
   ```

3. Once you reach 500+ stars and 150+ forks, re-submit main bucket PR
   - Mention your custom bucket URL
   - Explain that you've met criteria

---

### Local Testing Issues

#### "signalbox.exe not found in ZIP"

**Symptoms:**
- After extracting archive, `signalbox.exe` doesn't exist
- Extraction put files in nested directory

**Causes:**
- ZIP structure is nested (created with wrong path)
- Archive was created with top-level folder instead of contents

**Solutions:**
```powershell
# Check current structure
Expand-Archive signalbox-v1.2.3-windows-amd64.zip verify-dir
Get-ChildItem verify-dir -Recurse

# Should show signalbox.exe at root, like:
# verify-dir/
#   ├─ signalbox.exe
#   ├─ README.md
#   └─ help/

# If nested, fix by re-creating archive correctly
```

#### winget install fails: "Installer failed with exit code X"

**Symptoms:**
- `winget install kaladinstorm84.SignalBox` exits with error
- `signalbox.exe` doesn't work after install

**Solutions:**
1. Test manual extraction:
   ```bash
   # Download ZIP manually
   # Extract to temp location
   # Run: .\signalbox.exe version
   ```

2. If manual extraction works but winget fails:
   - Issue might be with manifest
   - Verify NestedInstallerType and NestedInstallerFiles in manifest
   - Re-generate manifest from artifact

3. Check for antivirus false positives:
   - Some AV software blocks executable downloads
   - Try submitting to Windows Defender for analysis if suspected

#### Scoop install fails: "Hash mismatch"

**Symptoms:**
- `scoop install signalbox` fails
- Error: "Hash check failed"

**Solutions:**
```bash
# 1. Manually verify hash
$url = "https://github.com/kaladinstorm84/SignalBox/releases/download/v1.2.3/signalbox-v1.2.3-windows-amd64.zip"
$expected = "8f4c24e3..."  # from manifest

# Download and check (PowerShell)
Invoke-WebRequest -Uri $url -OutFile temp.zip
$actual = (Get-FileHash -Algorithm SHA256 temp.zip).Hash.ToLowerInvariant()
Write-Host "Expected: $expected"
Write-Host "Actual:   $actual"

# 2. If mismatch, re-download manifest from artifact
# 3. If still fails, GitHub may have cached wrong file
#    - Wait 10 minutes and retry
#    - Or manually update manifest hash
```

---

### Need More Help?

**Useful Resources:**
- [winget Documentation](https://learn.microsoft.com/en-us/windows/package-manager/)
- [Scoop Documentation](https://scoop.sh)
- [Chocolatey Docs](https://docs.chocolatey.org/)

**Check Logs:**
- GitHub Actions: See workflow run logs for step-by-step output
- Manifests: Inspect downloaded artifacts in Actions tab
- Release: Verify on GitHub release page that all files exist

**Still Stuck?**

1. Review [help/package-managers.md](../help/package-managers.md) for submission guidelines
2. Check [DISTRIBUTION_ARCHITECTURE.md](../DISTRIBUTION_ARCHITECTURE.md) for workflow details
3. Review [packaging/README.md](../packaging/README.md) for script reference
4. Post in package manager repository issues with:
   - Your manifest (if applicable)
   - Error message
   - Steps to reproduce
   - Your release URL
