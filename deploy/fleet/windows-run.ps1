param([Parameter(Mandatory=$true)][string]$ConfigPath)
$ErrorActionPreference = 'Stop'
$deviceConfig = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
$bootstrap = Join-Path $deviceConfig.root 'bootstrap\9router-fleet.exe'
$logPath = Join-Path $deviceConfig.root 'supervisor.log'
$errorLogPath = Join-Path $deviceConfig.root 'supervisor.error.log'
# Windows PowerShell 5 treats redirected native stderr as an error record;
# normal helper logging must not terminate an otherwise healthy supervisor.
$process = Start-Process -FilePath $bootstrap -ArgumentList "launch --config `"$ConfigPath`"" -WindowStyle Hidden -Wait -PassThru -RedirectStandardOutput $logPath -RedirectStandardError $errorLogPath
exit $process.ExitCode
