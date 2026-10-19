#!/usr/bin/env python3
"""Generate a versioned static installation site and raw Markdown agent guide."""
import argparse
import html
import json
import shutil
from pathlib import Path
from release_manifest import channel_for, validate

DEFAULT_SOURCE = Path(__file__).resolve().parent.parent

def build(source, output, version, repo, base_url, manifest=None, homepage=True):
    channel = channel_for(version)
    base_url = base_url.rstrip("/")
    if not base_url.startswith("https://"):
        raise ValueError("base URL must use HTTPS")
    release = f"https://github.com/{repo}/releases/download/{version}"
    if manifest is None:
        raise ValueError("release manifest is required")
    validate(manifest)
    if manifest["version"] != version or manifest["repository"] != repo:
        raise ValueError("site and manifest identities differ")
    version_dir = output / "versions" / version
    docs = version_dir / "agent"
    docs.mkdir(parents=True, exist_ok=True)
    for file in (source / "docs/agent").glob("*.md"):
        (docs / file.name).write_text(file.read_text().replace("v1.2.3", version))
    for name in ("install.sh", "install.ps1"):
        shutil.copy2(source / "scripts" / name, version_dir / name)
    (version_dir / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    for file in (source / "docs/site").iterdir():
        if file.suffix in (".js", ".css"):
            shutil.copy2(file, version_dir / file.name)
    guide_url = f"{base_url}/versions/{version}/agent/README.md"
    prompt = (f"请帮助我安装和配置 xmesh {version}。先阅读 {guide_url} 及其中的 install/setup/recovery 文档。"
              "检查操作系统、架构和已有安装，使用该版本 manifest 校验安装包，遵守系统签名检查。"
              "请确认我的用途（本机/多设备/云端）、工作目录、开发或只读权限、Codex/Claude Code 客户端；"
              "展示 setup plan 后等待我确认，再 apply。浏览器登录由我完成。保存 plan/job ID，"
              "遇到问题使用 status/resume/verify/doctor，并在真实客户端请求验证后报告完成。")
    (version_dir / "prompt.txt").write_text(prompt + "\n")
    navigation = (source / "llms.txt").read_text().replace("docs/agent/", "agent/")
    navigation = navigation.replace("(docs/", f"(https://raw.githubusercontent.com/{repo}/{version}/docs/")
    (version_dir / "llms.txt").write_text(navigation)
    config = {"version": version, "release": release, "assets": [x for x in manifest["assets"] if x["kind"] == "desktop"]}
    template = (source / "docs/site/template.html").read_text()
    def render(prefix):
        values = {"VERSION": version, "CHANNEL": channel, "RELEASE": f"https://github.com/{repo}/releases/tag/{version}",
                  "REPO": f"https://github.com/{repo}", "DOCS": prefix + "agent", "LLMS": prefix + "llms.txt",
                  "STATIC": prefix.rstrip("/") or ".", "PROMPT": prompt,
                  "CONFIG": json.dumps(config, ensure_ascii=False).replace("<", "\\u003c")}
        page = template
        for key, value in values.items():
            page = page.replace("@@" + key + "@@", value if key == "CONFIG" else html.escape(value, quote=True))
        return page
    (version_dir / "index.html").write_text(render(""))
    if homepage:
        output.mkdir(parents=True, exist_ok=True)
        (output / "index.html").write_text(render(f"versions/{version}/"))
        (output / "llms.txt").write_text(navigation.replace("(agent/", f"(versions/{version}/agent/"))
    return version_dir

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=DEFAULT_SOURCE)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--repo", default="tzf1003/xmesh")
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--version-only", action="store_true")
    args = parser.parse_args()
    try:
        build(args.source, args.output, args.version, args.repo, args.base_url,
              json.loads(args.manifest.read_text()), not args.version_only)
    except (ValueError, OSError, KeyError) as exc:
        parser.exit(1, f"install site: {exc}\n")

if __name__ == "__main__":
    main()
