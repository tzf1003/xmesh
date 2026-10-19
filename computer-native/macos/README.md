# macOS background input bridge

The Rust desktop executor links this Swift code into the same signed native
binary. The Go Agent continues to own network authorization, leases, action IDs,
fencing, target selection, budgets and cancellation. The bridge exposes only a
bounded internal C ABI; it opens no listener and installs no daemon or MCP server.

## Reused implementation

`vendor/open-computer-use/upstream.json` pins the source snapshot and SHA-256 of
each copied file. SkyLight dynamic symbol discovery, the Chromium-compatible
click recipe, target-only synthetic activation, key-window records, command-menu
dispatch and shortcut parsing come from iFurySt/open-computer-use under MIT.
Its transitive Cua and yabai notices are preserved. Pointer gesture delivery in
`Sources/XmeshBridge.swift` adapts computer-use-mcp's MIT implementation. The
selected upstream snapshots were inspected as source; their README claims are
not xmesh runtime acceptance evidence.

The upstream snapshot files are unchanged. `build.rs` stages two deliberate
integration patches in Cargo's shared output directory: use a private click event
source, and ask current AX foreground state instead of NSWorkspace's run-loop
cache. `UpstreamKeyboard.swift` retains the upstream text/chord methods with
private keyboard event sources. Integration hooks route cancellation and
permission checks to the Rust executor and keep diagnostics out of input logs.

`THIRD_PARTY_NOTICES.txt` accompanies the macOS application bundle. Runtime
dependency inspection must confirm that the native binary resolves Swift from
system libraries; no separate upstream runtime installation is required.

## Current boundary

The backend binds a session to an executor-issued application window identity.
Independent-window screenshots use ScreenCaptureKit. The input bridge checks the
lease, target process lifetime, window ownership and foreground state. A user
working in the target application prevents background dispatch. It returns an
error when required private symbols or an action are unsupported and does not
select global cursor delivery.

The reused click recipe currently supports left single/double click. The
background backend reports other click forms as unsupported. Device-specific
AppKit/Electron compatibility, simultaneous user input, occlusion, cancellation
during native dispatch and cross-version private-API behavior remain acceptance
requirements. Compilation and pure recipe checks do not establish those results.

`scripts/test_computer_background.py` checks snapshot hashes, builds and runs
pure recipe/admission tests. It does not open apps, take screenshots or post
native input. Real desktop acceptance requires the desktop user's approval.
