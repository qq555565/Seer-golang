# 使用 Token 推送到 GitHub（只在本机运行，Token 不会提交到仓库）
# 用法：
#   1. 在 GitHub 创建 PAT: https://github.com/settings/tokens (勾选 repo)
#   2. 在项目根目录创建文件 .github-token，内容只写一行：你的 Token
#   3. 运行: .\push-with-token.ps1
# 或设置环境变量: $env:GITHUB_TOKEN = "你的Token"; .\push-with-token.ps1

$ErrorActionPreference = "Stop"
$repoRoot = $PSScriptRoot
Write-Host "正在推送到 GitHub..." -ForegroundColor Cyan
$remote = "https://github.com/qq555565/Seer-golang.git"
$gitExe = "C:\Program Files\Git\bin\git.exe"
if (-not (Test-Path $gitExe)) { $gitExe = "git" }

$token = $env:GITHUB_TOKEN
if (-not $token -and (Test-Path "$repoRoot\.github-token")) {
    $token = (Get-Content "$repoRoot\.github-token" -Raw).Trim()
}
if (-not $token) {
    Write-Host "请先设置 Token：" -ForegroundColor Yellow
    Write-Host "  方式1: 在项目根目录创建 .github-token 文件，内容为你的 GitHub Personal Access Token" -ForegroundColor Gray
    Write-Host "  方式2: 在 PowerShell 中执行: `$env:GITHUB_TOKEN = '你的Token'" -ForegroundColor Gray
    Write-Host "创建 PAT: https://github.com/settings/tokens (勾选 repo 权限)" -ForegroundColor Gray
    Read-Host "按回车键退出"
    exit 1
}

$user = "qq555565"
$urlWithToken = "https://${user}:${token}@github.com/qq555565/Seer-golang.git"

$err = 0
Push-Location $repoRoot
try {
    # 增大 POST 缓冲区，避免大仓库推送时 "unable to rewind rpc post data"
    & $gitExe config http.postBuffer 524288000
    & $gitExe remote set-url origin $urlWithToken
    & $gitExe push -u origin main --force
    $err = $LASTEXITCODE
} catch {
    Write-Host "错误: $_" -ForegroundColor Red
    $err = 1
} finally {
    & $gitExe remote set-url origin $remote
}
Pop-Location

if ($err -eq 0) { Write-Host "`n推送成功。" -ForegroundColor Green } else { Write-Host "`n推送失败，请检查上方错误信息。" -ForegroundColor Red }
Read-Host "`n按回车键关闭窗口"
exit $err
