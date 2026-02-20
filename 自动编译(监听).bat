@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo 已启动监听模式：修改 .go 文件将自动重新编译，按 Ctrl+C 退出
powershell -NoProfile -ExecutionPolicy Bypass -File ".\build.ps1" -Watch
