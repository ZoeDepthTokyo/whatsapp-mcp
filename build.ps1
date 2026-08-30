$env:PATH = "C:\Program Files\Go\bin;C:\msys64\ucrt64\bin;" + $env:PATH
$env:CGO_ENABLED = "1"
Set-Location "$PSScriptRoot\whatsapp-bridge"
Write-Host "Go version:" (go version)
Write-Host "GCC version:" (gcc --version | Select-Object -First 1)
Write-Host "CGO_ENABLED:" $env:CGO_ENABLED
Write-Host "Building whatsapp-bridge..."
go build -o whatsapp-bridge.exe .
if ($LASTEXITCODE -eq 0) {
    Write-Host "BUILD SUCCESS: whatsapp-bridge.exe created"
    Get-Item whatsapp-bridge.exe | Select-Object Name, Length, LastWriteTime
} else {
    Write-Host "BUILD FAILED with exit code $LASTEXITCODE"
}
