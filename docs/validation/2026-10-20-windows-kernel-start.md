# Windows kernel startup recovery

The installed v0.1.0-beta.5 desktop discovered `%APPDATA%\xmesh\beta-install\xmesh.yaml`, but the current user's `xmesh-beta-install-<SID hash>` scheduled task was absent. `start --name xmesh-beta-install --json` correctly returned `not_found` / `not_installed`. A saved configuration alone does not prove that its service is installed.

Restoring the task exposed a second failure: the saved configuration was still schema version 1. The kernel exited with `设备连接需要 schema_version: 2，请转换配置并重新配对`, although `schtasks /Run` and the old `xmesh start` reported success.

Recovery used the persistent installed kernel as the task executable, the existing configuration, and an explicit state directory. `xmesh migrate --name xmesh-beta-install` backed up the original configuration and legacy device state before migration. Restart then produced a responding control pipe; `status --control --node beta-install --json` reported `lifecycle: running`, Windows/amd64, kernel v0.1.0-beta.5, and the existing agent/gateway roles. Device peers need to be paired again after the protocol migration.

Changes:

- Manual starts in the packaged desktop use the existing migration and kernel-version acceptance flow.
- Windows `start` requires a responding kernel control channel after Task Scheduler accepts the request. Starting an already running kernel remains idempotent. Computer hosts retain their separate readiness protocol.
- Startup verification also recognizes a manifest's node name when it differs from the state directory name.

Validation on Windows in the interactive user session:

```powershell
go build -o .tmp/xmesh.exe ./cmd/xmesh
$env:XMESH_TEST_WINDOWS_USER_SERVICE = '1'
$env:XMESH_TEST_WINDOWS_BINARY = Join-Path $PWD '.tmp/xmesh.exe'
go test ./cmd/xmesh -run 'WindowsUser(TaskRuntime|KernelStart)|KernelStart|TaskStateDirectory|ServiceNotInstalled' -count=1 -v -timeout=120s
```

The production-kernel acceptance test installs a disposable login task, rejects an unmigrated configuration, migrates it, starts and probes the kernel, checks repeated start, and stops/removes the task. It does not use the existing node's state.

Also verified: `go test ./cmd/xmesh`, desktop TypeScript typecheck/build, and the actions, CLI, update-kernels, and update-kernel-configs unit tests (44 passing).
