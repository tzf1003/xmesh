# Windows kernel startup recovery

The installed v0.1.0-beta.5 desktop discovered `%APPDATA%\xmesh\beta-install\xmesh.yaml`, but the current user's `xmesh-beta-install-<SID hash>` scheduled task was absent. `start --name xmesh-beta-install --json` correctly returned `not_found` / `not_installed`. A saved configuration alone does not prove that its service is installed.

Restoring the task exposed a second failure: the saved configuration was still schema version 1. The kernel exited with `设备连接需要 schema_version: 2，请转换配置并重新配对`, although `schtasks /Run` and the old `xmesh start` reported success.

Recovery used the persistent installed kernel as the task executable, the existing configuration, and an explicit state directory. `xmesh migrate --name xmesh-beta-install` backed up the original configuration and legacy device state before migration. Restart then produced a responding control pipe; `status --control --node beta-install --json` reported `lifecycle: running`, Windows/amd64, kernel v0.1.0-beta.5, and the existing agent/gateway roles. Device peers need to be paired again after the protocol migration.

Changes:

- The About page now offers install, uninstall, reinstall, start, stop and restart for discovered local nodes, including stopped configurations. Install registers without starting; reinstall stops, migrates, unregisters, installs and verifies the replacement. Uninstall retains configurations, tokens and state. Reinstall restores default login autostart settings, as explained in its confirmation.
- Manual lifecycle actions use explicit configuration/state paths, migration, old-process exit checks and kernel-version acceptance. Renderer requests contain only validated operations and discovered node IDs; system-level nodes require administrator management. Concurrent actions for a node are rejected.
- Windows `start` requires a responding kernel control channel after Task Scheduler accepts the request. Starting an already running kernel remains idempotent. Computer hosts retain their separate readiness protocol.
- Startup verification also recognizes a manifest's node name when it differs from the state directory name.
- Background CLI invocations decode redirected Task Scheduler XML using the Windows OEM code page when it is neither UTF-8 nor UTF-16. On Chinese Windows, the non-ASCII task description otherwise prevented parsing before startup. UTF-8/UTF-16 paths remain supported.
- A saved service identity is retained across configuration renames, shutdowns and desktop restarts; explicit reinstall updates that identity. Startup acceptance prefers an exact state-directory ID before considering a manifest-name alias. Before reinstall removes a registration, the read-only `service-info` command captures its custom OAuth password-file path and local-only flag, so the replacement retains those options without reading credential contents.
- Stopped services skip redundant stop requests. Reinstall migrates before unregistering, preserving the registration if migration fails. Custom instance IDs may include dots/underscores. Unresponsive custom-config nodes recover their paths from registration metadata. Windows management checks the actual SCM registration with query-only permissions, independently of the state root; inaccessible scope fails closed.
- Saved Windows state paths use case-insensitive discovery after shutdown. Tasks without an explicit state directory derive readiness paths from their current configuration, including renamed nodes and relative config paths. Missing macOS LaunchAgents report `not_installed`. App updates and manual lifecycle operations reject overlap in both directions, and all six controls are disabled during download/installation.

Validation on Windows in the interactive user session:

```powershell
go build -o .tmp/xmesh.exe ./cmd/xmesh
$env:XMESH_TEST_WINDOWS_USER_SERVICE = '1'
$env:XMESH_TEST_WINDOWS_BINARY = Join-Path $PWD '.tmp/xmesh.exe'
go test ./cmd/xmesh -run 'WindowsUser(TaskRuntime|KernelStart)|KernelStart|TaskStateDirectory|ServiceNotInstalled' -count=1 -v -timeout=120s
```

The production-kernel acceptance test installs a disposable login task, rejects an unmigrated configuration, migrates it, starts and probes the kernel, checks repeated start, and stops/removes the task. It does not use the existing node's state.

Also verified:

- `go test ./cmd/xmesh`, including old control-protocol compatibility and deterministic GBK task-XML decoding. Native startup acceptance now invokes the production CLI in a hidden window, matching Electron.
- Desktop TypeScript typecheck/build and 111 related unit tests (actions, lifecycle, service options, preload, packaging and kernel updates), including Windows case handling and update/lifecycle exclusion.
- Nine real Electron end-to-end tests covering all six controls, confirmation/cancellation, offline installation/startup, visible failures and existing update flows.
- A disposable native Windows node exercised all six operations through the production `KernelLifecycle` backend and CLI, including repeated installation after uninstall, an aliased state directory, rename/restart/stop/start/reinstall, a custom OAuth password file and retention of the configuration. Its task and temporary state were removed afterwards.
- Visual checks at 1100×1000 and 480×900: controls wrap without clipping, the reinstall confirmation is readable, and no page errors or relevant console warnings were observed. Browser plugin was unavailable; the repository's Playwright/Electron harness was used.
- Packaged-app CI caught an unbundled YAML import in the main process. The config parser now ships as a bundled module; a regression test copies it outside the source tree and parses both YAML and JSON without runtime dependencies. The local Windows release package also passed the production fuse/content checks and fake-kernel startup smoke test.

The full local desktop unit suite had one environment-dependent failure: creating a file symlink in `gateway-token.test` requires a Windows privilege unavailable in this session. The test was retained; all final CI jobs must pass before merge.
