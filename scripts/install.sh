#!/bin/bash
# xmesh macOS bootstrap. Run a downloaded, reviewed script with bash.
set -Eeuo pipefail
VERSION=""; CHANNEL=stable; CHANNEL_SET=false; REPO=tzf1003/xmesh
ARCH=""; ROOT=""; OFFLINE=""; DOWNLOAD_ONLY=false; ALLOW_UNSIGNED=false
JSON=false; LAUNCH=true; PHASE=arguments; CLI=""; APP=""
INSTALL_DIR="$HOME/Applications"; CACHE="${XDG_CACHE_HOME:-$HOME/Library/Caches}/xmesh/install"
usage() {
  cat <<'USAGE'
Usage: bash install.sh [--version vX.Y.Z[-beta.N|-rc.N] | --channel stable|testing]
  --arch amd64|arm64      Override detected architecture
  --repo OWNER/REPO       Release repository (default tzf1003/xmesh)
  --install-dir PATH      User application directory (default ~/Applications)
  --root PATH             State root included in setup guidance
  --cache PATH            Persistent verified-download cache
  --offline DIR           Read manifest.json and the matching DMG from DIR
  --download-only         Download and verify; return package path
  --allow-unsigned        Explicitly permit testing packages lacking release signatures
  --no-launch             Install and report paths; defer opening the application
  --json                  Emit newline-delimited JSON events
  --help                  Show this help
USAGE
}
# macOS includes JavaScript for Automation, so bootstrap needs no Python/Node runtime.
jxa() {
  /usr/bin/osascript -l JavaScript - "$@" <<'JXA'
ObjC.import('Foundation');
function read(p) {
  const v = $.NSString.stringWithContentsOfFileEncodingError(p, $.NSUTF8StringEncoding, null);
  if (!v) throw Error('cannot read JSON file');
  return JSON.parse(ObjC.unwrap(v));
}
function tag(v) {
  const m = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(beta|rc)\.(0|[1-9][0-9]*))?$/.exec(v);
  if (!m) throw Error('invalid version');
  return [Number(m[1]), Number(m[2]), Number(m[3]), m[4] ? (m[4] === 'rc' ? 1 : 0) : 2, Number(m[5] || 0)];
}
function run(a) {
  if (a[0] === 'event') return JSON.stringify({ok:a[1]==='true',phase:a[2],message:a[3],next_action:a[4],version:a[5],cli_path:a[6],package_path:a[7]});
  if (a[0] === 'resolve') {
    const candidates = read(a[1]).filter(r => {
      if(r.draft || typeof r.tag_name !== 'string') return false;
      try { const t=tag(r.tag_name); return a[2] === 'stable' ? t[3]===2 : t[3]!==2; } catch (_) {return false;}
    });
    candidates.sort((x,y)=>{const p=tag(x.tag_name),q=tag(y.tag_name);for(let i=0;i<p.length;i++)if(p[i]!==q[i])return q[i]-p[i];return 0;});
    if (!candidates.length) throw Error('no release in requested channel; pin --version');
    return candidates[0].tag_name;
  }
  if (a[0] === 'manifest') {
    const m=read(a[1]); tag(m.version);
    const expectedChannel=tag(m.version)[3]===2 ? 'stable':'testing';
    if(m.schema_version!==1 || m.version!==a[2] && a[2]!=='' || m.repository!==a[3] || m.channel!==expectedChannel || m.docs_version!==m.version) throw Error('manifest identity mismatch');
    const entries=m.assets.filter(x=>x.os==='darwin' && x.arch===a[4] && x.kind==='desktop');
    if(entries.length!==1) throw Error('manifest platform entry missing or duplicated');
    const x=entries[0], expected='xmesh-ui-'+m.version.slice(1)+'-mac-'+(a[4]==='amd64'?'x64':'arm64')+'.dmg';
    if(x.name!==expected || x.url!=='https://github.com/'+a[3]+'/releases/download/'+m.version+'/'+x.name || !/^[a-f0-9]{64}$/.test(x.sha256) || !Number.isSafeInteger(x.size) || x.size<1) throw Error('invalid asset URL/checksum/size');
    const trust=x.trust||{}, trusted=trust.signed===true, complete=trusted && trust.notarized===true && trust.stapled===true;
    if(expectedChannel==='stable' && !complete) throw Error('stable macOS release requires signed/notarized/stapled package');
    if(!trusted && a[5]!=='true') throw Error('unsigned testing package requires --allow-unsigned');
    return [m.version,x.name,x.sha256,x.url,String(x.size),String(trusted),String(trust.stapled===true)].join('\t');
  }
  throw Error('invalid parser mode');
}
JXA
}
emit() {
  if "$JSON"; then jxa event "$1" "$PHASE" "$2" "${3:-}" "$VERSION" "$CLI" "${PACKAGE:-}";
  else printf '%s: %s\n' "$PHASE" "$2"; [ -z "${3:-}" ] || printf 'Next: %s\n' "$3"; fi
}
fail() {
  trap - ERR
  emit false "$1" "${2:-Rerun with the same arguments; verified downloads remain in the cache.}" >&1
  exit 1
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--channel|--arch|--repo|--root|--cache|--offline|--install-dir)
      [ "$#" -ge 2 ] || { printf 'Missing option value\n' >&2; exit 2; }
      case "$1" in
        --version) VERSION="$2";; --channel) CHANNEL="$2"; CHANNEL_SET=true;; --arch) ARCH="$2";;
        --repo) REPO="$2";; --root) ROOT="$2";; --cache) CACHE="$2";; --offline) OFFLINE="$2";; --install-dir) INSTALL_DIR="$2";;
      esac; shift 2;;
    --download-only) DOWNLOAD_ONLY=true; shift;; --allow-unsigned) ALLOW_UNSIGNED=true; shift;;
    --json) JSON=true; shift;; --no-launch) LAUNCH=false; shift;; --help|-h) usage; exit 0;;
    *) printf 'Unknown option: %s\n' "$1" >&2; exit 2;;
  esac
done
PHASE=platform
if [ "$(uname -s)" != Darwin ]; then
  if "$JSON"; then printf '{"ok":false,"phase":"platform","message":"This installer requires macOS","next_action":"Use install.ps1 on Windows or a Linux CLI archive."}\n'; else printf 'This installer requires macOS.\n' >&2; fi
  exit 1
fi
trap 'fail "Operation failed in phase $PHASE."' ERR
[ -n "$ARCH" ] || ARCH="$(uname -m)"
case "$ARCH" in x86_64|x64) ARCH=amd64;; aarch64) ARCH=arm64;; esac
case "$ARCH" in amd64|arm64) ;; *) fail 'Unsupported architecture.' 'Pass --arch amd64 or --arch arm64 after checking About This Mac.';; esac
[[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail 'Invalid repository.'
case "$CHANNEL" in stable|testing) ;; *) fail 'Invalid channel.' 'Use stable or testing.';; esac
if [ -n "$VERSION" ]; then
  [[ "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(beta|rc)\.(0|[1-9][0-9]*))?$ ]] || fail 'Invalid version.'
  inferred=stable; [[ "$VERSION" != *-* ]] || inferred=testing
  if "$CHANNEL_SET" && [ "$CHANNEL" != "$inferred" ]; then fail 'Version and channel disagree.'; fi
  CHANNEL="$inferred"
fi
mkdir -p "$CACHE"
fetch() { curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 --retry 3 --retry-delay 2 --connect-timeout 15 --max-time 900 "$1" -o "$2"; }
PHASE=resolve
if [ -n "$OFFLINE" ]; then
  MANIFEST="$OFFLINE/manifest.json"
else
  if [ -z "$VERSION" ]; then
    fetch "https://api.github.com/repos/$REPO/releases?per_page=100" "$CACHE/releases.json.part"
    VERSION="$(jxa resolve "$CACHE/releases.json.part" "$CHANNEL")"
  fi
  MANIFEST="$CACHE/$VERSION-manifest.json"
  fetch "https://github.com/$REPO/releases/download/$VERSION/manifest.json" "$MANIFEST.part"
  mv "$MANIFEST.part" "$MANIFEST"
fi
PHASE=manifest
fields="$(jxa manifest "$MANIFEST" "$VERSION" "$REPO" "$ARCH" "$ALLOW_UNSIGNED")"
IFS=$'\t' read -r VERSION NAME HASH URL SIZE TRUSTED STAPLED <<< "$fields"
inferred=stable; [[ "$VERSION" != *-* ]] || inferred=testing
if "$CHANNEL_SET" && [ "$CHANNEL" != "$inferred" ]; then fail 'Offline version and channel disagree.'; fi
PACKAGE="$CACHE/$NAME"
[ -z "$OFFLINE" ] || PACKAGE="$OFFLINE/$NAME"
if [ "$TRUSTED" != true ]; then emit true 'Testing package is explicitly unsigned; system trust policy still applies.'; fi
valid_package() { [ -f "$PACKAGE" ] && [ "$(shasum -a 256 "$PACKAGE" | cut -d ' ' -f 1)" = "$HASH" ] && [ "$(stat -f %z "$PACKAGE")" = "$SIZE" ]; }
PHASE=download
if ! valid_package; then
  [ -z "$OFFLINE" ] || fail 'Offline package checksum or size mismatch.' 'Obtain the matching package and manifest from the same release.'
  fetch "$URL" "$PACKAGE.part"
  PHASE=checksum
  [ "$(shasum -a 256 "$PACKAGE.part" | cut -d ' ' -f 1)" = "$HASH" ] && [ "$(stat -f %z "$PACKAGE.part")" = "$SIZE" ] || fail 'Downloaded package checksum or size mismatch.' 'Delete the .part download and retry; keep the matching release manifest.'
  mv "$PACKAGE.part" "$PACKAGE"
fi
PHASE=checksum
valid_package || fail 'Package verification failed.'
emit true 'Release package checksum and size verified.'
if "$DOWNLOAD_ONLY"; then PHASE=complete; emit true 'Package ready for offline transfer.'; exit 0; fi
PHASE=trust
MOUNT="$(mktemp -d "$CACHE/mount.XXXXXX")"
cleanup() {
  if [ -n "${STAGE:-}" ] && [ -d "$STAGE" ]; then rm -rf "$STAGE"; fi
  hdiutil detach "$MOUNT" -quiet >/dev/null 2>&1 || true
  rmdir "$MOUNT" 2>/dev/null || true
}
trap cleanup EXIT
hdiutil attach "$PACKAGE" -mountpoint "$MOUNT" -nobrowse -readonly -quiet >&2
SOURCE="$MOUNT/xmesh.app"
[ -d "$SOURCE" ] || fail 'DMG does not contain xmesh.app.'
if [ "$TRUSTED" = true ]; then
  codesign --verify --deep --strict "$SOURCE" >&2
  if [ "$STAPLED" = true ]; then xcrun stapler validate "$SOURCE" >&2; fi
fi
spctl --assess --type execute --verbose=2 "$SOURCE" >&2 || fail 'macOS did not accept this application.' 'Use a signed release or your organization’s approved system authorization procedure, then rerun. The verified DMG is retained.'
PHASE=install
mkdir -p "$INSTALL_DIR"
APP="$INSTALL_DIR/xmesh.app"; CLI="$APP/Contents/Resources/bin/xmesh"
STAGE="$(mktemp -d "$INSTALL_DIR/.xmesh-install.XXXXXX")"
ditto "$SOURCE" "$STAGE/xmesh.app"
PHASE=verify
STAGED_CLI="$STAGE/xmesh.app/Contents/Resources/bin/xmesh"
[ -x "$STAGED_CLI" ] || fail 'Bundled CLI missing from application.'
[ "$("$STAGED_CLI" version)" = "xmesh $VERSION" ] || fail 'Staged CLI version differs from release.' 'Obtain the matching application and manifest from the same release; the existing application is preserved.'
PHASE=install
BACKUP=""
if [ -e "$APP" ]; then BACKUP="$INSTALL_DIR/xmesh.previous.$(date +%Y%m%d%H%M%S).app"; mv "$APP" "$BACKUP"; fi
if ! mv "$STAGE/xmesh.app" "$APP"; then
  [ -z "$BACKUP" ] || mv "$BACKUP" "$APP"
  fail 'Application replacement failed.' 'Quit xmesh, check directory permissions, and rerun.'
fi
rmdir "$STAGE"
PHASE=verify
[ "$("$CLI" version)" = "xmesh $VERSION" ] || fail 'Installed CLI version differs from release.' 'Restore the previous application or rerun with the intended version.'
if "$LAUNCH"; then PHASE=launch; open "$APP" >&2; fi
PHASE=complete
NEXT='Continue in the setup wizard; use setup inspect --json to inspect state.'
[ -z "$ROOT" ] || NEXT="$NEXT State root: $ROOT (pass --root to each setup subcommand)."
emit true "Installed $VERSION. Bundled CLI: $CLI" "$NEXT"
