# Installs caveira, the terminal client, on Windows (PowerShell 5.1 or 7):
#
#   irm https://caveira.app/install.ps1 | iex
#
# It downloads the latest release built for this machine from the public
# justin06lee/caveira-releases repo on GitHub, checks it against the
# release's checksums.txt, puts caveira.exe in ~\.local\bin with a cav.exe
# beside it, and adds that folder to your PATH. Run it again to update:
# caveira does not update itself on Windows. Builds are not signed.
#
#   $env:CAVEIRA_VERSION      = 'v0.2.0'          that release instead of the latest
#   $env:CAVEIRA_INSTALL_DIR  = 'C:\tools\caveira'  somewhere other than ~\.local\bin
#   $env:CAVEIRA_DOWNLOAD_URL = '<url>'           a mirror of a release's files
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # the progress bar makes downloads slow in Windows PowerShell
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function Fail($msg) { Write-Error "caveira: $msg"; exit 1 }

$repo = 'justin06lee/caveira-releases'
$dir = if ($env:CAVEIRA_INSTALL_DIR) { $env:CAVEIRA_INSTALL_DIR } else { Join-Path $env:USERPROFILE '.local\bin' }

# Releases carry amd64 only; Windows 11 on ARM runs it under its own emulation.
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'amd64' }
  default { Fail "there is no caveira built for $($env:PROCESSOR_ARCHITECTURE)" }
}

$base = if ($env:CAVEIRA_DOWNLOAD_URL) { $env:CAVEIRA_DOWNLOAD_URL.TrimEnd('/') }
        elseif ($env:CAVEIRA_VERSION) { "https://github.com/$repo/releases/download/$($env:CAVEIRA_VERSION)" }
        else { "https://github.com/$repo/releases/latest/download" }
$asset = "caveira_windows_$arch.zip"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("caveira-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading caveira for windows/$arch"
  try { Invoke-WebRequest -UseBasicParsing "$base/$asset" -OutFile (Join-Path $tmp $asset) } catch { Fail "could not download $base/$asset" }
  try { Invoke-WebRequest -UseBasicParsing "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') } catch { Fail "could not download $base/checksums.txt" }

  $want = (Get-Content (Join-Path $tmp 'checksums.txt') | ForEach-Object { $f = $_ -split '\s+'; if ($f.Count -eq 2 -and $f[1].TrimStart('*') -eq $asset) { $f[0] } } | Select-Object -First 1)
  $got = (Get-FileHash (Join-Path $tmp $asset) -Algorithm SHA256).Hash.ToLower()
  if (-not $want -or $want.ToLower() -ne $got) { Fail "$asset does not match the release's checksum; nothing was installed" }

  Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force
  if (-not (Test-Path (Join-Path $tmp 'caveira.exe'))) { Fail "$asset has no caveira.exe in it" }

  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  foreach ($name in 'caveira.exe', 'cav.exe') {
    $target = Join-Path $dir $name
    # A running .exe cannot be overwritten or deleted, but it can be renamed:
    # the old one is moved aside, and removed now or on the next run.
    Get-ChildItem $dir -Filter "$name.old*" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
    if (Test-Path $target) { Move-Item -Force $target "$target.old$([DateTime]::Now.Ticks)" }
    Copy-Item (Join-Path $tmp 'caveira.exe') $target
    Get-ChildItem $dir -Filter "$name.old*" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

Write-Host "Installed $(& (Join-Path $dir 'caveira.exe') --version) in $dir"
$onPath = ($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $dir.TrimEnd('\') }
if (-not $onPath) {
  $user = [Environment]::GetEnvironmentVariable('Path', 'User')
  [Environment]::SetEnvironmentVariable('Path', ($(if ($user) { $user.TrimEnd(';') + ';' }) + $dir), 'User')
  $env:Path = $env:Path.TrimEnd(';') + ';' + $dir
  Write-Host "Added $dir to your PATH. Terminals already open elsewhere need reopening."
}
Write-Host ""
Write-Host "Run caveira in a project and type /login."
