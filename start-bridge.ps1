# start-bridge.ps1 - Launch WhatsApp Go bridge in background
# Usage: powershell -File X:/Projects/_GAIA/_WHATSAPP/start-bridge.ps1

$BridgeDir = "X:\Projects\_GAIA\_WHATSAPP\whatsapp-bridge"
$BridgeExe = "$BridgeDir\whatsapp-bridge.exe"
$LogFile   = "X:\Projects\_GAIA\_WHATSAPP\bridge.log"

# Check if bridge executable exists
if (-not (Test-Path $BridgeExe)) {
    Write-Host "[ERROR] Bridge not found: $BridgeExe" -ForegroundColor Red
    Write-Host "  Build it first: cd $BridgeDir && go build -o whatsapp-bridge.exe ."
    exit 1
}

# Check if already running
$existing = Get-Process -Name "whatsapp-bridge" -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "[OK] Bridge already running (PID $($existing.Id))" -ForegroundColor Green
    exit 0
}

# Launch in background
$timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
Add-Content -Path $LogFile -Value "[$timestamp] Starting bridge..."

$proc = Start-Process -FilePath $BridgeExe `
    -WorkingDirectory $BridgeDir `
    -WindowStyle Hidden `
    -RedirectStandardOutput "$BridgeDir\stdout.log" `
    -RedirectStandardError "$BridgeDir\stderr.log" `
    -PassThru

Start-Sleep -Seconds 2

# Verify it started
$check = Get-Process -Name "whatsapp-bridge" -ErrorAction SilentlyContinue
if ($check) {
    $msg = "[$timestamp] Bridge started (PID $($proc.Id))"
    Add-Content -Path $LogFile -Value $msg
    Write-Host "[OK] $msg" -ForegroundColor Green
} else {
    $msg = "[$timestamp] Bridge failed to start - check $BridgeDir\stderr.log"
    Add-Content -Path $LogFile -Value $msg
    Write-Host "[ERROR] $msg" -ForegroundColor Red
    exit 1
}
