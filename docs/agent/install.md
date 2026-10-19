# Install and locate xmesh

Download the desktop package and `manifest.json` from the same versioned GitHub release. Match Windows/macOS and amd64 (Intel/AMD x64) or arm64 (Apple silicon/Windows on ARM). When architecture is uncertain, check Windows Settings → System → About or macOS About This Mac. The install page keeps manual selection available.

Download and review the bootstrap script before running it. The scripts install for the current user, verify the selected package SHA-256 and size, retain verified downloads for retries, and report a persistent bundled CLI path. A checksum detects changed bytes relative to the release manifest; release provenance also depends on HTTPS and system code signing.

```bash
bash install.sh --version v1.2.3 --json
bash install.sh --channel testing --allow-unsigned --download-only --json
bash install.sh --offline /absolute/xmesh-offline --version v1.2.3 --json
```

```powershell
.\install.ps1 -Version v1.2.3 -Json
.\install.ps1 -Channel testing -AllowUnsigned -DownloadOnly -Json
.\install.ps1 -Offline C:\transfer\xmesh-offline -Version v1.2.3 -Json
```

`v1.2.3` illustrates a pinned release; substitute an existing version. Stable selects normal semantic versions. Testing selects beta/rc versions, even when historical GitHub metadata labeled them Latest. Pinned versions must match an explicitly selected channel. Resolution examines the newest 100 release records; pin a version when the requested channel falls outside that window.

Stable Windows packages require verified Authenticode signatures. Stable macOS packages require a signed, notarized, stapled application. Testing packages can be explicitly unsigned; supply `--allow-unsigned` / `-AllowUnsigned` only when accepting that release's stated trust status. System trust checks remain active. An organization policy or Gatekeeper rejection requires an accepted release or the organization's approved authorization procedure. The scripts preserve the package and report the `trust` phase; they never change quarantine or security policy.

Offline ZIPs contain the selected desktop installer, manifest, both bootstrap scripts, and this documentation. Extract the whole ZIP and run its script with the extracted folder as `--offline` / `-Offline`. Install scripts support `--arch`, `--cache`, `--install-dir`, `--no-launch`, and their PowerShell equivalents. Use `--download-only` first on a connected machine, or transfer the complete offline ZIP. The offline path makes no release API or package download requests; OS signature validation can still need network access or locally available trust data.

The CLI persists at:

- macOS: `~/Applications/xmesh.app/Contents/Resources/bin/xmesh` (or the chosen application directory).
- Windows: `%LOCALAPPDATA%\Programs\xmesh\resources\bin\xmesh.exe` (or the existing/current user's chosen installer directory).

The bootstrap returns `cli_path`, `package_path`, `version`, `phase`, and `next_action`. Bash `--json` and PowerShell `-Json` emit one JSON object per event; PowerShell returns objects by default. Use the returned CLI path in every subsequent command. On Windows invoke a quoted path with `&`.

The installed desktop application opens its setup wizard. `--no-launch` / `-NoLaunch` defers opening it. `xmesh setup` also prints entry guidance. A repeat installation reuses verified downloads and applies the selected version through the native installer. macOS retains a timestamped previous application for recovery. Close running applications before upgrading. Keep the configuration/state directory when replacing an application.
