$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$work = Join-Path $root 'target\packaging\windows'
$pack = Join-Path $work 'pack'
$releases = Join-Path $work 'releases'

$manifest = Get-Content (Join-Path $root 'Cargo.toml') -Raw
if ($manifest -notmatch '(?ms)\[workspace\.package\].*?^\s*version\s*=\s*"([^"]+)"') {
    throw 'Version introuvable dans [workspace.package] de Cargo.toml'
}
$version = $Matches[1]
Write-Host "Version : $version"

Remove-Item -Recurse -Force $pack -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $pack | Out-Null
New-Item -ItemType Directory -Force $releases | Out-Null

Push-Location (Join-Path $root 'bridge')
try {
    $env:CGO_ENABLED = '0'
    go build -ldflags "-X main.bridgeVersion=$version" -o (Join-Path $pack 'missive-bridge.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'Échec de go build' }
}
finally {
    Pop-Location
}

Push-Location $root
try {
    cargo build --release --package missive
    if ($LASTEXITCODE -ne 0) { throw 'Échec de cargo build' }
}
finally {
    Pop-Location
}

$exe = Join-Path $root 'target\release\missive.exe'
if (-not (Test-Path $exe)) { throw "Binaire introuvable : $exe" }
Copy-Item $exe (Join-Path $pack 'missive.exe')

if (-not (Get-Command 'vpk' -ErrorAction SilentlyContinue)) {
    Write-Host ''
    Write-Host 'vpk (Velopack) est introuvable : le paquet n''a pas été généré.'
    Write-Host 'Installation : dotnet tool install -g vpk --version 1.2.161'
    Write-Host "Fichiers prêts dans : $pack"
    Write-Host 'Relancer ensuite ce script.'
    exit 0
}

$repo = $env:MISSIVE_UPDATE_REPO
if ($repo) {
    $download = @('download', 'github', '--repoUrl', "https://github.com/$repo", '--channel', 'win', '--outputDir', $releases)
    if ($env:GITHUB_TOKEN) { $download += @('--token', $env:GITHUB_TOKEN) }
    vpk @download
    if ($LASTEXITCODE -ne 0) { Write-Host 'Aucune release précédente téléchargée, pas de delta.' }
}

$packArgs = @(
    'pack',
    '--packId', 'Missive',
    '--packVersion', $version,
    '--packDir', $pack,
    '--mainExe', 'missive.exe',
    '--packTitle', 'Missive',
    '--packAuthors', 'Missive contributors',
    '--aumid', 'Missive.Desktop',
    '--shortcuts', 'Desktop,StartMenuRoot',
    '--channel', 'win',
    '--outputDir', $releases
)
$icon = Join-Path $PSScriptRoot 'missive.ico'
if (Test-Path $icon) { $packArgs += @('--icon', $icon) }
if ($env:MISSIVE_SIGN_PARAMS) { $packArgs += @('--signParams', $env:MISSIVE_SIGN_PARAMS) }
if ($env:MISSIVE_AZURE_SIGN_FILE) { $packArgs += @('--azureTrustedSignFile', $env:MISSIVE_AZURE_SIGN_FILE) }

vpk @packArgs
if ($LASTEXITCODE -ne 0) { throw 'Échec de vpk pack' }

Write-Host ''
Write-Host "Livrables : $releases"
