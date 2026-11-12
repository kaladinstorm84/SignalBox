# Installation and updates

[Back to SignalBox](../README.md)

## Getting started

Building from source requires Go 1.25 or newer. The scaffolded default pipeline
uses the `mock` runtime, so no AI credentials are needed to try it. For real
runtimes, install/authenticate the relevant CLI or set the API key environment
variable configured for the OpenAI-compatible runtime. Code steps require
their language's interpreter on `PATH`.

### Install from GitHub

Install [Go](https://go.dev/dl/) 1.25 or newer, then run:

```powershell
go install github.com/kaladinstorm84/SignalBox/cmd/signalbox@latest
```

This builds the latest released version and installs only the executable, not
a project. Go installs it into `GOBIN` when set, otherwise the first
`GOPATH` directory's `bin` folder. On Windows the default is
`C:\Users\<your-user>\go\bin\signalbox.exe`. Check your settings with:

```powershell
go env GOBIN GOPATH
```

#### Add the install directory to PATH on Windows

If `signalbox` is not recognized, run the following in PowerShell. It adds
the effective Go install directory permanently to your **user Path**, preserving
existing entries, and also enables it in the current terminal. Administrator
rights are not required.

```powershell
$bin = go env GOBIN
if (-not $bin) {
    $goPath = go env GOPATH
    $bin = Join-Path (($goPath -split ';')[0]) 'bin'
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$entries = @($userPath -split ';' | Where-Object { $_ })
if ($entries -notcontains $bin) {
    [Environment]::SetEnvironmentVariable('Path', (($entries + $bin) -join ';'), 'User')
}
if (($env:Path -split ';') -notcontains $bin) {
    $env:Path += ";$bin"
}

signalbox version
```

Fully close and reopen VS Code and any other terminals so future sessions pick
up the permanent change. On macOS/Linux, add `GOBIN` (or `$(go env GOPATH)/bin`
when unset) to `PATH` in your shell profile.

#### Create and run a project

```powershell
signalbox init my-project     # creates config, starter pipeline, .gitignore, and AGENTS.md
Set-Location my-project
signalbox validate            # check config and all pipelines
signalbox serve               # web UI on http://127.0.0.1:8484
```

Open `http://127.0.0.1:8484`. `serve` runs until stopped; use another terminal
in the project directory for a terminal execution, or stop the server first:

```powershell
signalbox run default --input "Hello SignalBox"
```

`signalbox init` also creates an `AGENTS.md` project guide with layout,
workflow, and pipeline-authoring conventions, plus a `.gitignore` for local
execution state. Existing scaffold files are left untouched by default; pass
`--force` to overwrite them.

#### Update an installed version

Stop the running SignalBox server and allow active executions/workers to finish
or cancel them before replacing the executable. Then reinstall:

```powershell
go install github.com/kaladinstorm84/SignalBox/cmd/signalbox@latest
signalbox version
```

Your projects, configuration, and execution history are not replaced. Restart
`signalbox serve` from your project directory afterward. To select a specific
published version instead, replace `@latest` with its tag, for example:

```powershell
go install github.com/kaladinstorm84/SignalBox/cmd/signalbox@v1.0.0
```

`@latest` selects a published module version, not unreleased changes on the
default branch. Older releases before the module-version reporting fix may
print `dev` even when installed from a version tag. To see the version embedded
in the installed executable, run:

```powershell
go version -m (Get-Command signalbox).Source
```

If you see an unexpected version, `Get-Command signalbox -All` lists matching
commands and their paths; an older executable earlier on `PATH` may be taking
precedence.

### Build and run locally on Windows

```powershell
git clone https://github.com/kaladinstorm84/SignalBox.git
Set-Location SignalBox
go build -o signalbox.exe ./cmd/signalbox
.\signalbox.exe init my-project
Set-Location my-project
..\signalbox.exe validate
..\signalbox.exe serve
```

Open `http://127.0.0.1:8484`. From the project directory, a terminal execution is
`..\signalbox.exe run default --input "Hello SignalBox"`. From the repository
root you can also use `go run ./cmd/signalbox` followed by a CLI command. Running
`go run .` is not supported because the entry point is in `cmd/signalbox`.

After changing Go source or the embedded UI, rebuild from the repository root
and restart the executable. Pipeline YAML is reloaded for new UI runs, but
runtime definitions and other project settings require a server restart.

### Downloadable releases

Platform builds are published on the
[GitHub Releases page](https://github.com/kaladinstorm84/SignalBox/releases).
Choose the archive matching your operating system and processor:

| Platform | Archive |
|----------|---------|
| Windows x64 | `signalbox-v<VERSION>-windows-amd64.zip` |
| Linux x64 | `signalbox-v<VERSION>-linux-amd64.tar.gz` |
| Linux ARM64 | `signalbox-v<VERSION>-linux-arm64.tar.gz` |
| macOS Intel | `signalbox-v<VERSION>-darwin-amd64.tar.gz` |
| macOS Apple Silicon | `signalbox-v<VERSION>-darwin-arm64.tar.gz` |

Extract the ZIP on Windows and run `signalbox.exe`. On Linux/macOS, extract
the `.tar.gz` archive with `tar -xzf` and run `./signalbox`; the archive preserves
the executable permission. macOS builds are not Developer ID signed or notarized.
Each archive contains the executable (including the embedded web UI), README,
help guides and screenshots, and license. Agent runtimes such as Codex, Claude,
Copilot, and optional tools such as Python or OpenSpec must still be installed
separately. Older releases may only include the Windows build.

Each release includes `SHA256SUMS.txt` covering every archive. Compare your
archive's checksum with its entry before
extracting it:

```powershell
Get-FileHash .\signalbox-v1.0.0-windows-amd64.zip -Algorithm SHA256
Get-Content .\SHA256SUMS.txt
```

On Linux use `sha256sum <archive>`; on macOS use `shasum -a 256 <archive>`.
