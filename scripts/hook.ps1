# hook.ps1 - OpenSteamTool Hook Installer
# Served via: irm http://server:8787/hook | iex
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
Write-Host "[*] 服务器: $ApiBase" -ForegroundColor Cyan

# ── Test connectivity ────────────────────────────────────────────────────────
Write-Host "[*] 测试连接..." -ForegroundColor Cyan
try {
    $testResp = Invoke-WebRequest -Uri "$ApiBase/api/ping" -UseBasicParsing -TimeoutSec 10
    Write-Host "[+] 服务器连接正常" -ForegroundColor Green
} catch {
    Pause-Exit "[!] 无法连接服务器 $ApiBase — 请检查：`n    1. 服务器是否已启动`n    2. 防火墙是否放行 8787 端口`n    3. IP 地址是否正确`n`n    错误: $_"
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
    foreach ($p in @("$env:ProgramFiles (x86)\Steam", "$env:ProgramFiles\Steam", "C:\Steam", "D:\Steam", "E:\Steam")) {
        if ($p -and (Test-Path (Join-Path $p "steam.exe"))) { return $p }
    }
    return $null
}

$SteamPath = Find-SteamPath
if (-not $SteamPath) {
    Pause-Exit "[!] 未找到 Steam 安装路径，请先安装 Steam"
    return
}
Write-Host "[+] Steam 路径: $SteamPath" -ForegroundColor Green

# ── Paths ────────────────────────────────────────────────────────────────────
$dllXinput  = Join-Path $SteamPath "xinput1_4.dll"
$dllDwmapi  = Join-Path $SteamPath "dwmapi.dll"
$dllOST     = Join-Path $SteamPath "OpenSteamTool.dll"
$tomlPath   = Join-Path $SteamPath "opensteamtool.toml"
$bridgeExe  = Join-Path $SteamPath "cdk-bridge.exe"

# ── Detect old SteamTools (must clean up) ────────────────────────────────────
$hasOldSteamTools = (Test-Path "HKCU:\Software\Valve\Steamtools") -or
    ((Test-Path $dllXinput) -and -not (Test-Path $dllOST))
if ($hasOldSteamTools) {
    Write-Host "[!] 检测到旧版 SteamTools，将自动清理并重新安装" -ForegroundColor Yellow
}

# ── Force reinstall: always replace DLLs to ensure latest version ────────────
$fullyInstalled = $false
if ($false) {  # DISABLED — always reinstall to pick up new DLL builds
    if ((Test-Path $dllXinput) -and (Test-Path $dllDwmapi) -and (Test-Path $dllOST) -and (Test-Path $tomlPath) -and (Test-Path $bridgeExe)) {
        $sizeOK = ((Get-Item $dllXinput).Length -gt 50000) -and ((Get-Item $dllDwmapi).Length -gt 50000) -and ((Get-Item $dllOST).Length -gt 100000) -and ((Get-Item $bridgeExe).Length -gt 100000)
        if ($sizeOK) { $fullyInstalled = $true }
    }
}

if ($fullyInstalled) {
    # Already fully installed — just make sure bridge is running
    if (-not (Get-Process -Name "cdk-bridge" -ErrorAction SilentlyContinue)) {
        Start-Process -FilePath $bridgeExe -WindowStyle Hidden -ErrorAction SilentlyContinue
        Write-Host "[+] CDK 激活器已启动" -ForegroundColor Green
    }
    Pause-Exit "[*] OpenSteamTool 已完整安装，无需重复操作。`n    双击桌面「Steam 激活游戏」即可激活 CDK。" "Green"
    return
}

Write-Host "[*] 开始安装 OpenSteamTool..." -ForegroundColor Cyan

# ── Kill Steam + all related processes ───────────────────────────────────────
Write-Host "[*] 正在关闭 Steam..." -ForegroundColor Yellow
foreach ($proc in @("steam", "steamwebhelper", "steamservice", "cdk-bridge")) {
    Stop-Process -Name $proc -Force -ErrorAction SilentlyContinue
}
Start-Sleep -Seconds 3
for ($i = 0; $i -lt 15; $i++) {
    if (-not (Get-Process -Name "steam" -ErrorAction SilentlyContinue)) { break }
    Stop-Process -Name "steam" -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 500
}
Write-Host "[+] Steam 已关闭" -ForegroundColor Green

# ── Delete old DLLs (ensure clean install) ───────────────────────────────────
Write-Host "[*] 清理旧文件..." -ForegroundColor Yellow
foreach ($old in @($dllXinput, $dllDwmapi, $dllOST, $bridgeExe)) {
    if (Test-Path $old) {
        Remove-Item $old -Force -ErrorAction SilentlyContinue
        if (Test-Path $old) {
            Write-Host "[!] 无法删除 $(Split-Path $old -Leaf)，文件可能被占用" -ForegroundColor Red
            Write-Host "    请确保 Steam 已完全关闭后重试" -ForegroundColor Yellow
            Pause-Exit "安装中断：请手动关闭 Steam 后重试"
            return
        }
        Write-Host "  已删除 $(Split-Path $old -Leaf)" -ForegroundColor Gray
    }
}
# Also clean conflicting injectors
foreach ($name in @("version.dll", "user32.dll", "steam.cfg", "hid.dll")) {
    $f = Join-Path $SteamPath $name
    if (Test-Path $f) { Remove-Item $f -Force -ErrorAction SilentlyContinue }
}

# ── Download helper ──────────────────────────────────────────────────────────
function Download-File([string]$Url, [string]$Target) {
    for ($i = 1; $i -le 3; $i++) {
        try {
            Write-Host "[*] 下载 $(Split-Path $Target -Leaf) (第${i}次)..." -ForegroundColor Cyan
            if (Test-Path $Target) { Remove-Item $Target -Force -ErrorAction SilentlyContinue }
            Invoke-WebRequest -Uri $Url -OutFile $Target -UseBasicParsing -TimeoutSec 120
            if ((Test-Path $Target) -and (Get-Item $Target).Length -gt 1000) {
                Write-Host "[+] $(Split-Path $Target -Leaf) OK" -ForegroundColor Green
                return $true
            }
        } catch {
            Write-Host "[!] 下载失败: $_" -ForegroundColor Red
        }
        if ($i -lt 3) { Start-Sleep -Seconds 2 }
    }
    return $false
}

# ── Remove conflicting injectors ─────────────────────────────────────────────
foreach ($name in @("version.dll", "user32.dll", "steam.cfg", "hid.dll")) {
    $f = Join-Path $SteamPath $name
    if (Test-Path $f) { Remove-Item $f -Force -ErrorAction SilentlyContinue }
}

# ── Download DLLs ────────────────────────────────────────────────────────────
$ok1 = Download-File "$ApiBase/static/inject/xinput1_4.dll"     $dllXinput
$ok2 = Download-File "$ApiBase/static/inject/dwmapi.dll"        $dllDwmapi
$ok3 = Download-File "$ApiBase/static/inject/OpenSteamTool.dll" $dllOST

if (-not $ok1 -or -not $ok2 -or -not $ok3) {
    Pause-Exit "[!] DLL 下载失败，请检查网络连接后重试"
    return
}

# ── Create config/lua directory ──────────────────────────────────────────────
$luaDir = Join-Path $SteamPath "config\lua"
New-Item -ItemType Directory -Force -Path $luaDir -ErrorAction SilentlyContinue | Out-Null
Write-Host "[+] 目录就绪: config\lua" -ForegroundColor Green

# ── Write opensteamtool.toml ─────────────────────────────────────────────────
@"
[log]
level = "info"

[manifest]
url = "opensteamtool"
"@
[IO.File]::WriteAllText($tomlPath, $tomlContent, [System.Text.UTF8Encoding]::new($false))
Write-Host "[+] 配置写入: opensteamtool.toml" -ForegroundColor Green

# ── Clean up old SteamTools ──────────────────────────────────────────────────
try {
    if (Test-Path "HKCU:\Software\Valve\Steamtools") {
        Remove-Item -Path "HKCU:\Software\Valve\Steamtools" -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "[+] 已清理旧版 SteamTools 注册表" -ForegroundColor Green
    }
} catch {}

$stPluginDir = Join-Path $SteamPath "config\stplug-in"
if (Test-Path $stPluginDir) {
    Get-ChildItem -Path $stPluginDir -Filter "*.st" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
}

# ── Write hook.json ──────────────────────────────────────────────────────────
$hookDir = Join-Path $env:LOCALAPPDATA "steam"
New-Item -ItemType Directory -Force -Path $hookDir -ErrorAction SilentlyContinue | Out-Null
$hookObj = @{
    api       = $ApiBase
    steam     = ($SteamPath -replace '\\', '/')
    engine    = "opensteamtool"
    installed = (Get-Date -Format "yyyy-MM-dd HH:mm:ss")
}
$hookJsonStr = $hookObj | ConvertTo-Json
$hookJsonPath = Join-Path $hookDir "hook.json"
[IO.File]::WriteAllText($hookJsonPath, $hookJsonStr, [System.Text.UTF8Encoding]::new($false))
Write-Host "[+] 状态写入: hook.json" -ForegroundColor Green

# ── Start Steam ──────────────────────────────────────────────────────────────
$steamExe = Join-Path $SteamPath "steam.exe"
if (Test-Path $steamExe) {
    Write-Host "[*] 正在启动 Steam..." -ForegroundColor Cyan
    Start-Process -FilePath $steamExe -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host "========================================" -ForegroundColor Green
Write-Host "  安装完成！" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Green
Write-Host ""
Write-Host "  激活游戏：" -ForegroundColor White
Write-Host "  Steam → 游戏 → 激活产品 → 输入CDK" -ForegroundColor Yellow
Write-Host ""

Pause-Exit "安装成功，按任意键关闭窗口" "Green"

} catch {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Red
    Write-Host "  安装过程出错" -ForegroundColor Red
    Write-Host "========================================" -ForegroundColor Red
    Write-Host ""
    Write-Host "错误详情:" -ForegroundColor Yellow
    Write-Host $_.Exception.Message -ForegroundColor Red
    Write-Host ""
    Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
    Write-Host ""
    Write-Host "按任意键关闭..." -ForegroundColor Gray
    try { $null = $Host.UI.RawUI.ReadKey("NoEcho,IncludeKeyDown") } catch { Read-Host "按 Enter 关闭" }
}
