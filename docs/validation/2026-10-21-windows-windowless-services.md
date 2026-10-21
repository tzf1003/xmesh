# Windows windowless kernel and desktop host

The current-user kernel and Computer Host scheduled tasks now use the sibling
`xmesh-background.exe`, built from the same command with `-H=windowsgui`. The
console CLI remains `xmesh.exe`. Installation verifies the background binary's
PE subsystem. Start/restart rewrites a legacy `xmesh.exe` task action before
launching it, preserving the complete XML, arguments, user and working directory.
SCM installations keep the existing `install --system` behavior.

Computer native processes use `CREATE_NO_WINDOW` with inherited anonymous pipes.
Scheduler operations and setup subprocesses also run without console windows.
Missing standard streams are replaced with NUL; inherited CLI pipes are retained.
This matters for the Computer Host startup metadata write under Task Scheduler.

Release archives and desktop bundles include the background binary. Windows
signing covers it, and package verification checks its GUI subsystem and version
against the console CLI.

Validation on Windows, 2026-10-21:

- `go test -race ./cmd/xmesh ./internal/computer` and targeted `go vet` passed.
- Native `Test_WindowsUserKernelStart` passed using persistent CLI/background
  builds: install an isolated task, simulate a legacy console action, migrate it
  on start, reject an old config, migrate the config, probe control readiness,
  repeat start, verify the actual process image is the GUI background build, then
  stop/uninstall the temporary task.
- `TestOnboardingNativeService` passed the actual guided installation flow and
  removed its temporary service.
- `TestBackgroundStandardIO` passed for inherited pipes and missing streams;
  `TestNativeProcessHasNoConsole` verified `GetConsoleWindow() == 0` in a real
  subprocess while receiving its JSON over stdout.
- UI build, typecheck and 41 packaging/lifecycle/Computer Host unit tests passed.
- An unpacked Windows x64 desktop bundle passed release fuse/content checks,
  background PE subsystem checks and CLI/background version equality. The native
  helper used for this packaging check was the existing installed helper; it was
  not rebuilt or installed.
- Release distribution Python tests passed (13 tests, 5 platform skips); workflow
  YAML parsing and the 146/146 contract claiming-test check passed.
- Windows arm64 background, macOS arm64 and Linux arm64 builds passed.

The full desktopbroker suite could not bind the user's fixed Computer pipe
because the installed desktop host was already running. Its five binding tests
reported access denied. That installed host and the user's live nodes were left
running; full installed Computer Host lifecycle acceptance remains for an isolated
user session/CI. Local packaging produced an unpacked artifact only; the installed
application was not replaced, and no release or signing claim is made.
