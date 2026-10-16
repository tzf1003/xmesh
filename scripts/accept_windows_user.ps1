<#
.SYNOPSIS
  Step F of docs/validation/real-environment-runbook.md: xmesh in an ordinary Windows user session.

.DESCRIPTION
  -Role owner  Run as the ordinary (non-administrator) user that installed xmesh, after logging off
               and on again. Reports who runs the process, whether the token is elevated, and the
               scheduled task's run level.
  -Role other  Run as a second local user. Tries to read the owner's state directory and to call the
               owner's Gateway without the PAT. Every attempt is expected to be DENIED.

  The Gateway listens on 127.0.0.1, which every local user can reach, so step F configures a PAT and
  the second user is expected to get HTTP 401 without it. Prints one JSON document.

.EXAMPLE
  powershell -NoProfile -File scripts\accept_windows_user.ps1 -Role owner -StateDir C:\xmesh\state
  powershell -NoProfile -File scripts\accept_windows_user.ps1 -Role other -StateDir C:\xmesh\state
#>
param(
  [Parameter(Mandatory)][ValidateSet('owner', 'other')][string]$Role,
  [Parameter(Mandatory)][string]$StateDir,
  [string]$Gateway = '127.0.0.1:8080'
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http

function Try-Access([string]$what, [scriptblock]$action) {
  try { & $action | Out-Null; [ordered]@{ attempt = $what; outcome = 'ALLOWED' } }
  catch {
    $kind = $_.Exception.GetType().Name
    [ordered]@{ attempt = $what; outcome = $(if ($kind -eq 'ItemNotFoundException') { 'NOT_FOUND' } else { 'DENIED' }); detail = $kind }
  }
}

function Gateway-Status([string]$address) {
  $client = [System.Net.Http.HttpClient]::new()
  try {
    $body = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"accept","version":"1"}}}'
    $request = [System.Net.Http.HttpRequestMessage]::new('POST', "http://$address/mcp")
    $request.Content = [System.Net.Http.StringContent]::new($body, [Text.Encoding]::UTF8, 'application/json')
    $request.Headers.Accept.ParseAdd('application/json')
    $request.Headers.Accept.ParseAdd('text/event-stream')
    [int]$client.SendAsync($request).GetAwaiter().GetResult().StatusCode
  } catch { "unreachable: $($_.Exception.Message)" } finally { $client.Dispose() }
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$report = [ordered]@{
  role = $Role
  user = $identity.Name
  session_id = (Get-Process -Id $PID).SessionId
  is_administrator = ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if ($Role -eq 'owner') {
  $report.high_integrity_token = [bool](whoami /groups | Select-String 'High Mandatory Level')
  $report.processes = @(Get-CimInstance Win32_Process -Filter "Name='xmesh.exe'" | ForEach-Object {
      $owner = Invoke-CimMethod -InputObject $_ -MethodName GetOwner
      [ordered]@{ pid = $_.ProcessId; session_id = $_.SessionId; owner = "$($owner.Domain)\$($owner.User)" }
    })
  $report.tasks = @(Get-ScheduledTask | Where-Object { $_.TaskName -like '*xmesh*' } | ForEach-Object {
      [ordered]@{ name = $_.TaskName; state = [string]$_.State; run_level = [string]$_.Principal.RunLevel;
                  logon_type = [string]$_.Principal.LogonType; user = $_.Principal.UserId }
    })
  $report.state_dir_acl = @((Get-Acl -LiteralPath $StateDir).Access | ForEach-Object {
      "$($_.IdentityReference): $($_.FileSystemRights) ($($_.AccessControlType))" })
  $report.gateway_http_without_token = Gateway-Status $Gateway
  $report.expect = 'processes owned by this user in this session, run_level Limited, is_administrator false, high_integrity_token false, gateway 401 when a PAT is configured'
} else {
  $attempts = @(
    Try-Access "list $StateDir" { Get-ChildItem -LiteralPath $StateDir -Force }
    Try-Access "list $StateDir\audit" { Get-ChildItem -LiteralPath (Join-Path $StateDir 'audit') -Force }
    Try-Access "write $StateDir\accept-probe.txt" {
      $probe = Join-Path $StateDir 'accept-probe.txt'
      Set-Content -LiteralPath $probe -Value x
      Remove-Item -LiteralPath $probe
    }
  )
  $status = Gateway-Status $Gateway
  $attempts += [ordered]@{ attempt = "POST http://$Gateway/mcp without the PAT"; outcome = $(if ($status -eq 401) { 'DENIED' } else { "ALLOWED ($status)" }) }
  $report.attempts = $attempts
  # NOT_FOUND on the first attempt would mean the path is wrong, not that access was refused.
  $report.result = $(if ($attempts[0].outcome -eq 'DENIED' -and @($attempts | Where-Object { $_.outcome -like 'ALLOWED*' }).Count -eq 0) { 'PASS' } else { 'FAIL' })
}
$report | ConvertTo-Json -Depth 5
