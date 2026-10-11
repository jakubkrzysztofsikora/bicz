# Fixture tests for scripts/install.ps1 against a LOCAL loopback HTTP server.
# Nothing here contacts a real release host or touches the real profile.
#   pwsh -File scripts/test_install.ps1 [-Exe path\to\bicz.exe]
# Without -Exe a small placeholder file stands in for bicz.exe; the installer only
# runs the binary on a real Windows host, so on other hosts this proves download,
# checksum, extract and atomic-replace logic, not execution of the Windows binary.
[CmdletBinding()]
param([string]$Exe = '')
$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$installer = Join-Path $here 'install.ps1'
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("bicz-ps-test." + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
$script:pass = 0; $script:fail = 0
function Ok([string]$m) { $script:pass++; Write-Output "ok   $m" }
function Bad([string]$m) { $script:fail++; Write-Output "FAIL $m" }

$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) { 'X64' { 'amd64' } 'Arm64' { 'arm64' } default { 'amd64' } }
$asset = "bicz_windows_$arch.zip"

$stage = Join-Path $tmp 'stage'; New-Item -ItemType Directory -Path $stage | Out-Null
if ($Exe) { Copy-Item -LiteralPath $Exe -Destination (Join-Path $stage 'bicz.exe') }
else { Set-Content -LiteralPath (Join-Path $stage 'bicz.exe') -Value 'PLACEHOLDER-NEW' -NoNewline }

function New-Release([string]$Name, [string]$Sums, [switch]$Tamper) {
  $dir = Join-Path $tmp "srv/$Name"; New-Item -ItemType Directory -Force -Path $dir | Out-Null
  Compress-Archive -LiteralPath (Join-Path $stage 'bicz.exe') -DestinationPath (Join-Path $dir $asset)
  $real = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $dir $asset)).Hash.ToLower()
  if ($Tamper) { Set-Content -LiteralPath (Join-Path $dir $asset) -Value 'tampered' -NoNewline }
  switch ($Sums) {
    'good'  { "$real  $asset" | Set-Content -LiteralPath (Join-Path $dir 'SHA256SUMS') }
    'wrong' { ('1' * 64) + "  $asset" | Set-Content -LiteralPath (Join-Path $dir 'SHA256SUMS') }
    'other' { "$real  bicz_other.zip" | Set-Content -LiteralPath (Join-Path $dir 'SHA256SUMS') }
  }
  return $real
}
$goodHash = New-Release 'good' 'good'
New-Release 'badsum' 'wrong' | Out-Null
$null = New-Release 'tampered' 'good' -Tamper
New-Release 'noentry' 'other' | Out-Null

$py = (Get-Command python3 -ErrorAction SilentlyContinue)
if (-not $py) { $py = Get-Command python -ErrorAction Stop }
@'
import http.server, socketserver, sys
class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *a): pass
with socketserver.TCPServer(("127.0.0.1", 0), Quiet) as s:
    open(sys.argv[1], "w").write(str(s.server_address[1]))
    s.serve_forever()
'@ | Set-Content -LiteralPath (Join-Path $tmp 'server.py')
$portFile = Join-Path $tmp 'port'
$srvProc = Start-Process -FilePath $py.Source -ArgumentList @((Join-Path $tmp 'server.py'), $portFile) -WorkingDirectory (Join-Path $tmp 'srv') -PassThru -NoNewWindow
try {
  for ($i = 0; $i -lt 100 -and -not (Test-Path $portFile); $i++) { Start-Sleep -Milliseconds 100 }
  if (-not (Test-Path $portFile)) { throw 'fixture server did not start' }
  Start-Sleep -Milliseconds 200
  $base = "http://127.0.0.1:$((Get-Content $portFile).Trim())"

  function Invoke-Installer([string]$Dest, [string]$Srv, [string]$Url = '') {
    if (-not $Url) { $Url = "$base/$Srv" }
    try { $out = & $installer -BaseUrl $Url -InstallDir $Dest -Os windows -Arch $arch 2>&1 | Out-String; return @{ ok = $true; out = $out } }
    catch { return @{ ok = $false; out = $_.Exception.Message } }
  }
  $exeName = 'bicz.exe'

  $d = Join-Path $tmp 't1/bin'
  $r = Invoke-Installer $d 'good'
  if ($r.ok -and (Test-Path (Join-Path $d $exeName)) -and ((Get-FileHash -Algorithm SHA256 (Join-Path $d $exeName)).Hash.ToLower() -eq (Get-FileHash -Algorithm SHA256 (Join-Path $stage 'bicz.exe')).Hash.ToLower())) { Ok 'verified install writes the exact extracted binary' } else { Bad "verified install: $($r.out)" }

  $r = Invoke-Installer $d 'good'
  if ($r.ok -and (Test-Path (Join-Path $d 'bicz.previous.exe')) -and -not (Get-ChildItem $d -Filter '.bicz.new.*' -Force)) { Ok 're-run is clean and keeps bicz.previous.exe' } else { Bad "re-run: $($r.out)" }

  $d3 = Join-Path $tmp 't3/bin'; New-Item -ItemType Directory -Force -Path $d3 | Out-Null
  Set-Content -LiteralPath (Join-Path $d3 $exeName) -Value 'OLD-BINARY' -NoNewline
  $r = Invoke-Installer $d3 'badsum'
  if (-not $r.ok -and $r.out -match 'checksum mismatch' -and ((Get-Content -Raw (Join-Path $d3 $exeName)) -eq 'OLD-BINARY') -and -not (Test-Path (Join-Path $d3 'bicz.previous.exe'))) { Ok 'checksum mismatch rejected, old binary untouched' } else { Bad "mismatch: $($r.ok) $($r.out)" }

  $d4 = Join-Path $tmp 't4/bin'
  $r = Invoke-Installer $d4 'tampered'
  if (-not $r.ok -and $r.out -match 'checksum mismatch' -and -not (Test-Path (Join-Path $d4 $exeName))) { Ok 'tampered archive rejected, nothing installed' } else { Bad "tampered: $($r.ok) $($r.out)" }

  $d5 = Join-Path $tmp 't5/bin'
  $r = Invoke-Installer $d5 'noentry'
  if (-not $r.ok -and $r.out -match 'no entry for' -and -not (Test-Path (Join-Path $d5 $exeName))) { Ok 'missing SHA256SUMS entry rejected' } else { Bad "noentry: $($r.ok) $($r.out)" }

  $r = Invoke-Installer (Join-Path $tmp 't7') '' 'http://example.invalid/rel'
  if (-not $r.ok -and $r.out -match 'non-https') { Ok 'non-https non-loopback source refused' } else { Bad "non-https: $($r.out)" }

  # Hostile archives: honest SHA256SUMS, malicious members. Entries are created with
  # ZipArchive so names like ../escape are stored verbatim.
  Add-Type -AssemblyName System.IO.Compression
  function New-HostileZip([string]$Case) {
    $dir = Join-Path $tmp "srv/h-$Case"; New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $path = Join-Path $dir $asset
    $fs = [IO.File]::Create($path)
    $za = New-Object System.IO.Compression.ZipArchive($fs, [System.IO.Compression.ZipArchiveMode]::Create)
    function Add-Entry($name, $symlink = $false) {
      $e = $za.CreateEntry($name)
      if ($symlink) { $e.ExternalAttributes = (0xA1FF -shl 16) }
      $w = New-Object IO.StreamWriter($e.Open()); $w.Write('BINARY'); $w.Dispose()
    }
    switch ($Case) {
      'traversal' { Add-Entry 'bicz.exe'; Add-Entry '../escape' }
      'nested'    { Add-Entry 'x/bicz.exe' }
      'backslash' { Add-Entry 'bicz.exe'; Add-Entry '..\\escape' }
      'extra'     { Add-Entry 'bicz.exe'; Add-Entry 'evil.dll' }
      'symlink'   { Add-Entry 'bicz.exe' $true }
      'dup'       { Add-Entry 'bicz.exe'; Add-Entry 'bicz.exe' }
    }
    $za.Dispose(); $fs.Dispose()
    ((Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLower() + "  $asset") | Set-Content -LiteralPath (Join-Path $dir 'SHA256SUMS')
  }
  foreach ($case in 'traversal', 'nested', 'backslash', 'extra', 'symlink', 'dup') {
    New-HostileZip $case
    $dh = Join-Path $tmp "h-$case/dest/bin"
    $r = Invoke-Installer $dh "h-$case"
    $escaped = @(Get-ChildItem -LiteralPath $tmp -Recurse -Force -Filter 'escape' -ErrorAction SilentlyContinue | Where-Object { $_.FullName -notlike "$tmp/srv/*" })
    if (-not $r.ok -and -not (Test-Path (Join-Path $dh $exeName)) -and $escaped.Count -eq 0) { Ok "hostile zip '$case' rejected before extraction, nothing written" } else { Bad "hostile '$case': ok=$($r.ok) escaped=$($escaped.Count) $($r.out)" }
  }

  if (-not $IsWindows -and $PSVersionTable.PSEdition -eq 'Core') {
    $d9 = Join-Path $tmp 't9/bin'; New-Item -ItemType Directory -Force -Path $d9 | Out-Null
    Set-Content -LiteralPath (Join-Path $tmp 'victim') -Value 'VICTIM' -NoNewline
    New-Item -ItemType SymbolicLink -Path (Join-Path $d9 $exeName) -Target (Join-Path $tmp 'victim') | Out-Null
    $r = Invoke-Installer $d9 'good'
    if (-not $r.ok -and $r.out -match 'symlink' -and ((Get-Content -Raw (Join-Path $tmp 'victim')) -eq 'VICTIM')) { Ok 'symlinked target refused, victim untouched' } else { Bad "symlink: $($r.ok) $($r.out)" }
  }
} finally {
  if ($srvProc -and -not $srvProc.HasExited) { Stop-Process -Id $srvProc.Id -Force }
  Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Output ("`n{0} passed, {1} failed" -f $script:pass, $script:fail)
if ($script:fail -gt 0) { exit 1 }
