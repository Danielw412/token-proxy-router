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
    # Windows PowerShell 5.1 downloads far more slowly with its progress bar on.
    $ProgressPreference = 'SilentlyContinue'
    # Windows PowerShell 5.1 may default to protocols GitHub refuses.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    # curl.exe ships with Windows 10 and later, and shows speed and time left. A
    # connection that stops moving fails rather than waiting forever.
    function Get-Build {
        param([string]$Uri, [string]$OutFile)

        $curl = Get-Command curl.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($curl) {
            $curlArgs = @('-fSL', '--retry', '3', '--connect-timeout', '30', '--speed-limit', '1024', '--speed-time', '120', '-o', $OutFile, $Uri)
            if ([Console]::IsErrorRedirected) { $curlArgs = @('-s') + $curlArgs }
            # The progress display is written to stderr, which is not an error.
            $ErrorActionPreference = 'Continue'
            & $curl.Source @curlArgs
            $code = $LASTEXITCODE
            $ErrorActionPreference = 'Stop'
            if ($code -eq 0) { return }
            Write-Host "curl.exe stopped with code $code. Trying again another way."
        }
        Write-Host 'No progress is shown from here. This can take several minutes on a slow connection.'
        Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $OutFile
    }

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
        $checksums = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $checksums

        $sums = @{}
        foreach ($line in Get-Content $checksums) {
            $fields = -split $line
            if ($fields.Count -eq 2) { $sums[$fields[1].TrimStart('*')] = $fields[0].ToLower() }
        }

        # The compressed build is about a third of the size.
        $name = "$asset.gz"
        if (-not $sums.ContainsKey($name)) { $name = $asset }
        if (-not $sums.ContainsKey($name)) { throw "checksums.txt does not list $asset, so the download cannot be verified." }

        $download = Join-Path $tmp $name
        Write-Host "Downloading $name ..."
        Get-Build -Uri "$base/$name" -OutFile $download

        $got = (Get-FileHash -Algorithm SHA256 -Path $download).Hash.ToLower()
        if ($sums[$name] -ne $got) { throw "Checksum mismatch for ${name}: the download is damaged or was tampered with. Nothing was installed." }
        Write-Host 'Checksum verified.'

        $binary = Join-Path $tmp $asset
        if ($name -ne $asset) {
            $packed = [IO.File]::OpenRead($download)
            try {
                $gzip = New-Object IO.Compression.GZipStream($packed, [IO.Compression.CompressionMode]::Decompress)
                $unpacked = [IO.File]::Create($binary)
                try { $gzip.CopyTo($unpacked) } finally { $unpacked.Dispose() }
            }
            finally { $packed.Dispose() }
        }

        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        $target = Join-Path $binDir 'tpr.exe'
        if (Test-Path $target) {
            # A running exe can be renamed but not overwritten. tpr removes the old
            # copy on a later run.
            Move-Item -Force -Path $target -Destination ($target + '.old-' + [DateTime]::UtcNow.ToString('yyyyMMddHHmmss'))
        }
        Move-Item -Force -Path $binary -Destination $target
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
