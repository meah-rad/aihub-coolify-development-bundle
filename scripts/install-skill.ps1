param(
    [string]$Target = $env:AIHUB_SKILLS_DIR
)

$ErrorActionPreference = 'Stop'
$source = Join-Path (Split-Path $PSScriptRoot -Parent) 'skills\aihub-deploy'
if (-not $Target) {
    $Target = Join-Path $HOME '.aihub\skills\aihub-deploy'
} else {
    $Target = Join-Path $Target 'aihub-deploy'
}

New-Item -ItemType Directory -Force -Path $Target | Out-Null
Copy-Item -Path (Join-Path $source '*') -Destination $Target -Recurse -Force
Write-Output "Installed AI Hub deploy skill to $Target"
