# 执行提交并推送到 GitHub（在 upload-to-github.ps1 之后使用）
$ErrorActionPreference = "Stop"
$repoRoot = $PSScriptRoot

$gitCmd = $null
if (Get-Command git -ErrorAction SilentlyContinue) { $gitCmd = "git" }
elseif (Test-Path "C:\Program Files\Git\bin\git.exe") { $gitCmd = "C:\Program Files\Git\bin\git.exe" }
elseif (Test-Path "$env:LOCALAPPDATA\Programs\Git\bin\git.exe") { $gitCmd = "$env:LOCALAPPDATA\Programs\Git\bin\git.exe" }

if (-not $gitCmd) {
    Write-Host "未找到 Git。" -ForegroundColor Red
    exit 1
}

Set-Location $repoRoot

& $gitCmd add .
& $gitCmd commit -m "Initial commit: Seer golang project"
if ($LASTEXITCODE -ne 0) {
    Write-Host "提交失败或没有变更。若已提交过，可直接执行 git push。" -ForegroundColor Yellow
    exit $LASTEXITCODE
}
& $gitCmd branch -M main
& $gitCmd push -u origin main
Write-Host "推送完成。" -ForegroundColor Green
