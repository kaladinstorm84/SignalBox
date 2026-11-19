# Distribution Documentation Index

This index helps you navigate all the new distribution documentation and scripts added for winget and Scoop support.

## Quick Links

**Getting Started:**
- [QUICKSTART_DISTRIBUTION.md](./QUICKSTART_DISTRIBUTION.md) ← **Start here** for your first release

**User Guides:**
- [help/package-managers.md](./help/package-managers.md) — How to submit to and install from each package manager

**Technical Documentation:**
- [DISTRIBUTION_IMPLEMENTATION.md](./DISTRIBUTION_IMPLEMENTATION.md) — What was added and why
- [DISTRIBUTION_ARCHITECTURE.md](./DISTRIBUTION_ARCHITECTURE.md) — Visual diagrams and flow charts
- [packaging/README.md](./packaging/README.md) — Reference for packaging scripts

**Troubleshooting:**
- [TROUBLESHOOTING_DISTRIBUTION.md](./TROUBLESHOOTING_DISTRIBUTION.md) — Solutions to common problems

**Original Release Process:**
- [help/releasing.md](./help/releasing.md) — Complete release procedure

---

## Document Guide

### 1. QUICKSTART_DISTRIBUTION.md
**For:** You need to know the basics quickly
**Contains:**
- 5-minute overview of what changed
- Release checklist
- Submission options (automatic vs. manual)
- Installation commands for end users

**Read this if:**
- You're preparing for your first release with these changes
- You need a quick reference

---

### 2. help/package-managers.md
**For:** Detailed submission and installation instructions
**Contains:**
- Detailed explanation of each package manager (winget, Scoop, Chocolatey)
- Step-by-step submission process
- Validation and approval timelines
- Local testing procedures
- Requirements and criteria
- Troubleshooting for each platform

**Read this if:**
- You're submitting to winget or Scoop
- You want to understand the differences between package managers
- You need to test manifests locally
- You need installation instructions for users

---

### 3. DISTRIBUTION_IMPLEMENTATION.md
**For:** Technical understanding of the implementation
**Contains:**
- Overview of changes made
- PowerShell scripts added
- GitHub Actions workflow updates
- Distribution channels summary
- Technical design decisions
- Requirements and checklist
- Future enhancement ideas

**Read this if:**
- You want to understand the architecture
- You're modifying the scripts or workflow
- You need to explain the changes to your team
- You want to know about design tradeoffs

---

### 4. DISTRIBUTION_ARCHITECTURE.md
**For:** Visual understanding of workflows and processes
**Contains:**
- Release workflow diagram
- Distribution channels diagram
- File flow visualization
- Decision tree for submissions
- Manifest generation process
- User installation paths
- Maintainer checklist

**Read this if:**
- You prefer visual explanations
- You want to understand the full workflow
- You're debugging a specific stage
- You need to plan your release timing

---

### 5. packaging/README.md
**For:** Reference documentation for packaging scripts
**Contains:**
- Detailed parameter descriptions for each script
- Prerequisites and requirements
- Output locations and formats
- Next steps after generation
- Common workflow examples
- Troubleshooting specific to scripts

**Read this if:**
- You need to run scripts locally
- You want to understand script parameters
- You're testing offline or manual releases
- You need API documentation

---

### 6. TROUBLESHOOTING_DISTRIBUTION.md
**For:** Problem-solving and debugging
**Contains:**
- Workflow job failures and solutions
- winget manifest validation issues
- Scoop JSON validation issues
- Submission approval stuck situations
- Local testing failures
- Hash mismatches and URL problems

**Read this if:**
- Something went wrong
- You got an error message
- Validation is failing
- Manifests aren't generating correctly

---

### 7. help/releasing.md
**For:** Complete release process (original + updates)
**Contains:**
- Overview of all distribution channels
- Step-by-step release procedure
- retag.ps1 usage
- Chocolatey submission process
- Cross-links to package manager docs

**Read this if:**
- You need the complete release procedure
- You want to understand the full pipeline
- You need to understand tag versioning
- You're updating release documentation

---

## Which Document Should I Read?

```
I need to...                               Read this...
─────────────────────────────────────────────────────────────
Release a new version                      QUICKSTART_DISTRIBUTION.md
Understand the workflow                    DISTRIBUTION_ARCHITECTURE.md
Submit to winget                           help/package-managers.md
Submit to Scoop                            help/package-managers.md
Create a custom Scoop bucket               help/package-managers.md
Something is broken                        TROUBLESHOOTING_DISTRIBUTION.md
Understand what was added                  DISTRIBUTION_IMPLEMENTATION.md
Run scripts locally                        packaging/README.md
Explain this to my team                    DISTRIBUTION_IMPLEMENTATION.md
Test manifests locally                     help/package-managers.md
Complete release process                   help/releasing.md
Get manifests back for offline release     packaging/README.md
```

---

## PowerShell Scripts

All scripts are located in the `packaging/` directory:

| Script | Location | Purpose |
|--------|----------|---------|
| `New-WingetManifest.ps1` | `packaging/winget/` | Generate winget YAML manifests |
| `New-ScoopManifest.ps1` | `packaging/scoop/` | Generate Scoop JSON manifest |
| `New-ChocolateyPackage.ps1` | `packaging/chocolatey/` | Generate & submit Chocolatey package |

Each script:
- Downloads and verifies checksums
- Generates platform-specific manifests
- Outputs ready-to-submit files
- Provides helpful next-step messages

**Usage:**
```powershell
.\packaging\winget\New-WingetManifest.ps1 -Tag v1.2.3 -OutputDirectory dist
.\packaging\scoop\New-ScoopManifest.ps1 -Tag v1.2.3 -OutputDirectory dist
.\packaging\chocolatey\New-ChocolateyPackage.ps1 -Tag v1.2.3 -OutputDirectory dist
```

See [packaging/README.md](./packaging/README.md) for full reference.

---

## CI/CD Workflow Changes

**Modified:** `.github/workflows/release.yml`

**Added jobs:**
- `winget-manifest` — Generates and uploads winget manifests
- `scoop-manifest` — Generates and uploads Scoop manifest

**Behavior:**
- Both run after `publish` job
- Only run for stable releases (non-prerelease)
- Download manifests from GitHub Actions artifacts (90-day retention)

**Timeline:**
```
test + build + build-unix (parallel)
           ↓
       publish (create release)
           ↓
  chocolatey + winget-manifest + scoop-manifest (parallel)
```

---

## Distribution Channels

### Chocolatey
- **Automation:** Automatic submission with CI
- **Secret:** `CHOC_KEY` (required)
- **Approval:** Days (automated + human)
- **Install:** `choco install signalbox`
- **Link:** https://community.chocolatey.org/packages/signalbox

### Windows Package Manager (winget)
- **Automation:** Manifest generation (manual PR)
- **Repository:** https://github.com/microsoft/winget-pkgs
- **Approval:** 1-2 weeks (human review)
- **Install:** `winget install kaladinstorm84.SignalBox`
- **Docs:** help/package-managers.md

### Scoop
- **Automation:** Manifest generation (manual PR)
- **Repository:** https://github.com/ScoopInstaller/Main-Bucket
- **Criteria:** 500+ stars, 150+ forks (for main bucket)
- **Fallback:** Custom bucket if criteria not met
- **Approval:** 3-5 days (human review)
- **Install:** `scoop install signalbox`
- **Docs:** help/package-managers.md

---

## First Release Checklist

- [ ] Review `.github/workflows/release.yml` changes
- [ ] Read [QUICKSTART_DISTRIBUTION.md](./QUICKSTART_DISTRIBUTION.md)
- [ ] Verify `CHOC_KEY` secret is set (for Chocolatey)
- [ ] Tag release: `git tag v1.0.0 && git push origin v1.0.0`
- [ ] Wait for workflow completion (10-15 minutes)
- [ ] Download `winget-manifest` artifact from run
- [ ] Download `scoop-manifest` artifact from run
- [ ] (Optional) Test manifests locally
- [ ] (Optional) Create PR to winget-pkgs
- [ ] (Optional) Create PR to Scoop bucket

---

## Common Questions

**Q: Do I need to do anything for Chocolatey?**
A: No! If `CHOC_KEY` secret is set, it's automatic.

**Q: Do I need to submit to winget and Scoop?**
A: No, it's optional. Manifests are generated for you to submit whenever you want.

**Q: What if my repo doesn't meet Scoop criteria?**
A: Create a custom Scoop bucket in your GitHub org instead.

**Q: How long does approval take?**
A: Chocolatey: days | winget: 1-2 weeks | Scoop: 3-5 days

**Q: Can I test locally before submitting?**
A: Yes! See [help/package-managers.md](./help/package-managers.md) for instructions.

**Q: What if something fails?**
A: See [TROUBLESHOOTING_DISTRIBUTION.md](./TROUBLESHOOTING_DISTRIBUTION.md).

---

## Support

Need help?

1. **Check the relevant guide** using the "Which Document Should I Read?" table above
2. **Search for your issue** in [TROUBLESHOOTING_DISTRIBUTION.md](./TROUBLESHOOTING_DISTRIBUTION.md)
3. **Review workflow logs** in GitHub Actions for step-by-step output
4. **Download artifacts** from the failed/successful run to inspect manifests

---

## File Structure

```
signalbox/
├── .github/workflows/
│   └── release.yml                          ← Updated with new jobs
├── help/
│   ├── releasing.md                         ← Updated with overview
│   └── package-managers.md                  ← New user guide
├── packaging/
│   ├── README.md                            ← New script reference
│   ├── chocolatey/
│   │   └── New-ChocolateyPackage.ps1
│   ├── winget/
│   │   └── New-WingetManifest.ps1           ← New
│   └── scoop/
│       └── New-ScoopManifest.ps1            ← New
├── DISTRIBUTION_IMPLEMENTATION.md           ← New technical details
├── DISTRIBUTION_ARCHITECTURE.md             ← New visual guide
├── QUICKSTART_DISTRIBUTION.md               ← New quick reference
├── TROUBLESHOOTING_DISTRIBUTION.md          ← New troubleshooting
└── DISTRIBUTION_INDEX.md                    ← This file
```

---

## Version History

**v1.0** - Initial Implementation
- Added winget manifest generation
- Added Scoop manifest generation
- Enhanced GitHub Actions workflow
- Comprehensive documentation suite
- Ready for production use
