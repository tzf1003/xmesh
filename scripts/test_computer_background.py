#!/usr/bin/env python3
"""Verify vendored source and pure macOS background recipes without desktop input."""
import hashlib
import json
import pathlib
import platform
import subprocess
import sys
import tempfile


def verify_snapshot(root):
    vendor = root / "computer-native/macos/vendor/open-computer-use"
    manifest = json.loads((vendor / "upstream.json").read_text())
    assert len(manifest["revision"]) == 40
    for name, info in manifest["files"].items():
        assert pathlib.Path(name).name == name
        assert hashlib.sha256((vendor / name).read_bytes()).hexdigest() == info["sha256"], name
    return vendor


def verify_focus_stall_guard():
    # A focus sample older than the limit can hide a focus change, so the
    # acceptance harness must stop rather than pass on stale identity.
    scripts = pathlib.Path(__file__).resolve().parent
    if str(scripts) not in sys.path:
        sys.path.insert(0, str(scripts))
    import accept_computer_background as harness
    fields = {"x": 1.0, "y": 2.0, "pid": 3, "app_name": "Fixture", "focus_generation": 4, "focus_available": True}
    limit = harness.FOCUS_SAMPLE_MAX_AGE_MS
    with tempfile.TemporaryDirectory(prefix="xmesh-focus-guard-") as temporary:
        observation = pathlib.Path(temporary) / "observation.json"
        harness.observation_file = observation
        try:
            for age, accepted in ((0, True), (limit, True), (limit + 1, False), (-1, False), (None, False), ("late", False)):
                observation.write_text(json.dumps({**fields, "focus_sample_age_ms": age}))
                try:
                    harness.pointer_and_focus()
                except harness.FocusObservationStalled:
                    assert not accepted, age
                else:
                    assert accepted, age
            observation.write_text(json.dumps(fields))
            try:
                harness.pointer_and_focus()
            except harness.FocusObservationStalled:
                pass
            else:
                raise AssertionError("observation without focus_sample_age_ms was accepted")
        finally:
            harness.observation_file = None


def verify_observer_compiles(root):
    # The acceptance harness compiles this observer at run time. Compiling it
    # here catches Swift errors before a merged change reaches the Mac.
    observer = root / "computer-native/macos/Tests/DesktopObserver.swift"
    with tempfile.TemporaryDirectory(prefix="xmesh-observer-") as temporary:
        subprocess.run(["xcrun", "swiftc", "-swift-version", "5", "-parse-as-library",
                        "-o", str(pathlib.Path(temporary) / "observer"), str(observer)], check=True)


def main():
    root = pathlib.Path(__file__).resolve().parents[1]
    vendor = verify_snapshot(root)
    bridge = root / "computer-native/macos"
    # The reachable adapter surface must not grow global injection or activation.
    for source in (bridge / "Sources").glob("*.swift"):
        text = source.read_text()
        for forbidden in (".post(tap:", "CGWarpMouseCursorPosition(", ".activate(", "NSPasteboard"):
            assert forbidden not in text, (source.name, forbidden)
    verify_focus_stall_guard()
    if platform.system() != "Darwin":
        print("PASS: pinned source hashes, focus stall guard and background adapter route checks; Swift recipes require macOS")
        return
    verify_observer_compiles(root)
    target = "arm64" if platform.machine() == "arm64" else "x86_64"
    with tempfile.TemporaryDirectory(prefix="xmesh-background-recipes-") as temporary:
        output = pathlib.Path(temporary) / "checks"
        sources = sorted((bridge / "Sources").glob("*.swift")) + sorted(vendor.glob("*.swift"))
        # Exercise the same hooks as build.rs while preserving pinned snapshots.
        staged = []
        for source in sources:
            if source.name in ("SkyClickSimulation.swift", "SkyKeyboardSimulation.swift"):
                text = source.read_text()
                assert text.count("NSWorkspace.shared.frontmostApplication?.processIdentifier") == 1
                text = text.replace("NSWorkspace.shared.frontmostApplication?.processIdentifier", "xmeshFrontmostPID()")
                if source.name == "SkyKeyboardSimulation.swift":
                    text = text.replace("if xmeshFrontmostPID() == target.pid {", "if try xmeshUseDirectKeyboard(target.pid, window: target.windowID) || xmeshFrontmostPID() == target.pid {")
                assert text.count("spi.endSyntheticTargetFocus(focusContext)") == 2
                text = text.replace("spi.endSyntheticTargetFocus(focusContext)", "xmeshEndSyntheticTargetFocus(spi, focusContext, pid: target.pid)")
                source = pathlib.Path(temporary) / source.name
                source.write_text(text)
            staged.append(source)
        subprocess.run(["xcrun", "swiftc", "-swift-version", "5", "-parse-as-library",
                        "-module-name", "XmeshBackgroundChecks", "-target", target + "-apple-macosx14.0",
                        "-o", str(output), *map(str, staged), str(bridge / "Tests/RecipeChecks.swift")], check=True)
        imports=subprocess.check_output(["xcrun","nm","-u",str(output)],text=True)
        symbols={line.split()[-1] for line in imports.splitlines() if line.split()}
        assert "_CGEventPostToPid" in symbols, "process-directed delivery was not linked"
        assert not symbols.intersection({"_CGEventPost","_CGWarpMouseCursorPosition","_SetFrontProcess","_SetFrontProcessWithOptions"}), "background bridge links global input or activation"
        subprocess.run([str(output)], check=True)


if __name__ == "__main__":
    main()
