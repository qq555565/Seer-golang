@echo off
chcp 65001 >nul
cd /d "%~dp0.."
echo 正在启动游戏服务器（已跳过免责申明确认）...
echo 工作目录: %CD%
bin\gameserver.exe -y
if errorlevel 1 (
    echo.
    echo 服务器异常退出，请查看上方错误信息。
    pause
)
