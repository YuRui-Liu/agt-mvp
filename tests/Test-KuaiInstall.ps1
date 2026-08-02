$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("kuai-install-test-" + [Guid]::NewGuid().ToString("N"))
$fixture = Join-Path $temporary "fixture"
$env:LOCALAPPDATA = Join-Path $temporary "local path's data"
$env:KUAI_RELEASE_URL = "https://kuai.example/release"
$oldUserPath = [Environment]::GetEnvironmentVariable("Path", "User")
$script:downloads = @()

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Invoke-WebRequest {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][string]$OutFile,
        [switch]$UseBasicParsing,
        [int]$MaximumRedirection,
        [switch]$PassThru
    )
    $script:downloads += $Uri
    $leaf = [IO.Path]::GetFileName($Uri)
    Copy-Item -LiteralPath (Join-Path $fixture "kuai-$leaf") -Destination $OutFile
}

function Get-Item {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$LiteralPath,
        [string]$Stream
    )
    if ($env:KUAI_TEST_MOTW_FAILURE -eq "1" -and $Stream -eq "Zone.Identifier") {
        throw "simulated MOTW inspection failure"
    }
    Microsoft.PowerShell.Management\Get-Item @PSBoundParameters
}

function Invoke-InstallWithCapturedError {
    $originalError = [Console]::Error
    $writer = New-Object IO.StringWriter
    try {
        [Console]::SetError($writer)
        $null = & (Join-Path $root "install.ps1")
        return $writer.ToString()
    }
    finally {
        [Console]::SetError($originalError)
        $writer.Dispose()
    }
}

function Write-Fixtures([string]$Architecture, [bool]$BreakKuai) {
    New-Item -ItemType Directory -Force -Path $fixture | Out-Null
    $kuaiName = "kuai-windows-$Architecture.exe"
    Set-Content -NoNewline -Path (Join-Path $fixture "kuai-$kuaiName") -Value "new-kuai-$Architecture"
    $kuaiHash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $fixture "kuai-$kuaiName")).Hash.ToLowerInvariant()
    if ($BreakKuai) { $kuaiHash = "0" * 64 }
    Set-Content -Path (Join-Path $fixture "kuai-SHA256SUMS") -Value "$kuaiHash  $kuaiName"
}

try {
    $env:KUAI_TEST_ARCH = "x64"
    $env:KUAI_RELEASE_URL = "http://kuai.example/release"
    $script:downloads = @()
    $failed = $false
    try { & (Join-Path $root "install.ps1") } catch { $failed = $true }
    Assert-True $failed "HTTP release base unexpectedly succeeded"
    Assert-True ($script:downloads.Count -eq 0) "HTTP release base downloaded before rejection"
    $env:KUAI_RELEASE_URL = "https://kuai.example/release"

    foreach ($case in @(
        @{ Input = "x64"; Artifact = "amd64" },
        @{ Input = "arm64"; Artifact = "arm64" }
    )) {
        $env:KUAI_TEST_ARCH = $case.Input
        $architecture = $case.Artifact
        Write-Fixtures $architecture $false
        $script:downloads = @()
        & (Join-Path $root "install.ps1")
        Assert-True ($script:downloads -contains "$env:KUAI_RELEASE_URL/kuai-windows-$architecture.exe") "missing kuai $architecture download"
        Assert-True ($script:downloads.Count -eq 2) "installer downloaded more than the manifest and kuai"
    }

    $installDir = Join-Path $env:LOCALAPPDATA "kuai\bin"
    Set-Content -Path (Join-Path $installDir "kuai.exe") -Value "old-kuai"
    Set-Content -Path (Join-Path $installDir "agentsview.exe") -Value "old-agentsview"
    Write-Fixtures "arm64" $true
    $failed = $false
    try { & (Join-Path $root "install.ps1") } catch { $failed = $true }
    Assert-True $failed "checksum failure unexpectedly succeeded"
    Assert-True ((Get-Content -Raw (Join-Path $installDir "kuai.exe")).Trim() -eq "old-kuai") "old kuai was overwritten"
    Assert-True ((Get-Content -Raw (Join-Path $installDir "agentsview.exe")).Trim() -eq "old-agentsview") "old agentsview was overwritten"

    Write-Fixtures "arm64" $false
    & (Join-Path $root "install.ps1")
    & (Join-Path $root "install.ps1")
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $matches = @($userPath.Split(";") | Where-Object { $_.TrimEnd("\") -eq $installDir.TrimEnd("\") })
    Assert-True ($matches.Count -eq 1) "CurrentUser PATH contains duplicate install entries"

    Write-Fixtures "arm64" $false
    $fixtureArtifact = Join-Path $fixture "kuai-kuai-windows-arm64.exe"
    Set-Content -Path ($fixtureArtifact + ":Zone.Identifier") -Value "[ZoneTransfer]`nZoneId=3"
    $motwOutput = Invoke-InstallWithCapturedError
    $target = Join-Path $installDir "kuai.exe"
    $quotedTarget = "'" + $target.Replace("'", "''") + "'"
    Assert-True ($motwOutput.Contains("SmartScreen may block or prompt")) "missing conservative MOTW advisory"
    Assert-True ($motwOutput.Contains("Unblock-File -LiteralPath $quotedTarget")) "MOTW remediation path is not safely quoted"
    Assert-True ($null -ne (Get-Item -LiteralPath $target -Stream "Zone.Identifier" -ErrorAction SilentlyContinue)) "installer cleared MOTW instead of only reporting it"

    $env:KUAI_TEST_MOTW_FAILURE = "1"
    Write-Fixtures "arm64" $false
    $null = & (Join-Path $root "install.ps1")
    Assert-True ((Get-Content -Raw $target).Trim() -eq "new-kuai-arm64") "MOTW detection failure changed a successful install"
    Remove-Item Env:KUAI_TEST_MOTW_FAILURE -ErrorAction SilentlyContinue
    Write-Host "kuai install.ps1 tests passed"
}
finally {
    [Environment]::SetEnvironmentVariable("Path", $oldUserPath, "User")
    Remove-Item Env:KUAI_TEST_ARCH -ErrorAction SilentlyContinue
    Remove-Item Env:KUAI_TEST_MOTW_FAILURE -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $temporary -ErrorAction SilentlyContinue
}
