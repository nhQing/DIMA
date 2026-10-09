<#
.SYNOPSIS
  Build DIMA cho mọi nền tảng vào .\dist

.EXAMPLE
  .\build.ps1                 # chạy test rồi build cho chính máy này (một file)
  .\build.ps1 -All            # build cả 6 nền tảng, để đem cho người khác
  .\build.ps1 -Only darwin    # chỉ build cho macOS
  .\build.ps1 -SkipTests      # bỏ qua test
#>
[CmdletBinding()]
param(
  [string]$Version = "0.2.0",
  [ValidateSet("all","windows","darwin","linux")]
  [string]$Only = "",
  # Build cho mọi nền tảng. Chỉ cần khi đóng gói đem cho người khác.
  [switch]$All,
  [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Không tìm thấy lệnh 'go'. Cài Go 1.22 trở lên rồi mở lại cửa sổ PowerShell."
}

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"

if (-not $SkipTests) {
  Write-Host "Chạy test..." -ForegroundColor Cyan
  # Dấu build cho cả file test, cùng lý do với bản phát hành ở dưới.
  #
  # Smart App Control nhớ phán quyết theo hash file, mà `go test` dựng ra một
  # binary tái lập được: cùng mã nguồn thì cùng hash. Nên một lần bị chặn là
  # mọi lần sau đều bị chặn y hệt, dù build lại bao nhiêu lần — lúc đó
  # $LASTEXITCODE khác 0 vì file test không chạy nổi, chứ không phải vì có
  # test nào sai. Dấu này làm mỗi lượt test ra một file khác nhau.
  go test -count=1 -ldflags "-X main.buildStamp=test-$stamp" ./...
  if ($LASTEXITCODE -ne 0) {
    throw ("Test không qua — dừng lại, không build." + [Environment]::NewLine +
           "Nếu phía trên ghi 'An Application Control policy has blocked this file' thì " +
           "test chưa hề chạy: Windows chặn chính file test, không phải test sai. " +
           "Chạy lại .\build.ps1 là được, vì mỗi lượt giờ sinh ra file có hash khác.")
  }
}

$dist = Join-Path $PSScriptRoot "dist"
New-Item -ItemType Directory -Force $dist | Out-Null

# Không xoá cả thư mục dist: chạy app thẳng từ đó là chuyện bình thường, và
# khi đó file đang bị khoá nên lệnh xoá gãy với một thông báo quyền truy cập
# chẳng nói lên điều gì. Chỉ ghi đè những file lượt build này sinh ra, và nếu
# file bị giữ thì nói rõ ai đang giữ.
foreach ($old in (Get-ChildItem $dist -File -ErrorAction SilentlyContinue)) {
  $held = Get-Process -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq $old.FullName } | Select-Object -First 1
  if ($held) {
    throw ("$($old.Name) đang được chạy (PID $($held.Id)) nên không ghi đè được. " +
           "Đóng ứng dụng đó rồi build lại. Dùng bản đã cài ở %LOCALAPPDATA%\Programs\DIMA " +
           "thì không vướng chuyện này.")
  }
}

$targets = @(
  @{os="windows"; arch="amd64"; ext=".exe"},
  @{os="windows"; arch="arm64"; ext=".exe"},
  @{os="darwin";  arch="amd64"; ext=""},
  @{os="darwin";  arch="arm64"; ext=""},
  @{os="linux";   arch="amd64"; ext=""},
  @{os="linux";   arch="arm64"; ext=""}
)
# Mặc định chỉ build cho chính máy này.
#
# Một file chạy được thì rõ ràng; hai file mà một cái Windows từ chối vì sai
# kiến trúc CPU thì trông y như một lần build hỏng. Bản cho nền tảng khác chỉ
# cần khi đem cho người khác, và lúc đó là chủ ý — dùng -All.
$hostArch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
if ($All) {
  # giữ nguyên cả 6
} elseif ($Only) {
  $targets = $targets | Where-Object { $_.os -eq $Only }
} else {
  $targets = $targets | Where-Object { $_.os -eq "windows" -and $_.arch -eq $hostArch }
}

Write-Host "Build phiên bản $Version (dấu build $stamp)" -ForegroundColor Cyan
foreach ($t in $targets) {
  $name = "dima-$Version-$($t.os)-$($t.arch)$($t.ext)"
  $env:CGO_ENABLED = "0"; $env:GOOS = $t.os; $env:GOARCH = $t.arch
  # -H windowsgui: mở app không kèm cửa sổ console đen. Chạy từ terminal
  # vẫn in ra được nhờ attachConsole trong console_windows.go.
  #
  # Dấu build làm mỗi lần build ra một file khác nhau. Go build vốn tái lập
  # được, mà Smart App Control thì nhớ phán quyết theo hash — không có dấu
  # này, một bản lỡ bị chặn sẽ bị chặn vĩnh viễn dù build lại bao nhiêu lần.
  $ld = "-s -w -X main.buildStamp=$stamp"
  if ($t.os -eq "windows") { $ld += " -H windowsgui" }
  go build -trimpath -ldflags $ld -o (Join-Path $dist $name) .
  if ($LASTEXITCODE -ne 0) { throw "Build $name thất bại." }
  $mb = [math]::Round((Get-Item (Join-Path $dist $name)).Length / 1MB, 1)
  Write-Host ("  {0,-34} {1} MB" -f $name, $mb)
}
Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue

# Gói kèm mã nguồn để bản phát hành tự dựng lại được.
$stage = Join-Path $env:TEMP "dima-src-$Version"
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
New-Item -ItemType Directory -Force (Join-Path $stage "ui") | Out-Null
Copy-Item (Join-Path $PSScriptRoot "*.go") $stage
Copy-Item (Join-Path $PSScriptRoot "go.mod"),
          (Join-Path $PSScriptRoot "build.sh"),
          (Join-Path $PSScriptRoot "build.ps1"),
          (Join-Path $PSScriptRoot "install.ps1"),
          (Join-Path $PSScriptRoot "uninstall.ps1"),
          (Join-Path $PSScriptRoot "README.md") $stage
Copy-Item (Join-Path $PSScriptRoot "ui\*") (Join-Path $stage "ui")
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath (Join-Path $dist "dima-src-$Version.zip") -Force
Remove-Item -Recurse -Force $stage

# Nói ra những file .exe còn sót lại từ lượt build trước.
#
# dist không bị xoá sạch mỗi lần build (xem lý do ở trên), nên một bản cho
# nền tảng khác — chẳng hạn arm64 từ lần chạy -All nào đó — cứ nằm lại mãi.
# Hai file .exe nằm cạnh nhau mà một cái Windows từ chối vì sai kiến trúc CPU
# trông y hệt một lần build hỏng. Không tự xoá, vì có thể bạn cố ý giữ để
# đem cho người khác; chỉ nói ra để không ai phải đoán.
$made = $targets | ForEach-Object { "dima-$Version-$($_.os)-$($_.arch)$($_.ext)" }
$stale = Get-ChildItem $dist -File -Filter "*.exe" -ErrorAction SilentlyContinue |
  Where-Object { $made -notcontains $_.Name }
if ($stale) {
  Write-Host ""
  Write-Host "Còn file .exe từ lượt build trước, lượt này không sinh ra:" -ForegroundColor Yellow
  $stale | ForEach-Object { Write-Host ("  " + $_.Name + "   (" + $_.LastWriteTime + ")") -ForegroundColor DarkGray }
  Write-Host "  install.ps1 chỉ dùng bản đúng kiến trúc máy này; xoá đi nếu không cần." -ForegroundColor DarkGray
}

Write-Host ""
Write-Host "Xong. Sản phẩm nằm trong .\dist" -ForegroundColor Green
Get-ChildItem $dist | Select-Object Name, @{n="MB";e={[math]::Round($_.Length/1MB,2)}} | Format-Table -AutoSize
Write-Host "Cài lên máy này:  .\install.ps1" -ForegroundColor Cyan
