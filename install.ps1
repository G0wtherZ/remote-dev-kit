# Download a pinned Windows release, verify it, and start the interactive setup.
$ErrorActionPreference = 'Stop'
$Version = '0.1.0-alpha.5'
$Base = "https://github.com/G0wtherZ/remote-dev-kit/releases/download/v$Version"
if ($env:OS -ne 'Windows_NT') { throw 'Run this installer on your local Windows PC. macOS: use install.sh.' }
$NativeArch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $NativeArch = $env:PROCESSOR_ARCHITEW6432 }
switch ($NativeArch) {
    'ARM64' { $Arch = 'arm64' }
    'AMD64' { $Arch = 'amd64' }
    default { throw "Unsupported Windows architecture: $NativeArch" }
}
$Temp = Join-Path ([IO.Path]::GetTempPath()) ('rdev-download-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Temp | Out-Null
try {
    $Archive = "remote-dev-kit-$Version-windows-$Arch.zip"
    $ArchivePath = Join-Path $Temp $Archive
    Write-Host "Downloading Remote Dev Kit $Version (windows-$Arch)..."
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/$Archive" -OutFile $ArchivePath
    $ManifestPath = Join-Path $Temp 'SHA256SUMS'
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/SHA256SUMS" -OutFile $ManifestPath
    $Manifest = Get-Content -LiteralPath $ManifestPath -Raw
    $MatchesForArchive = @($Manifest -split '\r?\n' | Where-Object {
        $_ -match ('^[a-f0-9]{64}  ' + [regex]::Escape($Archive) + '$')
    })
    if ($MatchesForArchive.Count -ne 1) { throw 'Missing or invalid release checksum; installation stopped.' }
    $Expected = ($MatchesForArchive[0] -split '  ', 2)[0]
    $Actual = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected) { throw 'Release checksum mismatch; installation stopped.' }
    Expand-Archive -LiteralPath $ArchivePath -DestinationPath $Temp
    $Setup = Join-Path $Temp "remote-dev-kit-$Version-windows-$Arch\setup.ps1"
    & $Setup @args
    if ($LASTEXITCODE -ne 0) { throw "Setup exited with code $LASTEXITCODE" }
}
finally {
    Remove-Item -LiteralPath $Temp -Recurse -Force
}
