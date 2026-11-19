# SignalBox Distribution Architecture

## Release Workflow Overview

```
Developer creates tag: git tag v1.2.3
                        ↓
              git push origin v1.2.3
                        ↓
        GitHub Actions Release Workflow
                        ↓
    ┌─────────────────────────────────────────┐
    │  Test & Build Stage (Parallel)          │
    ├─────────────────────────────────────────┤
    │  • Full test suite (Linux)              │
    │  • Build Windows x64                    │
    │  • Build Linux x64 & ARM64              │
    │  • Build macOS Intel & Apple Silicon    │
    └─────────────────────────────────────────┘
                        ↓ (all pass)
    ┌─────────────────────────────────────────┐
    │  Publish Job                            │
    ├─────────────────────────────────────────┤
    │  • Verify SHA256 checksums              │
    │  • Create GitHub Release                │
    │  • Upload archives & signatures         │
    └─────────────────────────────────────────┘
                        ↓ (success)
    ┌─────────────────────────────────────────────────────────────┐
    │  Post-Release Jobs (Only for stable releases, not -rc.1)    │
    ├─────────────────────────────────────────────────────────────┤
    │                                                               │
    │  ┌─────────────────┐  ┌──────────────┐  ┌──────────────┐    │
    │  │  Chocolatey     │  │   winget     │  │    Scoop     │    │
    │  ├─────────────────┤  ├──────────────┤  ├──────────────┤    │
    │  │ AUTOMATIC ✓     │  │ MANUAL PR ○  │  │ MANUAL PR ○  │    │
    │  │                 │  │              │  │              │    │
    │  │ • Build package │  │ • Generate   │  │ • Generate   │    │
    │  │ • Run tests     │  │   YAML files │  │   JSON file  │    │
    │  │ • Push to repo  │  │ • Upload as  │  │ • Upload as  │    │
    │  │ (needs CHOC_KEY)│  │   artifact   │  │   artifact   │    │
    │  │                 │  │ • Ready for  │  │ • Ready for  │    │
    │  │ Approval: Days  │  │   PR submit  │  │   PR submit  │    │
    │  │ Publish: Days   │  │              │  │              │    │
    │  │                 │  │ Approval:    │  │ Approval:    │    │
    │  │ Command:        │  │ 1-2 weeks    │  │ 3-5 days (if │    │
    │  │ choco install   │  │              │  │ criteria met)│    │
    │  │  signalbox      │  │ Command:     │  │              │    │
    │  │                 │  │ winget       │  │ Command:     │    │
    │  │                 │  │  install     │  │ scoop        │    │
    │  │                 │  │  kaladinstorm│  │  install     │    │
    │  │                 │  │  84.SignalBox│  │  signalbox   │    │
    │  └─────────────────┘  └──────────────┘  └──────────────┘    │
    │                                                               │
    │  ✓ = Automatic      ○ = Requires manual PR submission        │
    └─────────────────────────────────────────────────────────────┘
```

## Distribution Channels

```
                        GitHub Release
                             ↓
        ┌────────────────────┼────────────────────┐
        ↓                    ↓                    ↓
    Direct Download    Chocolatey         winget + Scoop
    (All platforms)    (Windows only)   (Windows only)
        ↓                    ↓                    ↓
    ZIP/TAR.GZ         .nupkg package      YAML/JSON
    + SHA256           (Auto-submitted)   (Manual PR)
```

## File Flow

```
Release Tag: v1.2.3
     ↓
GitHub Actions Checkout
     ↓
Build Artifacts:
  • signalbox-v1.2.3-windows-amd64.zip
  • signalbox-v1.2.3-linux-amd64.tar.gz
  • signalbox-v1.2.3-linux-arm64.tar.gz
  • signalbox-v1.2.3-darwin-amd64.tar.gz
  • signalbox-v1.2.3-darwin-arm64.tar.gz
  • SHA256SUMS.txt
     ↓
Publishing Pipeline:
     │
     ├─→ Chocolatey Job
     │   ├─ Download Windows ZIP
     │   ├─ Verify SHA256
     │   ├─ Create .nuspec
     │   ├─ Run: choco pack
     │   ├─ Test: choco install/uninstall
     │   └─ Push: choco push (with CHOC_KEY secret)
     │
     ├─→ winget Manifest Job
     │   ├─ Download Windows ZIP
     │   ├─ Verify SHA256
     │   ├─ Generate: installer.yaml
     │   ├─ Generate: locale.yaml
     │   ├─ Generate: version.yaml
     │   └─ Upload: GitHub Actions artifact
     │       └─ Ready for PR to: github.com/microsoft/winget-pkgs
     │
     └─→ Scoop Manifest Job
         ├─ Download Windows ZIP
         ├─ Verify SHA256
         ├─ Generate: signalbox.json
         └─ Upload: GitHub Actions artifact
             └─ Ready for PR to: github.com/ScoopInstaller/Main-Bucket
                 (or custom bucket)
```

## Decision Tree: Where to Submit

```
                    Release v1.2.3 Ready
                             ↓
                    Is it a stable release?
                      (no '-rc.1' suffix?)
                             ↓
                    ┌────────┴────────┐
                   NO               YES
                    ↓                ↓
            Skip submissions     Continue
                                     ↓
                        Does your GitHub have
                        500+ stars & 150+ forks?
                                     ↓
                        ┌────────────┴────────────┐
                       NO                        YES
                        ↓                         ↓
                  Custom Scoop Bucket      Submit to Main Buckets
                        ↓                         ↓
                  1. Create repo              winget-pkgs
                  2. Add manifest             Main-Bucket (Scoop)
                  3. Users run:               │
                  scoop bucket add            └─→ Full distribution
                  signalbox https://...           across all channels
```

## Manifest Generation Process

```
For winget:
  Release v1.2.3 published
           ↓
  New-WingetManifest.ps1
           ↓
  Download: signalbox-v1.2.3-windows-amd64.zip
  Download: SHA256SUMS.txt
           ↓
  Verify: Hash matches SHA256SUMS.txt entry
           ↓
  Generate 3 YAML files:
    • kaladinstorm84.SignalBox.installer.yaml
    • kaladinstorm84.SignalBox.locale.en-US.yaml
    • kaladinstorm84.SignalBox.yaml
           ↓
  Upload as GitHub Actions artifact
           ↓
  Ready to submit PR to: manifests/k/kaladinstorm84/SignalBox/1.2.3/

For scoop:
  Release v1.2.3 published
           ↓
  New-ScoopManifest.ps1
           ↓
  Download: signalbox-v1.2.3-windows-amd64.zip
  Download: SHA256SUMS.txt
           ↓
  Verify: Hash matches SHA256SUMS.txt entry
           ↓
  Generate JSON manifest:
    • version
    • url
    • hash (verified)
    • bin
    • checkver (auto-discovery)
    • autoupdate (auto-update config)
           ↓
  Upload as GitHub Actions artifact
           ↓
  Ready to submit PR to: bucket/signalbox.json
```

## User Installation Paths

```
User wants SignalBox
       ↓
   winget installed?
   ├─ YES → winget install kaladinstorm84.SignalBox
   │        ↓
   │        (Requires approved PR to microsoft/winget-pkgs)
   │
   └─ NO
      ↓
   Scoop installed?
   ├─ YES → scoop install signalbox
   │        ├─ Main bucket (if criteria met)
   │        │  └─ scoop bucket add signalbox https://github.com/ScoopInstaller/Main-Bucket
   │        │
   │        └─ Custom bucket (if criteria not met)
   │           └─ scoop bucket add signalbox https://github.com/kaladinstorm84/scoop-signalbox
   │
   └─ NO
      ↓
   Chocolatey installed?
   ├─ YES → choco install signalbox
   │
   └─ NO
      ↓
   Download from GitHub
      └─ https://github.com/kaladinstorm84/SignalBox/releases
```

## Checklist for Maintainer

```
Release Pipeline:
  ☐ Commit all changes
  ☐ Create tag: git tag v1.2.3
  ☐ Push tag: git push origin v1.2.3
  ☐ Wait for GitHub Actions (10-15 minutes)
  
Review Results:
  ☐ All test jobs pass ✓
  ☐ GitHub Release created with all binaries ✓
  ☐ Chocolatey submission logged (if CHOC_KEY set) ✓
  
Post-Release (if stable release):
  ☐ Download winget-manifest artifact from run
  ☐ Download scoop-manifest artifact from run
  ☐ (Optional) Test locally: 
     - winget validate manifests/.../kaladinstorm84.SignalBox.yaml
     - scoop install ./signalbox.json
  ☐ Create PR to winget-pkgs (optional but recommended)
  ☐ Create PR to Scoop bucket (optional):
     - Main bucket (if 500+ stars, 150+ forks)
     - Custom bucket (alternative)
  ☐ Monitor PR reviews
  ☐ Celebrate when approved! 🎉
```

## See Also

- [help/package-managers.md](../help/package-managers.md) — Full submission guide
- [packaging/README.md](../packaging/README.md) — Script reference
- [DISTRIBUTION_IMPLEMENTATION.md](../DISTRIBUTION_IMPLEMENTATION.md) — Technical details
- [QUICKSTART_DISTRIBUTION.md](../QUICKSTART_DISTRIBUTION.md) — Quick reference
