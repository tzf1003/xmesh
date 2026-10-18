**Pre-release for hands-on testing. Not for production use.**

This build exists so the real-environment acceptance steps can be run on Windows, Linux and macOS
machines. The steps are in
[docs/validation/real-environment-runbook.md](https://github.com/tzf1003/xmesh/blob/__TAG__/docs/validation/real-environment-runbook.md);
each one is independent and can be skipped. Every archive also contains the runbook and the
`scripts/` it uses, so nothing else needs to be cloned.

## What changed since beta.2

- Most configuration changes apply without restarting the node, and the gateway and ingress roles can be
  started, stopped and rebound at runtime. When a change still needs a restart, the control panel says so
  and shows a restart button.
- Devices can pair with a connection password, and LAN discovery finds other nodes on the same network.
- The authorization page can add MCP servers by hand and turn built-in tools on or off. API keys for MCP
  servers are kept as environment variables; the app shows only the variable names.
- The control panel has a new visual style. Text size (100–150%) and the interface and code fonts can be
  set; Ctrl +, Ctrl - and Ctrl 0 change the size.
- The Tailnet page has a form to save Tailscale or Headscale credentials. The app does not keep the secret
  and does not show it again after you submit it.
- The control panel checks for newer releases and can install one for you. It checks the download against
  `SHA256SUMS.txt` first, and it does not change your configuration or tokens. On macOS the app must be in
  Applications for the update to replace it.

beta.2 does not have the update check, so install this build by hand once. After that, later releases can
be installed from the control panel.

## Download and verify

Pick the archive for your system (`linux`, `darwin` or `windows`; `amd64` or `arm64`), then check it
against `SHA256SUMS.txt` from this release:

```
sha256sum --ignore-missing -c SHA256SUMS.txt        # Linux
shasum -a 256 --ignore-missing -c SHA256SUMS.txt    # macOS
```

Windows (PowerShell): `(Get-FileHash .\xmesh-__TAG__-windows-amd64.zip).Hash` must equal the matching
line in `SHA256SUMS.txt`. Then run `xmesh version`; it must print `xmesh __TAG__`.

## Desktop app

The tray app ships as an installer. Each installer also contains the `xmesh` binary for its platform, the
same one as in the archives above.

- Windows: `xmesh-ui-*-win-x64.exe` (64-bit Intel or AMD) or `xmesh-ui-*-win-arm64.exe` (ARM). Run it and
  follow the installer.
- macOS: `xmesh-ui-*-mac-arm64.dmg` (Apple silicon) or `xmesh-ui-*-mac-x64.dmg` (Intel). Open it and drag
  xmesh to Applications.

The installers are not code-signed, so the system warns on first launch:

- Windows: SmartScreen shows "Windows protected your PC". Choose "More info", then "Run anyway".
- macOS: Gatekeeper may say the app is damaged. Run `/usr/bin/xattr -dr com.apple.quarantine /Applications/xmesh.app`
  in Terminal and open the app again. Alternatively, open System Settings > Privacy & Security and choose
  "Open Anyway".
  Run the command before the first launch. If the app was already opened while it was still quarantined, macOS
  runs it from a read-only copy under AppTranslocation, and the command does nothing to that copy: quit xmesh,
  run the command, then open it again from Applications.

The control panel opens from the tray icon. It shows the overview, devices, pairing, configuration, audit,
sessions, authorization, ingress and mesh pages. Try them, and the tray menu's restart and stop actions.

On Windows, if the firewall blocks the node from receiving connections, the tray icon turns yellow and
the overview shows a warning. The warning stays until the block rule for xmesh is removed or changed in
Windows Defender Firewall. Uninstalling removes the logon task the installer created for your user and
that install folder; tasks of other users and of other folders are kept.

## Known limits of this build

- The binaries are not code-signed or notarized. macOS: run `/usr/bin/xattr -d com.apple.quarantine xmesh` after
  extracting. Windows: SmartScreen may ask for confirmation.
- LAN mode: the Agent always listens on TCP 7433 and that port cannot be configured, so two LAN nodes cannot run
  on the same host.
- Windows L2 isolation is not available in production yet: a Windows node configured with
  `agent.policy.isolation: L2` refuses exec and PTY with `sandbox.unavailable`. This is expected, and
  step F of the runbook checks for exactly that refusal.
- The desktop app is a first build. Apart from the tray app, everything is driven by the `xmesh` command line.
- OAuth tokens are kept in memory only, so an ingress restart requires authorizing again.

The release commit passed the Go, contract fixture and Headscale interop workflows, including the
Windows native jobs, before these binaries were built.
