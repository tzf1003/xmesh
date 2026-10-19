#!/usr/bin/env python3
"""Build and validate the public, version-pinned bootstrap manifest."""
import argparse
import hashlib
import json
import re
import zipfile
from pathlib import Path

TAG = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(beta|rc)\.(0|[1-9][0-9]*))?$")
REPO = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
DESKTOP = {(os, arch) for os in ("darwin", "windows") for arch in ("amd64", "arm64")}

def channel_for(version):
    match = TAG.fullmatch(version)
    if not match:
        raise ValueError("version must be vMAJOR.MINOR.PATCH[-beta.N|-rc.N]")
    return "testing" if match[4] else "stable"

def validate(manifest):
    version = manifest.get("version", "")
    channel = channel_for(version)
    if manifest.get("schema_version") != 1 or manifest.get("channel") != channel:
        raise ValueError("manifest schema/channel mismatch")
    if manifest.get("docs_version") != version:
        raise ValueError("docs_version must match version")
    repo = manifest.get("repository", "")
    if not REPO.fullmatch(repo):
        raise ValueError("invalid repository")
    prefix = f"https://github.com/{repo}/releases/download/{version}/"
    seen = set()
    for asset in manifest.get("assets", []):
        key = (asset.get("os"), asset.get("arch"), asset.get("kind"))
        if key in seen:
            raise ValueError("duplicate platform asset")
        seen.add(key)
        name = asset.get("name", "")
        if not re.fullmatch(r"[A-Za-z0-9_.-]+", name) or asset.get("url") != prefix + name:
            raise ValueError("invalid asset name or URL")
        if not re.fullmatch(r"[a-f0-9]{64}", asset.get("sha256", "")) or type(asset.get("size")) is not int or asset["size"] <= 0:
            raise ValueError("invalid asset checksum/size")
        if key[0] not in ("darwin", "windows", "linux") or key[1] not in ("amd64", "arm64") or key[2] not in ("desktop", "cli"):
            raise ValueError("unsupported platform asset")
        trust = asset.get("trust", {})
        if channel == "stable" and key[0] in ("darwin", "windows"):
            if key[2] != "desktop" or trust.get("signed") is not True:
                raise ValueError("stable native assets require verified desktop signatures")
            if key[0] == "darwin" and (trust.get("notarized") is not True or trust.get("stapled") is not True):
                raise ValueError("stable macOS assets require notarization and staple")
    if not {(os, arch, "desktop") for os, arch in DESKTOP}.issubset(seen):
        raise ValueError("four desktop platform assets are required")
    return manifest

def build(dist, version, repo):
    channel = channel_for(version)
    assets = []
    for os, arch in sorted(DESKTOP):
        label, ext = ("mac", "dmg") if os == "darwin" else ("win", "exe")
        machine = "x64" if arch == "amd64" else arch
        name = f"xmesh-ui-{version[1:]}-{label}-{machine}.{ext}"
        file = dist / name
        evidence = dist / f"signing-{os}-{arch}.json"
        trust = json.loads(evidence.read_text(encoding="utf-8-sig")) if evidence.exists() else {"signed": False, "notarized": False, "stapled": False}
        assets.append(record(file, repo, version, os, arch, "desktop", trust))
    for os in ("linux", "darwin", "windows"):
        for arch in ("amd64", "arm64"):
            if channel == "stable" and os != "linux":
                continue
            file = dist / f"xmesh-{version}-{os}-{arch}.{ 'zip' if os == 'windows' else 'tar.gz'}"
            if file.exists():
                assets.append(record(file, repo, version, os, arch, "cli", {"signed": False}))
    return validate({"schema_version": 1, "repository": repo, "version": version, "channel": channel,
                     "docs_version": version, "assets": assets})

def record(file, repo, version, os, arch, kind, trust):
    data = file.read_bytes()
    return {"os": os, "arch": arch, "kind": kind, "name": file.name, "size": len(data),
            "sha256": hashlib.sha256(data).hexdigest(), "url": f"https://github.com/{repo}/releases/download/{version}/{file.name}", "trust": trust}

def bundles(dist, manifest, root):
    from build_install_site import render_agent_documents
    documents = render_agent_documents(root / "docs/agent", manifest["version"])
    for asset in manifest["assets"]:
        if asset["kind"] != "desktop":
            continue
        target = dist / f"xmesh-offline-{manifest['version']}-{asset['os']}-{asset['arch']}.zip"
        with zipfile.ZipFile(target, "w", zipfile.ZIP_STORED) as bundle:
            bundle.write(dist / asset["name"], asset["name"])
            bundle.writestr("manifest.json", json.dumps(manifest, indent=2) + "\n")
            for file in [root / "llms.txt", root / "docs/operations.md", root / "docs/validation/real-environment-runbook.md", *sorted((root / "scripts").glob("*.py")), *sorted((root / "scripts").glob("*.ps1")), root / "scripts/install.sh"]:
                bundle.write(file, str(file.relative_to(root)))
            for name in ("install.sh", "install.ps1"):
                bundle.write(root / "scripts" / name, name)
            for name, content in documents.items():
                bundle.writestr("docs/agent/" + name, content.encode("utf-8"))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--offline-bundles", action="store_true")
    args = parser.parse_args()
    try:
        manifest = build(args.dist, args.version, args.repo)
        output = args.output or args.dist / "manifest.json"
        output.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        if args.offline_bundles:
            bundles(args.dist, manifest, Path(__file__).resolve().parent.parent)
    except (OSError, ValueError, KeyError) as exc:
        parser.exit(1, f"manifest: {exc}\n")

if __name__ == "__main__":
    main()
