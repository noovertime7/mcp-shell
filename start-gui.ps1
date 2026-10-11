param([switch]$NoBuild, [switch]$Debug)
$ErrorActionPreference = 'Stop'
if (-not $NoBuild) {
    & (Join-Path $PSScriptRoot 'build-gui.ps1') -Debug:$Debug
}
$exe = Join-Path $PSScriptRoot 'dist\mcp-shell-desktop\mcp-shell-gui.exe'
if (-not (Test-Path -LiteralPath $exe)) { throw 'Desktop executable not found. Run .\build-gui.ps1 first.' }
# This is the user-requested visible, interactive GUI.
Start-Process -FilePath $exe -WorkingDirectory (Split-Path -Parent $exe)
