# Installs Strife for Windows (amd64 or arm64) from the latest release, after
# checking the archive against the release's SHA256SUMS.
#
#   irm https://<the Strife website>/install.ps1 | iex
#
# Environment:
#   STRIFE_VERSION      a release tag such as v0.2.0 (default: the latest)
#   STRIFE_INSTALL_DIR  where to install (default: %LOCALAPPDATA%\strife\bin)
#   STRIFE_RELEASES     base URL of the releases (default: the GitHub project)
& {
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'
  # Windows PowerShell 5.1 may not offer TLS 1.2 by default; GitHub needs it.
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

  $releases = if ($env:STRIFE_RELEASES) { $env:STRIFE_RELEASES } else { 'https://github.com/Kemalhafizh/strife/releases' }
  $dir = if ($env:STRIFE_INSTALL_DIR) { $env:STRIFE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'strife\bin' }

  $arch = 'amd64'
  try {
    if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { $arch = 'arm64' }
  } catch {
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { $arch = 'arm64' }
  }

  $version = $env:STRIFE_VERSION
  if (-not $version) {
    # The latest release page redirects to .../tag/<version>; without any
    # release it lands on the release list instead.
    $r = Invoke-WebRequest -UseBasicParsing -Method Head -Uri "$releases/latest"
    $final = if ($r.BaseResponse.ResponseUri) { $r.BaseResponse.ResponseUri.AbsoluteUri } else { $r.BaseResponse.RequestMessage.RequestUri.AbsoluteUri }
    if ($final -notmatch '/tag/([^/]+)$') {
      throw 'strife: no release has been published yet; build from source with: go install github.com/kemalhafizh/strife/cmd/strife@latest'
    }
    $version = $Matches[1]
  }

  $name = "strife_${version}_windows_$arch"
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString())
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    $zip = Join-Path $tmp "$name.zip"
    $sums = Join-Path $tmp 'SHA256SUMS'
    Write-Host "strife: downloading $name.zip"
    Invoke-WebRequest -UseBasicParsing -Uri "$releases/download/$version/$name.zip" -OutFile $zip
    Invoke-WebRequest -UseBasicParsing -Uri "$releases/download/$version/SHA256SUMS" -OutFile $sums

    $want = $null
    foreach ($line in Get-Content $sums) {
      $parts = $line -split '\s+', 2
      if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq "$name.zip") { $want = $parts[0].ToLower() }
    }
    if (-not $want) { throw "strife: $name.zip is not listed in SHA256SUMS" }
    $got = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLower()
    if ($want -ne $got) { throw "strife: checksum mismatch for $name.zip (expected $want, got $got); nothing was installed" }
    Write-Host 'strife: checksum ok'

    Expand-Archive -Path $zip -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item -Force -Path (Join-Path (Join-Path $tmp $name) 'strife.exe') -Destination (Join-Path $dir 'strife.exe')
    Write-Host "strife: installed $version in $dir"

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (($userPath -split ';') -contains $dir)) {
      [Environment]::SetEnvironmentVariable('Path', (($userPath, $dir) -ne '' -join ';'), 'User')
      Write-Host "strife: added $dir to your PATH; open a new terminal to use it"
    }
    Write-Host 'strife: next: strife sim --system raft-unsafe-fsync --faults kill,io-stall --seed 13'
  } finally {
    Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
  }
}
