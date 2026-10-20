xmesh __TAG__ connects selected workspaces to local MCP clients, other devices and cloud agents.

## Install and connect

Use the [versioned install page](https://tzf1003.github.io/xmesh/versions/__TAG__/) to select Windows/macOS and amd64/arm64, download the desktop installer, or copy a version-pinned Agent prompt. Open the installed application, choose purpose/workspace/permissions/clients, review the setup plan and verify a real client request.

This release includes `manifest.json` with platform, architecture, SHA-256, byte size and verified signing metadata. Download and review `install.sh` (macOS) or `install.ps1` (Windows), then pin this version:

```bash
bash install.sh --version __TAG__ --json
```

```powershell
.\install.ps1 -Version __TAG__ -Json
```

Stable releases require Authenticode verification on Windows and a signed, notarized, stapled macOS application. Beta/rc versions belong to the testing channel. Consult each asset's `trust` entry; explicitly unsigned testing packages require `--allow-unsigned` / `-AllowUnsigned`. System security policies and signature checks remain active.

For offline installation, extract `xmesh-offline-__TAG__-<os>-<arch>.zip`, then use `--offline <folder>` / `-Offline <folder>`. The ZIP contains the native package, manifest, scripts and Agent documentation. OS trust verification may require network access or locally cached trust data. Verified downloads remain cached for retries. Bootstrap JSON events identify the phase, package, installed CLI path and recovery action.

The persistent CLI is inside the installed application: macOS `xmesh.app/Contents/Resources/bin/xmesh`, Windows `resources/bin/xmesh.exe`. Agent documentation is included in the offline bundle and published as raw Markdown under the versioned install page. Use `setup inspect`, `plan --input`, `apply --plan`, `status/resume/verify --job`, and `doctor --json`. Preserve plan/job IDs for recovery. A running service and client verification have separate readiness states.

## Verify and validate

Every downloadable file is listed in `SHA256SUMS.txt`. Match the package hash with the corresponding release record. The signed desktop packages contain the platform CLI; stable Windows/macOS distribution uses these native bundles. Linux CLI archives include the scripts and [real-environment runbook](https://github.com/tzf1003/xmesh/blob/__TAG__/docs/validation/real-environment-runbook.md).

The release tag must pass the full CI run on the same commit before artifacts are built. This covers Go, contracts, Headscale, Onboarding, UI and the native desktop executor, including native user-service acceptance when the runner has a supported user session. Native installer launch and upgrade acceptance must also be run on target devices; passing build checks alone does not establish those outcomes.

## Operational limits

- Windows L2 isolation is unavailable in production. A Windows node configured with `agent.policy.isolation: L2` refuses exec and PTY with `sandbox.unavailable`; runbook step F checks that boundary.
- LAN Agent TCP port 7433 is fixed, so two LAN nodes cannot run on the same host.
- OAuth tokens are held in memory; an ingress restart requires authorization again.
- Cloud connections require a reachable public endpoint, browser authorization and a successful request from the intended client. Setup reports prerequisites and recovery actions when these remain pending.
