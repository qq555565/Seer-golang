# 手动运行即可编译；加 -Watch 则监听 .go 变化自动重新编译
# 用法: .\build.ps1  或  .\build.ps1 -Watch
param([switch]$Watch)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
if (-not $root) { $root = Get-Location }
Set-Location $root

function Build {
    $ts = Get-Date -Format "yyyy-MM-dd_HHmm"
    $out = "test\gameserver_$ts.exe"
    Write-Host "[$(Get-Date -Format 'HH:mm:ss')] 编译中 -> $out" -ForegroundColor Cyan
    go build -o $out ./cmd/gameserver
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[$(Get-Date -Format 'HH:mm:ss')] 编译成功: $out" -ForegroundColor Green
        return $true
    } else {
        Write-Host "[$(Get-Date -Format 'HH:mm:ss')] 编译失败" -ForegroundColor Red
        return $false
    }
}

if ($Watch) {
    Write-Host "监听模式: 修改 .go 文件将自动重新编译 (Ctrl+C 退出)" -ForegroundColor Yellow
    Build | Out-Null
    $lastHash = $null
    while ($true) {
        Start-Sleep -Seconds 2
        $files = Get-ChildItem -Path $root -Recurse -Include "*.go" -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -notmatch "\\vendor\\" }
        $hash = ($files | Get-FileHash -Algorithm MD5 -ErrorAction SilentlyContinue).Hash -join ""
        if ($null -ne $lastHash -and $hash -ne $lastHash) {
            Build | Out-Null
        }
        $lastHash = $hash
    }
} else {
    Build
}
