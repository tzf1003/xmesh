# Installation and setup recovery

The bootstrap emits stable phases: `arguments`, `platform`, `resolve`, `manifest`, `download`, `checksum`, `trust`, `install`, `verify`, `launch`, `complete`. A failure has `ok: false`, the current phase and `next_action`; the process exits nonzero.

| Phase | Check and next action |
| --- | --- |
| platform | Check OS/architecture. Choose amd64 or arm64 manually when automatic detection is insufficient. |
| resolve | Check HTTPS/network and repository access; pin an existing release version or use an extracted offline bundle. |
| manifest | Keep script, manifest and package from one version. Check channel and explicit testing trust consent. |
| download | Retry with the same cache. Incomplete files use `.part`; complete verified packages are reused. |
| checksum | Delete the `.part` file and fetch again. An offline mismatch requires a correct copy from the same release. |
| trust | Read system verification output. Use an accepted signed release or your organization's approved authorization procedure. |
| install | Close xmesh, check user directory permissions and free space, rerun the same version. Windows reports the native installer's exit code. |
| verify | Check the returned CLI location and version. macOS keeps the previous app beside the new app; restore it after quitting the application if needed. |
| launch | Open the installed application manually, then run setup inspect from the returned CLI. |

Setup failures identify a step and next action in their JSON result. Preserve the job ID, inspect status, resolve its reported prerequisite, resume the same job, and verify. Run `xmesh doctor --json` for local diagnostics. Share diagnostic codes and relevant redacted output; keep browser sessions, auth keys and private workspace content local.

Windows L2 isolation remains unavailable in production. A Windows node configured for L2 refuses exec and PTY with `sandbox.unavailable`. This is an operational boundary independent of setup readiness. See the release's real-environment validation runbook for platform acceptance.
