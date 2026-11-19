# Quick Start: winget & Scoop Distribution

## What Just Happened?

Your SignalBox release workflow now automatically generates manifests for **Windows Package Manager (winget)** and **Scoop** in addition to Chocolatey.

## For Your Next Release

### 1. Tag and Push

```powershell
git tag v1.2.3
git push origin v1.2.3
```

### 2. Wait for Workflow

GitHub Actions will:
- Build and test on Windows/Linux/macOS ✓
- Create GitHub release ✓
- Submit to Chocolatey (automatic, requires `CHOC_KEY` secret) ✓
- **Generate winget manifest** → artifact
- **Generate Scoop manifest** → artifact

### 3. Download Artifacts

From the GitHub Actions run, download:
- **winget-manifest** artifact
- **scoop-manifest** artifact

## Submission Options

### Option A: Submit to Official Repositories (Recommended)

**winget:**
1. Download `winget-manifest` artifact
2. Fork https://github.com/microsoft/winget-pkgs
3. Create PR with files at: `manifests/k/kaladinstorm84/SignalBox/1.2.3/`
4. Title: `New package: SignalBox v1.2.3` (or Update for newer versions)
5. Expected approval: 1-2 weeks

**Scoop:**
1. Download `scoop-manifest` artifact (file: `signalbox.json`)
2. If you have 500+ stars and 150+ forks:
   - Fork https://github.com/ScoopInstaller/Main-Bucket
   - Create PR with file at: `bucket/signalbox.json`
   - Expected approval: 3-5 days
3. Otherwise:
   - Create custom bucket repo: `github.com/kaladinstorm84/scoop-signalbox`
   - Add `bucket/signalbox.json`
   - Users add: `scoop bucket add signalbox https://github.com/kaladinstorm84/scoop-signalbox`

### Option B: Do Nothing

The manifests are generated automatically and stored in artifacts. You can submit them later or whenever you want. No time pressure!

## Installation Commands (Once Approved)

**winget:**
```bash
winget install kaladinstorm84.SignalBox
```

**Scoop (main bucket):**
```bash
scoop install signalbox
```

**Scoop (custom bucket):**
```bash
scoop bucket add signalbox https://github.com/kaladinstorm84/scoop-signalbox
scoop install signalbox
```

**Chocolatey (automatic):**
```bash
choco install signalbox
```

## Need Help?

- **Full documentation:** See [help/package-managers.md](../help/package-managers.md)
- **Script reference:** See [packaging/README.md](../packaging/README.md)
- **Implementation details:** See [DISTRIBUTION_IMPLEMENTATION.md](../DISTRIBUTION_IMPLEMENTATION.md)

## Key Points

✅ **Zero manual work for Chocolatey** — Automatic submission with `CHOC_KEY` secret

✅ **Easy winget submission** — Manifests are pre-generated, just create a PR

✅ **Flexible Scoop** — Official bucket for high-star projects, custom bucket option otherwise

✅ **Fully tested** — All manifests verify checksums before generation

✅ **Non-breaking** — Existing release workflow still works exactly the same

## Files You Might Need to Know

```
packaging/
  winget/
    New-WingetManifest.ps1          ← Generates winget manifests
  scoop/
    New-ScoopManifest.ps1           ← Generates Scoop manifest
  chocolatey/
    New-ChocolateyPackage.ps1       ← Generates & submits Chocolatey
  README.md                          ← Script reference
help/
  package-managers.md                ← User guide (submission & install)
  releasing.md                       ← Release process (updated with links)
.github/workflows/
  release.yml                        ← CI workflow (now with manifest jobs)
DISTRIBUTION_IMPLEMENTATION.md       ← Full technical details
```

## First Release Checklist

- [ ] Review the updated workflow: `.github/workflows/release.yml`
- [ ] Ensure `CHOC_KEY` secret is still set (for Chocolatey auto-submit)
- [ ] Read `help/package-managers.md` to understand submission process
- [ ] Tag v1.2.3: `git tag v1.2.3 && git push origin v1.2.3`
- [ ] Let workflow run to completion
- [ ] Download winget and Scoop manifests from artifacts
- [ ] (Optional) Test manifests locally
- [ ] (Optional) Create PRs to winget-pkgs and/or Scoop buckets
- [ ] Celebrate! 🎉
