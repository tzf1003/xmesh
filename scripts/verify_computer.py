#!/usr/bin/env python3
"""Check native helper architecture, packaged identity and native-host version."""
import argparse
import os
import pathlib
import platform
import plistlib
import struct
import subprocess


def verify(root, target_os, arch, version):
    root = pathlib.Path(root)
    name = "xmesh-computer.exe" if target_os == "windows" else "xmesh-computer"
    binary = root / name
    if target_os == "darwin":
        app = root / "xmesh-computer.app"
        binary = app / "Contents" / "MacOS" / name
        with (app / "Contents" / "Info.plist").open("rb") as stream:
            info = plistlib.load(stream)
        assert info["CFBundleIdentifier"] == "app.xmesh.computer", "application identity mismatch"
        release = version.removeprefix("v")
        numeric = release.split("-", 1)[0]
        assert info["CFBundleVersion"] == numeric, "application build version mismatch"
        assert info["CFBundleShortVersionString"] == numeric, "application short version mismatch"
        assert info["XmeshReleaseVersion"] == release, "application release version mismatch"
        notices=(app / "Contents/Resources/ThirdPartyNotices.txt").read_text()
        assert all(name in notices for name in ("2026 Leo","2025 Cua AI, Inc.","Matthew Lam","yabai")), "background engine license notices missing"
        subprocess.run(["codesign", "--verify", "--strict", str(app)], check=True)
    with binary.open("rb") as stream:
        header = stream.read(64)
        if target_os == "windows":
            assert header[:2] == b"MZ", "native executor is not PE"
            stream.seek(struct.unpack_from("<I", header, 60)[0])
            signature, machine = struct.unpack("<4sH", stream.read(6))
            assert signature == b"PE\0\0" and machine == {"amd64": 0x8664, "arm64": 0xAA64}[arch], "PE architecture mismatch"
        elif target_os == "linux":
            assert header[:6] == b"\x7fELF\x02\x01", "native executor is not ELF64 little endian"
            assert struct.unpack_from("<H", header, 18)[0] == {"amd64": 62, "arm64": 183}[arch], "ELF architecture mismatch"
        else:
            assert header[:4] == b"\xcf\xfa\xed\xfe", "native executor is not Mach-O64 little endian"
            assert struct.unpack_from("<I", header, 4)[0] == {"amd64": 0x01000007, "arm64": 0x0100000C}[arch], "Mach-O architecture mismatch"
    if target_os != "windows":
        assert os.access(binary, os.X_OK), "native executor lacks executable mode"
    host_os = {"Darwin": "darwin", "Windows": "windows", "Linux": "linux"}.get(platform.system())
    host_arch = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    if (host_os, host_arch) == (target_os, arch):
        result = subprocess.run([str(binary.resolve()), "--version"], capture_output=True, text=True, check=True, timeout=10)
        assert result.stdout.strip() == f"xmesh-computer {version} protocol=1", "native executor version mismatch"
    print(f"verified native executor: {target_os}/{arch} {version}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--platform", choices=["darwin", "linux", "windows"], required=True)
    parser.add_argument("--arch", choices=["amd64", "arm64"], required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    verify(args.root, args.platform, args.arch, args.version)
