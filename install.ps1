$ErrorActionPreference = "Stop"
Set-StrictMode -Version 2.0

$kuaiReleaseUrl = if ($env:KUAI_RELEASE_URL) { $env:KUAI_RELEASE_URL.TrimEnd("/") } else { "https://github.com/YuRui-Liu/agt-mvp/releases/latest/download" }

function Get-TrustedHttpsUri([string]$Value) {
    $parsed = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$parsed) -or
        $parsed.Scheme -cne "https" -or $parsed.UserInfo -or -not $parsed.Host) {
        throw "kuai: release URL must be an absolute HTTPS URL without user information"
    }
    return $parsed
}

$kuaiReleaseUri = Get-TrustedHttpsUri $kuaiReleaseUrl
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$architectureName = if ($env:KUAI_TEST_ARCH) {
    $env:KUAI_TEST_ARCH
} elseif ($env:PROCESSOR_ARCHITEW6432) {
    $env:PROCESSOR_ARCHITEW6432.ToLowerInvariant()
} else {
    $env:PROCESSOR_ARCHITECTURE.ToLowerInvariant()
}
switch ($architectureName) {
    { $_ -in @("x64", "amd64") } { $architecture = "amd64"; break }
    { $_ -in @("arm64") } { $architecture = "arm64"; break }
    default { throw "kuai: unsupported Windows architecture" }
}

$kuaiName = "kuai-windows-$architecture.exe"
if ($env:KUAI_INSTALL_DRY_RUN -eq "1") {
    Write-Host "Would download and verify:"
    Write-Host "  $($kuaiReleaseUri.AbsoluteUri.TrimEnd('/'))/SHA256SUMS"
    Write-Host "  $($kuaiReleaseUri.AbsoluteUri.TrimEnd('/'))/$kuaiName"
    Write-Host "Would atomically install kuai to $env:LOCALAPPDATA\kuai\bin\kuai.exe"
    return
}
$stage = Join-Path ([IO.Path]::GetTempPath()) ("kuai-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $stage | Out-Null

function Download-File([string]$Uri, [string]$Destination) {
    $current = Get-TrustedHttpsUri $Uri
    for ($redirects = 0; $redirects -le 5; $redirects++) {
        Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
        $response = $null
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $current.AbsoluteUri -OutFile $Destination -MaximumRedirection 0 -PassThru
        }
        catch {
            $response = $_.Exception.Response
            if ($null -eq $response) { throw "kuai: trusted download failed" }
        }
        if ($null -eq $response) {
            if (Test-Path -LiteralPath $Destination) { return }
            throw "kuai: trusted download failed"
        }
        $status = [int]$response.StatusCode
        if ($status -ge 200 -and $status -lt 300) { return }
        if ($status -lt 300 -or $status -ge 400 -or $redirects -eq 5) {
            throw "kuai: trusted download failed"
        }
        $location = $response.Headers["Location"]
        if (-not $location) { throw "kuai: trusted download failed" }
        $current = Get-TrustedHttpsUri ([Uri]::new($current, $location).AbsoluteUri)
    }
    throw "kuai: trusted download failed"
}

function Get-ExpectedChecksum([string]$Manifest, [string]$FileName) {
    $matches = @()
    foreach ($line in Get-Content -LiteralPath $Manifest) {
        if ($line -match "^([0-9A-Fa-f]{64})[ `t]+(\*?)(.+)$" -and $Matches[3] -ceq $FileName) {
            $matches += $Matches[1].ToLowerInvariant()
        }
    }
    if ($matches.Count -ne 1) {
        throw "kuai: trusted checksum entry missing or ambiguous"
    }
    return $matches[0]
}

function Assert-Checksum([string]$Manifest, [string]$FileName, [string]$Artifact) {
    $expected = Get-ExpectedChecksum $Manifest $FileName
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Artifact).Hash.ToLowerInvariant()
    if ($actual -cne $expected) {
        throw "kuai: checksum verification failed"
    }
}

try {
    $kuaiManifest = Join-Path $stage "kuai-SHA256SUMS"
    $kuaiArtifact = Join-Path $stage $kuaiName
    Download-File ([Uri]::new($kuaiReleaseUri, $kuaiReleaseUri.AbsolutePath.TrimEnd("/") + "/SHA256SUMS").AbsoluteUri) $kuaiManifest
    Download-File ([Uri]::new($kuaiReleaseUri, $kuaiReleaseUri.AbsolutePath.TrimEnd("/") + "/$kuaiName").AbsoluteUri) $kuaiArtifact
    Assert-Checksum $kuaiManifest $kuaiName $kuaiArtifact

    $installDir = Join-Path $env:LOCALAPPDATA "kuai\bin"
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    $suffix = [Guid]::NewGuid().ToString("N")
    $kuaiTarget = Join-Path $installDir "kuai.exe"
    $kuaiNew = Join-Path $installDir ".kuai.new.$suffix"
    $kuaiOld = Join-Path $installDir ".kuai.old.$suffix"
    Copy-Item -LiteralPath $kuaiArtifact -Destination $kuaiNew

    $hadKuai = Test-Path -LiteralPath $kuaiTarget
    $oldUserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $backedUpKuai = $false
    $installedKuai = $false
    try {
        if ($hadKuai) {
            Move-Item -LiteralPath $kuaiTarget -Destination $kuaiOld
            $backedUpKuai = $true
        }
        Move-Item -LiteralPath $kuaiNew -Destination $kuaiTarget
        $installedKuai = $true
        $pathEntries = @()
        if ($oldUserPath) {
            $pathEntries = @($oldUserPath.Split(";") | Where-Object {
            $_ -and -not [string]::Equals($_.TrimEnd("\"), $installDir.TrimEnd("\"), [StringComparison]::OrdinalIgnoreCase)
            })
        }
        $newUserPath = (@($installDir) + $pathEntries) -join ";"
        [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    }
    catch {
        $originalFailure = $_
        if ($installedKuai) { Remove-Item -LiteralPath $kuaiTarget -Force -ErrorAction SilentlyContinue }
        if ($backedUpKuai) { Move-Item -LiteralPath $kuaiOld -Destination $kuaiTarget -ErrorAction SilentlyContinue }
        Remove-Item -LiteralPath $kuaiNew -Force -ErrorAction SilentlyContinue
        try { [Environment]::SetEnvironmentVariable("Path", $oldUserPath, "User") } catch {}
        throw $originalFailure
    }
    Remove-Item -LiteralPath $kuaiOld -Force -ErrorAction SilentlyContinue
    Write-Host "Installed kuai to $installDir"
}
finally {
    Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
}
