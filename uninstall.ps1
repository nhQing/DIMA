<#
.SYNOPSIS
  Gỡ DIMA khỏi máy này.

  File này được install.ps1 chép vào chính thư mục cài, và mục "Installed
  apps" của Windows trỏ tới bản chép đó. Nhờ vậy gỡ được kể cả khi thư mục
  mã nguồn đã bị xoá — người cài xong rồi dọn repo là chuyện bình thường,
  và một trình gỡ chỉ chạy được khi còn mã nguồn thì coi như không có.

  Dữ liệu trong ~\.dima KHÔNG bị xoá: lịch sử build và cấu hình là của
  người dùng, không phải của ứng dụng. Muốn xoá thì -Data.

.EXAMPLE
  .\uninstall.ps1            # gỡ ứng dụng, giữ dữ liệu
  .\uninstall.ps1 -Data      # gỡ luôn cả ~\.dima
  .\uninstall.ps1 -Quiet     # không in gì, dùng cho lệnh gỡ im lặng của Windows
#>
[CmdletBinding()]
param(
  [switch]$Data,
  [switch]$Quiet
)

$ErrorActionPreference = "Stop"
$AppName   = "DIMA"

# Gỡ đúng bản cài mà FILE NÀY thuộc về, không phải "bản cài ở chỗ mặc định".
#
# install.ps1 chép file này vào thư mục cài, nên khi Windows gọi nó từ
# "Installed apps", $PSScriptRoot chính là thư mục cần gỡ. Chỉ khi chạy từ
# thư mục mã nguồn — nơi không có dima.exe — mới quay về chỗ mặc định.
#
# Khác biệt này không phải chuyện lý thuyết: một bản sao của script đặt ở
# thư mục khác mà vẫn xoá thư mục mặc định là một khẩu súng chĩa vào chân,
# và nó đã nổ một lần trong lúc thử.
$InstallTo = Join-Path $env:LOCALAPPDATA "Programs\DIMA"
if ($PSScriptRoot -and (Test-Path (Join-Path $PSScriptRoot "dima.exe"))) {
  $InstallTo = $PSScriptRoot
}
$StartMenu = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\$AppName.lnk"
$DeskLnk   = Join-Path ([Environment]::GetFolderPath("Desktop")) "$AppName.lnk"
$RegKey    = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\DIMA"

function Say([string]$msg, [string]$color = "Gray") {
  if (-not $Quiet) { Write-Host $msg -ForegroundColor $color }
}

Say "Gỡ $AppName..." "Cyan"

# Tiến trình đang chạy giữ file .exe, Windows không xoá được file đang mở.
Get-Process -ErrorAction SilentlyContinue |
  Where-Object { $_.Path -and $_.Path.StartsWith($InstallTo) } |
  ForEach-Object {
    Say "  dừng tiến trình đang chạy (PID $($_.Id))"
    Stop-Process -Id $_.Id -Force
  }
Start-Sleep -Milliseconds 400

foreach ($lnk in @($StartMenu, $DeskLnk)) {
  if (Test-Path $lnk) { Remove-Item $lnk -Force; Say "  bỏ shortcut $lnk" }
}

if (Test-Path $RegKey) { Remove-Item $RegKey -Recurse -Force; Say "  bỏ khỏi danh sách ứng dụng của Windows" }

$p = [Environment]::GetEnvironmentVariable("Path", "User")
if ($p -and $p.Split(';') -contains $InstallTo) {
  [Environment]::SetEnvironmentVariable("Path", (($p.Split(';') | Where-Object { $_ -ne $InstallTo }) -join ';'), "User")
  Say "  bỏ khỏi PATH"
}

if ($Data) {
  $dataDir = Join-Path $env:USERPROFILE ".dima"
  if (Test-Path $dataDir) { Remove-Item -Recurse -Force $dataDir; Say "  xóa dữ liệu $dataDir" "Yellow" }
}

# Xoá thư mục cài sau cùng, vì file này đang nằm trong đó.
#
# PowerShell đọc cả script vào bộ nhớ trước khi chạy, nên tự xoá mình giữa
# chừng là được — nhưng chỉ khi không còn gì khác trong thư mục đang bị giữ.
if (Test-Path $InstallTo) {
  try {
    Remove-Item -Recurse -Force $InstallTo
    Say "  xóa $InstallTo"
  } catch {
    Say "  chưa xóa được $InstallTo — có thể còn file đang mở." "Yellow"
    Say "  Đóng hết cửa sổ DIMA rồi xóa tay thư mục đó là xong." "DarkGray"
  }
}

if (-not $Data) {
  Say "Đã gỡ. Dữ liệu trong ~\.dima vẫn giữ nguyên (dùng -Data để xóa luôn)." "Green"
} else {
  Say "Đã gỡ, và đã xóa cả dữ liệu." "Green"
}
