# Install the deconflict client from a GitHub release, on Windows.
#
#   irm https://raw.githubusercontent.com/cloudcons/deconflict-cli/main/install.ps1 | iex
#
# $env:DECONFLICT_VERSION picks a release (default: latest) and
# $env:DECONFLICT_INSTALL_DIR the directory (default:
# %LOCALAPPDATA%\Programs\deconflict), which is added to the user PATH. The
# archive is checked against the release's SHA256SUMS before it is unpacked.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

# Windows PowerShell 5.1 does not offer TLS 1.2 by default, and GitHub requires it.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

& {
    $repo = 'cloudcons/deconflict-cli'
    $dir = if ($env:DECONFLICT_INSTALL_DIR) { $env:DECONFLICT_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\deconflict' }

    $arch = switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { 'amd64' }
        'ARM64' { 'arm64' }
        default { throw "deconflict: unsupported architecture $env:PROCESSOR_ARCHITECTURE; releases are built for amd64 and arm64" }
    }

    $version = $env:DECONFLICT_VERSION
    if (-not $version) {
        $version = (Invoke-RestMethod -UseBasicParsing -Uri "https://api.github.com/repos/$repo/releases/latest").tag_name
    }
    $version = $version.TrimStart('v')

    $name = "deconflict_${version}_windows_${arch}"
    $base = "https://github.com/$repo/releases/download/v$version"
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "deconflict: downloading $name"
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$name.zip" -OutFile "$tmp\$name.zip"
        Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile "$tmp\SHA256SUMS"

        $line = Get-Content "$tmp\SHA256SUMS" | Where-Object { ($_ -split '\s+')[1] -in "$name.zip", "*$name.zip" }
        if (-not $line) { throw "deconflict: $name.zip is not listed in SHA256SUMS" }
        $want = ($line -split '\s+')[0]
        $got = (Get-FileHash -Algorithm SHA256 "$tmp\$name.zip").Hash
        if ($want -ne $got) { throw "deconflict: checksum mismatch for ${name}.zip: expected $want, got $got" }

        Expand-Archive -Path "$tmp\$name.zip" -DestinationPath "$tmp\x"
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        # A running deconflict.exe (an agent's MCP server, say) cannot be
        # overwritten, but it can be renamed out of the way.
        $exe = Join-Path $dir 'deconflict.exe'
        if (Test-Path $exe) {
            Remove-Item -Force "$exe.old" -ErrorAction SilentlyContinue
            Rename-Item -Force $exe 'deconflict.exe.old'
        }
        Copy-Item -Force "$tmp\x\deconflict.exe" $exe
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    $userPath = "$([Environment]::GetEnvironmentVariable('Path', 'User'))"
    if (($userPath -split ';') -notcontains $dir) {
        [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ";$dir").TrimStart(';'), 'User')
        $env:Path += ";$dir"
        Write-Host "deconflict: added $dir to your user PATH; open a new terminal to pick it up"
    }
    Write-Host "deconflict: installed $(& (Join-Path $dir 'deconflict.exe') version) to $dir"
    Write-Host 'deconflict: next: deconflict install --agent all'
}
