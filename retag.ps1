param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Arguments
)

$ErrorActionPreference = 'Stop'

function Invoke-RepositoryGit {
    param([string[]]$GitArguments)

    $output = & git -C $PSScriptRoot @GitArguments
    if ($LASTEXITCODE -ne 0) {
        throw "git $($GitArguments -join ' ') failed (exit $LASTEXITCODE)."
    }
    return $output
}

if ($Arguments.Count -ne 1 -or $Arguments[0] -notin @('--revision', '--minor', '--major')) {
    throw 'Usage: .\retag.ps1 --revision | --minor | --major'
}

$versions = @(
    Invoke-RepositoryGit -GitArguments @('tag', '--list') |
        Where-Object { $_ -cmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' } |
        ForEach-Object { [version]$_.Substring(1) } |
        Sort-Object -Descending
)
if ($versions.Count -eq 0) {
    throw 'No stable vMAJOR.MINOR.REVISION tags found. Fetch existing tags or create the initial version tag first.'
}

$current = $versions[0]
$major = $current.Major
$minor = $current.Minor
$revision = $current.Build
switch ($Arguments[0]) {
    '--revision' { $revision++ }
    '--minor' { $minor++; $revision = 0 }
    '--major' { $major++; $minor = 0; $revision = 0 }
}
$tag = "v$major.$minor.$revision"
Write-Host "Releasing v$current -> $tag from HEAD (committed changes only)."

Invoke-RepositoryGit -GitArguments @('push', 'origin', 'HEAD')
Invoke-RepositoryGit -GitArguments @('tag', $tag)
Invoke-RepositoryGit -GitArguments @('push', 'origin', $tag)