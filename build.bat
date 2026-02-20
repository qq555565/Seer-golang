@echo off
chcp 65001 >nul
cd /d "%~dp0"
if not exist test mkdir test
for /f "usebackq" %%i in (`powershell -NoProfile -Command "Get-Date -Format 'yyyy-MM-dd_HHmm'"`) do set TIMESTAMP=%%i
set OUT=test\gameserver_%TIMESTAMP%.exe
echo 正在编译 gameserver ...
go build -ldflags "-s -w" -o "%OUT%" ./cmd/gameserver
if %errorlevel% neq 0 (
    echo 编译失败
    pause
    exit /b 1
)
echo.
echo 编译成功，输出到: %OUT%
pause
