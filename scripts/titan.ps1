# Windows entry point: runs the Bash scripts through Git Bash or WSL.
#   .\scripts\titan.ps1 up -Stack lite
#   .\scripts\titan.ps1 load
#   .\scripts\titan.ps1 down
param(
    [Parameter(Position = 0)]
    [ValidateSet('up', 'kind', 'images', 'addons', 'deploy', 'smoke', 'load', 'down')]
    [string]$Command = 'up',
    [ValidateSet('lite', 'full')]
    [string]$Stack = 'lite',
    [string]$Tag = 'dev'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot

$scripts = @{
    up     = "scripts/up.sh --profile $Stack --tag $Tag"
    kind   = 'scripts/kind-up.sh'
    images = "scripts/build-images.sh --tag $Tag"
    addons = "scripts/install-addons.sh --profile $Stack"
    deploy = "scripts/deploy.sh --profile $Stack --tag $Tag"
    smoke  = 'scripts/smoke.sh'
    load   = 'scripts/load-test.sh local'
    down   = 'scripts/teardown.sh'
}

$bash = Get-Command bash -ErrorAction SilentlyContinue
if (-not $bash) {
    throw 'bash not found. Install Git for Windows or enable WSL2 (see docs/setup-windows.md).'
}

Push-Location $root
try {
    & bash -c $scripts[$Command]
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
