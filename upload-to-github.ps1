# 将当前项目上传到 GitHub (qq555565/Seer-golang)
# 使用前请先安装 Git: https://git-scm.com/download/win
# 首次推送需在 GitHub 上创建好仓库，并配置好认证（HTTPS 或 SSH）

$ErrorActionPreference = "Stop"
$repoRoot = $PSScriptRoot
$remoteUrl = "https://github.com/qq555565/Seer-golang.git"

# 尝试找到 git（PATH 或常见安装路径）
$gitCmd = $null
if (Get-Command git -ErrorAction SilentlyContinue) { $gitCmd = "git" }
elseif (Test-Path "C:\Program Files\Git\bin\git.exe") { $gitCmd = "C:\Program Files\Git\bin\git.exe" }
elseif (Test-Path "$env:LOCALAPPDATA\Programs\Git\bin\git.exe") { $gitCmd = "$env:LOCALAPPDATA\Programs\Git\bin\git.exe" }

if (-not $gitCmd) {
    Write-Host "未找到 Git。请先安装: https://git-scm.com/download/win" -ForegroundColor Red
    exit 1
}

Set-Location $repoRoot

if (-not (Test-Path ".git")) {
    & $gitCmd init
    Write-Host "已初始化 Git 仓库。" -ForegroundColor Green
}

$remotes = & $gitCmd remote 2>$null
if ($remotes -notmatch "origin") {
    & $gitCmd remote add origin $remoteUrl
    Write-Host "已添加远程 origin: $remoteUrl" -ForegroundColor Green
} else {
    & $gitCmd remote set-url origin $remoteUrl
    Write-Host "已更新远程 origin 地址。" -ForegroundColor Green
}

& $gitCmd add .
& $gitCmd status
Write-Host "`n请确认要提交的文件，然后执行:" -ForegroundColor Yellow
Write-Host "  git commit -m `"你的提交说明`"" -ForegroundColor Cyan
Write-Host "  git branch -M main" -ForegroundColor Cyan
Write-Host "  git push -u origin main" -ForegroundColor Cyan
Write-Host "`n若已配置好 GitHub 认证，也可直接运行: .\upload-to-github-push.ps1" -ForegroundColor Gray
