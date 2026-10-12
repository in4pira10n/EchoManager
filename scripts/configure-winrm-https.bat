@echo off
setlocal

set "SCRIPT=%~dp0configure-winrm-https.ps1"
if not exist "%SCRIPT%" (
    echo Could not find configure-winrm-https.ps1 next to this file.
    pause
    exit /b 1
)

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT%"
exit /b %ERRORLEVEL%
