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
Accessibility hit-testing, ancestor action selection and text/selection helpers in
`Sources/AccessibilityClick.swift` also adapt computer-use-mcp. Its scoped
geometry search adapts Open Computer Use snapshot traversal and click-priority
selection, with finite node/depth/time budgets. A single semantic
mutation is selected before dispatch; an AX mutation error never retries a click
through another delivery route. The selected upstream snapshots were inspected
as source; their README claims are
not xmesh runtime acceptance evidence.

The upstream snapshot files are unchanged. `build.rs` stages a synchronous
foreground query in Cargo's shared output directory. The executor services
AppKit's main loop while the serial input worker dispatches actions; foreground
queries execute on that serviced main thread. A second staging patch selects
direct process keyboard delivery for native AppKit after checking that its
focused AX element belongs to the bound window. Chromium/Electron keep the
upstream synthetic key-window sequence. Both routes retain the same menu
shortcut and typing dispatcher. Renderer detection adapts the upstream
WindowOcclusionKeepAlive.swift framework check.
`UpstreamKeyboard.swift` retains the upstream text/chord methods with
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

The background backend tries AXPress / AXShowMenu first for single left / right
clicks in a confirmed target window. Its event route supports left single / double
click; unsupported forms fail before synthetic input. AppKit and Electron disposable targets passed click, Chinese text, select-all
with replacement, drag and scroll through the actual MCP Gateway / Agent / Host
chain on the development Mac. Each step asserted actual control state while an
independent read-only observer monitored the system pointer and foreground app.
The final worker/main-loop integration also passed a fully covered AppKit
window in a two-window application through MCP. Native drag cancellation after
movement began returned unknown and delivered the button release. The final
Electron MCP run completed during 98 hardware user-activity events with the
interference checks passing. These results cover the tested host and fixtures;
other applications and cross-version private-API behavior require their own
acceptance evidence. Extended AppKit and Electron MCP runs both passed
consecutive insertion and an actual Command+Backspace chord with the user-input
policy enabled. The AppKit run also covered a fully obscured target and refused
a key sent to another window in the same app while its focused field belonged
to the original target.

`scripts/test_computer_background.py` checks snapshot hashes, builds and runs
pure recipe/admission tests. It does not open apps, take screenshots or post
native input. Real desktop acceptance requires the desktop user's approval.

The AX value-splice route requires a valid exposed UTF-16 selection, rejects
surrogate-pair splits and caps the replacement value at 1,048,576 UTF-16 units.
It rechecks window ownership and focused-element identity before mutation.
A populated field uses AX point-to-range mapping to preserve coordinate-caret
semantics. AX metadata reads have a 100ms process-local default timeout; selected semantic
mutations receive a bounded one-second timeout for their callbacks. Setting a
timeout on the application object alone does not bound returned child objects.

Opt-in acceptance uses a disposable AppKit or Electron fixture:

```sh
XMESH_TEST_ALLOW_BACKGROUND_INPUT=1 python3 scripts/accept_computer_background.py \
  --helper /absolute/path/xmesh-computer --binary /absolute/path/xmesh \
  --fixture appkit
```

Use `--fixture electron` for the existing Electron dependency, or `--occluded`
with AppKit for a second fixture window covering the selected target. The
background-input flag requires the desktop user's prior approval. The harness
uses an independent observer, stops on unexplained pointer/focus changes,
verifies native MCP images plus action deduplication/status/renewal/close, and
removes its private profiles, tokens, processes and temporary files. It saves
neither desktop images nor user input text. The observer compares opaque AX
foreground-window/control identity every 50ms and requires an available AX
baseline before input. It reads no titles, values or selections; this covers
sampled exposed focus identity rather than transient or app-private focus.
Synthetic-focus cleanup checks real foreground identity so user activation of
the target is retained while held input is released.
`--cancel-drag` with AppKit and without `--binary` additionally verifies native
lease cancellation after a drag has begun and observes its button release.
Use `--pause-on-user-input` with `--binary` to exercise the kernel intervention
policy. `--steps click,text,append,key,native_key,drag,scroll` additionally checks
consecutive insertion and an actual key chord; the AppKit native-key acceptance
coverage above is limited to the tested applications. Use
`--occluded --reject-other-window-key` with AppKit and `--binary` to verify
that an unfocused window cannot redirect native keys to the app's focused window.
Run desktop acceptance sequentially
because the native executor holds a per-user desktop lock. A locked desktop
is reported as unavailable for input.
