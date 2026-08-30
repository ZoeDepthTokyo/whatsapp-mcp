$env:PATH = "C:\Program Files\Go\bin;C:\msys64\ucrt64\bin;" + $env:PATH
$env:CGO_ENABLED = "1"
Set-Location "$PSScriptRoot\whatsapp-bridge"

Write-Host "Updating whatsmeow to latest..."
go get go.mau.fi/whatsmeow@latest
go mod tidy

Write-Host ""
Write-Host "Updated go.mod:"
Get-Content go.mod | Select-String "whatsmeow"

Write-Host ""
Write-Host "Rebuilding whatsapp-bridge.exe..."
go build -o whatsapp-bridge.exe .

if ($LASTEXITCODE -eq 0) {
    Write-Host "BUILD SUCCESS"
    Get-Item whatsapp-bridge.exe | Select-Object Name, Length, LastWriteTime
} else {
    Write-Host "BUILD FAILED with exit code $LASTEXITCODE"
}
