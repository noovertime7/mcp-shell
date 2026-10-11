# Downloads an official Windows x64 tunnel binary into this repository only.
# Release asset SHA-256 is verified against GitHub's release metadata.
$ErrorActionPreference = 'Stop'
$release = Invoke-RestMethod -Uri 'https://api.github.com/repos/cloudflare/cloudflared/releases/latest' -Headers @{ 'User-Agent' = 'mcp-shell-desktop' }
$asset = $release.assets | Where-Object { $_.name -eq 'cloudflared-windows-amd64.exe' } | Select-Object -First 1
if (-not $asset -or $asset.digest -notmatch '^sha256:([0-9a-fA-F]{64})$') {
    throw 'Official release has no verifiable SHA-256 digest; download and verify cloudflared manually.'
}
$expected = $Matches[1]
if (-not $asset.browser_download_url.StartsWith('https://github.com/cloudflare/cloudflared/releases/download/')) {
    throw 'Unexpected release download origin.'
}
$binDir = Join-Path $PSScriptRoot 'bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$download = Join-Path $binDir ('cloudflared-' + [guid]::NewGuid().ToString('N') + '.download')
try {
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $download
    $actual = (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash
    if ($actual -ne $expected) { throw 'cloudflared SHA-256 mismatch; refusing to install.' }
    Move-Item -LiteralPath $download -Destination (Join-Path $binDir 'cloudflared.exe') -Force
    Write-Host "Verified cloudflared $($release.tag_name) saved to bin\cloudflared.exe"
} finally {
    if (Test-Path -LiteralPath $download) { Remove-Item -LiteralPath $download }
}
