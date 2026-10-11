# bicz installer (Windows PowerShell 5.1+ / PowerShell 7). User-scope only.
#
# Downloads a release zip plus SHA256SUMS, verifies the checksum, and only then
# replaces <InstallDir>\bicz.exe. A mismatch or missing checksum aborts with the
# existing install untouched.
#   irm https://raw.githubusercontent.com/jakubkrzysztofsikora/bicz/main/scripts/install.ps1 | iex
# Releases come from github.com/jakubkrzysztofsikora/bicz unless you override:
#   -Repo owner/repo     GitHub repository that publishes the releases
#   -BaseUrl URL         directory containing the zip and SHA256SUMS
#                        (https, or http on loopback for tests)
#   -Version vX.Y.Z      release tag (default: latest; ignored with -BaseUrl)
#   -InstallDir DIR      default: $env:LOCALAPPDATA\bicz\bin
#   -Os / -Arch          override detection (windows|linux|darwin, amd64|arm64);
#                        used by the local fixture tests
#
# The checksum guards against a corrupted or swapped archive relative to the
# SHA256SUMS from the same origin. It is not a signature.
[CmdletBinding()]
param(
  [string]$Repo = $(if ($env:BICZ_REPO) { $env:BICZ_REPO } else { 'jakubkrzysztofsikora/bicz' }),
  [string]$BaseUrl = $env:BICZ_BASE_URL,
  [string]$Version = $env:BICZ_VERSION,
  [string]$InstallDir = $env:BICZ_INSTALL_DIR,
  [string]$Os = '',
  [string]$Arch = ''
)
$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
  throw "bicz-install: $Message"
}

if (-not $InstallDir) {
  $root = $env:LOCALAPPDATA
  if (-not $root) { $root = Join-Path $HOME '.local' }
  $InstallDir = Join-Path (Join-Path $root 'bicz') 'bin'
}

if (-not $Os) { $Os = 'windows' }
if (-not $Arch) {
  $Arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
    'X64'   { 'amd64' }
    'Arm64' { 'arm64' }
    default { Fail "unsupported architecture $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
  }
}
$ext = if ($Os -eq 'windows') { 'zip' } else { Fail "unsupported OS '$Os' for this script (use install.sh on linux/darwin)" }
$asset = "bicz_${Os}_${Arch}.$ext"
$exeName = 'bicz.exe'

if ($BaseUrl) {
  $base = $BaseUrl.TrimEnd('/')
} else {
  if ($Repo -notmatch '^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$') { Fail 'Repo must look like owner/repo' }
  if ($Version) { $base = "https://github.com/$Repo/releases/download/$Version" }
  else { $base = "https://github.com/$Repo/releases/latest/download" }
}
if ($base -notmatch '^https://' -and $base -notmatch '^http://(127\.0\.0\.1|localhost|\[::1\])([:/]|$)') {
  Fail "refusing non-https release source: $base"
}

# Target safety, before any download.
function Test-Reparse([string]$Path) {
  if (-not (Test-Path -LiteralPath $Path)) { return $false }
  return [bool]((Get-Item -LiteralPath $Path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)
}
$target = Join-Path $InstallDir $exeName
if (Test-Reparse $InstallDir) { Fail "install dir $InstallDir is a symlink/junction; refusing" }
if (Test-Reparse $target) { Fail "$target is a symlink; refusing to replace it" }
if ((Test-Path -LiteralPath $target) -and -not (Test-Path -LiteralPath $target -PathType Leaf)) {
  Fail "$target exists and is not a regular file; refusing"
}

$work = Join-Path ([IO.Path]::GetTempPath()) ("bicz-install." + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
  $ProgressPreference = 'SilentlyContinue'
  try { Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile (Join-Path $work 'SHA256SUMS') -TimeoutSec 120 }
  catch { Fail "could not download $base/SHA256SUMS ($($_.Exception.Message))" }
  try { Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $work $asset) -TimeoutSec 120 }
  catch { Fail "could not download $base/$asset ($($_.Exception.Message))" }

  $want = $null
  foreach ($line in Get-Content -LiteralPath (Join-Path $work 'SHA256SUMS')) {
    $parts = $line.Trim() -split '\s+', 2
    if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $asset) { $want = $parts[0].ToLowerInvariant(); break }
  }
  if (-not $want) { Fail "SHA256SUMS has no entry for $asset; refusing to install" }
  $got = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $work $asset)).Hash.ToLowerInvariant()
  if ($want -ne $got) { Fail "checksum mismatch for ${asset}: SHA256SUMS says $want, downloaded file is $got; nothing installed" }

  # Validate the zip entry list BEFORE extracting anything: exactly one regular
  # bicz.exe (plus an optional LICENSE), no path separators, "..", directories or
  # symlink entries. Only bicz.exe is extracted, by streaming its content into a
  # file we name ourselves; entry names are never used as output paths. This
  # guards the common malicious-archive paths; it is not a containment boundary.
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $extract = Join-Path $work 'x'
  New-Item -ItemType Directory -Path $extract | Out-Null
  $exe = Join-Path $extract $exeName
  $zip = [System.IO.Compression.ZipFile]::OpenRead((Join-Path $work $asset))
  try {
    $found = 0
    foreach ($entry in $zip.Entries) {
      $name = $entry.FullName
      if ($name -ne $exeName -and $name -ne 'LICENSE') { Fail "unexpected archive member '$name'; refusing (expected only $exeName and LICENSE)" }
      if ((($entry.ExternalAttributes -shr 28) -band 0xF) -eq 0xA) { Fail "archive member '$name' is a symlink; refusing" }
      if ($name -eq $exeName) { $found++ }
    }
    if ($found -ne 1) { Fail "archive must contain exactly one $exeName; refusing" }
    $binEntry = $zip.Entries | Where-Object { $_.FullName -eq $exeName } | Select-Object -First 1
    [System.IO.Compression.ZipFileExtensions]::ExtractToFile($binEntry, $exe, $false)
  } finally { $zip.Dispose() }
  if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { Fail "extracted $exeName is not a regular file" }

  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  if (Test-Reparse $InstallDir) { Fail "install dir $InstallDir is a symlink/junction; refusing" }
  if (Test-Path -LiteralPath $target -PathType Leaf) {
    Copy-Item -LiteralPath $target -Destination (Join-Path $InstallDir 'bicz.previous.exe') -Force
  }
  $staged = Join-Path $InstallDir (".bicz.new." + [Guid]::NewGuid().ToString('N') + '.exe')
  Copy-Item -LiteralPath $exe -Destination $staged
  # Same-directory rename: the target is either the old file or the new file.
  Move-Item -LiteralPath $staged -Destination $target -Force
  Write-Output "bicz-install: installed $target (sha256 $got)"
  if (Test-Path -LiteralPath (Join-Path $InstallDir 'bicz.previous.exe')) {
    Write-Output "bicz-install: previous binary kept at $(Join-Path $InstallDir 'bicz.previous.exe') (rollback: move it back over bicz.exe)"
  }
  $onPath = ($env:PATH -split [IO.Path]::PathSeparator) -contains $InstallDir
  if (-not $onPath) { Write-Output "bicz-install: $InstallDir is not on PATH; add it yourself (this script does not edit PATH)" }
  Write-Output "bicz-install: the downloaded binary was not executed by this script; run: $target version"
} finally {
  Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
