#!/usr/bin/env python3
"""Assemble the native desktop executor with its stable macOS application identity."""
import argparse
import pathlib
import plistlib
import re
import shutil


def package(binary, output, platform, version):
    binary, output = pathlib.Path(binary), pathlib.Path(output)
    if not binary.is_file():
        raise ValueError("native executor binary is missing")
    if output.exists():
        raise ValueError("output already exists")
    if platform == "darwin":
        release = version.removeprefix("v")
        match = re.fullmatch(r"(\d+\.\d+\.\d+)(?:-[0-9A-Za-z.-]+)?", release)
        if not match:
            raise ValueError("unsupported macOS release version")
        numeric = match.group(1)
        target = output / "Contents" / "MacOS" / "xmesh-computer"
        target.parent.mkdir(parents=True)
        info = {
            "CFBundleIdentifier": "app.xmesh.computer",
            "CFBundleName": "XmeSh Computer",
            "CFBundleExecutable": "xmesh-computer",
            "CFBundlePackageType": "APPL",
            "CFBundleVersion": numeric,
            "CFBundleShortVersionString": numeric,
            "XmeshReleaseVersion": release,
            "LSMinimumSystemVersion": "14.0",
            "LSUIElement": True,
            "NSHighResolutionCapable": True,
            "NSPrincipalClass": "NSApplication",
            "NSScreenCaptureUsageDescription": "XmeSh shares the desktop with agents you authorize.",
        }
        with (output / "Contents" / "Info.plist").open("wb") as stream:
            plistlib.dump(info, stream)
        notices = pathlib.Path(__file__).resolve().parents[1] / "computer-native/macos/THIRD_PARTY_NOTICES.txt"
        resources = output / "Contents/Resources"
        resources.mkdir()
        shutil.copy2(notices, resources / "ThirdPartyNotices.txt")
    else:
        target = output
        target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(binary, target)
    target.chmod(0o755)
    return target


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--platform", choices=["darwin", "windows", "linux"], required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    package(args.binary, args.output, args.platform, args.version)
