# Remo Panel — Windows セットアップ
#
#   PowerShell で実行:
#     powershell -ExecutionPolicy Bypass -File .\install-windows.ps1
#
#   アンインストール:
#     powershell -ExecutionPolicy Bypass -File .\install-windows.ps1 -Uninstall
#
# 管理者権限は不要です（ログオン時に自分の権限で動くタスクを作ります）。

param(
    [int]$Port = 8080,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$TaskName = "RemoPanel"
$InstallDir = Join-Path $env:LOCALAPPDATA "RemoPanel"
$Target = Join-Path $InstallDir "remo-panel.exe"
$LogPath = Join-Path $env:USERPROFILE ".remo-panel\remo-panel.log"

if ($Uninstall) {
    Stop-Process -Name "remo-panel" -Force -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "自動起動を解除しました。設定 (~\.remo-panel) は残しています。"
    exit 0
}

# --- 1. コンソールの出ない版を配置 ------------------------------------------
$Root = Split-Path -Parent $PSScriptRoot
$Source = Join-Path $Root "dist\remo-panel-windows-silent.exe"
if (-not (Test-Path $Source)) {
    throw "$Source が見つかりません。dist フォルダごとコピーしてから実行してください。"
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Stop-Process -Name "remo-panel" -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 300
Copy-Item $Source $Target -Force

# Windows はダウンロードしたファイルに «ブロック» マークを付けるので外す
Unblock-File -Path $Target -ErrorAction SilentlyContinue

# --- 2. ログオン時に起動するタスクを登録 ------------------------------------
$action = New-ScheduledTaskAction -Execute $Target `
    -Argument "-port $Port -log `"$LogPath`"" -WorkingDirectory $InstallDir
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Settings $settings -Description "Nature Remo のローカル操作パネル" | Out-Null

Start-ScheduledTask -TaskName $TaskName
Start-Sleep -Seconds 2

if (Get-Process -Name "remo-panel" -ErrorAction SilentlyContinue) {
    Write-Host ""
    Write-Host "✓ 起動しました: http://localhost:$Port/"
    Write-Host "  ログ: $LogPath"
    Write-Host "  設定: $env:USERPROFILE\.remo-panel\config.json"
    Write-Host ""
    Write-Host "ブラウザで開いて、設定からアクセストークンを登録してください。"
    Write-Host "解除: .\install-windows.ps1 -Uninstall"
} else {
    Write-Warning "起動を確認できませんでした。ログを確認してください: $LogPath"
}
