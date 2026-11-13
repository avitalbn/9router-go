param([Parameter(Mandatory=$true)][string]$ConfigPath)
$ErrorActionPreference = 'Stop'
$deviceConfig = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
$bootstrap = Join-Path $deviceConfig.root 'bootstrap\9router-fleet.exe'
$logPath = Join-Path $deviceConfig.root 'supervisor.log'
& $bootstrap launch --config $ConfigPath *> $logPath
exit $LASTEXITCODE
