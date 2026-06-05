# activate.ps1 - CDK Activation Script
# Served via: $cdk="XXXX-XXXX-XXXX-XXXX"; irm http://server:8787 | iex
$BoxApiBase = "__INJECT_API_BASE__"

$ProgressPreference = 'SilentlyContinue'
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch {}

function Pause-Exit([string]$Msg, [string]$Color = "Red") {
    Write-Host ""
    Write-Host $Msg -ForegroundColor $Color
    Write-Host ""
    Write-Host "按任意键关闭..." -ForegroundColor Gray
    try { $null = $Host.UI.RawUI.ReadKey("NoEcho,IncludeKeyDown") } catch { Read-Host "按 Enter 关闭" }
}

try {

# ── Admin check ──────────────────────────────────────────────────────────────
$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator
)
if (-not $isAdmin) {
    Pause-Exit "[!] 请以管理员身份运行 PowerShell 再执行此命令"
    return
}

# ── Resolve API base ────────────────────────────────────────────────────────
$ApiBase = $BoxApiBase
if ($ApiBase -like "*INJECT*") { $ApiBase = "http://127.0.0.1:8787" }
$ApiBase = $ApiBase.TrimEnd("/")

# ── Read CDK ─────────────────────────────────────────────────────────────────
$cdkCode = $null
if ($cdk -and $cdk -ne '') {
    $cdkCode = $cdk.Trim().ToUpper()
} else {
    Write-Host ""
    Write-Host "请输入激活码 (格式: XXXX-XXXX-XXXX-XXXX):" -ForegroundColor Cyan
    $cdkCode = (Read-Host "CDK").Trim().ToUpper()
}

if (-not $cdkCode) {
    Pause-Exit "[!] 激活码不能为空"
    return
}
if ($cdkCode -notmatch '^[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}$') {
    Pause-Exit "[!] 激活码格式错误，正确格式: XXXX-XXXX-XXXX-XXXX"
    return
}
Write-Host "[*] 激活码: $cdkCode" -ForegroundColor Cyan

# ── Test connectivity ────────────────────────────────────────────────────────
Write-Host "[*] 连接服务器..." -ForegroundColor Cyan
try {
    Invoke-WebRequest -Uri "$ApiBase/api/ping" -UseBasicParsing -TimeoutSec 10 | Out-Null
} catch {
    Pause-Exit "[!] 无法连接服务器 $ApiBase`n    请检查网络和防火墙设置`n    错误: $_"
    return
}

# ── Find Steam path ──────────────────────────────────────────────────────────
function Find-SteamPath {
    try {
        $reg = Get-ItemProperty -Path "HKCU:\Software\Valve\Steam" -Name "SteamPath" -ErrorAction SilentlyContinue
        if ($reg.SteamPath -and (Test-Path $reg.SteamPath)) { return $reg.SteamPath }
    } catch {}
    try {
        $reg = Get-ItemProperty -Path "HKLM:\Software\WOW6432Node\Valve\Steam" -Name "InstallPath" -ErrorAction SilentlyContinue
        if ($reg.InstallPath -and (Test-Path $reg.InstallPath)) { return $reg.InstallPath }
    } catch {}
    foreach ($p in @("$env:ProgramFiles (x86)\Steam", "$env:ProgramFiles\Steam", "C:\Steam", "D:\Steam")) {
        if ($p -and (Test-Path (Join-Path $p "steam.exe"))) { return $p }
    }
    return $null
}

$SteamPath = Find-SteamPath
if (-not $SteamPath) {
    Pause-Exit "[!] 未找到 Steam 安装路径，请先安装 Steam"
    return
}
Write-Host "[+] Steam: $SteamPath" -ForegroundColor Green

# ── Ensure hook installed ────────────────────────────────────────────────────
$dllXinput = Join-Path $SteamPath "xinput1_4.dll"
$dllDwmapi = Join-Path $SteamPath "dwmapi.dll"
$dllOST    = Join-Path $SteamPath "OpenSteamTool.dll"
$tomlPath  = Join-Path $SteamPath "opensteamtool.toml"

$hookOK = (Test-Path $dllXinput) -and (Test-Path $dllDwmapi) -and (Test-Path $dllOST) -and (Test-Path $tomlPath)
if ($hookOK) {
    $hookOK = ((Get-Item $dllXinput).Length -gt 50000) -and ((Get-Item $dllDwmapi).Length -gt 50000)
}

if (-not $hookOK) {
    Write-Host "[*] 首次使用，正在安装组件..." -ForegroundColor Yellow

    # Kill Steam
    Stop-Process -Name "steam" -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2

    function Download-File([string]$Url, [string]$Target) {
        for ($i = 1; $i -le 3; $i++) {
            try {
                if (Test-Path $Target) { Remove-Item $Target -Force -ErrorAction SilentlyContinue }
                Invoke-WebRequest -Uri $Url -OutFile $Target -UseBasicParsing -TimeoutSec 120
                if ((Test-Path $Target) -and (Get-Item $Target).Length -gt 1000) { return $true }
            } catch {}
            if ($i -lt 3) { Start-Sleep -Seconds 2 }
        }
        return $false
    }

    # Remove conflicting DLLs
    foreach ($name in @("version.dll", "user32.dll", "steam.cfg", "hid.dll")) {
        $f = Join-Path $SteamPath $name
        if (Test-Path $f) { Remove-Item $f -Force -ErrorAction SilentlyContinue }
    }

    $ok1 = Download-File "$ApiBase/static/inject/xinput1_4.dll"     $dllXinput
    $ok2 = Download-File "$ApiBase/static/inject/dwmapi.dll"        $dllDwmapi
    $ok3 = Download-File "$ApiBase/static/inject/OpenSteamTool.dll" $dllOST

    if (-not $ok1 -or -not $ok2 -or -not $ok3) {
        Pause-Exit "[!] 组件下载失败，请检查网络"
        return
    }

    $luaDir = Join-Path $SteamPath "config\lua"
    New-Item -ItemType Directory -Force -Path $luaDir -ErrorAction SilentlyContinue | Out-Null

    @"
[log]
level = "info"

[manifest]
url = "opensteamtool"
"@ | Set-Content -Path $tomlPath -Encoding UTF8 -Force

    # Clean old SteamTools
    try {
        if (Test-Path "HKCU:\Software\Valve\Steamtools") {
            Remove-Item "HKCU:\Software\Valve\Steamtools" -Recurse -Force -ErrorAction SilentlyContinue
        }
    } catch {}

    Write-Host "[+] 组件安装完成" -ForegroundColor Green
}

# ── Redeem CDK ───────────────────────────────────────────────────────────────
Write-Host "[*] 正在验证激活码..." -ForegroundColor Cyan

$machine = "$env:COMPUTERNAME|$env:USERNAME"
$body = @{ cdk = $cdkCode; machine = $machine } | ConvertTo-Json -Compress

try {
    $resp = Invoke-RestMethod -Uri "$ApiBase/api/redeem" -Method Post `
        -ContentType "application/json; charset=utf-8" -Body $body -TimeoutSec 300
} catch {
    $detail = ""
    try { $detail = ($_.ErrorDetails.Message | ConvertFrom-Json).message } catch {}
    if (-not $detail) { $detail = $_.Exception.Message }
    Pause-Exit "[!] 激活失败: $detail"
    return
}

if (-not $resp.ok) {
    $msg = if ($resp.message) { $resp.message } else { "未知错误" }
    Pause-Exit "[!] 激活失败: $msg"
    return
}

$gameName = if ($resp.name) { $resp.name } else { "AppID $($resp.appid)" }
$appId = $resp.appid

Write-Host "[+] 验证通过: $gameName (AppID: $appId)" -ForegroundColor Green

# ── Write Lua script ─────────────────────────────────────────────────────────
$luaDir = Join-Path $SteamPath "config\lua"
New-Item -ItemType Directory -Force -Path $luaDir -ErrorAction SilentlyContinue | Out-Null

if ($resp.lua_b64) {
    $luaBytes = [Convert]::FromBase64String($resp.lua_b64)
    $luaFile = Join-Path $luaDir "game_$appId.lua"
    [IO.File]::WriteAllBytes($luaFile, $luaBytes)
    Write-Host "[+] 脚本写入: game_$appId.lua" -ForegroundColor Green
}

# ── Write manifests ──────────────────────────────────────────────────────────
if ($resp.manifests) {
    $depotDir = Join-Path $SteamPath "config\depotcache"
    New-Item -ItemType Directory -Force -Path $depotDir -ErrorAction SilentlyContinue | Out-Null
    foreach ($m in @($resp.manifests)) {
        try {
            $mBytes = [Convert]::FromBase64String($m.b64)
            [IO.File]::WriteAllBytes((Join-Path $depotDir $m.name), $mBytes)
            Write-Host "[+] 清单写入: $($m.name)" -ForegroundColor Green
        } catch {}
    }
}

# ── Restart Steam ────────────────────────────────────────────────────────────
Write-Host "[*] 重启 Steam..." -ForegroundColor Yellow
Stop-Process -Name "steam" -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 3
for ($i = 0; $i -lt 10; $i++) {
    if (-not (Get-Process -Name "steam" -ErrorAction SilentlyContinue)) { break }
    Start-Sleep -Milliseconds 500
}

$steamExe = Join-Path $SteamPath "steam.exe"
if (Test-Path $steamExe) {
    Start-Process -FilePath $steamExe -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host "========================================" -ForegroundColor Green
Write-Host "  激活成功！" -ForegroundColor Green
Write-Host "  游戏: $gameName" -ForegroundColor Green
Write-Host "  AppID: $appId" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Green
Write-Host ""

Pause-Exit "激活完成，按任意键关闭" "Green"

} catch {
    Write-Host ""
    Write-Host "======== 出错了 ========" -ForegroundColor Red
    Write-Host $_.Exception.Message -ForegroundColor Red
    Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "按任意键关闭..." -ForegroundColor Gray
    try { $null = $Host.UI.RawUI.ReadKey("NoEcho,IncludeKeyDown") } catch { Read-Host "按 Enter 关闭" }
}
