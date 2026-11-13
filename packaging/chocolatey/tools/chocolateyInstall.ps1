$ErrorActionPreference = 'Stop'

$packageArgs = @{
  PackageName    = $env:ChocolateyPackageName
  UnzipLocation  = Split-Path -Parent $MyInvocation.MyCommand.Definition
  Url64bit       = 'https://github.com/{{REPOSITORY}}/releases/download/{{TAG}}/signalbox-{{TAG}}-windows-amd64.zip'
  Checksum64     = '{{SHA256}}'
  ChecksumType64 = 'sha256'
}

Install-ChocolateyZipPackage @packageArgs
