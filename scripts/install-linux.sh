#!/bin/bash
# xmesh Linux installer: the kernel and the web management panel, each as its own
# systemd user service. Run a downloaded, reviewed script with bash.
set -Eeuo pipefail
VERSION=""; CHANNEL=stable; CHANNEL_SET=false; REPO=tzf1003/xmesh; ARCH=""; OFFLINE=""
PREFIX="${XDG_DATA_HOME:-$HOME/.local/share}/xmesh"; BIN_DIR="$HOME/.local/bin"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/xmesh/install"
WANT_KERNEL=true; WANT_WEBUI=true; ASSUME_YES=false; DOWNLOAD_ONLY=false
NODE_NAME=""; WORKSPACE=""; PERMISSION=""; PURPOSE=""
WEB_LISTEN=""; WEB_KEY_FROM=""; WEB_TLS_CERT=""; WEB_TLS_KEY=""; WEB_INSECURE=false; WEB_LANG=""
LINGER=ask; PHASE=arguments
WORK=""; TTY=""

usage() {
  cat <<'USAGE'
用法: bash install-linux.sh [选项]

发布与来源
  --version vX.Y.Z[-beta.N|-rc.N]   固定版本（默认取所选通道的最新版本）
  --channel stable|testing          发布通道（默认 stable）
  --repo OWNER/REPO                 发布仓库（默认 tzf1003/xmesh）
  --arch amd64|arm64                覆盖自动检测的架构
  --offline DIR                     从 DIR 读取 manifest.json 和归档，不访问网络
  --cache DIR                       已校验下载的缓存目录
  --download-only                   只下载并校验，不安装

安装位置
  --prefix DIR                      程序目录（默认 ~/.local/share/xmesh）
  --bin-dir DIR                     放置 xmesh 链接的目录（默认 ~/.local/bin）

内核服务（首次安装时创建节点）
  --no-kernel                       不安装内核服务
  --purpose local|multi-device      用途（默认 local；云端/Tailnet 请在网页向导里完成）
  --node-name NAME                  节点名（默认主机名）
  --workspace DIR                   允许 Agent 访问的工作目录（绝对路径）
  --permission development|readonly 开发（可读写、可执行命令）或只读

网页管理界面服务
  --no-webui                        不安装网页管理界面
  --web-listen ADDR                 监听地址（默认 127.0.0.1:7480）
  --web-key-from FILE               从 FILE 第一行读取管理密钥（不给则交互输入，留空自动生成）
  --web-tls-cert FILE --web-tls-key FILE   以 HTTPS 提供
  --web-insecure                    非本机地址且无 TLS 时仍然明文监听（不推荐）
  --lang zh|en                      网页界面的默认语言

其他
  --linger | --no-linger            是否让服务在退出登录后继续运行（loginctl enable-linger）
  --yes                             非交互：不提问；须同时给出 --workspace 和 --permission
  --help                            显示本帮助

管理密钥只通过交互输入、文件或随机生成提供，不接受命令行参数，避免出现在进程列表里。
USAGE
}

say() { printf '%s\n' "$*"; }
warn() { printf '警告: %s\n' "$*" >&2; }
fail() {
  trap - ERR
  printf '失败（阶段 %s）: %s\n' "$PHASE" "$1" >&2
  [ -z "${2:-}" ] || printf '下一步: %s\n' "$2" >&2
  exit 1
}
die_usage() { printf '%s\n' "$1" >&2; exit 2; }
cleanup() { [ -z "$WORK" ] || rm -rf "$WORK"; }
trap cleanup EXIT

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--channel|--repo|--arch|--offline|--cache|--prefix|--bin-dir|--purpose|--node-name|--workspace|--permission|--web-listen|--web-key-from|--web-tls-cert|--web-tls-key|--lang)
      [ "$#" -ge 2 ] || die_usage "缺少 $1 的值"
      case "$1" in
        --version) VERSION="$2";; --channel) CHANNEL="$2"; CHANNEL_SET=true;; --repo) REPO="$2";;
        --arch) ARCH="$2";; --offline) OFFLINE="$2";; --cache) CACHE="$2";; --prefix) PREFIX="$2";;
        --bin-dir) BIN_DIR="$2";; --purpose) PURPOSE="$2";; --node-name) NODE_NAME="$2";;
        --workspace) WORKSPACE="$2";; --permission) PERMISSION="$2";; --web-listen) WEB_LISTEN="$2";;
        --web-key-from) WEB_KEY_FROM="$2";; --web-tls-cert) WEB_TLS_CERT="$2";; --web-tls-key) WEB_TLS_KEY="$2";;
        --lang) WEB_LANG="$2";;
      esac; shift 2;;
    --no-kernel) WANT_KERNEL=false; shift;; --no-webui) WANT_WEBUI=false; shift;;
    --web-insecure) WEB_INSECURE=true; shift;; --linger) LINGER=yes; shift;; --no-linger) LINGER=no; shift;;
    --download-only) DOWNLOAD_ONLY=true; shift;; --yes|-y) ASSUME_YES=true; shift;;
    --help|-h) usage; exit 0;;
    *) die_usage "未知选项: $1（见 --help）";;
  esac
done

PHASE=platform
[ "$(uname -s)" = Linux ] || die_usage "本脚本只支持 Linux；macOS 请用 install.sh，Windows 请用 install.ps1。"
[ "$(id -u)" -ne 0 ] || warn "正以 root 运行：内核和网页界面都是用户服务，会安装给 root 自己。日常使用请改用目标用户登录后运行（不要用 sudo）。"

# One JSON/semver helper for everything below; python3 is on practically every Linux.
command -v python3 >/dev/null 2>&1 || fail "需要 python3 来解析发布清单。" "安装 python3 后重试。"
py() {
python3 - "$@" <<'PYTHON'
import json, os, re, sys

TAG = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(beta|rc)\.(0|[1-9][0-9]*))?$")

def tag_key(v):
    m = TAG.fullmatch(v)
    if not m:
        raise SystemExit("invalid version")
    return (int(m[1]), int(m[2]), int(m[3]), (1 if m[4] == "rc" else 0) if m[4] else 2, int(m[5] or 0))

def load(path):
    with open(path, encoding="utf-8-sig") as f:
        return json.load(f)

def dig(value, path):
    for part in path.split("."):
        if part == "":
            continue
        if isinstance(value, list):
            value = value[int(part)]
        elif isinstance(value, dict):
            value = value.get(part)
        else:
            return None
    return value

mode = sys.argv[1]
if mode == "resolve":
    releases = load(sys.argv[2]); channel = sys.argv[3]
    found = []
    for r in releases:
        if r.get("draft") or not isinstance(r.get("tag_name"), str):
            continue
        try:
            k = tag_key(r["tag_name"])
        except SystemExit:
            continue
        if (k[3] == 2) == (channel == "stable"):
            found.append((k, r["tag_name"]))
    if not found:
        raise SystemExit("no release in the requested channel; pin --version")
    print(max(found)[1])
elif mode == "manifest":
    m = load(sys.argv[2]); want_version, repo, arch, want_webui = sys.argv[3], sys.argv[4], sys.argv[5], sys.argv[6] == "true"
    k = tag_key(m["version"])
    channel = "stable" if k[3] == 2 else "testing"
    if m.get("schema_version") != 1 or (want_version and m["version"] != want_version) or m.get("repository") != repo or m.get("channel") != channel or m.get("docs_version") != m["version"]:
        raise SystemExit("manifest identity mismatch")
    version = m["version"]; base = f"https://github.com/{repo}/releases/download/{version}/"
    def pick(kind, a, expected):
        hits = [x for x in m["assets"] if x.get("os") == "linux" and x.get("arch") == a and x.get("kind") == kind]
        if len(hits) > 1:
            raise SystemExit(f"manifest lists {kind} twice")
        if not hits:
            return None
        x = hits[0]
        if x.get("name") != expected or x.get("url") != base + expected or not re.fullmatch(r"[a-f0-9]{64}", x.get("sha256", "")) or type(x.get("size")) is not int or x["size"] < 1:
            raise SystemExit(f"invalid {kind} asset name, URL, checksum or size")
        return "\t".join([kind, x["name"], x["sha256"], x["url"], str(x["size"])])
    print(version)
    cli = pick("cli", arch, f"xmesh-{version}-linux-{arch}.tar.gz")
    if cli is None:
        raise SystemExit(f"this release has no Linux {arch} kernel archive")
    print(cli)
    if want_webui:
        web = pick("webui", "any", f"xmesh-webui-{version}-linux.tar.gz")
        if web is None:
            raise SystemExit("this release has no web panel archive; rerun with --no-webui")
        print(web)
elif mode == "get":
    v = dig(load(sys.argv[2]), sys.argv[3])
    if isinstance(v, bool):
        print("true" if v else "false")
    elif isinstance(v, list):
        for x in v:
            print(x)
    elif v is not None:
        print(v)
elif mode == "nodes":
    for n in dig(load(sys.argv[2]), "data.nodes") or []:
        print("\t".join([n.get("name", ""), "true" if n.get("running") else "false"]))
elif mode == "failed-steps":
    d = dig(load(sys.argv[2]), "data") or {}
    for s in d.get("steps", []):
        if s.get("status") not in ("succeeded", "pending"):
            print(f"{s.get('id')}: {s.get('status')} {s.get('message', '')} {s.get('next_action', '')}".rstrip())
    if d.get("next_action"):
        print("下一步: " + d["next_action"])
elif mode == "input":
    purpose, node, workspace, permission = sys.argv[2:6]
    print(json.dumps({"schema_version": 1, "purpose": purpose, "node_name": node, "workspace": workspace,
                      "permission": permission, "clients": [], "autostart": True, "network": {"mode": "lan"}}))
else:
    raise SystemExit("invalid helper mode")
PYTHON
}

# ---- interaction ---------------------------------------------------------------
if "$ASSUME_YES"; then
  INTERACTIVE=false
elif [ -t 0 ]; then
  INTERACTIVE=true; TTY=/dev/stdin
elif [ -r /dev/tty ] && : < /dev/tty 2>/dev/null; then
  INTERACTIVE=true; TTY=/dev/tty
else
  INTERACTIVE=false
fi
# ask VAR "提示" [默认值]: leaves VAR alone when it is already set (from a flag).
ask() {
  local __var="$1" __prompt="$2" __default="${3:-}" __reply
  [ -z "${!__var}" ] || return 0
  if ! "$INTERACTIVE"; then
    [ -z "$__default" ] || printf -v "$__var" '%s' "$__default"
    return 0
  fi
  if [ -n "$__default" ]; then printf '%s [%s]: ' "$__prompt" "$__default" >&2; else printf '%s: ' "$__prompt" >&2; fi
  IFS= read -r __reply < "$TTY" || __reply=""
  printf -v "$__var" '%s' "${__reply:-$__default}"
}
# confirm "提示" yes|no: true/false; non-interactive runs take the default.
confirm() {
  local reply
  "$INTERACTIVE" || { [ "$2" = yes ]; return; }
  if [ "$2" = yes ]; then printf '%s [Y/n]: ' "$1" >&2; else printf '%s [y/N]: ' "$1" >&2; fi
  IFS= read -r reply < "$TTY" || reply=""
  case "${reply:-$2}" in y|Y|yes|YES|Yes) return 0;; *) return 1;; esac
}
read_secret() {
  local __var="$1" __prompt="$2" __value
  printf '%s: ' "$__prompt" >&2
  IFS= read -rs __value < "$TTY" || __value=""
  printf '\n' >&2
  printf -v "$__var" '%s' "$__value"
}

# ---- validate arguments --------------------------------------------------------
PHASE=arguments
[ -n "$ARCH" ] || ARCH="$(uname -m)"
case "$ARCH" in x86_64|x64) ARCH=amd64;; aarch64) ARCH=arm64;; esac
case "$ARCH" in amd64|arm64) ;; *) fail "不支持的架构 $ARCH。" "传入 --arch amd64 或 --arch arm64。";; esac
[[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "仓库格式无效。"
case "$CHANNEL" in stable|testing) ;; *) fail "通道无效。" "使用 stable 或 testing。";; esac
if [ -n "$VERSION" ]; then
  [[ "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(beta|rc)\.(0|[1-9][0-9]*))?$ ]] || fail "版本号无效。"
  inferred=stable; [[ "$VERSION" != *-* ]] || inferred=testing
  if "$CHANNEL_SET" && [ "$CHANNEL" != "$inferred" ]; then fail "版本与通道不一致。"; fi
  CHANNEL="$inferred"
fi
case "$PURPOSE" in ""|local|multi-device) ;; *) fail "用途只能是 local 或 multi-device。" "云端和 Tailnet 的设置需要浏览器登录，请先装完，再在网页管理界面的向导里完成。";; esac
case "$PERMISSION" in ""|development|readonly) ;; *) fail "权限只能是 development 或 readonly。";; esac
[ -z "$WORKSPACE" ] || [[ "$WORKSPACE" = /* ]] || fail "--workspace 必须是绝对路径。"
[[ "$PREFIX" = /* && "$BIN_DIR" = /* ]] || fail "--prefix 和 --bin-dir 必须是绝对路径。"
[ -z "$WEB_LANG" ] || [ "$WEB_LANG" = zh ] || [ "$WEB_LANG" = en ] || fail "--lang 只能是 zh 或 en。"
[ -z "$WEB_LISTEN" ] || [[ "$WEB_LISTEN" =~ ^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]*):[0-9]{1,5}$ ]] || fail "--web-listen 须为 host:port。"
if [ -n "$WEB_TLS_CERT$WEB_TLS_KEY" ]; then
  [ -n "$WEB_TLS_CERT" ] && [ -n "$WEB_TLS_KEY" ] || fail "--web-tls-cert 与 --web-tls-key 需要同时给出。"
  [ -r "$WEB_TLS_CERT" ] && [ -r "$WEB_TLS_KEY" ] || fail "读不到 TLS 证书或私钥文件。"
fi
[ -z "$WEB_KEY_FROM" ] || [ -r "$WEB_KEY_FROM" ] || fail "读不到 --web-key-from 指定的文件。"
"$WANT_KERNEL" || "$WANT_WEBUI" || "$DOWNLOAD_ONLY" || fail "--no-kernel 与 --no-webui 同时给出时没有可安装的内容。"

# ---- tools ---------------------------------------------------------------------
PHASE=preflight
for tool in tar mktemp; do command -v "$tool" >/dev/null 2>&1 || fail "缺少 $tool。"; done
[ -n "$OFFLINE" ] || command -v curl >/dev/null 2>&1 || fail "缺少 curl。" "安装 curl，或用 --offline 提供已下载的文件。"
if command -v sha256sum >/dev/null 2>&1; then sha() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then sha() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else fail "缺少 sha256sum。"; fi

NODE_BIN=""
if "$WANT_WEBUI" && ! "$DOWNLOAD_ONLY"; then
  NODE_BIN="$(command -v node || true)"
  [ -n "$NODE_BIN" ] || fail "网页管理界面需要 Node.js 22.18 或更新版本，但没有找到 node。" "安装 Node.js 后重试，或加 --no-webui 只安装内核。"
  node_version="$("$NODE_BIN" -p 'process.versions.node' 2>/dev/null || true)"
  IFS=. read -r nmaj nmin _ <<< "$node_version"
  if ! [[ "${nmaj:-}" =~ ^[0-9]+$ && "${nmin:-}" =~ ^[0-9]+$ ]] || [ "$nmaj" -lt 22 ] || { [ "$nmaj" -eq 22 ] && [ "$nmin" -lt 18 ]; }; then
    fail "Node.js 版本 ${node_version:-未知} 过旧，需要 22.18 或更新。" "升级 Node.js，或加 --no-webui 只安装内核。"
  fi
  NODE_BIN="$(readlink -f "$NODE_BIN")"
fi
if ! "$DOWNLOAD_ONLY" && { "$WANT_KERNEL" || "$WANT_WEBUI"; }; then
  command -v systemctl >/dev/null 2>&1 || fail "没有找到 systemctl：服务需要 systemd。" "在没有 systemd 的系统上请手动运行 xmesh serve 和 xmesh-webui。"
  systemctl --user show-environment >/dev/null 2>&1 || fail "没有可用的 systemd 用户会话。" "用日常登录方式（例如 SSH 登录）运行本脚本；若无会话可先由管理员执行 loginctl enable-linger $(id -un)，并确认 XDG_RUNTIME_DIR 已设置。"
fi

# ---- answers, all before anything is downloaded --------------------------------
PHASE=questions
# A node that already has a configuration is kept as it is; nothing about it is asked again.
EXISTING_NODE=false
compgen -G "${XDG_CONFIG_HOME:-$HOME/.config}/xmesh/*/xmesh.yaml" >/dev/null 2>&1 && EXISTING_NODE=true
ask_kernel() {
  if "$EXISTING_NODE"; then
    say "== 内核服务 =="; say "已有节点配置，保持不变，只更新程序。"
    return 0
  fi
  say "== 内核服务 =="
  [ -n "$PURPOSE" ] || ask PURPOSE "用途：local（只在本机使用）或 multi-device（和局域网内其他设备配对）" local
  [ -n "$PURPOSE" ] || PURPOSE=local
  case "$PURPOSE" in local|multi-device) ;; *) fail "用途只能是 local 或 multi-device。";; esac
  ask NODE_NAME "节点名" "$(hostname 2>/dev/null | cut -d. -f1)"
  [[ "$NODE_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || fail "节点名只能含字母、数字、点、下划线和连字符，且以字母或数字开头。"
  # What a remote agent may touch is never defaulted silently: asked here, or required as flags.
  if "$INTERACTIVE"; then
    ask WORKSPACE "Agent 可访问的工作目录（绝对路径，必填）"
    if [ -z "$PERMISSION" ]; then
      say "  development：允许文件读写与命令执行（工作目录策略不等同于系统沙箱）"
      say "  readonly：只读取工作目录"
      ask PERMISSION "权限" development
    fi
  fi
  [ -n "$WORKSPACE" ] && [ -n "$PERMISSION" ] || fail "没有给出 --workspace 和 --permission：它们决定远端 Agent 能访问什么，非交互运行不使用默认值。" "加上这两个选项，或去掉 --yes 以交互方式回答。"
  [[ "$WORKSPACE" = /* ]] || fail "工作目录必须是绝对路径。"
  case "$PERMISSION" in development|readonly) ;; *) fail "权限只能是 development 或 readonly。";; esac
  [ -d "$WORKSPACE" ] || if confirm "目录 $WORKSPACE 不存在，创建它？" yes; then mkdir -p "$WORKSPACE"; else fail "工作目录不存在。"; fi
}
KEY_SOURCE=none   # none: keep or generate; file; typed
TYPED_KEY=""
WEB_KEY_EXISTS=false
ask_webui() {
  say "== 网页管理界面 =="
  local default_listen="127.0.0.1:7480" key_file="${XDG_CONFIG_HOME:-$HOME/.config}/xmesh-webui/webui.key"
  ask WEB_LISTEN "监听地址（默认只监听本机，远程请用 SSH 隧道或 TLS）" "$default_listen"
  [[ "$WEB_LISTEN" =~ ^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]*):[0-9]{1,5}$ ]] || fail "监听地址须为 host:port。"
  local host="${WEB_LISTEN%:*}"; host="${host#[}"; host="${host%]}"
  if [ -z "$WEB_TLS_CERT" ] && ! [[ "$host" = localhost || "$host" = ::1 || "$host" = 127.* ]] && ! "$WEB_INSECURE"; then
    if "$INTERACTIVE"; then
      say "  $WEB_LISTEN 不是本机地址，而且没有配置 TLS：管理密钥和会话会以明文传输。"
      ask WEB_TLS_CERT "TLS 证书文件（PEM；留空则继续明文）"
      if [ -n "$WEB_TLS_CERT" ]; then
        ask WEB_TLS_KEY "TLS 私钥文件（PEM）"
        [ -r "$WEB_TLS_CERT" ] && [ -r "$WEB_TLS_KEY" ] || fail "读不到 TLS 证书或私钥文件。"
      else
        confirm "确认以明文监听 $WEB_LISTEN？" no && WEB_INSECURE=true || fail "已取消。" "改用 127.0.0.1 并通过 SSH 隧道访问，或提供 TLS 证书。"
      fi
    else
      fail "$WEB_LISTEN 不是本机地址且没有 TLS。" "加 --web-tls-cert/--web-tls-key，或 --web-insecure，或改用 127.0.0.1。"
    fi
  fi
  [ ! -e "$key_file" ] || WEB_KEY_EXISTS=true
  if [ -n "$WEB_KEY_FROM" ]; then
    KEY_SOURCE=file
  elif "$INTERACTIVE"; then
    if "$WEB_KEY_EXISTS"; then
      if confirm "已有管理密钥（$key_file）。更换它？" no; then :; else return 0; fi
    fi
    say "  管理密钥用于登录网页界面：至少 16 个字符，不含空格。"
    local first second
    while :; do
      read_secret first "管理密钥（留空则自动生成随机密钥）"
      if [ -z "$first" ]; then KEY_SOURCE=none; WEB_REGENERATE=true; break; fi
      if [ "${#first}" -lt 16 ] || [[ "$first" =~ [[:space:]] ]]; then warn "密钥至少 16 个字符且不含空白，请重新输入。"; continue; fi
      read_secret second "再次输入以确认"
      [ "$first" = "$second" ] && { TYPED_KEY="$first"; KEY_SOURCE=typed; break; }
      warn "两次输入不一致，请重新输入。"
    done
  fi
}
WEB_REGENERATE=false
"$DOWNLOAD_ONLY" || ! "$WANT_KERNEL" || ask_kernel
"$DOWNLOAD_ONLY" || ! "$WANT_WEBUI" || ask_webui
if ! "$DOWNLOAD_ONLY" && [ "$LINGER" = ask ]; then
  linger_now="$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null || true)"
  if [ "$linger_now" = yes ]; then LINGER=already
  elif confirm "让服务在退出登录后继续运行（loginctl enable-linger）？" yes; then LINGER=yes
  else LINGER=no; fi
fi

# ---- download and verify -------------------------------------------------------
PHASE=resolve
mkdir -p "$CACHE"
fetch() { curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 --retry 3 --retry-delay 2 --connect-timeout 15 --max-time 900 "$1" -o "$2"; }
if [ -n "$OFFLINE" ]; then
  MANIFEST="$OFFLINE/manifest.json"
  [ -f "$MANIFEST" ] || fail "$OFFLINE 里没有 manifest.json。"
else
  if [ -z "$VERSION" ]; then
    fetch "https://api.github.com/repos/$REPO/releases?per_page=100" "$CACHE/releases.json.part"
    VERSION="$(py resolve "$CACHE/releases.json.part" "$CHANNEL")" || fail "找不到所选通道的发布版本。" "用 --version 固定版本。"
  fi
  MANIFEST="$CACHE/$VERSION-manifest.json"
  fetch "https://github.com/$REPO/releases/download/$VERSION/manifest.json" "$MANIFEST.part"
  mv "$MANIFEST.part" "$MANIFEST"
fi
PHASE=manifest
fields="$(py manifest "$MANIFEST" "$VERSION" "$REPO" "$ARCH" "$WANT_WEBUI")" || fail "发布清单无效或缺少所需文件。" "确认 manifest.json 与归档来自同一个发布。"
IFS=$'\n' read -r -d '' VERSION CLI_LINE WEB_LINE <<< "$fields"$'\n' || true
inferred=stable; [[ "$VERSION" != *-* ]] || inferred=testing
if "$CHANNEL_SET" && [ "$CHANNEL" != "$inferred" ]; then fail "版本与通道不一致。"; fi
IFS=$'\t' read -r _ CLI_NAME CLI_SHA CLI_URL CLI_SIZE <<< "$CLI_LINE"
WEB_NAME=""
[ -z "${WEB_LINE:-}" ] || IFS=$'\t' read -r _ WEB_NAME WEB_SHA WEB_URL WEB_SIZE <<< "$WEB_LINE"

get_archive() { # name sha url size -> prints the verified path
  local name="$1" hash="$2" url="$3" size="$4" file
  file="$CACHE/$name"; [ -z "$OFFLINE" ] || file="$OFFLINE/$name"
  valid() { [ -f "$file" ] && [ "$(sha "$file")" = "$hash" ] && [ "$(stat -c %s "$file")" = "$size" ]; }
  if ! valid; then
    [ -z "$OFFLINE" ] || fail "离线文件 $name 的校验和或大小不符。" "从同一个发布重新获取 manifest.json 和归档。"
    PHASE=download; fetch "$url" "$file.part"
    PHASE=checksum
    [ "$(sha "$file.part")" = "$hash" ] && [ "$(stat -c %s "$file.part")" = "$size" ] || fail "下载的 $name 校验和或大小不符。" "删除缓存中的 .part 文件后重试。"
    mv "$file.part" "$file"
  fi
  valid || fail "$name 校验失败。"
  printf '%s\n' "$file"
}
PHASE=download
CLI_ARCHIVE="$(get_archive "$CLI_NAME" "$CLI_SHA" "$CLI_URL" "$CLI_SIZE")"
WEB_ARCHIVE=""
[ -z "$WEB_NAME" ] || WEB_ARCHIVE="$(get_archive "$WEB_NAME" "$WEB_SHA" "$WEB_URL" "$WEB_SIZE")"
PHASE=checksum
say "已校验 $VERSION 的发布文件（SHA-256 与大小）。"
if "$DOWNLOAD_ONLY"; then
  say "内核归档: $CLI_ARCHIVE"; [ -z "$WEB_ARCHIVE" ] || say "网页界面归档: $WEB_ARCHIVE"
  exit 0
fi

# ---- install files -------------------------------------------------------------
PHASE=extract
WORK="$(mktemp -d)"
safe_extract() { # archive dir
  local entries
  entries="$(tar -tvzf "$1")" || fail "无法读取归档 $(basename "$1")。"
  while IFS= read -r line; do
    case "${line:0:1}" in -|d) ;; *) fail "归档 $(basename "$1") 含有链接或特殊文件，已拒绝。";; esac
  done <<< "$entries"
  while IFS= read -r name; do
    case "$name" in /*|..|../*|*/..|*/../*) fail "归档 $(basename "$1") 含有越界路径，已拒绝。";; esac
  done < <(tar -tzf "$1")
  mkdir -p "$2"; tar -xzf "$1" -C "$2" --no-same-owner
}
safe_extract "$CLI_ARCHIVE" "$WORK/kernel"
KERNEL_SRC="$(find "$WORK/kernel" -maxdepth 2 -type f -name xmesh | head -n 1)"
[ -n "$KERNEL_SRC" ] || fail "内核归档里没有 xmesh 可执行文件。"
if [ -n "$WEB_ARCHIVE" ]; then
  safe_extract "$WEB_ARCHIVE" "$WORK/webui"
  WEB_SRC="$(dirname "$(find "$WORK/webui" -maxdepth 2 -type f -name xmesh-webui.js | head -n 1)")"
  [ -f "$WEB_SRC/xmesh-webui.js" ] && [ -f "$WEB_SRC/public/index.html" ] || fail "网页界面归档内容不完整。"
fi

PHASE=install
mkdir -p "$PREFIX/bin" "$BIN_DIR"
install -m 0755 "$KERNEL_SRC" "$PREFIX/bin/.xmesh.new.$$"
mv -f "$PREFIX/bin/.xmesh.new.$$" "$PREFIX/bin/xmesh"
ln -sfn "$PREFIX/bin/xmesh" "$BIN_DIR/xmesh"
reported="$("$PREFIX/bin/xmesh" version 2>/dev/null || true)"
[ "$reported" = "xmesh $VERSION" ] || fail "安装的 xmesh 报告版本为“${reported:-无输出}”，应为“xmesh $VERSION”。" "确认下载的是适合本机架构（$ARCH）的发布，且系统能运行它。"
say "已安装 xmesh $VERSION 到 $PREFIX/bin/xmesh（链接 $BIN_DIR/xmesh）"
if [ -n "$WEB_ARCHIVE" ]; then
  rm -rf "$PREFIX/webui.old"
  [ ! -d "$PREFIX/webui" ] || mv "$PREFIX/webui" "$PREFIX/webui.old"
  mv "$WEB_SRC" "$PREFIX/webui"
  rm -rf "$PREFIX/webui.old"
  "$NODE_BIN" "$PREFIX/webui/xmesh-webui.js" --help >/dev/null 2>&1 || fail "网页界面无法在 $NODE_BIN 上启动。" "确认 Node.js 版本不低于 22.18。"
  say "已安装网页管理界面到 $PREFIX/webui"
fi

# ---- kernel service ------------------------------------------------------------
XMESH="$PREFIX/bin/xmesh"
KERNEL_SUMMARY=""
if "$WANT_KERNEL"; then
  PHASE=kernel
  "$XMESH" setup inspect --json > "$WORK/inspect.json" 2>"$WORK/inspect.err" || fail "xmesh setup inspect 失败：$(head -c 300 "$WORK/inspect.err")"
  nodes="$(py nodes "$WORK/inspect.json")"
  if [ -n "$nodes" ]; then
    # Already set up: keep the node and its configuration, and start the new binary.
    while IFS=$'\t' read -r name running; do
      [ -n "$name" ] || continue
      if [ "$running" = true ]; then
        "$XMESH" restart --name "xmesh-$name" --json >/dev/null 2>"$WORK/restart.err" && say "已重启节点 $name 的服务以使用新版本。" \
          || warn "节点 $name 的服务重启失败：$(head -c 200 "$WORK/restart.err")（可手动 systemctl --user restart xmesh-$name）"
      else
        say "节点 $name 已存在但未运行；配置保持不变。启动：systemctl --user start xmesh-$name"
      fi
    done <<< "$nodes"
    KERNEL_SUMMARY="已有节点（配置未改动）"
  else
    [ -n "$WORKSPACE" ] && [ -n "$PERMISSION" ] && [ -n "$PURPOSE" ] && [ -n "$NODE_NAME" ] || fail "程序已安装，但没有节点配置可更新，也没有给出创建节点所需的 --workspace 和 --permission。" "加上这两个选项后重新运行本脚本。"
    py input "$PURPOSE" "$NODE_NAME" "$WORKSPACE" "$PERMISSION" > "$WORK/input.json"
    chmod 600 "$WORK/input.json"
    "$XMESH" setup plan --input "$WORK/input.json" --json > "$WORK/plan.json" 2>"$WORK/plan.err" || fail "生成设置计划失败：$(py get "$WORK/plan.json" error.message 2>/dev/null || head -c 300 "$WORK/plan.err")"
    say "设置计划："
    py get "$WORK/plan.json" data.summary | sed 's/^/  - /'
    py get "$WORK/plan.json" data.config_path | sed 's/^/  配置文件: /'
    confirm "按此计划创建节点并启动服务？" yes || fail "已取消；程序已安装，未创建节点。" "准备好后重新运行本脚本。"
    PLAN_ID="$(py get "$WORK/plan.json" data.id)"
    "$XMESH" setup apply --plan "$PLAN_ID" --json > "$WORK/apply.json" 2>"$WORK/apply.err" || fail "应用设置计划失败：$(head -c 300 "$WORK/apply.err")"
    JOB_ID="$(py get "$WORK/apply.json" data.id)"
    say "正在安装并启动内核服务…"
    ready=false; tries=0
    while [ "$tries" -lt 60 ]; do
      sleep "${XMESH_INSTALL_POLL_SECONDS:-2}"; tries=$((tries + 1))
      "$XMESH" setup status --job "$JOB_ID" --json > "$WORK/status.json" 2>/dev/null || true
      status="$(py get "$WORK/status.json" data.status 2>/dev/null || true)"
      [ "$(py get "$WORK/status.json" data.readiness.service_ready 2>/dev/null || true)" = true ] && { ready=true; break; }
      case "$status" in failed|cancelled) break;; esac
    done
    if ! "$ready"; then
      printf '内核服务没有就绪（状态 %s）：\n' "${status:-未知}" >&2
      py failed-steps "$WORK/status.json" 2>/dev/null | sed 's/^/  /' >&2 || true
      fail "内核服务安装未完成。" "排查后执行 $XMESH setup resume --job $JOB_ID --json，或运行 $XMESH doctor --json。"
    fi
    KERNEL_SUMMARY="节点 $NODE_NAME（systemd 用户服务 xmesh-$NODE_NAME）"
    say "内核服务已运行：$(py get "$WORK/plan.json" data.endpoint)"
    say "尚未连接客户端：要让 Codex 或 Claude Code 使用它，请在网页管理界面的向导里完成，或运行 $XMESH setup。"
  fi
fi

# ---- web panel service ---------------------------------------------------------
WEB_SUMMARY=""
if [ -n "$WEB_ARCHIVE" ]; then
  PHASE=webui
  args=(install --listen "$WEB_LISTEN" --xmesh-bin "$XMESH")
  [ -z "$WEB_TLS_CERT" ] || args+=(--tls-cert "$WEB_TLS_CERT" --tls-key "$WEB_TLS_KEY")
  ! "$WEB_INSECURE" || args+=(--insecure)
  [ -z "$WEB_LANG" ] || args+=(--lang "$WEB_LANG")
  [ "$LINGER" != yes ] || args+=(--linger)
  case "$KEY_SOURCE" in
    file) args+=(--key-stdin); key_input="$(head -n 1 "$WEB_KEY_FROM" | tr -d '\r')";;
    typed) args+=(--key-stdin); key_input="$TYPED_KEY";;
    *) key_input=""; ! "$WEB_REGENERATE" || args+=(--rotate-key);;
  esac
  if [ "$KEY_SOURCE" = none ]; then
    PATH="$PREFIX/bin:$PATH" "$NODE_BIN" "$PREFIX/webui/xmesh-webui.js" "${args[@]}" || fail "安装网页管理界面服务失败。" "查看上面的输出，修正后重新运行本脚本（已装的程序和节点不会受影响）。"
  else
    # A pipe, not a here-string: older bash writes here-strings to a temporary file.
    printf '%s\n' "$key_input" | PATH="$PREFIX/bin:$PATH" "$NODE_BIN" "$PREFIX/webui/xmesh-webui.js" "${args[@]}" || fail "安装网页管理界面服务失败。" "查看上面的输出，修正后重新运行本脚本。"
  fi
  scheme=http; [ -z "$WEB_TLS_CERT" ] || scheme=https
  shown="$WEB_LISTEN"; [[ "$shown" != :* ]] || shown="127.0.0.1$shown"
  WEB_SUMMARY="$scheme://$shown/（systemd 用户服务 xmesh-webui）"
fi

# ---- linger for kernel-only installs, then summary ----------------------------
if [ -z "$WEB_ARCHIVE" ] && "$WANT_KERNEL" && [ "$LINGER" = yes ]; then
  loginctl enable-linger >/dev/null 2>&1 || warn "loginctl enable-linger 失败；没有登录会话时服务不会运行。"
fi
PHASE=complete
say ""
say "== 完成 =="
say "版本:       $VERSION"
[ -z "$KERNEL_SUMMARY" ] || say "内核:       $KERNEL_SUMMARY"
[ -z "$WEB_SUMMARY" ] || say "网页界面:   $WEB_SUMMARY"
case "$LINGER" in no) say "提示: 未启用 linger，退出登录后服务会停止；可执行 loginctl enable-linger。";; esac
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) say "提示: $BIN_DIR 不在 PATH 里；把它加入 PATH，或直接使用 $XMESH。";; esac
[ -z "$WEB_SUMMARY" ] || say "管理密钥保存在 ${XDG_CONFIG_HOME:-$HOME/.config}/xmesh-webui/webui.key；忘记时用 $NODE_BIN $PREFIX/webui/xmesh-webui.js --print-key 查看。"
[ -z "$WEB_SUMMARY" ] || say "远程访问请用 SSH 隧道：ssh -L 7480:127.0.0.1:7480 <主机>，再打开 http://127.0.0.1:7480/。"
