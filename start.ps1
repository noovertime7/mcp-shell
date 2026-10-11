# mcp-shell build and start script (Windows PowerShell 5+ / pwsh)
#   .\start.ps1             build bin\mcp-shell.exe with the go on PATH (Go 1.20+), then start it
#   .\start.ps1 -NoBuild    start the existing bin\mcp-shell.exe
#   .\start.ps1 -NewToken   print a random token for config.json and exit
#   .\start.ps1 -Go C:\go\bin\go.exe   build with a specific go
# Manual equivalent:
#   go build -o bin\mcp-shell.exe .\cmd\mcp-shell
#   .\bin\mcp-shell.exe -config config.json
param(
    [switch]$NoBuild,
    [switch]$NewToken,
    [string]$Go = 'go',
    [string]$Config = (Join-Path $PSScriptRoot 'config.json')
)
$ErrorActionPreference = 'Stop'

if ($NewToken) {
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    ([Convert]::ToBase64String($bytes) -replace '[+/=]', '')
    return
}

$exe = Join-Path $PSScriptRoot 'bin\mcp-shell.exe'

if (-not $NoBuild) {
    Push-Location $PSScriptRoot
    try {
        & $Go build -o $exe ./cmd/mcp-shell
        if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
}

if (-not (Test-Path $Config)) {
    throw "config not found: $Config (copy config.example.json to config.json and fill it in)"
}
& $exe -config $Config
