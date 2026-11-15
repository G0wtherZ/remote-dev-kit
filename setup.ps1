$ErrorActionPreference = "Stop"
$Target = Join-Path $env:LOCALAPPDATA "RemoteDevKit\app"
$Exe = Join-Path $Target "rdev.exe"
if (!(Test-Path (Join-Path $PSScriptRoot "SHA256SUMS")) -or !(Test-Path (Join-Path $PSScriptRoot "assets"))) {
    throw "请从完整安装包运行 setup.ps1，不要单独复制脚本。"
}
Get-Content (Join-Path $PSScriptRoot "SHA256SUMS") | ForEach-Object {
    $Parts = $_ -split '  ', 2
    if ($Parts.Count -ne 2 -or $Parts[1] -match '(^[/\\]|\.\.[/\\]|:)') { throw "无效校验清单" }
    $Hash = (Get-FileHash (Join-Path $PSScriptRoot $Parts[1]) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($Hash -ne $Parts[0]) { throw "文件校验失败：$($Parts[1])" }
}
if (Test-Path $Exe) {
    Write-Host "已安装，未覆盖现有版本：$Exe"
    Write-Host "可运行 doctor 或 setup --resume。"
    exit 0
}
New-Item -ItemType Directory -Force -Path $Target | Out-Null
foreach ($Item in @("rdev.exe", "assets", "licenses", "docs", "README.md", "LICENSE", "THIRD_PARTY_NOTICES.md", "SHA256SUMS")) {
    Copy-Item -Path (Join-Path $PSScriptRoot $Item) -Destination $Target -Recurse -Force
}
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($UserPath -split ';') -notcontains $Target) {
    if ($null -eq $UserPath) { $UserPath = "" }
    [Environment]::SetEnvironmentVariable("Path", ($UserPath.TrimEnd(';') + ';' + $Target), "User")
    $env:Path += ";$Target"
}
Write-Host "命令已安装：$Exe"
& $Exe setup @args
exit $LASTEXITCODE
