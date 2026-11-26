[CmdletBinding()]
param(
    [ValidateSet('Install','Start','Stop','Restart','Status','Supervise','EnableAutoRenew')]
    [string]$Action = 'Start',
    [string]$Distro = 'Ubuntu-24.04',
    [ValidateRange(1024,65535)][int]$DatabasePort = 18432
)
$ErrorActionPreference = 'Stop'
$taskRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$taskState = Join-Path $taskRoot '.dev\local-service'
$taskScript = $PSCommandPath
$taskPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$taskName = 'MCPGuard Local Service'

function Get-LocalHealth {
    try {
        $taskHealth = Invoke-RestMethod -Uri 'http://127.0.0.1:18080/healthz' -TimeoutSec 2
        return $taskHealth.status -eq 'up' -and $taskHealth.auth -eq 'dev-tokens'
    } catch { return $false }
}

function Invoke-LocalController([string]$ControllerAction) {
    $taskLinuxRoot = (& wsl.exe -d $Distro -- wslpath -a ($taskRoot.Replace('\','/')) | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or -not $taskLinuxRoot.StartsWith('/')) { throw 'Cannot resolve repository path in WSL.' }
    & wsl.exe -d $Distro --cd $taskLinuxRoot -- python3 scripts/local_service.py $ControllerAction --database-port $DatabasePort
    if ($LASTEXITCODE -ne 0) { throw "执契控制程序失败，操作 $ControllerAction" }
}

function Get-SupervisorProcess {
    $taskPidFile = Join-Path $taskState 'windows-supervisor.json'
    if (-not (Test-Path -LiteralPath $taskPidFile)) { return $null }
    try {
        $taskPidInfo = Get-Content -LiteralPath $taskPidFile -Raw | ConvertFrom-Json
        $taskProc = Get-CimInstance Win32_Process -Filter "ProcessId=$([int]$taskPidInfo.pid)" -ErrorAction Stop
        if ($taskProc -and $taskProc.CommandLine -and $taskProc.CommandLine.IndexOf($taskScript,[StringComparison]::OrdinalIgnoreCase) -ge 0 -and $taskProc.CommandLine -match '-Action\s+Supervise') { return $taskProc }
    } catch { }
    return $null
}

function Start-LocalService {
    if (-not (Test-Path -LiteralPath (Join-Path $taskState 'service.json'))) { throw 'Run -Action Install first to preserve and adopt the existing fixture.' }
    $taskStopped = Join-Path $taskState 'stopped'
    if (Test-Path -LiteralPath $taskStopped) { Remove-Item -LiteralPath $taskStopped }
    if (-not (Get-SupervisorProcess)) {
        $taskRegistered = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        if ($taskRegistered -and $taskRegistered.Actions.Arguments -like ('*' + $taskScript + '*')) {
            Start-ScheduledTask -TaskName $taskName
        } else {
            $taskStamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
            $taskArgs = '-NoProfile -NonInteractive -WindowStyle Hidden -File "' + $taskScript + '" -Action Supervise -Distro "' + $Distro + '"'
            $taskChild = Start-Process -FilePath $taskPowerShell -ArgumentList $taskArgs -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $taskState "supervisor-$taskStamp.out.log") -RedirectStandardError (Join-Path $taskState "supervisor-$taskStamp.err.log")
        }
    }
    for ($taskTry = 0; $taskTry -lt 60; $taskTry++) {
        if (Get-LocalHealth) {
            # A healthy gateway alone does not prove its enrolled identity is current.
            Invoke-LocalController 'renew'
            Write-Output '执契本机服务已就绪 http://127.0.0.1:18080/mcp'
            return
        }
        Start-Sleep -Milliseconds 500
    }
    throw '执契本机服务未通过健康检查，请查看 .dev/local-service/heartbeat.json 和监督进程日志。'
}

function Stop-LocalService {
    Invoke-LocalController 'stop'
    for ($taskTry = 0; $taskTry -lt 20; $taskTry++) {
        if (-not (Get-SupervisorProcess)) { return }
        Start-Sleep -Milliseconds 500
    }
    throw '执契监督进程尚未停止，请查看监督进程日志。'
}

function Install-LocalStartup {
    $taskArgs = '-NoProfile -NonInteractive -WindowStyle Hidden -File "' + $taskScript + '" -Action Supervise -Distro "' + $Distro + '"'
    $taskUser = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    try {
        $taskExisting = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        if ($taskExisting -and -not ($taskExisting.Actions.Arguments -like ('*' + $taskScript + '*'))) { throw 'Scheduled task name belongs to another workspace.' }
        $taskTrigger = New-ScheduledTaskTrigger -AtLogOn -User $taskUser
        $taskPrincipal = New-ScheduledTaskPrincipal -UserId $taskUser -LogonType Interactive -RunLevel Limited
        $taskSettings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
        $taskTaskAction = New-ScheduledTaskAction -Execute $taskPowerShell -Argument $taskArgs -WorkingDirectory $taskRoot
        Register-ScheduledTask -TaskName $taskName -Action $taskTaskAction -Trigger $taskTrigger -Principal $taskPrincipal -Settings $taskSettings -Description '通过 WSL 运行仅回环访问的执契本机服务。' -Force | Out-Null
        @{kind='scheduled-task';name=$taskName;user=$taskUser;script=$taskScript} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $taskState 'windows-startup.json') -Encoding UTF8
    } catch [Microsoft.Management.Infrastructure.CimException] {
        # A current-user Startup entry works without granting elevated rights.
        $taskStartupPath = Join-Path ([Environment]::GetFolderPath('Startup')) 'MCPGuard Local Service.lnk'
        $taskShell = New-Object -ComObject WScript.Shell
        $taskShortcut = $taskShell.CreateShortcut($taskStartupPath)
        if ((Test-Path -LiteralPath $taskStartupPath) -and $taskShortcut.Arguments -notlike ('*' + $taskScript + '*')) { throw 'Startup shortcut belongs to another workspace.' }
        $taskShortcut.TargetPath = $taskPowerShell
        $taskShortcut.Arguments = $taskArgs
        $taskShortcut.WorkingDirectory = $taskRoot
        $taskShortcut.WindowStyle = 7
        $taskShortcut.Description = '执契本机服务（仅回环访问）'
        $taskShortcut.Save()
        @{kind='startup-shortcut';path=$taskStartupPath;user=$taskUser;script=$taskScript} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $taskState 'windows-startup.json') -Encoding UTF8
    }
}

switch ($Action) {
    'Install' { Invoke-LocalController 'setup'; Install-LocalStartup; Start-LocalService }
    'Start' { Start-LocalService }
    'Stop' { Stop-LocalService }
    'Restart' { Stop-LocalService; Start-LocalService }
    'EnableAutoRenew' { Start-LocalService; Invoke-LocalController 'enable-renewal' }
    'Status' {
        Invoke-LocalController 'status'
        @{supervisorRunning=[bool](Get-SupervisorProcess);windowsHealthy=(Get-LocalHealth)} | ConvertTo-Json -Compress
    }
    'Supervise' {
        if (Test-Path -LiteralPath (Join-Path $taskState 'stopped')) { exit 0 }
        $taskHasher = [Security.Cryptography.SHA256]::Create()
        $taskRootHash = ([BitConverter]::ToString($taskHasher.ComputeHash([Text.Encoding]::UTF8.GetBytes($taskRoot)))).Replace('-','').Substring(0,20)
        $taskHasher.Dispose()
        $taskMutex = New-Object Threading.Mutex($false,('Local\MCPGuard-' + $taskRootHash))
        $taskOwnsMutex = $false
        try {
            try { $taskOwnsMutex = $taskMutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $taskOwnsMutex = $true }
            if (-not $taskOwnsMutex) { return }
            @{pid=$PID;startedAt=[DateTime]::UtcNow.ToString('o');distro=$Distro} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $taskState 'windows-supervisor.json') -Encoding UTF8
            while (-not (Test-Path -LiteralPath (Join-Path $taskState 'stopped'))) {
                try { Invoke-LocalController 'serve' } catch { Write-Warning '执契的 WSL 监督已中断，正在本机重试。' }
                if (-not (Test-Path -LiteralPath (Join-Path $taskState 'stopped'))) { Start-Sleep -Seconds 3 }
            }
        } finally {
            if ($taskOwnsMutex) { $taskMutex.ReleaseMutex() }
            $taskMutex.Dispose()
        }
    }
}
