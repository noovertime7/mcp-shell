param([switch]$Debug)
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    $out = Join-Path $PSScriptRoot 'dist\mcp-shell-desktop'
    New-Item -ItemType Directory -Force -Path $out | Out-Null
    & go build -trimpath -o (Join-Path $out 'mcp-shell.exe') ./cmd/mcp-shell
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
    $cargoArgs = @('build', '--locked', '--manifest-path', 'gui/Cargo.toml')
    $profile = 'debug'
    if (-not $Debug) { $cargoArgs += '--release'; $profile = 'release' }
    & cargo @cargoArgs
    if ($LASTEXITCODE -ne 0) { throw 'Rust build failed' }
    Copy-Item -LiteralPath "gui\target\$profile\mcp-shell-gui.exe" -Destination $out
    Copy-Item -LiteralPath 'docs\desktop.md' -Destination (Join-Path $out '使用说明.md')
    Copy-Item -LiteralPath 'LICENSE' -Destination $out
    $zip = Join-Path $PSScriptRoot 'dist\mcp-shell-desktop-windows-x64.zip'
    Compress-Archive -LiteralPath @((Join-Path $out 'mcp-shell.exe'), (Join-Path $out 'mcp-shell-gui.exe'), (Join-Path $out '使用说明.md'), (Join-Path $out 'LICENSE')) -DestinationPath $zip -Force
    Write-Host "Desktop build ready: $out"
    Write-Host "Portable package: $zip"
} finally { Pop-Location }
