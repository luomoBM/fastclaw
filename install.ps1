# FastClaw Windows Installer
# Usage: powershell -ExecutionPolicy Bypass -Command "Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/fastclaw-ai/fastclaw/main/install.ps1' -OutFile 'install.ps1'; .\install.ps1"
# Or: powershell -ExecutionPolicy Bypass -Command "iex(New-Object Net.WebClient).DownloadString('https://raw.githubusercontent.com/fastclaw-ai/fastclaw/main/install.ps1')"

param(
    [switch]$Help,
    [string]$Path = "$env:USERPROFILE\.fastclaw\bin"
)

$ErrorActionPreference = "Stop"

function Write-Header {
    Write-Host ""
    Write-Host "⚡ FastClaw - Lightweight AI Agent Framework" -ForegroundColor Cyan
    Write-Host ""
}

function Write-Info {
    param([string]$Message)
    Write-Host "[*] $Message" -ForegroundColor Green
}

function Write-Error-Msg {
    param([string]$Message)
    Write-Host "[!] $Message" -ForegroundColor Red
}

function Write-Success {
    param([string]$Message)
    Write-Host "[+] $Message" -ForegroundColor Green
}

function Show-Help {
    Write-Header
    Write-Host "Usage: powershell -ExecutionPolicy Bypass -Command `"iex(New-Object Net.WebClient).DownloadString('https://raw.githubusercontent.com/fastclaw-ai/fastclaw/main/install.ps1')`"" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "Or download and run locally:"
    Write-Host "  powershell -ExecutionPolicy Bypass -File .\install.ps1" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "Options:"
    Write-Host "  -Help              Show this help message"
    Write-Host "  -Path <path>       Custom install path (default: $env:USERPROFILE\.fastclaw\bin)"
    Write-Host ""
}

function Get-LatestRelease {
    Write-Info "Fetching latest release info..."
    try {
        $apiUrl = "https://api.github.com/repos/fastclaw-ai/fastclaw/releases/latest"
        $response = Invoke-WebRequest -Uri $apiUrl -UseBasicParsing -Headers @{"Accept"="application/vnd.github.v3+json"}
        $release = $response.Content | ConvertFrom-Json
        return $release
    }
    catch {
        Write-Error-Msg "Failed to fetch release info: $_"
        Write-Host ""
        Write-Host "Manual installation:" -ForegroundColor Yellow
        Write-Host "1. Visit: https://github.com/fastclaw-ai/fastclaw/releases"
        Write-Host "2. Download fastclaw_windows_amd64.zip or fastclaw_windows_arm64.zip"
        Write-Host "3. Extract and run fastclaw.exe"
        Write-Host ""
        exit 1
    }
}

function Detect-Architecture {
    try {
        # Check actual processor architecture using Win32_Processor
        $processor = Get-WmiObject Win32_Processor -ErrorAction Stop | Select-Object -First 1
        # Architecture property: 9 = x64 (amd64), 12 = ARM64
        switch ($processor.Architecture) {
            12 { return "arm64" }
            9 { return "amd64" }
            default {
                Write-Error-Msg "Unsupported processor architecture: $($processor.Architecture)"
                Write-Host "Supported: x64 (amd64) and ARM64"
                exit 1
            }
        }
    }
    catch {
        Write-Error-Msg "Failed to detect architecture: $_"
        exit 1
    }
}

function Download-Release {
    param(
        [object]$Release,
        [string]$Arch
    )

    $filename = "fastclaw_windows_$Arch.zip"
    $asset = $Release.assets | Where-Object { $_.name -eq $filename }

    if (-not $asset) {
        Write-Error-Msg "Release not found for architecture: $Arch"
        Write-Host "Available assets:" -ForegroundColor Yellow
        $Release.assets | ForEach-Object { Write-Host "  - $($_.name)" }
        exit 1
    }

    $downloadUrl = $asset.browser_download_url
    $tempDir = [System.IO.Path]::GetTempPath()
    $zipPath = Join-Path $tempDir $filename

    Write-Info "Downloading FastClaw ($Arch) from $downloadUrl..."
    try {
        Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath -UseBasicParsing -ErrorAction Stop
        Write-Success "Downloaded to $zipPath"
        return $zipPath
    }
    catch {
        Write-Error-Msg "Failed to download: $_"
        exit 1
    }
}

function Extract-Archive {
    param(
        [string]$ZipPath,
        [string]$TargetPath
    )

    Write-Info "Extracting to $TargetPath..."

    # If target exists, back it up instead of deleting
    if (Test-Path $TargetPath) {
        $backupPath = "$TargetPath.backup.$(Get-Date -Format 'yyyyMMdd_HHmmss')"
        Write-Info "Backing up existing installation to $backupPath..."
        try {
            Rename-Item -Path $TargetPath -NewName $backupPath -ErrorAction Stop | Out-Null
        }
        catch {
            Write-Error-Msg "Failed to backup existing installation: $_"
            exit 1
        }
    }

    try {
        New-Item -ItemType Directory -Path $TargetPath -Force | Out-Null
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [System.IO.Compression.ZipFile]::ExtractToDirectory($ZipPath, $TargetPath)
        Write-Success "Extracted successfully"
        return $true
    }
    catch {
        Write-Error-Msg "Failed to extract: $_"
        exit 1
    }
}

function Add-ToPath {
    param([string]$BinPath)

    $userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    $pathEntries = $userPath -split ';' | Where-Object { $_ }

    # Check if path is already present (exact match)
    if ($pathEntries -contains $BinPath) {
        Write-Info "Path already in PATH"
        return $true
    }

    Write-Info "Adding to PATH..."
    $newPath = if ($userPath) { "$userPath;$BinPath" } else { $BinPath }

    try {
        [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
        $env:PATH = $env:PATH + ";$BinPath"
        Write-Success "Added to PATH"
        return $true
    }
    catch {
        Write-Error-Msg "Failed to add to PATH (may require admin): $_"
        return $false
    }
}

function Create-StartMenuShortcut {
    param([string]$ExePath)

    try {
        $startMenuPath = "$env:APPDATA\Microsoft\Windows\Start Menu\Programs"
        $shortcutPath = Join-Path $startMenuPath "FastClaw.lnk"

        $shell = New-Object -ComObject WScript.Shell
        $shortcut = $shell.CreateShortcut($shortcutPath)
        $shortcut.TargetPath = $ExePath
        $shortcut.WorkingDirectory = $env:USERPROFILE
        $shortcut.Description = "FastClaw - AI Agent Framework"
        $shortcut.Save()

        Write-Success "Created Start Menu shortcut"
    }
    catch {
        Write-Info "Could not create Start Menu shortcut (optional): $_"
    }
}

function Test-Installation {
    param([string]$ExePath)

    Write-Info "Testing installation..."
    try {
        $result = & $ExePath version 2>&1 | Select-Object -First 1
        if ($result) {
            Write-Success "Installation verified: $result"
            return $true
        }
    }
    catch {
        Write-Info "Could not verify version (may still work)"
    }
    return $true
}

# Main script
Write-Header

if ($Help) {
    Show-Help
    exit 0
}

$arch = Detect-Architecture
Write-Info "Detected architecture: $arch"

$release = Get-LatestRelease
$version = $release.tag_name
Write-Info "Latest version: $version"

$zipPath = Download-Release -Release $release -Arch $arch
$installPath = $Path

Extract-Archive -ZipPath $zipPath -TargetPath $installPath

$exePath = Join-Path $installPath "fastclaw.exe"

$pathAdded = Add-ToPath -BinPath $installPath

Create-StartMenuShortcut -ExePath $exePath

Test-Installation -ExePath $exePath

Write-Host ""
Write-Success "FastClaw $version installed successfully!"
Write-Host ""
Write-Host "Next steps:" -ForegroundColor Cyan
Write-Host "1. Open a new PowerShell or Command Prompt window"
Write-Host "2. Run: fastclaw"
Write-Host "3. Setup wizard will open in your browser at http://localhost:18953"
Write-Host ""

if (-not $pathAdded) {
    Write-Host "⚠️  WARNING: FastClaw was not added to PATH (admin privileges may be required)" -ForegroundColor Yellow
    Write-Host "You can run fastclaw from: $exePath" -ForegroundColor Yellow
    Write-Host ""
}

Write-Host "Or run from Start Menu: FastClaw" -ForegroundColor Cyan
Write-Host ""

# Cleanup
Remove-Item -Path $zipPath -Force -ErrorAction SilentlyContinue | Out-Null

Write-Host "Installation complete! 🚀" -ForegroundColor Green
