# Publishing releases

[Back to SignalBox](../README.md)

## Publishing a release

The [release workflow](../.github/workflows/release.yml) runs when a tag starting
with `v` is pushed. It verifies dependencies, runs the Go tests, and builds
executables stamped with the tag for Windows x64, Linux x64/ARM64, and macOS
Intel/Apple Silicon. Each target builds on a matching native runner, validates
the example pipelines, and checks both the built and extracted executables.
Windows ships as a ZIP; Linux and macOS ship as `.tar.gz` archives, preserving
executable permissions. Every archive also includes the README, help guides,
screenshots, and license. macOS binaries are not Developer ID signed or notarized.
Only after all targets and tests succeed does the workflow create a GitHub
Release with generated notes, all five archives, and a combined `SHA256SUMS.txt`.
The checksums are verified after downloading all build artifacts, before release
creation. The release is initially a draft while its files upload, then published.

The full test suite runs on Linux, including the detached-worker binary
lifecycle test. All packaging jobs also run `go test -short ./...` on their
native architecture. Short mode skips the lifecycle test because GitHub-hosted
Windows jobs forbid the process breakaway it requires; the separate Linux
regression job retains full lifecycle coverage. Windows-specific unit tests
and executable/package checks still run. All jobs must pass before publication.
This does not relax the application's breakaway requirement: Windows hosts
running detached workers must permit it.

Commit and push the source and workflow first, then tag the commit to release:

```powershell
git tag v1.0.0
git push origin v1.0.0
```

For subsequent releases, [retag.ps1](../retag.ps1) selects the highest local stable
`vMAJOR.MINOR.REVISION` tag and increments it:

```powershell
git fetch origin --tags
.\retag.ps1 --revision   # v1.2.3 -> v1.2.4
.\retag.ps1 --minor      # v1.2.3 -> v1.3.0
.\retag.ps1 --major      # v1.2.3 -> v2.0.0
```

Choose exactly one flag. The script ignores prerelease tags, pushes the current
committed `HEAD` to `origin`, creates the new tag, and pushes it to trigger the
release workflow. Commit your changes first; the script does not commit files
or move existing tags. If a Git command fails, it stops without running the
remaining commands. If the tag push fails after local creation, resolve the
push error and push that tag explicitly rather than incrementing again.

Use `vMAJOR.MINOR.PATCH` for normal releases or a suffix such as
`v1.0.0-rc.1` for prereleases. The workflow rejects other tag formats and
automatically marks suffixed versions as prereleases. Publishing to GitHub uses
the built-in `GITHUB_TOKEN`; no personal access token or signing secrets are
needed. Only the publishing job has repository-content write permission. The
[Chocolatey job](#publishing-to-chocolatey) also needs the `CHOC_KEY` secret.

Existing releases are not overwritten. If upload or publication fails after a
draft is created, inspect that draft before retrying: finish publishing it
manually, or delete the incomplete draft before rerunning the workflow.

## Publishing to Chocolatey

After the GitHub Release is published, stable tags also run a `chocolatey` job
that submits the Windows build to the
[Chocolatey community repository](https://community.chocolatey.org/packages/signalbox).
Prerelease tags (those with a `-` suffix) are never submitted. The job:

1. Builds the package from the **published** Windows ZIP with
   [New-ChocolateyPackage.ps1](../packaging/chocolatey/New-ChocolateyPackage.ps1).
   The script requires the ZIP's SHA-256 to match its `SHA256SUMS.txt` entry and
   bakes that checksum into the install script, so Chocolatey verifies exactly
   what users download.
2. Installs and uninstalls the package on the runner, and requires
   `signalbox version` to report the tag.
3. Runs `choco push` with the `CHOC_KEY` repository secret.

Create `CHOC_KEY` once under **Settings > Secrets and variables > Actions**,
using the API key from your Chocolatey account page. The job fails with a clear
message if the secret is missing or empty.

A successful push only submits the version. Chocolatey moderates submitted
versions, so `choco install signalbox` does not find it until it is approved;
the job summary links the package page. If a moderator asks for changes,
rebuild and push the **same version** again. This needs
[Chocolatey](https://chocolatey.org/install) on `PATH`:

```powershell
.\packaging\chocolatey\New-ChocolateyPackage.ps1 -Tag v1.2.3 -OutputDirectory dist\choco
choco push dist\choco\signalbox.1.2.3.nupkg --source https://push.chocolatey.org/ --api-key <your-api-key>
```

The same commands submit a release made before the job existed. Never replace
the files of a published release: the package's checksum comes from them, so fix
problems with a new version. If only the `chocolatey` job fails for a transient
reason, re-run just that job. A re-run uses the workflow as it was at the tag,
so a bug in the job itself needs a new tag or the manual commands above.
