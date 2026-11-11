
$ErrorActionPreference = "Stop"


git bundle create ..\SignalBox-before-author-rewrite.bundle --all


$pythonCommand = Get-Command py -ErrorAction SilentlyContinue
if (-not $pythonCommand) {
    $pythonCommand = Get-Command python -ErrorAction SilentlyContinue
}
if (-not $pythonCommand) {
    throw "Python is required to install git-filter-repo. Install Python and ensure 'py' or 'python' is available."
}

$pythonExe = $pythonCommand.Source
$scriptsPath = (& $pythonExe -c "import sysconfig; print(sysconfig.get_path('scripts', 'nt_user'))").Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($scriptsPath)) {
    throw "Could not locate Python's user Scripts directory."
}

$filterRepoExe = Join-Path $scriptsPath "git-filter-repo.exe"
if (-not (Test-Path $filterRepoExe)) {
    & $pythonExe -m pip install --user git-filter-repo
    if ($LASTEXITCODE -ne 0) {
        throw "Installing git-filter-repo failed."
    }
}

if (-not (Test-Path $filterRepoExe)) {
    throw "git-filter-repo.exe was not found at $filterRepoExe"
}

$callback = @'
if commit.author_email == b"matt.anderson@fleetcor.com":
    commit.author_email = b"84manderson@googlemail.com"
if commit.committer_email == b"matt.anderson@fleetcor.com":
    commit.committer_email = b"84manderson@googlemail.com"
'@

& $filterRepoExe --commit-callback $callback --force
if ($LASTEXITCODE -ne 0) {
    throw "git-filter-repo failed."
}