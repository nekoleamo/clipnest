# clipnest 一键安装（Windows）：下载 → 校验 SHA256 → 解到 %LOCALAPPDATA%\Programs\clipnest → 建开始菜单快捷方式。
#
# 用法（PowerShell）：
#   irm https://raw.githubusercontent.com/<owner>/<repo>/main/tools/install.ps1 | iex
#   irm https://gitee.com/<owner>/<repo>/raw/main/tools/install.ps1 | iex        # 国内
#
# 管道执行时没法传参数，用环境变量控制：
#   $env:CLIPNEST_FROM    = "github"           # github | gitee | auto（默认 auto：先 Gitee，连不上换 GitHub）
#   $env:CLIPNEST_VERSION = "v1.2.3"           # 装指定版本，默认最新
#   $env:CLIPNEST_DIR     = "D:\Apps\clipnest" # 装到哪，默认 %LOCALAPPDATA%\Programs\clipnest
#   $env:CLIPNEST_REPO    = "owner/repo"       # 仓库（发布脚本没填占位符时才需要）
#
# 注意：Invoke-WebRequest 下载不会带「来自 Internet」标记，所以不会触发 SmartScreen
# 的「未知发布者」提示（比浏览器直接下载干净）。

$ErrorActionPreference = 'Stop'

$GitHubRepo = 'nekoleamo/clipnest'
$GiteeRepo  = 'null_593_5354/clipnest'

$From       = if ($env:CLIPNEST_FROM) { $env:CLIPNEST_FROM } else { 'auto' }
$Version    = $env:CLIPNEST_VERSION
$InstallDir = if ($env:CLIPNEST_DIR) { $env:CLIPNEST_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\clipnest' }

function Say($msg) { Write-Host $msg }
function Die($msg) { Write-Host "❌ $msg" -ForegroundColor Red; exit 1 }

function RepoFor($host_) {
    if ($host_ -eq 'github') {
        if ($env:CLIPNEST_REPO) { return $env:CLIPNEST_REPO }
        return $GitHubRepo
    }
    if ($env:CLIPNEST_REPO_GITEE) { return $env:CLIPNEST_REPO_GITEE }
    if ($env:CLIPNEST_REPO) { return $env:CLIPNEST_REPO }
    return $GiteeRepo
}

function HaveRepo($host_) {
    $r = RepoFor $host_
    return ($r -and -not $r.StartsWith('__'))
}

function BaseFor($host_) {
    if ($host_ -eq 'github') { return 'https://github.com' }
    return 'https://gitee.com'
}

function ApiFor($host_) {
    if ($host_ -eq 'github') { return 'https://api.github.com/repos' }
    return 'https://gitee.com/api/v5/repos'
}

function Reachable($host_) {
    $url = "$(ApiFor $host_)/$(RepoFor $host_)/releases/latest"
    try {
        $null = Invoke-RestMethod -Uri $url -Headers @{ 'User-Agent' = 'clipnest-installer' } -TimeoutSec 5
        return $true
    } catch {
        return $false
    }
}

function ResolveHost {
    if ($From -ne 'auto') {
        if (-not (HaveRepo $From)) { Die "仓库没配置：先设 `$env:CLIPNEST_REPO = 'owner/repo'" }
        return $From
    }
    # 先 Gitee：国内网络下 GitHub 的资产域（release-assets.githubusercontent.com）常是
    # 「API 探针通、真下载断」，探针选不出这种半通状态。海外用户可 $env:CLIPNEST_FROM='github'。
    foreach ($h in @('gitee', 'github')) {
        if (-not (HaveRepo $h)) { continue }
        if (Reachable $h) { return $h }
        Say "· $h 连不上，换下一个"
    }
    Die 'GitHub 和 Gitee 都连不上；可以用 $env:CLIPNEST_FROM 指定，或先检查网络/代理'
}

function ResolveVersion($host_) {
    if ($Version) { return $Version }
    $url = "$(ApiFor $host_)/$(RepoFor $host_)/releases/latest"
    try {
        $rel = Invoke-RestMethod -Uri $url -Headers @{ 'User-Agent' = 'clipnest-installer' } -TimeoutSec 10
    } catch {
        Die "拿不到最新版本号（$host_）：$($_.Exception.Message)"
    }
    if (-not $rel.tag_name) { Die "更新源没有返回 tag_name（$host_）" }
    return $rel.tag_name
}

$host_ = ResolveHost
$repo  = RepoFor $host_
$tag   = ResolveVersion $host_
$ver   = $tag.TrimStart('v')
Say "== 下载源 $host_（$repo），版本 $tag"

$zipName = "clipnest-$ver-windows-x86_64.zip"
$dlBase  = "$(BaseFor $host_)/$repo/releases/download/$tag"
$tmp     = Join-Path $env:TEMP ("clipnest-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
    Say '· 取校验和'
    $sumsPath = Join-Path $tmp 'SHA256SUMS'
    try {
        Invoke-WebRequest -Uri "$dlBase/SHA256SUMS" -OutFile $sumsPath -UseBasicParsing -TimeoutSec 20
    } catch {
        Die '拿不到 SHA256SUMS（发行版里要一起传）'
    }
    $expect = (Get-Content $sumsPath | Where-Object { $_ -match [regex]::Escape($zipName) } | Select-Object -First 1)
    if (-not $expect) { Die "SHA256SUMS 里没有 $zipName" }
    $expect = ($expect -split '\s+')[0].ToLower()

    Say "· 下载 $zipName"
    $zipPath = Join-Path $tmp $zipName
    try {
        Invoke-WebRequest -Uri "$dlBase/$zipName" -OutFile $zipPath -UseBasicParsing -TimeoutSec 300
    } catch {
        Die "下载失败：$dlBase/$zipName（$($_.Exception.Message)）"
    }

    Say '· 校验 SHA256'
    $got = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $expect) { Die "校验失败（期望 $expect，实际 $got），已中止，什么都没装" }

    Say '· 解包'
    $unpack = Join-Path $tmp 'unpack'
    Expand-Archive -Path $zipPath -DestinationPath $unpack -Force
    $exe = Get-ChildItem -Path $unpack -Filter 'clipnest.exe' -Recurse | Select-Object -First 1
    if (-not $exe) { Die '压缩包里没有 clipnest.exe' }

    # 覆盖安装时先关掉正在运行的实例
    if (Test-Path (Join-Path $InstallDir 'clipnest.exe')) {
        Get-Process -Name 'clipnest' -ErrorAction SilentlyContinue | ForEach-Object {
            Say '· 先退出正在运行的 clipnest'
            $_.CloseMainWindow() | Out-Null
            Start-Sleep -Milliseconds 800
            if (-not $_.HasExited) { $_.Kill() }
        }
    }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Copy-Item -Path $exe.FullName -Destination (Join-Path $InstallDir 'clipnest.exe') -Force

    # 开始菜单快捷方式（GUI 托盘应用，不需要加 PATH）
    $startMenu = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs'
    if (Test-Path $startMenu) {
        try {
            $ws = New-Object -ComObject WScript.Shell
            $lnk = $ws.CreateShortcut((Join-Path $startMenu 'clipnest.lnk'))
            $lnk.TargetPath = Join-Path $InstallDir 'clipnest.exe'
            $lnk.WorkingDirectory = $InstallDir
            $lnk.Description = '剪贴板历史管理器'
            $lnk.Save()
        } catch {
            Say "· 建快捷方式失败（不影响使用）：$($_.Exception.Message)"
        }
    }

    Say "✅ 已装到 $InstallDir\clipnest.exe（版本 $tag）"
    Say ''
    Say '下一步：'
    Say '  1) 启动 clipnest（托盘出现 ❐ 图标）'
    Say '  2) Ctrl+Shift+V 唤出面板；托盘菜单里有设置'
    Say '  3) 数据在 %APPDATA%\clipnest（卸载时不会自动删，需要的话手动清）'
    if ($env:CLIPNEST_NO_OPEN -ne '1') {
        Start-Process -FilePath (Join-Path $InstallDir 'clipnest.exe') | Out-Null
    }
} finally {
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
