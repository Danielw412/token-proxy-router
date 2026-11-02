# Token Proxy Router installer for Windows.
#
#   Hub:     irm <release-url>/install.ps1 | iex
#   Client:  $env:TPR_INSTALL_ARGS='connect --hub <hub-url> --key <key>'; irm <release-url>/install.ps1 | iex
#   Or:      & ([scriptblock]::Create((irm <release-url>/install.ps1))) connect --hub <hub-url> --key <key>
#
# Downloads the release for this machine, verifies its SHA-256 against
# checksums.txt, and places it in the data folder's bin\. Needs no admin rights.
#
# TPR_HOME changes the data folder. TPR_INSTALL_BASE changes the download address
# (for testing); checksums are still verified.

$tprArgs = @($args)

# Everything runs inside a function so that an error returns to the prompt rather
# than closing the window that `iex` is running in.
function Install-Tpr {
    param([string[]]$Arguments)

    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    # Windows PowerShell 5.1 may default to protocols GitHub refuses.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $base = $env:TPR_INSTALL_BASE
    if (-not $base) { $base = 'https://github.com/Danielw412/token-proxy-router/releases/latest/download' }
    $dataDir = $env:TPR_HOME
    if (-not $dataDir) { $dataDir = Join-Path $HOME '.token-proxy-router' }
    $binDir = Join-Path $dataDir 'bin'

    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "No release is built for $($env:PROCESSOR_ARCHITECTURE)." }
    }
    $asset = "tpr-windows-$arch.exe"

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $download = Join-Path $tmp $asset
        $checksums = Join-Path $tmp 'checksums.txt'
        Write-Host "Downloading $asset ..."
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $download
        Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $checksums

        $want = $null
        foreach ($line in Get-Content $checksums) {
            $fields = -split $line
            if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $asset) { $want = $fields[0].ToLower() }
        }
        if (-not $want) { throw "checksums.txt does not list $asset, so the download cannot be verified." }
        $got = (Get-FileHash -Algorithm SHA256 -Path $download).Hash.ToLower()
        if ($want -ne $got) { throw "Checksum mismatch for ${asset}: the download is damaged or was tampered with. Nothing was installed." }
        Write-Host 'Checksum verified.'

        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        $target = Join-Path $binDir 'tpr.exe'
        if (Test-Path $target) {
            # A running exe can be renamed but not overwritten. tpr removes the old
            # copy on a later run.
            Move-Item -Force -Path $target -Destination ($target + '.old-' + [DateTime]::UtcNow.ToString('yyyyMMddHHmmss'))
        }
        Move-Item -Force -Path $download -Destination $target
    }
    finally {
        Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
    }

    if ($Arguments.Count -eq 0) {
        & $target install
    }
    else {
        if ($Arguments[0] -eq 'connect') {
            # A machine that only uses a hub needs no service.
            & $target install --no-service
            if ($LASTEXITCODE -ne 0) { throw 'tpr install failed.' }
        }
        & $target @Arguments
    }
    if ($LASTEXITCODE -ne 0) { throw "tpr exited with code $LASTEXITCODE." }
}

if ($tprArgs.Count -eq 0 -and $env:TPR_INSTALL_ARGS) {
    $tprArgs = @(-split $env:TPR_INSTALL_ARGS)
    # It may hold an API key, so do not leave it in the session.
    Remove-Item Env:TPR_INSTALL_ARGS
}
Install-Tpr -Arguments $tprArgs
