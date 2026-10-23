#!/usr/bin/env python3
"""Generate a versioned static installation site and raw Markdown agent guide."""
import argparse
import html
import json
import re
import shutil
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit
from release_manifest import channel_for, validate

DEFAULT_SOURCE = Path(__file__).resolve().parent.parent


DOC_STYLE = """body{font:16px/1.7 system-ui,sans-serif;color:#20241f;background:#f7f8f3;margin:0}main{max-width:900px;margin:auto;padding:28px 24px 70px}nav{display:flex;gap:24px;flex-wrap:wrap;border-bottom:1px solid #d9dfd3;padding-bottom:18px;font-size:14px}a{color:#295b38;text-underline-offset:3px}h1,h2,h3{line-height:1.3;scroll-margin-top:20px}h1{font-size:32px;margin-top:32px}h2{margin-top:36px}pre{background:#e8eedf;padding:18px;overflow:auto;border-radius:7px}code{font-family:ui-monospace,monospace;font-size:.88em;overflow-wrap:anywhere}pre code{overflow-wrap:normal}table{display:block;overflow:auto;border-collapse:collapse;margin:20px 0}td,th{text-align:left;border:1px solid #cdd5c2;padding:10px;min-width:130px}th{background:#e8eedf}blockquote{border-left:3px solid #65824c;padding-left:18px;color:#596151}li{margin:7px 0}.version{font-size:13px;color:#596151}"""


def document_link(target):
    """Keep safe links and map local Markdown documents to their HTML peer."""
    parts = urlsplit(target)
    if parts.scheme and parts.scheme.lower() not in ('https', 'http', 'mailto'):
        return None
    if parts.netloc and not parts.scheme:
        return None
    if not parts.scheme and parts.path.endswith('.md'):
        parts = parts._replace(path=parts.path[:-3] + '.html')
    return urlunsplit(parts)


def inline_markdown(text):
    # Code spans are consumed first so markup and URLs inside code stay literal.
    pattern = re.compile(r'`([^`]+)`|\[([^\]\n]+)\]\(([^\s)]+)\)|\*\*([^*]+)\*\*')
    out = []
    position = 0
    for match in pattern.finditer(text):
        out.append(html.escape(text[position:match.start()]))
        code, label, target, strong = match.groups()
        if code is not None:
            out.append('<code>' + html.escape(code) + '</code>')
        elif label is not None:
            url = document_link(target)
            escaped_label = html.escape(label)
            out.append(escaped_label if url is None else '<a href="' + html.escape(url, quote=True) + '">' + escaped_label + '</a>')
        else:
            out.append('<strong>' + html.escape(strong) + '</strong>')
        position = match.end()
    out.append(html.escape(text[position:]))
    return ''.join(out)


def markdown_body(markdown):
    """Render the guide's headings, paragraphs, fences, lists and pipe tables.

    Raw HTML is always escaped. This deliberately supports the repository's
    documentation subset and does not execute extensions or remote content.
    """
    lines = markdown.splitlines()
    out = []
    heading_ids = {}
    i = 0
    while i < len(lines):
        line = lines[i]
        if not line.strip():
            i += 1
            continue
        fence = re.match(r'^\s*```([A-Za-z0-9_-]*)\s*$', line)
        if fence:
            language = fence[1]
            code = []
            i += 1
            while i < len(lines) and not re.match(r'^\s*```\s*$', lines[i]):
                code.append(lines[i])
                i += 1
            attr = ' class="language-' + language + '"' if language else ''
            out.append('<pre><code' + attr + '>' + html.escape('\n'.join(code)) + '</code></pre>')
            i += 1
            continue
        heading = re.match(r'^(#{1,6})\s+(.+)$', line)
        if heading:
            depth, title = len(heading[1]), heading[2]
            slug = re.sub(r'[^\w -]', '', title.lower()).strip().replace(' ', '-') or 'section'
            count = heading_ids.get(slug, 0)
            heading_ids[slug] = count + 1
            anchor = slug if not count else f'{slug}-{count}'
            out.append(f'<h{depth} id="{html.escape(anchor, quote=True)}">{inline_markdown(title)}</h{depth}>')
            i += 1
            continue
        if i + 1 < len(lines) and '|' in line and re.fullmatch(r'\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*', lines[i + 1]):
            cells = lambda row: [cell.strip() for cell in row.strip().strip('|').split('|')]
            headers = cells(line)
            rows = []
            i += 2
            while i < len(lines) and '|' in lines[i] and lines[i].strip():
                rows.append(cells(lines[i]))
                i += 1
            out.append('<table><thead><tr>' + ''.join('<th>' + inline_markdown(cell) + '</th>' for cell in headers) + '</tr></thead><tbody>')
            for row in rows:
                out.append('<tr>' + ''.join('<td>' + inline_markdown(cell) + '</td>' for cell in row) + '</tr>')
            out.append('</tbody></table>')
            continue
        item = re.match(r'^\s*(?:([-+*])|([0-9]+)\.)\s+(.+)$', line)
        if item:
            ordered = item[2] is not None
            tag = 'ol' if ordered else 'ul'
            out.append('<' + tag + '>')
            while i < len(lines):
                entry = re.match(r'^\s*(?:([-+*])|([0-9]+)\.)\s+(.+)$', lines[i])
                if not entry or (entry[2] is not None) != ordered:
                    break
                out.append('<li>' + inline_markdown(entry[3]) + '</li>')
                i += 1
            out.append('</' + tag + '>')
            continue
        if line.startswith('> '):
            out.append('<blockquote>' + inline_markdown(line[2:]) + '</blockquote>')
            i += 1
            continue
        paragraph = [line.strip()]
        i += 1
        while i < len(lines) and lines[i].strip() and not re.match(r'^(#{1,6}\s|\s*```|\s*[-+*]\s|\s*\d+\.\s|> )', lines[i]):
            if i + 1 < len(lines) and '|' in lines[i] and re.match(r'\s*\|?\s*:?-{3,}', lines[i + 1]):
                break
            paragraph.append(lines[i].strip())
            i += 1
        out.append('<p>' + inline_markdown(' '.join(paragraph)) + '</p>')
    return '\n'.join(out)


def render_agent_documents(source, version):
    """Return paired UTF-8 Markdown/standalone HTML suitable for site or ZIP."""
    result = {}
    for file in sorted(source.glob('*.md')):
        markdown = file.read_text(encoding='utf-8').replace('v1.2.3', version)
        result[file.name] = markdown
        title = next((line[2:] for line in markdown.splitlines() if line.startswith('# ')), file.stem)
        result[file.with_suffix('.html').name] = (
            '<!doctype html>\n<html lang="en"><head><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width, initial-scale=1">'
            '<title>' + html.escape(title) + ' · xmesh</title><style>' + DOC_STYLE + '</style></head><body><main>'
            '<nav aria-label="Documentation"><a href="README.html">Agent guide</a><a href="' + html.escape(file.name, quote=True) + '">Raw Markdown</a></nav>'
            '<p class="version">xmesh ' + html.escape(version) + '</p>' + markdown_body(markdown) + '</main></body></html>\n'
        )
    return result

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
    for name, content in render_agent_documents(source / "docs/agent", version).items():
        (docs / name).write_text(content, encoding="utf-8")
    for name in ("install.sh", "install.ps1"):
        shutil.copy2(source / "scripts" / name, version_dir / name)
    linux_installer = source / "scripts/install-linux.sh"
    if linux_installer.is_file():
        shutil.copy2(linux_installer, version_dir / linux_installer.name)
    (version_dir / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    for file in (source / "docs/site").iterdir():
        if file.suffix in (".js", ".css"):
            shutil.copy2(file, version_dir / file.name)
    guide_url = f"{base_url}/versions/{version}/agent/README.md"
    prompt = (f"请帮助我安装和配置 xmesh {version}。先阅读 {guide_url} 及其中的 install/setup/recovery 文档。"
              "检查操作系统、架构和已有安装，使用该版本 manifest 校验安装包，遵守系统签名检查。"
              "请确认我的用途（本机/多设备/云端）、工作目录、开发或只读权限、Codex/Claude Code 客户端；"
              "展示 setup plan 后等待我确认，再 apply。浏览器登录由我完成。保存 plan/job ID，"
              "遇到问题使用 status/resume/verify/doctor，并在真实客户端请求验证后报告完成。")
    (version_dir / "prompt.txt").write_text(prompt + "\n", encoding="utf-8")
    navigation = (source / "llms.txt").read_text(encoding="utf-8").replace("docs/agent/", "agent/")
    navigation = navigation.replace("(docs/", f"(https://raw.githubusercontent.com/{repo}/{version}/docs/")
    (version_dir / "llms.txt").write_text(navigation, encoding="utf-8")
    config = {"version": version, "release": release, "assets": [x for x in manifest["assets"] if x["kind"] == "desktop"]}
    template = (source / "docs/site/template.html").read_text(encoding="utf-8")
    def render(prefix):
        values = {"VERSION": version, "CHANNEL": channel, "RELEASE": f"https://github.com/{repo}/releases/tag/{version}",
                  "REPO": f"https://github.com/{repo}", "DOCS": prefix + "agent", "LLMS": prefix + "llms.txt",
                  "STATIC": prefix.rstrip("/") or ".", "PROMPT": prompt, "LINUX_SCRIPT": f"{release}/install-linux.sh",
                  "CONFIG": json.dumps(config, ensure_ascii=False).replace("<", "\\u003c")}
        page = template
        for key, value in values.items():
            page = page.replace("@@" + key + "@@", value if key == "CONFIG" else html.escape(value, quote=True))
        return page
    (version_dir / "index.html").write_text(render(""), encoding="utf-8")
    if homepage:
        output.mkdir(parents=True, exist_ok=True)
        (output / "index.html").write_text(render(f"versions/{version}/"), encoding="utf-8")
        (output / "llms.txt").write_text(navigation.replace("(agent/", f"(versions/{version}/agent/"), encoding="utf-8")
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
              json.loads(args.manifest.read_text(encoding="utf-8")), not args.version_only)
    except (ValueError, OSError, KeyError) as exc:
        parser.exit(1, f"install site: {exc}\n")

if __name__ == "__main__":
    main()
