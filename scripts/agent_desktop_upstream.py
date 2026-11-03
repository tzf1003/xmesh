#!/usr/bin/env python3
"""Propose a pin update when upstream agent-desktop publishes a newer release.

Run daily by .github/workflows/agent-desktop-upstream.yml. For the npm dist-tag
`latest` (or --requested-version) this script:

1. refuses anything that is not newer than the pinned version in the engine
   manifest, and never moves the pin backwards;
2. downloads the npm tarball and checks it against the registry's sha512
   integrity;
3. hashes each platform binary and helper inside the tarball and checks it
   against its .sha256 sidecar. Nothing is extracted to disk, and neither the
   upstream binaries nor the npm postinstall script are ever run;
4. rewrites only version, tarball, platforms and validation.record in the
   manifest, and writes a pending validation record for the new version.

It never adds a version to validation.validated_versions. People do that after
acceptance testing. The daily run does not reopen a proposal that was closed; name
the version by hand to propose it again.
"""

import argparse
import base64
import hashlib
import json
import re
import shutil
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

PACKAGE = "agent-desktop"
LICENSE = "Apache-2.0"
REGISTRY_DOC = "https://registry.npmjs.org/agent-desktop"
TARBALL_PREFIX = "https://registry.npmjs.org/agent-desktop/-/"
USER_AGENT = "xmesh-agent-desktop-upstream"
DEFAULT_MANIFEST = "internal/computer/adengine/manifest.json"
RECORD_DIR = "docs/validation"
BRANCH_PREFIX = "automation/agent-desktop-"
OUTPUT_KEYS = ("changed", "reason", "pinned", "upstream", "version", "branch", "title")
CHUNK = 1 << 20

# process.platform-process.arch -> (main executable, helper executables). Names are
# files in the npm package's bin/ directory and mirror lib/platform.js in the
# package. Anything else under bin/ is reported as unmapped and never recorded.
PLATFORMS = {
    "darwin-arm64": ("agent-desktop-darwin-arm64", ("agent-desktop-macos-helper-darwin-arm64",)),
    "darwin-x64": ("agent-desktop-darwin-x64", ("agent-desktop-macos-helper-darwin-x64",)),
    "linux-arm64": ("agent-desktop-linux-arm64", ("agent-desktop-linux-clipboard-owner-linux-arm64",)),
    "linux-x64": ("agent-desktop-linux-x64", ("agent-desktop-linux-clipboard-owner-linux-x64",)),
    "win32-arm64": ("agent-desktop-win32-arm64.exe", ()),
    "win32-x64": ("agent-desktop-win32-x64.exe", ()),
}
# The package's JavaScript launcher ships beside the binaries. It is not a platform binary.
LAUNCHER = "bin/agent-desktop"

SEMVER = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
SHA256 = re.compile(r"[0-9a-fA-F]{64}")


class UpstreamError(Exception):
    """A check failed. The message goes to the run log unchanged."""


@dataclass(frozen=True)
class Target:
    version: str
    latest: str
    tarball: str
    integrity: str
    published_at: str


def parse_version(text):
    match = SEMVER.fullmatch(text or "")
    if not match:
        raise UpstreamError(f"unsupported version {text!r}; expected MAJOR.MINOR.PATCH")
    return tuple(int(part) for part in match.groups())


def record_path(version):
    return f"{RECORD_DIR}/agent-desktop-{version}.md"


def select_target(packument, requested=""):
    latest = packument.get("dist-tags", {}).get("latest", "")
    parse_version(latest)
    version = requested or latest
    parse_version(version)
    doc = packument.get("versions", {}).get(version)
    if doc is None:
        raise UpstreamError(f"{PACKAGE}@{version} is not published")
    dist = doc.get("dist", {})
    tarball, integrity = dist.get("tarball", ""), dist.get("integrity", "")
    if not tarball.startswith(TARBALL_PREFIX) or not tarball.endswith(".tgz"):
        raise UpstreamError(f"unexpected tarball URL {tarball!r}")
    if not integrity.startswith("sha512-"):
        raise UpstreamError(f"{PACKAGE}@{version} has no sha512 integrity in the registry")
    return Target(version, latest, tarball, integrity, packument.get("time", {}).get(version, ""))


def decide(pinned, target, pulls, requested=False):
    """Return (propose, reason). `pulls` holds `gh pr list --state all` entries.

    An open proposal blocks every other one. A proposal that was closed without
    merging blocks the daily run for its version, so a rejected proposal is not
    reopened every day. A requested version is a deliberate choice, so it may be
    proposed again.
    """
    pinned_version, target_version = parse_version(pinned), parse_version(target)
    if target_version == pinned_version:
        return False, f"upstream {target} is already pinned"
    if target_version < pinned_version:
        return False, f"upstream {target} is older than the pinned {pinned}; the pin never moves backwards"
    for pull in pulls:
        if pull.get("state") == "OPEN" and str(pull.get("headRefName", "")).startswith(BRANCH_PREFIX):
            return False, f"open proposal #{pull.get('number')} ({pull.get('headRefName')}) is handled first"
    branch = BRANCH_PREFIX + target
    if not requested:
        for pull in pulls:
            if pull.get("headRefName") == branch:
                return False, (f"the proposal for {target} already exists as #{pull.get('number')} "
                               f"({str(pull.get('state', '')).lower()}); run this workflow by hand "
                               f"with version {target} to propose it again")
    return True, f"upstream {target} is newer than the pinned {pinned}"


class SameHostRedirects(urllib.request.HTTPRedirectHandler):
    """Follow a redirect only to the scheme and host the request started from.

    The tarball URL comes from the registry document, so a redirect may not point CI
    at another host. A refused redirect fails the run.
    """

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        origin = urllib.parse.urlsplit(req.full_url)
        target = urllib.parse.urlsplit(newurl)
        if (target.scheme, target.netloc) != (origin.scheme, origin.netloc):
            raise UpstreamError(f"refusing redirect from {origin.netloc} to {target.netloc or newurl}")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


OPENER = urllib.request.build_opener(SameHostRedirects)


def fetch_json(url):
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT, "Accept": "application/json"})
    with OPENER.open(request, timeout=60) as response:
        return json.loads(response.read().decode("utf-8"))


def fetch_file(url, destination):
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    with OPENER.open(request, timeout=120) as response, open(destination, "wb") as handle:
        shutil.copyfileobj(response, handle, CHUNK)


def load_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def verify_integrity(path, integrity):
    algorithm, _, expected = integrity.partition("-")
    if algorithm != "sha512":
        raise UpstreamError(f"unsupported integrity algorithm {algorithm!r}")
    digest = hashlib.sha512()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(CHUNK), b""):
            digest.update(chunk)
    actual = base64.b64encode(digest.digest()).decode("ascii")
    if actual != expected:
        raise UpstreamError(f"tarball sha512 {actual} does not match the registry integrity {expected}")


def index_archive(archive):
    """Map package-relative names to regular-file members. Anything else is rejected."""
    files = {}
    for member in archive.getmembers():
        parts = PurePosixPath(member.name).parts
        if member.name.startswith("/") or ".." in parts or not parts or parts[0] != "package":
            raise UpstreamError(f"unexpected archive entry {member.name!r}")
        relative = "/".join(parts[1:])
        if member.isdir():
            continue
        if not relative or not member.isreg():
            raise UpstreamError(f"archive entry {member.name!r} is not a regular file")
        files[relative] = member
    return files


def read_member(archive, member):
    handle = archive.extractfile(member)
    if handle is None:
        raise UpstreamError(f"cannot read {member.name!r}")
    with handle:
        return handle.read()


def sha256_of(archive, member):
    handle = archive.extractfile(member)
    if handle is None:
        raise UpstreamError(f"cannot read {member.name!r}")
    digest = hashlib.sha256()
    with handle:
        for chunk in iter(lambda: handle.read(CHUNK), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sidecar_digest(archive, files, name):
    member = files.get(f"bin/{name}.sha256")
    if member is None:
        raise UpstreamError(f"bin/{name} has no .sha256 sidecar")
    tokens = read_member(archive, member).decode("ascii", errors="replace").split()
    if not tokens or not SHA256.fullmatch(tokens[0]):
        raise UpstreamError(f"bin/{name}.sha256 does not begin with a SHA-256 digest")
    if len(tokens) > 1 and tokens[1].lstrip("*") != name:
        raise UpstreamError(f"bin/{name}.sha256 names another file: {tokens[1]!r}")
    return tokens[0].lower()


def platform_entry(archive, files, name):
    member = files.get(f"bin/{name}")
    if member is None:
        raise UpstreamError(f"upstream package has no bin/{name}")
    digest = sha256_of(archive, member)
    if digest != sidecar_digest(archive, files, name):
        raise UpstreamError(f"bin/{name} does not match its .sha256 sidecar")
    return {"member": f"bin/{name}", "sha256": digest}


def collect_platforms(archive, files):
    return {
        key: {
            "binary": platform_entry(archive, files, binary),
            "helpers": [platform_entry(archive, files, helper) for helper in helpers],
        }
        for key, (binary, helpers) in PLATFORMS.items()
    }


def unmapped_members(files):
    known = {LAUNCHER}
    for binary, helpers in PLATFORMS.values():
        for name in (binary, *helpers):
            known.update({f"bin/{name}", f"bin/{name}.sha256"})
    return sorted(name for name in files if name.startswith("bin/") and name not in known)


def check_package_json(archive, files, version):
    member = files.get("package.json")
    if member is None:
        raise UpstreamError("upstream package has no package.json")
    try:
        meta = json.loads(read_member(archive, member).decode("utf-8"))
    except ValueError as exc:
        raise UpstreamError(f"package.json is not valid JSON: {exc}") from exc
    if not isinstance(meta, dict):
        raise UpstreamError("package.json is not a JSON object")
    found = (meta.get("name"), meta.get("version"), meta.get("license"))
    expected = (PACKAGE, version, LICENSE)
    if found != expected:
        raise UpstreamError(f"package.json reports {found}, expected {expected}")


def updated_manifest(manifest, target, platforms):
    """Change only the fields this script owns. Everything else is kept as it was."""
    result = dict(manifest)
    result["version"] = target.version
    result["tarball"] = {"url": target.tarball, "integrity": target.integrity, "published_at": target.published_at}
    result["platforms"] = platforms
    upstream = dict(manifest.get("upstream") or {})
    upstream["latest"] = target.latest
    result["upstream"] = upstream
    validation = dict(manifest.get("validation") or {})
    validation.setdefault("validated_versions", [])
    validation["record"] = record_path(target.version)
    result["validation"] = validation
    return result


def render_record(target, pinned, platforms, unmapped):
    lines = [
        f"# agent-desktop {target.version} 验收记录",
        "",
        "状态：**待验收（pending）**。由 `scripts/agent_desktop_upstream.py` 生成。"
        f"人工验收通过之前，不要把 `{target.version}` 加入 `validation.validated_versions`。",
        "",
        "## 来源",
        "",
        f"- 上游包：`{PACKAGE}@{target.version}`",
        f"- npm dist-tag `latest`：{target.latest}",
        f"- 发布时间：{target.published_at or '未知'}",
        f"- tarball：{target.tarball}",
        f"- integrity：`{target.integrity}`（已与 registry 比对）",
        f"- 钉住版本：{pinned} → {target.version}",
        "- 校验范围：tarball 的 sha512；六个平台的二进制与 helper 逐一比对 `.sha256` 侧车。"
        "没有运行任何上游二进制，也没有运行 postinstall。",
        "",
        "## 六个平台的 SHA-256",
        "",
        "| 平台 | 二进制 | sha256 | helper | helper sha256 |",
        "| --- | --- | --- | --- | --- |",
    ]
    for key, entry in platforms.items():
        binary = entry["binary"]
        if entry["helpers"]:
            helper = entry["helpers"][0]
            helper_cells = f"`{helper['member']}` | `{helper['sha256']}`"
        else:
            helper_cells = "— | —"
        lines.append(f"| {key} | `{binary['member']}` | `{binary['sha256']}` | {helper_cells} |")
    lines += ["", "## 未映射的上游文件", ""]
    if unmapped:
        lines += [f"- `{name}`：不写入清单，也不会暴露为工具" for name in unmapped]
    else:
        lines.append("- 无")
    lines += [
        "",
        "## 验收清单",
        "",
        "- [ ] macOS arm64 与 x64：P0 矩阵（snapshot、ref_act、中文输入、拖拽），核对 ref 的 stale 语义",
        "- [ ] macOS：替换二进制后，辅助功能与屏幕录制授权是否需要重新授予",
        "- [ ] Windows：P0 矩阵，确认 Notepad 快照超时是否复现",
        "- [ ] Linux：参数语义与映射清单一致",
        "- [ ] 对照上游变更（README、发布说明）；新命令保持未映射、不暴露",
        "- [ ] 验收结论与证据补在本文件；通过后再把版本加入 `validation.validated_versions`",
    ]
    return "\n".join(lines) + "\n"


def decision_record(args, propose, reason, pinned="", upstream="", version="", branch="", title=""):
    values = {
        "changed": "true" if propose else "false",
        "reason": reason,
        "pinned": pinned,
        "upstream": upstream,
        "version": version,
        "branch": branch,
        "title": title,
    }
    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as handle:
            for key in OUTPUT_KEYS:
                handle.write(f"{key}={values[key]}\n")
    if args.step_summary:
        status = f"draft proposal prepared: {reason}" if propose else reason
        with open(args.step_summary, "a", encoding="utf-8") as handle:
            handle.write("\n".join([
                "## agent-desktop upstream check",
                "",
                f"- pinned: `{pinned or '—'}`",
                f"- upstream latest: `{upstream or '—'}`",
                f"- result: {status}",
                "",
            ]))
    print(json.dumps(values, ensure_ascii=False))
    return 0


def run(args):
    root = Path(args.repo_root).resolve()
    manifest_file = root / args.manifest
    if not manifest_file.is_file():
        message = f"{args.manifest} is not on this branch yet; nothing to propose"
        print(f"::notice::{message}")
        return decision_record(args, False, message)

    manifest = load_json(manifest_file)
    pinned = manifest.get("version", "")
    parse_version(pinned)
    packument = load_json(args.packument) if args.packument else fetch_json(REGISTRY_DOC)
    target = select_target(packument, args.requested_version)
    pulls = load_json(args.pulls) if args.pulls else []
    propose, reason = decide(pinned, target.version, pulls, requested=bool(args.requested_version))
    if not propose:
        print(f"::notice::{reason}")
        return decision_record(args, False, reason, pinned=pinned, upstream=target.latest)

    with tempfile.TemporaryDirectory(prefix="agent-desktop-upstream-") as scratch:
        tarball = Path(args.tarball) if args.tarball else Path(scratch) / "package.tgz"
        if not args.tarball:
            fetch_file(target.tarball, tarball)
        verify_integrity(tarball, target.integrity)
        with tarfile.open(tarball, "r:gz") as archive:
            files = index_archive(archive)
            check_package_json(archive, files, target.version)
            platforms = collect_platforms(archive, files)
            unmapped = unmapped_members(files)

    document = render_record(target, pinned, platforms, unmapped)
    manifest_file.write_text(
        json.dumps(updated_manifest(manifest, target, platforms), indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )
    record = root / record_path(target.version)
    if record.exists():
        print(f"::notice::{record_path(target.version)} already exists and is left unchanged")
    else:
        record.parent.mkdir(parents=True, exist_ok=True)
        record.write_text(document, encoding="utf-8")
    if args.pr_body:
        Path(args.pr_body).write_text(document, encoding="utf-8")
    return decision_record(
        args,
        True,
        reason,
        pinned=pinned,
        upstream=target.latest,
        version=target.version,
        branch=BRANCH_PREFIX + target.version,
        title=f"agent-desktop: 提议钉住上游 {target.version}（待验收）",
    )


def parse_args(argv):
    parser = argparse.ArgumentParser(description="Propose an agent-desktop pin update from upstream.")
    parser.add_argument("--repo-root", default=".", help="repository root (default: .)")
    parser.add_argument("--manifest", default=DEFAULT_MANIFEST, help="engine manifest path, relative to the root")
    parser.add_argument("--requested-version", default="", help="propose this version instead of dist-tag latest")
    parser.add_argument("--packument", help="offline: registry packument JSON, instead of fetching it")
    parser.add_argument("--tarball", help="offline: npm tarball, instead of downloading it")
    parser.add_argument("--pulls", help="JSON from `gh pr list --state all --json number,state,headRefName`")
    parser.add_argument("--pr-body", help="write the proposal's markdown here")
    parser.add_argument("--github-output", help="GitHub Actions output file")
    parser.add_argument("--step-summary", help="GitHub Actions step summary file")
    return parser.parse_args(argv)


def main(argv=None):
    return run(parse_args(argv))


if __name__ == "__main__":
    try:
        sys.exit(main())
    except UpstreamError as exc:
        print(f"::error::{exc}")
        sys.exit(1)
