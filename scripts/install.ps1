<#
.SYNOPSIS
  Installs (or upgrades) terminatr's tm on Windows.

.DESCRIPTION
  Downloads the release zip for this PC (amd64 or arm64), checks its sha256
  against the release's checksums.txt, and puts tm.exe, conpty.dll and
  OpenConsole.exe (keep the three together) in -Dir. A running tm.exe is
  moved aside, not overwritten, so this also upgrades a tm that is running;
  the old files are removed by the next install or `tm update`. Then it
  adds -Dir to your user PATH. It needs no admin rights.

    irm https://github.com/theclifmeister/terminatr/releases/latest/download/install.ps1 | iex

  To pass options, save the script and run it:

    .\install.ps1 -Service

.PARAMETER Version
  A release tag such as v0.5.0. Default: the latest release.

.PARAMETER Dir
  Where to install. Default: %LOCALAPPDATA%\Programs\terminatr.

.PARAMETER Service
  Also start the tm server at login (`tm server service install`).

.PARAMETER NoPath
  Leave the user PATH alone.

.PARAMETER From
  A folder holding the release's zip and checksums.txt, instead of
  downloading them (for testing).
#>
[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [string]$Dir = (Join-Path $env:LOCALAPPDATA 'Programs\terminatr'),
    [switch]$Service,
    [switch]$NoPath,
    [string]$From = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
$ProgressPreference = 'SilentlyContinue'   # Windows PowerShell 5's progress bar makes downloads crawl
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

$repo = 'https://github.com/theclifmeister/terminatr'
$files = @('tm.exe', 'conpty.dll', 'OpenConsole.exe')

# The PC's architecture, not this process's (a 32-bit or emulated shell lies).
$arch = try { [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { '' }
if (-not $arch) { $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE } }
switch -Regex ($arch) {
    '^(X64|AMD64)$' { $arch = 'amd64' }
    '^ARM64$' { $arch = 'arm64' }
    default { throw "terminatr has no Windows build for $arch (amd64 and arm64 only)" }
}
$archive = "tm_windows_$arch.zip"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("tm-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    # Download (or copy) the zip and checksums.txt.
    if ($From) {
        Copy-Item (Join-Path $From $archive), (Join-Path $From 'checksums.txt') $tmp
    } else {
        $base = if ($Version -eq 'latest') { "$repo/releases/latest/download" } else { "$repo/releases/download/$Version" }
        foreach ($f in $archive, 'checksums.txt') {
            Write-Host "downloading $base/$f"
            Invoke-WebRequest -UseBasicParsing -Uri "$base/$f" -OutFile (Join-Path $tmp $f)
        }
    }

    # Check the zip against checksums.txt ("<sha256>  <name>" lines).
    $want = Get-Content (Join-Path $tmp 'checksums.txt') |
        ForEach-Object { $p = ($_ -split '\s+', 2); if ($p.Count -eq 2 -and $p[1].TrimStart('*').Trim() -eq $archive) { $p[0].ToLower() } } |
        Select-Object -First 1
    if (-not $want) { throw "checksums.txt has no sha256 for $archive" }
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $archive)).Hash.ToLower()
    if ($got -ne $want) { throw "$archive`: sha256 $got does not match checksums.txt ($want)" }
    Write-Host "checksum   ok ($archive)"

    $stage = Join-Path $tmp 'x'
    Expand-Archive -LiteralPath (Join-Path $tmp $archive) -DestinationPath $stage
    foreach ($f in $files) {
        if (-not (Test-Path (Join-Path $stage $f))) { throw "$archive holds no $f" }
    }

    # Put the files in place. A file in use can be renamed but not
    # overwritten: move it aside first, and undo everything if one fails.
    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    $stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmss')
    foreach ($f in $files) {
        Get-ChildItem -LiteralPath $Dir -Filter "$f.old-*" -ErrorAction SilentlyContinue |
            ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction SilentlyContinue }
    }
    $moved = @()
    try {
        foreach ($f in $files | Sort-Object { $_ -eq 'tm.exe' }) {   # tm.exe last
            $dst = Join-Path $Dir $f
            $aside = $null
            if (Test-Path -LiteralPath $dst) {
                $aside = "$dst.old-$stamp"
                Move-Item -LiteralPath $dst -Destination $aside
            }
            $moved += , @($dst, $aside)
            Move-Item -LiteralPath (Join-Path $stage $f) -Destination $dst
            Unblock-File -LiteralPath $dst -ErrorAction SilentlyContinue
        }
    } catch {
        $err = $_
        foreach ($m in $moved) {
            Remove-Item -LiteralPath $m[0] -Force -ErrorAction SilentlyContinue
            if ($m[1]) { Move-Item -LiteralPath $m[1] -Destination $m[0] -ErrorAction SilentlyContinue }
        }
        throw "can't replace $($err.TargetObject): $($err.Exception.Message). Close what uses it (Get-Process tm, conhost, OpenConsole) and run this again; nothing was changed."
    }
    foreach ($f in 'LICENSE', 'README.md') {
        if (Test-Path (Join-Path $stage $f)) { Copy-Item -LiteralPath (Join-Path $stage $f) -Destination $Dir -Force }
    }
    foreach ($m in $moved) {
        if ($m[1]) { Remove-Item -LiteralPath $m[1] -Force -ErrorAction SilentlyContinue }   # stays while it runs
    }
} finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$tm = Join-Path $Dir 'tm.exe'
try { Write-Host "installed  $(& $tm version 2>&1 | Select-Object -First 1) at $tm" } catch { Write-Host "installed  $tm" }

# A server that runs keeps the old build until it restarts.
$running = @(Get-Process -Name tm -ErrorAction SilentlyContinue | Where-Object { $_.Path -and $_.Path -like "$Dir\*" })
if ($running.Count -gt 0) {
    Write-Host "A tm is running (pid $($running[0].Id)); a server keeps the old build until you run: tm server restart"
}

if (-not $NoPath) {
    $user = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @($user -split ';' | Where-Object { $_ })
    if (-not ($entries | Where-Object { $_.TrimEnd('\') -ieq $Dir.TrimEnd('\') })) {
        [Environment]::SetEnvironmentVariable('Path', (($entries + $Dir) -join ';'), 'User')
        Write-Host "added $Dir to your user PATH (open a new terminal to use tm)"
    }
    if (-not (($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $Dir.TrimEnd('\') })) { $env:Path += ";$Dir" }
}

if ($Service) {
    & $tm server service install
    if ($LASTEXITCODE -ne 0) { throw "tm server service install failed (exit $LASTEXITCODE)" }
}

Write-Host "Next: tm doctor"
