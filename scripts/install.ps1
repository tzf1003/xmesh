# xmesh Windows bootstrap. Run a downloaded, reviewed script with PowerShell.
[CmdletBinding()]
param(
    [string]$Version = '',
    [ValidateSet('stable', 'testing')][string]$Channel = 'stable',
    [ValidateSet('amd64', 'arm64', '')][string]$Arch = '',
    [string]$Repo = 'tzf1003/xmesh',
    [string]$InstallDir = '',
    [string]$Root = '',
    [string]$Cache = '',
    [string]$Offline = '',
    [switch]$DownloadOnly,
    [switch]$AllowUnsigned,
    [switch]$NoLaunch,
    [switch]$Json
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$Phase = 'arguments'; $Cli = ''; $Package = ''
function Write-Event([bool]$Ok, [string]$Message, [string]$NextAction = '') {
    $event = [pscustomobject][ordered]@{ok=$Ok; phase=$script:Phase; message=$Message; next_action=$NextAction; version=$script:Version; cli_path=$script:Cli; package_path=$script:Package}
    if ($Json) { $event | ConvertTo-Json -Compress } else { Write-Output $event }
}
function Get-VersionParts([string]$Tag) {
    if ($Tag -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(beta|rc)\.(0|[1-9][0-9]*))?$') { throw 'Version must be vMAJOR.MINOR.PATCH[-beta.N|-rc.N].' }
    $rank = if (!$Matches[4]) { 2 } elseif ($Matches[4] -eq 'rc') { 1 } else { 0 }
    $number = if ($Matches[5]) { [int]$Matches[5] } else { 0 }
    [pscustomobject]@{major=[int]$Matches[1]; minor=[int]$Matches[2]; patch=[int]$Matches[3]; rank=$rank; number=$number; channel=$(if ($rank -eq 2) {'stable'} else {'testing'})}
}
function Invoke-Retry([scriptblock]$Action) {
    for ($attempt = 1; $attempt -le 4; $attempt++) {
        try { return & $Action } catch { if ($attempt -eq 4) { throw }; Start-Sleep -Seconds (2 * $attempt) }
    }
}
function Save-Download([string]$Url, [string]$Path) {
    if (([uri]$Url).Scheme -ne 'https') { throw 'Download requires HTTPS.' }
    Invoke-Retry { Invoke-WebRequest -Uri $Url -OutFile $Path -UseBasicParsing -TimeoutSec 900 } | Out-Null
}
function Test-Package([string]$Path, $Asset) {
    return (Test-Path -LiteralPath $Path -PathType Leaf) -and ((Get-Item -LiteralPath $Path).Length -eq $Asset.size) -and ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() -ceq $Asset.sha256)
}
try {
    $Phase = 'platform'
    if ($env:OS -ne 'Windows_NT') { throw 'This installer requires Windows. Use install.sh on macOS.' }
    if (!$Arch) {
        $machine = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
        $Arch = switch ($machine) { 'x64' {'amd64'}; 'arm64' {'arm64'}; default {throw 'Unsupported architecture; pass -Arch after checking System > About.'} }
    }
    if ($Repo -cnotmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') { throw 'Invalid release repository.' }
    if (!$Cache) { $Cache = Join-Path $env:LOCALAPPDATA 'xmesh\install-cache' }
    if (!$InstallDir) {
        $existing = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue |
            Where-Object { $_.DisplayName -eq 'xmesh' -and $_.InstallLocation } | Select-Object -First 1
        $InstallDir = if ($existing) { $existing.InstallLocation } else { Join-Path $env:LOCALAPPDATA 'Programs\xmesh' }
    }
    $InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
    if ($InstallDir.Contains('"') -or $InstallDir.Contains("`n") -or $InstallDir.Contains("`r")) { throw 'Invalid install directory.' }
    if ($Version) {
        $parts = Get-VersionParts $Version
        if ($PSBoundParameters.ContainsKey('Channel') -and $Channel -ne $parts.channel) { throw 'Version and channel disagree.' }
        $Channel = $parts.channel
    }
    New-Item -ItemType Directory -Path $Cache -Force | Out-Null
    $Phase = 'resolve'
    if ($Offline) { $ManifestPath = Join-Path $Offline 'manifest.json' }
    else {
        if (!$Version) {
            $releases = Invoke-Retry { Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=100" -TimeoutSec 60 }
            $candidates = foreach ($release in $releases) {
                if ($release.draft) { continue }
                try { $parts = Get-VersionParts $release.tag_name } catch { continue }
                if ($parts.channel -eq $Channel) { $parts | Add-Member -NotePropertyName tag -NotePropertyValue $release.tag_name -PassThru }
            }
            $selected = $candidates | Sort-Object major,minor,patch,rank,number -Descending | Select-Object -First 1
            if (!$selected) { throw 'No release in requested channel; pin -Version.' }
            $Version = $selected.tag
        }
        $ManifestPath = Join-Path $Cache "$Version-manifest.json"
        Save-Download "https://github.com/$Repo/releases/download/$Version/manifest.json" "$ManifestPath.part"
        Move-Item -LiteralPath "$ManifestPath.part" -Destination $ManifestPath -Force
    }
    $Phase = 'manifest'
    $manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    $parts = Get-VersionParts $manifest.version
    if ($manifest.schema_version -ne 1 -or ($Version -and $manifest.version -cne $Version) -or $manifest.repository -cne $Repo -or $manifest.channel -cne $parts.channel -or $manifest.docs_version -cne $manifest.version) { throw 'Manifest identity/schema/channel mismatch.' }
    if (!$Version -and $PSBoundParameters.ContainsKey('Channel') -and $Channel -cne $parts.channel) { throw 'Offline version and channel disagree.' }
    $Version = $manifest.version
    $assets = @($manifest.assets | Where-Object { $_.os -ceq 'windows' -and $_.arch -ceq $Arch -and $_.kind -ceq 'desktop' })
    if ($assets.Count -ne 1) { throw 'Manifest platform entry missing or duplicated.' }
    $asset = $assets[0]
    $machine = if ($Arch -eq 'amd64') { 'x64' } else { 'arm64' }
    $expected = "xmesh-ui-$($Version.Substring(1))-win-$machine.exe"
    if ($asset.name -cne $expected -or $asset.url -cne "https://github.com/$Repo/releases/download/$Version/$expected" -or $asset.sha256 -cnotmatch '^[a-f0-9]{64}$' -or $asset.size -isnot [long] -and $asset.size -isnot [int] -or $asset.size -lt 1) { throw 'Invalid asset name, URL, checksum or size.' }
    $trusted = $asset.trust.signed -is [bool] -and $asset.trust.signed
    if (!$trusted -and ($parts.channel -eq 'stable' -or !$AllowUnsigned)) { throw 'Stable releases require signatures; unsigned testing requires -AllowUnsigned.' }
    if (!$trusted) { Write-Event $true 'Testing package is explicitly unsigned; Windows trust policy still applies.' }
    $Package = if ($Offline) { Join-Path $Offline $asset.name } else { Join-Path $Cache $asset.name }
    $Phase = 'download'
    if (!(Test-Package $Package $asset)) {
        if ($Offline) { throw 'Offline package checksum/size mismatch. Obtain package and manifest from the same release.' }
        Save-Download $asset.url "$Package.part"
        $Phase = 'checksum'
        if (!(Test-Package "$Package.part" $asset)) { throw 'Downloaded package checksum/size mismatch. Delete the .part file and retry.' }
        Move-Item -LiteralPath "$Package.part" -Destination $Package -Force
    }
    $Phase = 'checksum'
    if (!(Test-Package $Package $asset)) { throw 'Package verification failed.' }
    Write-Event $true 'Release package checksum and size verified.'
    if ($DownloadOnly) { $Phase = 'complete'; Write-Event $true 'Package ready for offline transfer.'; exit 0 }
    $Phase = 'trust'
    $signature = Get-AuthenticodeSignature -LiteralPath $Package
    if ($trusted -and $signature.Status -ne 'Valid') { throw "Authenticode verification failed: $($signature.Status). Use a verified release or contact your administrator." }
    if (!$trusted -and $signature.Status -notin @('Valid', 'NotSigned')) { throw "Package signature is invalid: $($signature.Status)." }
    $Phase = 'install'
    $process = Start-Process -FilePath $Package -ArgumentList @('/S', '/currentuser', "/D=$InstallDir") -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "Installer exited $($process.ExitCode). Quit xmesh, inspect Windows security prompts and rerun." }
    $Cli = Join-Path $InstallDir 'resources\bin\xmesh.exe'
    $App = Join-Path $InstallDir 'xmesh.exe'
    $Phase = 'verify'
    if (!(Test-Path -LiteralPath $Cli -PathType Leaf) -or !(Test-Path -LiteralPath $App -PathType Leaf)) { throw 'Installed application or bundled CLI missing. Rerun with -InstallDir matching the installer destination.' }
    if ($trusted -and ((Get-AuthenticodeSignature -LiteralPath $Cli).Status -ne 'Valid' -or (Get-AuthenticodeSignature -LiteralPath $App).Status -ne 'Valid')) { throw 'Installed application or CLI signature is invalid.' }
    $reportedVersion = & $Cli version
    if ($LASTEXITCODE -ne 0 -or $reportedVersion -cne "xmesh $Version") { throw 'Installed CLI version differs from release.' }
    if (!$NoLaunch) { $Phase = 'launch'; Start-Process -FilePath $App | Out-Null }
    $Phase = 'complete'
    $next = "Continue in the setup wizard; inspect state with & '$($Cli.Replace("'", "''"))' setup inspect --json"
    if ($Root) { $next += " --root '$($Root.Replace("'", "''"))'" }
    Write-Event $true "Installed $Version. Bundled CLI: $Cli" $next
} catch {
    Write-Event $false $_.Exception.Message 'Rerun with the same arguments. Verified downloads remain in the cache; use -Offline for a transferred release bundle.'
    exit 1
}
