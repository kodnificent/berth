# Installs berth on Windows:
#
#   irm https://useberth.dev/install.ps1 | iex
#
# $env:BERTH_VERSION      a release tag such as v0.1.0; the latest release when unset
# $env:BERTH_INSTALL_DIR  where berth.exe goes; %LOCALAPPDATA%\Programs\berth when unset

# Everything runs from this function, called on the last line, so a download cut short
# runs nothing.
function Install-Berth {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    # Windows PowerShell 5.1 offers only older TLS versions unless asked, and GitHub refuses them.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repo = 'kodnificent/berth'
    # PROCESSOR_ARCHITEW6432 holds the machine's architecture when a 32-bit shell runs on a 64-bit one.
    $machine = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    switch ($machine) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "berth install: berth has no build for $machine; build it with: go install github.com/$repo/cmd/berth@latest" }
    }

    $archive = "berth_windows_$arch.zip"
    $base = "https://github.com/$repo/releases/latest/download"
    if ($env:BERTH_VERSION) { $base = "https://github.com/$repo/releases/download/$($env:BERTH_VERSION)" }
    if ($env:BERTH_DOWNLOAD_BASE) { $base = $env:BERTH_DOWNLOAD_BASE }
    $dir = if ($env:BERTH_INSTALL_DIR) { $env:BERTH_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\berth' }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("berth-install-" + [Guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $zip = Join-Path $tmp $archive
        $sums = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile $zip
        Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $sums

        $want = $null
        foreach ($line in Get-Content $sums) {
            $hash, $name = $line -split '\s+', 2
            if ($name -eq $archive) { $want = $hash }
        }
        if (-not $want) { throw "berth install: checksums.txt does not list $archive" }
        $got = (Get-FileHash -Algorithm SHA256 $zip).Hash
        if ($got -ne $want) { throw "berth install: $archive does not match its checksum (got $got, want $want), so nothing was installed" }

        Expand-Archive -Path $zip -DestinationPath (Join-Path $tmp 'out')
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        Copy-Item -Force (Join-Path $tmp 'out\berth.exe') (Join-Path $dir 'berth.exe')
    }
    finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    $exe = Join-Path $dir 'berth.exe'
    Write-Host "Installed $(& $exe --version) at $exe"

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $dir) {
        [Environment]::SetEnvironmentVariable('Path', (($userPath, $dir | Where-Object { $_ }) -join ';'), 'User')
        Write-Host "Added $dir to your user PATH. Open a new terminal to use berth."
    }
}

Install-Berth
