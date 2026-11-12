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
automatically marks suffixed versions as prereleases. It uses GitHub's built-in
`GITHUB_TOKEN`; no personal access token or signing secrets are needed.
Only the publishing job has repository-content write permission.

Existing releases are not overwritten. If upload or publication fails after a
draft is created, inspect that draft before retrying: finish publishing it
manually, or delete the incomplete draft before rerunning the workflow.
