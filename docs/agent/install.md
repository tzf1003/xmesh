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
- Linux: `~/.local/share/xmesh/bin/xmesh`, linked from `~/.local/bin/xmesh` (see the Linux section).

The bootstrap returns `cli_path`, `package_path`, `version`, `phase`, and `next_action`. Bash `--json` and PowerShell `-Json` emit one JSON object per event; PowerShell returns objects by default. Use the returned CLI path in every subsequent command. On Windows invoke a quoted path with `&`.

The installed desktop application opens its setup wizard. `--no-launch` / `-NoLaunch` defers opening it. `xmesh setup` also prints entry guidance. A repeat installation reuses verified downloads and applies the selected version through the native installer. macOS retains a timestamped previous application for recovery. Close running applications before upgrading. Keep the configuration/state directory when replacing an application.

## Linux

Linux has no desktop package. `install-linux.sh` (published beside `manifest.json`) installs the kernel and the web management panel as two systemd **user** services, so run it as the user who will use them, not with `sudo`. It needs `curl`, `tar`, `sha256sum`, `python3`, a systemd user session (an ordinary login or SSH session has one) and, for the panel, Node.js 22.18 or newer.

```bash
bash install-linux.sh --channel stable
bash install-linux.sh --version v1.2.3 --no-webui          # kernel only, no Node.js needed
bash install-linux.sh --offline /absolute/xmesh-offline --version v1.2.3
bash install-linux.sh --download-only --version v1.2.3
```

For `--offline`, put `manifest.json`, `xmesh-<version>-linux-<arch>.tar.gz` and `xmesh-webui-<version>-linux.tar.gz` from the same release into one directory. The script checks every archive against the manifest's SHA-256 and size, rejects archives with links or paths outside their directory, installs only after everything verified, and checks that the installed `xmesh` reports the release version.

What it does, in order:

1. Asks every question first (nothing is downloaded until they are settled): purpose (`local`, or `multi-device` for pairing on the LAN), node name, the **workspace directory** and the **permission** (`development` allows file changes and commands, `readonly` only reads), the panel's listen address, and the panel's **management key**. Tailnet and cloud setups need a browser login: finish the install first, then use the panel's setup wizard.
2. Installs `xmesh` to `~/.local/share/xmesh/bin` and the panel to `~/.local/share/xmesh/webui`.
3. Creates the node through `xmesh setup plan` and `apply`, after showing the plan's summary and asking for confirmation. The kernel service is `xmesh-<node>`. A node that already has a configuration is never changed: the script only restarts it if it is running, and does not ask the setup questions again.
4. Runs `xmesh-webui install`, which writes the `xmesh-webui` user service and its management key (`~/.config/xmesh-webui/webui.key`, mode 0600) and starts it. The panel listens on `127.0.0.1:7480` by default; use an SSH tunnel for remote access, or `--web-tls-cert/--web-tls-key`. A non-local address without TLS is refused unless `--web-insecure` is given.
5. Offers `loginctl enable-linger` so both services keep running after logout (`--linger` / `--no-linger` to answer in advance).

Non-interactive use (`--yes`) never defaults what a remote agent may touch: pass both `--workspace DIR` and `--permission development|readonly` when a node must be created. Never give the management key on a command line or put it in a prompt. The user types it into the script's hidden prompt, or supplies it as a file with `--web-key-from FILE` (first line, at least 16 characters, no spaces); with neither, the panel generates a random key and prints it once. Show the user where the key is stored, not the key. Opening `http://127.0.0.1:7480/` then asks for it.

Rerun the script with a newer `--version` to upgrade: it replaces the programs, restarts a running kernel service and the panel service, and keeps the node configuration and the management key. To remove the panel service run `node ~/.local/share/xmesh/webui/xmesh-webui.js uninstall` (add `--purge` to delete its key as well).
