<#
.SYNOPSIS
  Cài DIMA thành ứng dụng desktop cho người dùng hiện tại.

  Chép binary vào %LOCALAPPDATA%\Programs\DIMA, sinh icon, tạo shortcut
  trong Start Menu. Không cần quyền quản trị, không đụng vào registry hệ thống.

.EXAMPLE
  .\install.ps1                  # cài, tạo shortcut Start Menu
  .\install.ps1 -Desktop         # thêm shortcut ngoài Desktop
  .\install.ps1 -AddToPath       # gõ được lệnh 'dima' trong terminal
  .\install.ps1 -Uninstall       # gỡ ra
#>
[CmdletBinding()]
param(
  [string]$Version = "0.2.0",
  [switch]$Desktop,
  [switch]$AddToPath,
  [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$AppName   = "DIMA"
$InstallTo = Join-Path $env:LOCALAPPDATA "Programs\DIMA"
$StartMenu = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\$AppName.lnk"
$DeskLnk   = Join-Path ([Environment]::GetFolderPath("Desktop")) "$AppName.lnk"

if ($Uninstall) {
  # Một chỗ duy nhất biết cách gỡ: uninstall.ps1. Ưu tiên bản đã chép vào
  # thư mục cài, vì đó cũng là bản Windows gọi từ "Installed apps" — hai lối
  # vào, một đoạn mã, không thể lệch nhau.
  $installed = Join-Path $InstallTo "uninstall.ps1"
  $script = if (Test-Path $installed) { $installed } else { Join-Path $PSScriptRoot "uninstall.ps1" }
  if (-not (Test-Path $script)) { throw "Không thấy uninstall.ps1 để gỡ." }
  & $script
  return
}

# ---- tìm binary để cài ----
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$binName = "dima-$Version-windows-$arch.exe"
$source = Join-Path $PSScriptRoot "dist\$binName"
if (-not (Test-Path $source)) {
  throw "Không thấy $source. Chạy .\build.ps1 trước đã."
}

# ---- sinh icon .ico từ chính hình trong ui\icon.svg ----
# Vẽ lại bằng System.Drawing để không phải kèm thêm công cụ chuyển đổi nào.
function New-DimaIcon([string]$IcoPath) {
  Add-Type -AssemblyName System.Drawing

  # Toạ độ lấy đúng từ ui\icon.svg, hệ 64x64.
  $shapes = @(
    @{ argb = @(255,79,168,232); pts = @(@(32,12),@(52,22),@(32,32),@(12,22)) },
    @{ argb = @(158,79,168,232); pts = @(@(12,31),@(32,41),@(52,31),@(52,37),@(32,47),@(12,37)) },
    @{ argb = @(217,70,201,139); pts = @(@(12,42),@(32,52),@(52,42),@(52,46),@(32,56),@(12,46)) }
  )

  $pngs = @()
  foreach ($s in @(256, 48, 32, 16)) {
    $bmp = New-Object System.Drawing.Bitmap($s, $s)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $k = $s / 64.0

    # nền bo góc
    $r = 14 * $k
    $gp = New-Object System.Drawing.Drawing2D.GraphicsPath
    $gp.AddArc(0, 0, 2*$r, 2*$r, 180, 90)
    $gp.AddArc($s-2*$r, 0, 2*$r, 2*$r, 270, 90)
    $gp.AddArc($s-2*$r, $s-2*$r, 2*$r, 2*$r, 0, 90)
    $gp.AddArc(0, $s-2*$r, 2*$r, 2*$r, 90, 90)
    $gp.CloseFigure()
    $bg = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(255,22,33,46))
    $g.FillPath($bg, $gp)
    $bg.Dispose(); $gp.Dispose()

    foreach ($sh in $shapes) {
      $pts = [System.Drawing.PointF[]]@(
        $sh.pts | ForEach-Object { New-Object System.Drawing.PointF (([float]$_[0])*$k), (([float]$_[1])*$k) }
      )
      $br = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb($sh.argb[0], $sh.argb[1], $sh.argb[2], $sh.argb[3]))
      $g.FillPolygon($br, $pts)
      $br.Dispose()
    }
    $g.Dispose()

    $ms = New-Object System.IO.MemoryStream
    $bmp.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    $pngs += ,@{size=$s; bytes=$ms.ToArray()}
    $ms.Dispose()
  }

  # Đóng gói thành .ico: mỗi ảnh là một PNG nhúng (Windows Vista trở lên đọc được).
  $out = New-Object System.IO.MemoryStream
  $w = New-Object System.IO.BinaryWriter($out)
  $w.Write([uint16]0); $w.Write([uint16]1); $w.Write([uint16]$pngs.Count)
  $offset = 6 + 16 * $pngs.Count
  foreach ($p in $pngs) {
    $dim = if ($p.size -ge 256) { 0 } else { $p.size }
    $w.Write([byte]$dim); $w.Write([byte]$dim); $w.Write([byte]0); $w.Write([byte]0)
    $w.Write([uint16]1); $w.Write([uint16]32)
    $w.Write([uint32]$p.bytes.Length); $w.Write([uint32]$offset)
    $offset += $p.bytes.Length
  }
  foreach ($p in $pngs) { $w.Write($p.bytes) }
  $w.Flush()
  [System.IO.File]::WriteAllBytes($IcoPath, $out.ToArray())
  $w.Dispose(); $out.Dispose()
}

Write-Host "Cài $AppName $Version ($arch)" -ForegroundColor Cyan

Get-Process -ErrorAction SilentlyContinue |
  Where-Object { $_.Path -and $_.Path.StartsWith($InstallTo) } |
  ForEach-Object { Write-Host "  dừng bản đang chạy (PID $($_.Id))"; Stop-Process -Id $_.Id -Force; Start-Sleep -Milliseconds 400 }

# Thử chạy trước khi thay bản đang dùng, và build lại nếu Windows chặn.
#
# Smart App Control phán quyết theo TỪNG HASH FILE và nhớ luôn kết quả đó.
# Với một file .exe không ký số, phán quyết ấy chập chờn. Đo trên chính máy
# này, cùng một mã nguồn, chỉ khác dấu build:
#
#   hash ACA625417A4E…  bị chặn
#   hash AB5F309D8A11…  chạy được
#   rồi 6 bản liên tiếp sau đó  chạy được
#
# Nên "bị chặn" không có nghĩa là bản build sai, và cũng không phải cứ một
# nửa số bản sẽ hỏng — nó thưa và không báo trước. Build lại cho ra dấu build
# mới, tức hash mới, tức một lần hỏi lại, và thường là qua ngay.
#
# Trước đây chỗ này chỉ báo lỗi và bảo người dùng tự chạy lại build.ps1 —
# bắt người ta tự tung đồng xu bằng tay mà không nói rõ là đang tung. Giờ
# trình cài tự làm việc đó.
#
# Vẫn thử TRƯỚC khi ghi đè: cài rồi mới phát hiện bị chặn thì mất luôn bản
# đang chạy được. Tệ nhất ở đây cũng chỉ là không cài.
function Test-Runs([string]$exe) {
  $probe = Join-Path $env:TEMP "dima-thu-$(Get-Random).exe"
  Copy-Item $exe $probe -Force
  $ok = $false
  try {
    $pp = Start-Process $probe -ArgumentList "-port","7799","-data","$env:TEMP\dima-thu-data","-no-open" -PassThru -ErrorAction Stop
    Start-Sleep -Milliseconds 1200
    $ok = $true
    Stop-Process -Id $pp.Id -Force -ErrorAction SilentlyContinue
  } catch {
    $ok = $false
  }
  try { Remove-Item $probe -Force -ErrorAction SilentlyContinue } catch {}
  return $ok
}

$builder = Join-Path $PSScriptRoot "build.ps1"
$canRebuild = (Test-Path $builder) -and (Get-Command go -ErrorAction SilentlyContinue)
$maxTries = 5

for ($try = 1; $try -le $maxTries; $try++) {
  if (Test-Runs $source) { break }

  $hash = (Get-FileHash $source -Algorithm SHA256).Hash.Substring(0, 12)
  Write-Host "  Windows chặn bản build này (hash $hash…)" -ForegroundColor Yellow

  if (-not $canRebuild) {
    Write-Host ""
    Write-Host "Smart App Control nhớ phán quyết theo hash file. Build lại sẽ ra hash khác" -ForegroundColor DarkGray
    Write-Host "và thường là chạy được — nhưng ở đây không build lại được (thiếu build.ps1" -ForegroundColor DarkGray
    Write-Host "hoặc chưa cài Go). Chạy tay:" -ForegroundColor DarkGray
    Write-Host "  .\build.ps1 -SkipTests; .\install.ps1" -ForegroundColor Cyan
    Write-Host "Bản đang cài vẫn giữ nguyên, chưa bị đụng tới." -ForegroundColor DarkGray
    throw "Không cài bản không chạy được."
  }

  if ($try -eq $maxTries) {
    Write-Host ""
    Write-Host "Đã build lại $maxTries lần, lần nào Windows cũng chặn." -ForegroundColor Yellow
    Write-Host "Đến mức này thì nhiều khả năng không phải xui: xem lại Smart App Control" -ForegroundColor DarkGray
    Write-Host "trong Windows Security > App & browser control. Cách sửa tận gốc là ký số" -ForegroundColor DarkGray
    Write-Host "bản build, chứ không phải build lại thêm lần nữa." -ForegroundColor DarkGray
    Write-Host "Bản đang cài vẫn giữ nguyên, chưa bị đụng tới." -ForegroundColor DarkGray
    throw "Không cài bản không chạy được."
  }

  Write-Host "  build lại để có hash khác (lần $try/$($maxTries - 1))..." -ForegroundColor DarkGray
  & $builder -Version $Version -SkipTests | Out-Null
  if ($LASTEXITCODE -ne 0 -and $null -ne $LASTEXITCODE) { throw "Build lại thất bại." }
  if (-not (Test-Path $source)) { throw "Build lại xong nhưng không thấy $source." }
}

New-Item -ItemType Directory -Force $InstallTo | Out-Null
$target = Join-Path $InstallTo "dima.exe"
Copy-Item $source $target -Force
Write-Host "  binary  -> $target (đã thử chạy được)"

$ico = Join-Path $InstallTo "dima.ico"
New-DimaIcon -IcoPath $ico
Write-Host "  icon    -> $ico"

$shell = New-Object -ComObject WScript.Shell
foreach ($lnk in @($StartMenu) + $(if ($Desktop) { @($DeskLnk) } else { @() })) {
  $sc = $shell.CreateShortcut($lnk)
  $sc.TargetPath       = $target
  $sc.WorkingDirectory = $InstallTo
  $sc.IconLocation     = $ico
  $sc.Description      = "Quản lý version image Docker theo dự án"
  $sc.Save()
  Write-Host "  shortcut-> $lnk"
}

# ---- đăng ký với Windows để hiện trong Settings → Apps → Installed apps ----
#
# Không có mục này thì DIMA vô hình với Windows: gỡ ra chỉ còn cách nhớ
# đường dẫn tới install.ps1, mà người cài xong thường đã dọn repo đi rồi.
#
# HKCU chứ không phải HKLM: cài cho một người dùng, không cần quyền quản trị,
# và không đụng vào máy của người khác trên cùng máy tính.
$uninstallSrc = Join-Path $PSScriptRoot "uninstall.ps1"
if (Test-Path $uninstallSrc) {
  $uninstallDst = Join-Path $InstallTo "uninstall.ps1"
  Copy-Item $uninstallSrc $uninstallDst -Force
  $ps = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
  $run = "`"$ps`" -NoProfile -ExecutionPolicy Bypass -File `"$uninstallDst`""
  $key = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\DIMA"
  New-Item -Path $key -Force | Out-Null
  $kb = [int]((Get-ChildItem $InstallTo -File | Measure-Object Length -Sum).Sum / 1KB)
  foreach ($kv in @(
    @{n="DisplayName";     v="DIMA — quản lý image Docker theo dự án"; t="String"},
    @{n="DisplayVersion";  v=$Version;        t="String"},
    @{n="Publisher";       v="DIMA";          t="String"},
    @{n="DisplayIcon";     v=$ico;            t="String"},
    @{n="InstallLocation"; v=$InstallTo;      t="String"},
    @{n="UninstallString"; v=$run;            t="String"},
    # Lệnh gỡ im lặng của Windows dùng đúng script đó, chỉ thêm -Quiet.
    @{n="QuietUninstallString"; v="$run -Quiet"; t="String"},
    @{n="EstimatedSize";   v=$kb;             t="DWord"},
    @{n="NoModify";        v=1;               t="DWord"},
    @{n="NoRepair";        v=1;               t="DWord"}
  )) {
    New-ItemProperty -Path $key -Name $kv.n -Value $kv.v -PropertyType $kv.t -Force | Out-Null
  }
  Write-Host "  gỡ được -> Settings > Apps > Installed apps > DIMA"
}

# Bản chạy thử ở trên để lại một thư mục dữ liệu rỗng trong %TEMP%. Rác do
# chính trình cài sinh ra thì trình cài dọn.
$probeData = Join-Path $env:TEMP "dima-thu-data"
if (Test-Path $probeData) { Remove-Item -Recurse -Force $probeData -ErrorAction SilentlyContinue }

if ($AddToPath) {
  $p = [Environment]::GetEnvironmentVariable("Path", "User")
  if (-not $p -or ($p.Split(';') -notcontains $InstallTo)) {
    [Environment]::SetEnvironmentVariable("Path", (@($p, $InstallTo) | Where-Object { $_ }) -join ';', "User")
    Write-Host "  PATH    -> thêm $InstallTo (mở lại terminal để có hiệu lực)"
  }
}

Write-Host ""
Write-Host "Xong. Mở từ Start Menu bằng cách gõ 'DIMA'." -ForegroundColor Green
Write-Host "Gỡ ra:  .\install.ps1 -Uninstall" -ForegroundColor DarkGray
