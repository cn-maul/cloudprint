#Requires -Version 7.0
<#
.SYNOPSIS
  在 Windows 上启动、停止和查看 CloudPrint。

.DESCRIPTION
  默认在后台启动 CloudPrint，并把 PID、日志和本地配置放在 DataDir。
  首次运行会生成管理员密码；密码只保存在本机配置文件中。

.EXAMPLE
  pwsh -File .\Start-CloudPrint.ps1

.EXAMPLE
  pwsh -File .\Start-CloudPrint.ps1 -AllowFirewall

.EXAMPLE
  pwsh -File .\Start-CloudPrint.ps1 -Action status

.EXAMPLE
  pwsh -File .\Start-CloudPrint.ps1 -Action logs -Tail 100
#>
[CmdletBinding()]
param(
    [ValidateSet('start', 'stop', 'restart', 'status', 'logs', 'config')]
    [string]$Action = 'start',

    [int]$Port = 0,

    [string]$DataDir = '',

    [string]$AdminUser = '',

    [switch]$AllowFirewall,

    [switch]$NoBrowser,

    [int]$Tail = 50
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ProjectRoot = $PSScriptRoot
$ServerExe = Join-Path $ProjectRoot 'backend\dist\cloudprint.exe'

if ([string]::IsNullOrWhiteSpace($DataDir)) {
    $DataDir = Join-Path $env:LOCALAPPDATA 'CloudPrint'
}
$DataDir = [IO.Path]::GetFullPath($DataDir)

$ConfigPath = Join-Path $DataDir 'cloudprint.config.json'
$PidPath = Join-Path $DataDir 'cloudprint.pid'
$LogDir = Join-Path $DataDir 'logs'
$StdoutLog = Join-Path $LogDir 'server.out.log'
$StderrLog = Join-Path $LogDir 'server.err.log'

function Write-Step([string]$Message) {
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Write-Ok([string]$Message) {
    Write-Host "OK  $Message" -ForegroundColor Green
}

function Write-Warn([string]$Message) {
    Write-Host "!!  $Message" -ForegroundColor Yellow
}

function New-RandomPassword {
    $bytes = [byte[]]::new(24)
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Protect-ConfigFile([string]$Path) {
    try {
        $acl = Get-Acl -LiteralPath $Path
        $acl.SetAccessRuleProtection($true, $false)
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            $identity,
            [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.AccessControlType]::Allow
        )
        $acl.AddAccessRule($rule)
        Set-Acl -LiteralPath $Path -AclObject $acl
    } catch {
        Write-Warn "无法收紧配置文件权限，请自行保护 $Path"
    }
}

function Save-Config {
    param(
        [int]$ConfigPort,
        [string]$ConfigAdminUser,
        [string]$Password
    )
    $config = [ordered]@{
        port           = $ConfigPort
        admin_user     = $ConfigAdminUser
        admin_password = $Password
        updated_at     = (Get-Date).ToString('o')
    }
    $config | ConvertTo-Json | Set-Content -LiteralPath $ConfigPath -Encoding utf8NoBOM
    Protect-ConfigFile $ConfigPath
}

function Load-Config {
    if (-not (Test-Path -LiteralPath $ConfigPath)) {
        return $null
    }
    try {
        return Get-Content -LiteralPath $ConfigPath -Raw -Encoding utf8 | ConvertFrom-Json
    } catch {
        throw "配置文件无法解析：$ConfigPath。请修复或删除后重试。"
    }
}

function Get-LanAddress {
    $preferred = Get-NetIPConfiguration -ErrorAction SilentlyContinue |
        Where-Object {
            $_.IPv4DefaultGateway -and
            $_.NetAdapter.Status -eq 'Up' -and
            $_.IPv4Address
        } |
        ForEach-Object { $_.IPv4Address.IPAddress } |
        Where-Object { $_ -and $_ -notlike '169.254.*' } |
        Select-Object -First 1
    if ($preferred) {
        return $preferred
    }
    try {
        [Net.Dns]::GetHostAddresses([Net.Dns]::GetHostName()) |
            Where-Object {
                $_.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork -and
                -not [Net.IPAddress]::IsLoopback($_)
            } |
            Select-Object -First 1 -ExpandProperty IPAddressToString
    } catch {
        $null
    }
}

function Test-PortBusy([int]$ListenPort) {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Any, $ListenPort)
    try {
        $listener.Start()
        $false
    } catch {
        $true
    } finally {
        try { $listener.Stop() } catch { }
    }
}

function Wait-PortFree([int]$ListenPort, [int]$TimeoutSeconds = 15) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-PortBusy $ListenPort)) {
            return $true
        }
        Start-Sleep -Milliseconds 300
    }
    return -not (Test-PortBusy $ListenPort)
}

function Get-ServerProcess {
    if (-not (Test-Path -LiteralPath $PidPath)) {
        return $null
    }
    $savedPid = 0
    if (-not [int]::TryParse((Get-Content -LiteralPath $PidPath -Raw).Trim(), [ref]$savedPid)) {
        return $null
    }
    $process = Get-Process -Id $savedPid -ErrorAction SilentlyContinue
    if (-not $process) {
        return $null
    }
    try {
        if ([IO.Path]::GetFullPath($process.Path) -ne [IO.Path]::GetFullPath($ServerExe)) {
            return $null
        }
    } catch {
        return $null
    }
    return $process
}

function Remove-StaleBackup {
    # Windows cannot overwrite a running .exe, so a rebuild while the server is
    # up leaves a "cloudprint.exe~" beside it. Keeping that file around makes it
    # easy to launch the wrong binary later.
    $backup = "$ServerExe~"
    if (-not (Test-Path -LiteralPath $backup)) {
        return
    }
    $owner = Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $backup }
    if ($owner) {
        return
    }
    Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
}

function Get-ListeningProcess([int]$ListenPort) {
    $connection = Get-NetTCPConnection -LocalPort $ListenPort -State Listen -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $connection) {
        return $null
    }
    return Get-Process -Id $connection.OwningProcess -ErrorAction SilentlyContinue
}

function Wait-UntilReady([int]$ListenPort, [int]$TimeoutSeconds = 25) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $ready = Invoke-RestMethod -Uri "http://127.0.0.1:$ListenPort/readyz" -TimeoutSec 2
            if ($ready.status -eq 'ready') {
                return $ready
            }
        } catch { }
        Start-Sleep -Milliseconds 400
    }
    return $null
}

function Show-RunningInfo([int]$ListenPort, $Ready) {
    $lan = Get-LanAddress
    Write-Ok 'CloudPrint 正在运行'
    Write-Host "    本机地址 : http://127.0.0.1:$ListenPort"
    if ($lan) {
        Write-Host "    局域网   : http://${lan}:$ListenPort"
    }
    Write-Host "    数据库   : $(Join-Path $DataDir 'cloudprint.db')"
    Write-Host "    日志     : $StdoutLog"
    if ($Ready) {
        $lo = if ($Ready.libreoffice) { '可用' } else { '不可用' }
        $preview = if ($Ready.pdf_preview) { '可用' } else { '不可用' }
        Write-Host "    Office   : $lo"
        Write-Host "    PDF 预览 : $preview"
    }
}

function Open-CloudPrint([int]$ListenPort) {
    if ($NoBrowser) {
        return
    }
    Start-Process "http://127.0.0.1:$ListenPort" | Out-Null
}

function Show-NetworkHint {
    $public = Get-NetConnectionProfile -ErrorAction SilentlyContinue |
        Where-Object { $_.NetworkCategory -eq 'Public' -and $_.IPv4Connectivity -ne 'Disconnected' }
    if ($public) {
        Write-Warn "当前网络是 Windows 公用网络：$($public[0].Name)"
        Write-Host '    -AllowFirewall 会同时放行公用网络；也可以把该网络改为专用。' -ForegroundColor DarkGray
    }
}

function Invoke-Start {
    if (-not (Test-Path -LiteralPath $ServerExe)) {
        throw "找不到服务端程序：$ServerExe。请先完成构建或确认目录结构。"
    }
    Remove-StaleBackup

    $existing = Get-ServerProcess
    if ($existing) {
        $config = Load-Config
        $listenPort = if ($Port -gt 0) { $Port } elseif ($config -and $config.port) { [int]$config.port } else { 8080 }
        $ready = Wait-UntilReady $listenPort 3
        Show-RunningInfo $listenPort $ready
        Write-Warn "服务已在运行（PID $($existing.Id)），未重复启动。"
        return
    }

    $config = Load-Config
    $listenPort = if ($Port -gt 0) { $Port } elseif ($config -and $config.port) { [int]$config.port } else { 8080 }
    $user = if ($AdminUser) { $AdminUser } elseif ($config -and $config.admin_user) { $config.admin_user } else { 'admin' }

    New-Item -ItemType Directory -Force -Path $DataDir, $LogDir | Out-Null
    $dbExists = Test-Path -LiteralPath (Join-Path $DataDir 'cloudprint.db')

    $password = if ($config -and $config.admin_password) { [string]$config.admin_password } else { '' }
    $generated = $false
    if (-not $dbExists -and [string]::IsNullOrWhiteSpace($password)) {
        $password = New-RandomPassword
        $generated = $true
    }

    if (-not $config -or $generated -or [int]$config.port -ne $listenPort -or
        [string]$config.admin_user -ne $user -or [string]$config.admin_password -ne $password) {
        Save-Config -ConfigPort $listenPort -ConfigAdminUser $user -Password $password
    }

    if (Test-PortBusy $listenPort) {
        $owner = Get-ListeningProcess $listenPort
        if ($owner) {
            throw "端口 $listenPort 已被 PID $($owner.Id)（$($owner.ProcessName)）占用。请换端口或先停止占用进程。"
        }
        throw "端口 $listenPort 已被占用。"
    }

    if ($AllowFirewall) {
        $isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
            [Security.Principal.WindowsBuiltInRole]::Administrator
        )
        if (-not $isAdmin) {
            throw '放行防火墙需要管理员权限。请用“以管理员身份运行”打开 pwsh 7。'
        }
        $ruleName = "CloudPrint TCP $listenPort"
        if (-not (Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) {
            New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Action Allow `
                -Protocol TCP -LocalPort $listenPort -Profile Private, Domain, Public | Out-Null
            Write-Ok "已添加防火墙规则：$ruleName"
        } else {
            Write-Ok "防火墙规则已存在：$ruleName"
        }
    }

    if (Test-Path -LiteralPath $StdoutLog) {
        Move-Item -LiteralPath $StdoutLog -Destination "$StdoutLog.prev" -Force
    }
    if (Test-Path -LiteralPath $StderrLog) {
        Move-Item -LiteralPath $StderrLog -Destination "$StderrLog.prev" -Force
    }

    $env:CLOUDPRINT_ADDR = ":$listenPort"
    $env:CLOUDPRINT_DATA = $DataDir
    $env:CLOUDPRINT_ADMIN_USER = $user
    if (-not [string]::IsNullOrWhiteSpace($password)) {
        $env:CLOUDPRINT_ADMIN_PASSWORD = $password
    }

    Write-Step "启动 CloudPrint（端口 $listenPort）"
    $process = Start-Process -FilePath $ServerExe -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $StdoutLog -RedirectStandardError $StderrLog
    Set-Content -LiteralPath $PidPath -Value $process.Id -Encoding ascii

    $ready = Wait-UntilReady $listenPort
    if (-not $ready) {
        $process.Refresh()
        if ($process.HasExited) {
            Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
            $errTail = if (Test-Path -LiteralPath $StderrLog) {
                (Get-Content -LiteralPath $StderrLog -Tail 20) -join [Environment]::NewLine
            } else { '' }
            $outTail = if (Test-Path -LiteralPath $StdoutLog) {
                (Get-Content -LiteralPath $StdoutLog -Tail 20) -join [Environment]::NewLine
            } else { '' }
            throw "CloudPrint 启动失败。`n$outTail`n$errTail"
        }
        throw "CloudPrint 进程已启动（PID $($process.Id)），但在 25 秒内没有就绪。请查看日志：$StdoutLog"
    }

    if ($generated) {
        Write-Host ''
        Write-Host '首次启动已生成管理员账号：' -ForegroundColor Yellow
        Write-Host "    用户名 : $user"
        Write-Host "    密码   : $password"
        Write-Host "    配置   : $ConfigPath" -ForegroundColor DarkGray
        Write-Host '    密码已保存在仅当前 Windows 用户可读的配置文件中。'
        Write-Host ''
    } elseif ($dbExists -and -not $config) {
        Write-Warn '检测到已有数据库，管理员密码沿用原密码（脚本不会重置）。'
    }

    Show-RunningInfo $listenPort $ready
    Show-NetworkHint
    if (-not $AllowFirewall) {
        Write-Host "    提示     : 同事无法访问时，用管理员权限运行 -AllowFirewall" -ForegroundColor DarkGray
    }
    Open-CloudPrint $listenPort
}

function Invoke-Stop {
    $process = Get-ServerProcess
    if (-not $process) {
        $config = Load-Config
        $listenPort = if ($Port -gt 0) { $Port } elseif ($config -and $config.port) { [int]$config.port } else { 8080 }
        $process = Get-ListeningProcess $listenPort
        if ($process) {
            try {
                if ([IO.Path]::GetFullPath($process.Path) -ne [IO.Path]::GetFullPath($ServerExe)) {
                    Write-Warn "端口 $listenPort 被其他程序占用：PID $($process.Id) $($process.ProcessName)"
                    return
                }
            } catch {
                return
            }
        }
    }
    if (-not $process) {
        Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
        Write-Warn 'CloudPrint 没有在运行。'
        return
    }

    Write-Step "停止 CloudPrint（PID $($process.Id)）"
    Stop-Process -Id $process.Id -Force
    try {
        Wait-Process -Id $process.Id -Timeout 10 -ErrorAction Stop
    } catch {
        # Process already exited.
    }
    Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
    Write-Ok '已停止。'
}

function Invoke-Status {
    $config = Load-Config
    $listenPort = if ($Port -gt 0) { $Port } elseif ($config -and $config.port) { [int]$config.port } else { 8080 }
    $process = Get-ServerProcess
    if (-not $process) {
        $process = Get-ListeningProcess $listenPort
        if ($process) {
            try {
                if ([IO.Path]::GetFullPath($process.Path) -ne [IO.Path]::GetFullPath($ServerExe)) {
                    $process = $null
                }
            } catch {
                $process = $null
            }
        }
    }
    if (-not $process) {
        Remove-Item -LiteralPath $PidPath -Force -ErrorAction SilentlyContinue
        Write-Warn "CloudPrint 没有在运行（端口 $listenPort）。"
        return
    }
    $ready = Wait-UntilReady $listenPort 3
    if (-not $ready) {
        Write-Warn "进程存在（PID $($process.Id)），但端口 $listenPort 未就绪。"
        Write-Host "    日志 : $StdoutLog"
        return
    }
    Show-RunningInfo $listenPort $ready
    Write-Host "    PID      : $($process.Id)"
}

function Invoke-Logs {
    if (-not (Test-Path -LiteralPath $StdoutLog)) {
        Write-Warn "还没有日志：$StdoutLog"
        return
    }
    Write-Step "最近 $Tail 行：$StdoutLog"
    Get-Content -LiteralPath $StdoutLog -Tail $Tail
    if ((Test-Path -LiteralPath $StderrLog) -and (Get-Item -LiteralPath $StderrLog).Length -gt 0) {
        Write-Step "标准错误：$StderrLog"
        Get-Content -LiteralPath $StderrLog -Tail $Tail
    }
}

function Invoke-Config {
    $config = Load-Config
    if (-not $config) {
        Write-Warn "还没有配置文件：$ConfigPath"
        return
    }
    Write-Host "配置文件 : $ConfigPath"
    Write-Host "端口     : $($config.port)"
    Write-Host "管理员   : $($config.admin_user)"
    if ($config.admin_password) {
        Write-Host "初始密码 : $($config.admin_password)"
        Write-Host "说明     : 该密码只在首次建库时生效；之后改密不会更新这里。" -ForegroundColor DarkGray
    } else {
        Write-Host '初始密码 : 未保存（数据库已存在，启动不需要它）'
    }
}

switch ($Action) {
    'start' { Invoke-Start }
    'stop' { Invoke-Stop }
    'restart' {
        Invoke-Stop
        $config = Load-Config
        $listenPort = if ($Port -gt 0) { $Port } elseif ($config -and $config.port) { [int]$config.port } else { 8080 }
        if (-not (Wait-PortFree $listenPort)) {
            throw "端口 $listenPort 在停止后仍未释放，请稍后重试。"
        }
        Invoke-Start
    }
    'status' { Invoke-Status }
    'logs' { Invoke-Logs }
    'config' { Invoke-Config }
}
