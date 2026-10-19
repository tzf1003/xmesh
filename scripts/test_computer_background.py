#!/usr/bin/env python3
"""Verify vendored source and pure macOS background recipes without desktop input."""
import hashlib
import json
import pathlib
import platform
import subprocess
import tempfile


def verify_snapshot(root):
    vendor = root / "computer-native/macos/vendor/open-computer-use"
    manifest = json.loads((vendor / "upstream.json").read_text())
    assert len(manifest["revision"]) == 40
    for name, info in manifest["files"].items():
        assert pathlib.Path(name).name == name
        assert hashlib.sha256((vendor / name).read_bytes()).hexdigest() == info["sha256"], name
    return vendor


def main():
    root = pathlib.Path(__file__).resolve().parents[1]
    vendor = verify_snapshot(root)
    bridge = root / "computer-native/macos"
    # The reachable adapter surface must not grow global injection or activation.
    for source in (bridge / "Sources").glob("*.swift"):
        text = source.read_text()
        for forbidden in (".post(tap:", "CGWarpMouseCursorPosition(", ".activate(", "NSPasteboard"):
            assert forbidden not in text, (source.name, forbidden)
    if platform.system() != "Darwin":
        print("PASS: pinned source hashes and background adapter route checks; Swift recipes require macOS")
        return
    target = "arm64" if platform.machine() == "arm64" else "x86_64"
    with tempfile.TemporaryDirectory(prefix="xmesh-background-recipes-") as temporary:
        output = pathlib.Path(temporary) / "checks"
        sources = sorted((bridge / "Sources").glob("*.swift")) + sorted(vendor.glob("*.swift"))
        subprocess.run(["xcrun", "swiftc", "-swift-version", "5", "-parse-as-library",
                        "-module-name", "XmeshBackgroundChecks", "-target", target + "-apple-macosx14.0",
                        "-o", str(output), *map(str, sources), str(bridge / "Tests/RecipeChecks.swift")], check=True)
        imports=subprocess.check_output(["xcrun","nm","-u",str(output)],text=True)
        symbols={line.split()[-1] for line in imports.splitlines() if line.split()}
        assert "_CGEventPostToPid" in symbols, "process-directed delivery was not linked"
        assert not symbols.intersection({"_CGEventPost","_CGWarpMouseCursorPosition","_SetFrontProcess","_SetFrontProcessWithOptions"}), "background bridge links global input or activation"
        subprocess.run([str(output)], check=True)


if __name__ == "__main__":
    main()
