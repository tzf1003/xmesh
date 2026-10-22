#!/usr/bin/env python3
"""Offline tests of release selection, trust gates, rendered links and bootstrap checks."""
import copy
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from urllib.parse import urlsplit, unquote
import zipfile

from release_manifest import build, bundles, channel_for, validate
from build_install_site import build as build_site, render_agent_documents, markdown_body

ROOT = Path(__file__).resolve().parent.parent

def fixture(directory, version="v1.2.3-beta.2", signed=False):
    for osname in ("darwin", "windows"):
        for arch in ("amd64", "arm64"):
            label, ext = ("mac", "dmg") if osname == "darwin" else ("win", "exe")
            machine = "x64" if arch == "amd64" else arch
            (directory / f"xmesh-ui-{version[1:]}-{label}-{machine}.{ext}").write_bytes(f"fixture {osname}/{arch}".encode())
            (directory / f"signing-{osname}-{arch}.json").write_text(json.dumps({"signed":signed,"notarized":signed,"stapled":signed}), encoding="utf-8")
    manifest = build(directory, version, "tzf1003/xmesh")
    (directory / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    return manifest

class ManifestTests(unittest.TestCase):
    def test_semver_channel(self):
        for tag in ("v0.1.0-beta.1", "v2.0.0-rc.3"):
            self.assertEqual(channel_for(tag), "testing")
        self.assertEqual(channel_for("v1.2.3"), "stable")
        for tag in ("v01.2.3", "1.2.3", "v1.2.3-dev", "v1.2.3-beta.01", "../v1.2.3"):
            with self.assertRaises(ValueError): channel_for(tag)

    def test_stable_trust_and_native_archives(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            with self.assertRaises(ValueError): fixture(directory, "v1.2.3")
            m = fixture(directory, "v1.2.3", True)
            (directory / "xmesh-v1.2.3-windows-amd64.zip").write_bytes(b"unsigned")
            self.assertEqual(len(build(directory,"v1.2.3","tzf1003/xmesh")["assets"]), 4)
            for field in ("signed", "notarized", "stapled"):
                changed = copy.deepcopy(m)
                changed["assets"][0]["trust"][field] = False
                with self.assertRaises(ValueError): validate(changed)

    def test_manifest_rejects_identity_and_asset_tampering(self):
        with tempfile.TemporaryDirectory() as temp:
            manifest = fixture(Path(temp))
            for key, value in (("schema_version", 2),("channel","stable"),("docs_version","v9.0.0")):
                changed = copy.deepcopy(manifest);changed[key]=value
                with self.assertRaises(ValueError):validate(changed)
            for key, value in (("name","../installer.exe"),("url","https://example.org/file"),("sha256","bad"),("size",0)):
                changed=copy.deepcopy(manifest);changed["assets"][0][key]=value
                with self.assertRaises(ValueError):validate(changed)
            changed=copy.deepcopy(manifest);changed["assets"].append(changed["assets"][0])
            with self.assertRaises(ValueError):validate(changed)

    def test_offline_bundle_has_matching_package_docs_and_scripts(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp);manifest=fixture(directory)
            bundles(directory,manifest,ROOT)
            for asset in manifest["assets"]:
                archive=directory/f"xmesh-offline-{manifest['version']}-{asset['os']}-{asset['arch']}.zip"
                with zipfile.ZipFile(archive) as bundle:
                    for name in (asset["name"],"manifest.json","scripts/install.sh","scripts/install.ps1","install.sh","install.ps1","docs/agent/setup.md","docs/agent/setup.html","docs/agent/README.html","docs/agent/recovery.html","llms.txt","docs/operations.md","docs/validation/real-environment-runbook.md"):
                        self.assertIn(name,bundle.namelist())
                    self.assertEqual(json.loads(bundle.read("manifest.json")),manifest)
                    for name in bundle.namelist():
                        if not name.startswith("docs/agent/") or not name.endswith(".html"): continue
                        parser=Links();parser.feed(bundle.read(name).decode("utf-8"))
                        for link in parser.links:
                            parts=urlsplit(link)
                            if parts.scheme or not parts.path: continue
                            path=Path(name).parent / unquote(parts.path)
                            self.assertIn(path.as_posix(),bundle.namelist(),f"{name}: {link}")

class Links(HTMLParser):
    def __init__(self): super().__init__();self.links=[]
    def handle_starttag(self,tag,attrs):
        values=dict(attrs)
        for key in ("href","src"):
            if key in values:self.links.append(values[key])

class SiteTests(unittest.TestCase):
    def test_rebuilds_historical_bundle_before_linux_installer(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source = directory / 'source'
            (source / 'scripts').mkdir(parents=True)
            shutil.copytree(ROOT / 'docs/agent', source / 'docs/agent')
            shutil.copytree(ROOT / 'docs/site', source / 'docs/site')
            shutil.copy2(ROOT / 'llms.txt', source / 'llms.txt')
            for name in ('install.sh', 'install.ps1'):
                shutil.copy2(ROOT / 'scripts' / name, source / 'scripts' / name)
            manifest = fixture(directory)
            version_dir = build_site(source, directory / 'site', manifest['version'],
                                     'tzf1003/xmesh', 'https://tzf1003.github.io/xmesh', manifest)
            self.assertTrue((version_dir / 'index.html').is_file())
            self.assertEqual(json.loads((version_dir / 'manifest.json').read_text()), manifest)
            for name in ('install.sh', 'install.ps1'):
                self.assertEqual((version_dir / name).read_bytes(), (source / 'scripts' / name).read_bytes())
            self.assertFalse((version_dir / 'install-linux.sh').exists())

    def test_versioned_site_links_and_prompt(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp);dist=directory/'dist';dist.mkdir();manifest=fixture(dist)
            site=directory/'site'
            version_dir=build_site(ROOT,site,manifest['version'],'tzf1003/xmesh','https://tzf1003.github.io/xmesh',manifest)
            self.assertEqual((version_dir/'install-linux.sh').read_bytes(), (ROOT/'scripts/install-linux.sh').read_bytes())
            for page in site.rglob('*.html'):
                text=page.read_text(encoding="utf-8");self.assertNotIn('@@',text)
                parser=Links();parser.feed(text)
                for link in parser.links:
                    parts=urlsplit(link)
                    if parts.scheme or not parts.path:continue
                    self.assertTrue((page.parent/unquote(parts.path)).is_file(),f'{page}: {link}')
            prompt=(version_dir/'prompt.txt').read_text(encoding="utf-8")
            self.assertIn('/versions/v1.2.3-beta.2/agent/README.md',prompt)
            self.assertIn('plan/job ID',prompt)
            self.assertIn(manifest['version'],(version_dir/'agent/install.md').read_text(encoding="utf-8"))
            for raw in (version_dir/'agent').glob('*.md'):
                rendered=raw.with_suffix('.html').read_text(encoding="utf-8")
                self.assertIn('Raw Markdown',rendered)
                self.assertIn('href="'+raw.name+'"',rendered)
                self.assertIn('<h1 ',rendered)
            self.assertIn('<table>',(version_dir/'agent/recovery.html').read_text(encoding="utf-8"))
            self.assertIn('<pre><code class="language-json">',(version_dir/'agent/setup.html').read_text(encoding="utf-8"))
            self.assertIn('agent/install.html',(site/'index.html').read_text(encoding="utf-8"))
            for navigation in (site/'llms.txt',version_dir/'llms.txt'):
                for link in re.findall(r'\]\(([^)]+)\)',navigation.read_text(encoding="utf-8")):
                    if not link.startswith('https:'):self.assertTrue((navigation.parent/link).is_file())
            self.assertNotIn('arch.value="amd64"', (ROOT/'docs/site/install.js').read_text(encoding="utf-8").split('if(navigator.userAgentData')[0])

    def test_human_documents_escape_html_and_map_local_links(self):
        with tempfile.TemporaryDirectory() as temp:
            source=Path(temp)
            (source/'README.md').write_text('# Guide 指南 <script>\n\n[Setup](setup.md#start) and [external](https://example.org/file.md).\n\n<script>alert("x")</script>\n\n[unsafe](javascript:alert)\n\n```html\n<img src=x onerror=alert(1)>\n```\n\n| Field | Value |\n| --- | --- |\n| **Name** | `a<b` |\n\n- First\n- Second\n',encoding='utf-8')
            (source/'setup.md').write_text('# Setup\n\n## Start\n',encoding='utf-8')
            documents=render_agent_documents(source,'v1.2.3-beta.2')
            rendered=documents['README.html']
            self.assertIn('指南',rendered)
            self.assertIn('href="setup.html#start"',rendered)
            self.assertIn('href="https://example.org/file.md"',rendered)
            self.assertIn('&lt;script&gt;',rendered)
            self.assertIn('&lt;img src=x onerror=alert(1)&gt;',rendered)
            self.assertNotIn('<script>',rendered)
            self.assertNotIn('href="javascript:',rendered)
            self.assertIn('<table>',rendered)
            self.assertIn('<strong>Name</strong>',rendered)
            self.assertIn('<code>a&lt;b</code>',rendered)
            self.assertIn('<ul>',rendered)
            self.assertIn('href="README.md"',rendered)

    def test_site_and_offline_docs_use_utf8_with_legacy_default(self):
        original_read=Path.read_text
        original_write=Path.write_text
        def legacy_read(path,encoding=None,errors=None):
            return original_read(path,encoding=encoding or 'cp1252',errors=errors)
        def legacy_write(path,data,encoding=None,errors=None,newline=None):
            return original_write(path,data,encoding=encoding or 'cp1252',errors=errors,newline=newline)
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp)
            with mock.patch.object(Path,'read_text',legacy_read), mock.patch.object(Path,'write_text',legacy_write):
                manifest=fixture(directory)
                version_dir=build_site(ROOT,directory/'site',manifest['version'],'tzf1003/xmesh','https://tzf1003.github.io/xmesh',manifest)
                bundles(directory,manifest,ROOT)
                self.assertIn('复制提示词',(directory/'site/index.html').read_text(encoding='utf-8'))
                self.assertIn('请帮助我安装',(version_dir/'prompt.txt').read_text(encoding='utf-8'))
                self.assertIn('Raw Markdown',(version_dir/'agent/README.html').read_text(encoding='utf-8'))

    def test_site_requires_manifest_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp);manifest=fixture(directory)
            with self.assertRaises(ValueError):build_site(ROOT,directory/'site','v9.9.9','tzf1003/xmesh','https://example.org',manifest)

class BootstrapTests(unittest.TestCase):
    def test_mac_staged_kernel_is_verified_before_replacing_existing_application(self):
        if platform.system() != 'Darwin': self.skipTest('macOS JXA is required')
        for version in (None, 'v9.9.9', 'v1.2.3-beta.2'):
            with self.subTest(version=version), tempfile.TemporaryDirectory() as temp:
                directory=Path(temp);fixture(directory)
                source=directory/'source/xmesh.app';kernel=source/'Contents/Resources/bin/xmesh'
                kernel.parent.mkdir(parents=True)
                if version:
                    kernel.write_text('#!/bin/sh\nprintf "xmesh '+version+'\\n"\n',encoding='utf-8');kernel.chmod(0o755)
                install=directory/'Applications with spaces';existing=install/'xmesh.app';existing.mkdir(parents=True)
                (existing/'original.txt').write_text('existing application',encoding='utf-8')
                commands=directory/'commands';commands.mkdir()
                scripts={
                    'spctl':'#!/bin/sh\nexit 0\n',
                    'hdiutil':f'#!{sys.executable}\nimport os,sys,shutil\nfrom pathlib import Path\nif sys.argv[1]=="attach":\n dest=Path(sys.argv[sys.argv.index("-mountpoint")+1])/"xmesh.app"\n shutil.copytree(os.environ["XMESH_TEST_SOURCE_APP"],dest)\nelif sys.argv[1]=="detach":\n shutil.rmtree(Path(sys.argv[2])/"xmesh.app")\n',
                    'ditto':f'#!{sys.executable}\nimport sys,shutil\nshutil.copytree(sys.argv[1],sys.argv[2])\n',
                }
                for name,content in scripts.items():
                    command=commands/name;command.write_text(content,encoding='utf-8');command.chmod(0o755)
                env=dict(os.environ,PATH=str(commands)+os.pathsep+os.environ['PATH'],XMESH_TEST_SOURCE_APP=str(source))
                result=subprocess.run(['bash',str(ROOT/'scripts/install.sh'),'--offline',str(directory),'--cache',str(directory/'cache'),'--install-dir',str(install),'--arch','arm64','--allow-unsigned','--no-launch','--json'],capture_output=True,text=True,env=env)
                events=[json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
                self.assertTrue(events,result.stdout+result.stderr)
                backups=list(install.glob('xmesh.previous.*.app'))
                if version=='v1.2.3-beta.2':
                    self.assertEqual(result.returncode,0,result.stdout+result.stderr)
                    self.assertEqual(events[-1]['phase'],'complete')
                    self.assertEqual(len(backups),1)
                    self.assertEqual((backups[0]/'original.txt').read_text(encoding='utf-8'),'existing application')
                    actual=subprocess.run([str(existing/'Contents/Resources/bin/xmesh'),'version'],capture_output=True,text=True,check=True).stdout.strip()
                    self.assertEqual(actual,'xmesh v1.2.3-beta.2')
                else:
                    self.assertNotEqual(result.returncode,0)
                    self.assertEqual(events[-1]['phase'],'verify')
                    self.assertEqual((existing/'original.txt').read_text(encoding='utf-8'),'existing application')
                    self.assertEqual(backups,[])
                self.assertEqual(list(install.glob('.xmesh-install.*')),[])

    def invoke(self,directory,osname,*args):
        env=dict(os.environ)
        if osname=='darwin':
            if platform.system()!='Darwin':self.skipTest('macOS JXA is required')
            command=['bash',str(ROOT/'scripts/install.sh'),'--offline',str(directory),'--cache',str(directory/'cache'),'--arch','arm64','--json',*args]
        else:
            if not shutil.which('pwsh'):self.skipTest('pwsh is required')
            env['OS']='Windows_NT'
            command=['pwsh','-NoProfile','-File',str(ROOT/'scripts/install.ps1'),'-Offline',str(directory),'-Cache',str(directory/'cache'),'-InstallDir',str(directory/'app'),'-Arch','arm64','-Json',*args]
        result=subprocess.run(command,capture_output=True,text=True,env=env)
        events=[json.loads(line) for line in result.stdout.splitlines() if line.startswith('{')]
        self.assertTrue(events, result.stdout+result.stderr)
        return result,events

    def test_offline_validation_and_failure_phases(self):
        for osname in ('darwin','windows'):
            with self.subTest(os=osname),tempfile.TemporaryDirectory() as temp:
                directory=Path(temp);manifest=fixture(directory)
                download='--download-only' if osname=='darwin' else '-DownloadOnly'
                allow='--allow-unsigned' if osname=='darwin' else '-AllowUnsigned'
                result,events=self.invoke(directory,osname,download)
                self.assertNotEqual(result.returncode,0);self.assertEqual(events[-1]['phase'],'manifest')
                result,events=self.invoke(directory,osname,download,allow)
                self.assertEqual(result.returncode,0,result.stderr+result.stdout);self.assertEqual(events[-1]['phase'],'complete')
                selected=next(x for x in manifest['assets'] if x['os']==osname and x['arch']=='arm64')
                (directory/selected['name']).write_bytes(b'corrupted')
                result,events=self.invoke(directory,osname,download,allow)
                self.assertNotEqual(result.returncode,0);self.assertEqual(events[-1]['phase'],'download')

    def test_signed_testing_download_needs_no_unsigned_opt_in(self):
        for osname in ('darwin','windows'):
            with self.subTest(os=osname),tempfile.TemporaryDirectory() as temp:
                directory=Path(temp);manifest=fixture(directory,signed=True)
                for asset in manifest['assets']:
                    asset['trust']['notarized']=False
                    asset['trust']['stapled']=False
                (directory/'manifest.json').write_text(json.dumps(manifest), encoding="utf-8")
                options=['--download-only'] if osname=='darwin' else ['-DownloadOnly']
                result,events=self.invoke(directory,osname,*options)
                self.assertEqual(result.returncode,0,result.stderr+result.stdout)
                self.assertEqual(events[-1]['phase'],'complete')

    def test_pinned_version_and_channel_mismatch(self):
        for osname in ('darwin','windows'):
            with self.subTest(os=osname),tempfile.TemporaryDirectory() as temp:
                directory=Path(temp);fixture(directory)
                options=['--download-only','--allow-unsigned','--version','v1.2.3'] if osname=='darwin' else ['-DownloadOnly','-AllowUnsigned','-Version','v1.2.3']
                result,events=self.invoke(directory,osname,*options)
                self.assertNotEqual(result.returncode,0);self.assertEqual(events[-1]['phase'],'manifest')
                options=['--download-only','--allow-unsigned','--channel','stable'] if osname=='darwin' else ['-DownloadOnly','-AllowUnsigned','-Channel','stable']
                result,events=self.invoke(directory,osname,*options)
                self.assertNotEqual(result.returncode,0);self.assertEqual(events[-1]['phase'],'manifest')

    def test_invalid_asset_url_refused_before_download(self):
        for osname in ('darwin','windows'):
            with self.subTest(os=osname),tempfile.TemporaryDirectory() as temp:
                directory=Path(temp);manifest=fixture(directory)
                for asset in manifest['assets']:asset['url']='https://example.org/installer'
                (directory/'manifest.json').write_text(json.dumps(manifest), encoding="utf-8")
                options=['--download-only','--allow-unsigned'] if osname=='darwin' else ['-DownloadOnly','-AllowUnsigned']
                result,events=self.invoke(directory,osname,*options)
                self.assertNotEqual(result.returncode,0);self.assertEqual(events[-1]['phase'],'manifest')


LINUX_VERSION = "v1.2.3-beta.2"
KEY = "typed-secret-key-0123456789"

FAKE_XMESH = """#!/bin/bash
# Stands in for the kernel: records calls and answers the `setup` protocol.
D="$FAKE_DIR"; echo "$*" >> "$D/calls.log"
case "$1 $2" in
  "version "*) echo "xmesh @VERSION@";;
  "setup inspect")
    if [ -f "$D/node" ]; then printf '{"ok":true,"data":{"nodes":[{"name":"%s","running":%s}]}}\\n' "$(cat "$D/node")" "${FAKE_NODE_RUNNING:-true}"
    else echo '{"ok":true,"data":{"nodes":[]}}'; fi;;
  "setup plan")
    while [ "$#" -gt 0 ]; do [ "$1" = --input ] && cp "$2" "$D/plan-input.json"; shift; done
    echo '{"ok":true,"data":{"id":"plan123","config_path":"/cfg/xmesh.yaml","endpoint":"http://127.0.0.1:8080/mcp","summary":["workspace ok","permission noted"]}}';;
  "setup apply") echo '{"ok":true,"data":{"id":"job456"}}'; echo new-node > "$D/node";;
  "setup status")
    if [ -f "$D/fail-status" ]; then echo '{"ok":true,"data":{"status":"failed","readiness":{"service_ready":false},"steps":[{"id":"install","status":"failed","message":"unit refused","next_action":"check systemd"}],"next_action":"run doctor"}}'
    else echo '{"ok":true,"data":{"status":"waiting_user","readiness":{"service_ready":true}}}'; fi;;
  "restart "*) echo '{"ok":true}';;
  *) echo "unexpected: $*" >&2; exit 9;;
esac
"""

FAKE_NODE = """#!/bin/bash
# Stands in for node: reports a version, and records how the panel installer is called.
D="$FAKE_DIR"
if [ "$1" = -p ]; then echo "${FAKE_NODE_VERSION:-22.20.0}"; exit 0; fi
if [ "$2" = --help ]; then exit 0; fi
echo "$*" >> "$D/node.log"
for a in "$@"; do [ "$a" = --key-stdin ] && cat > "$D/node.stdin"; done
echo "fake panel installed"
"""

def tar_dir(archive, top, files):
    import io, tarfile
    with tarfile.open(archive, "w:gz") as bundle:
        for name, content, mode in files:
            data = content.encode("utf-8")
            info = tarfile.TarInfo(f"{top}/{name}" if top else name)
            info.size = len(data); info.mode = mode
            bundle.addfile(info, io.BytesIO(data))

@unittest.skipUnless(platform.system() == "Linux" and shutil.which("bash") and shutil.which("python3"), "the Linux installer needs Linux, bash and python3")
class LinuxInstallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.dist = self.root / "dist"; self.dist.mkdir()
        self.state = self.root / "fake"; self.state.mkdir()
        self.home = self.root / "home"; self.home.mkdir()
        self.commands = self.root / "commands"; self.commands.mkdir()
        self.workspace = self.root / "work"; self.workspace.mkdir()
        self.build()
        for name, content in {
            "node": FAKE_NODE,
            "systemctl": '#!/bin/bash\necho "systemctl $*" >> "$FAKE_DIR/systemctl.log"\n[ "$FAKE_NO_SYSTEMD" = 1 ] && [ "$2" = show-environment ] && exit 1\nexit 0\n',
            "loginctl": '#!/bin/bash\necho "loginctl $*" >> "$FAKE_DIR/loginctl.log"\n[ "$1" = show-user ] && echo "${FAKE_LINGER:-no}"\nexit 0\n',
        }.items():
            f = self.commands / name; f.write_text(content, encoding="utf-8"); f.chmod(0o755)

    def build(self, kernel_extra=(), version=LINUX_VERSION):
        kernel = [("xmesh", FAKE_XMESH.replace("@VERSION@", version), 0o755), *kernel_extra]
        for arch in ("amd64", "arm64"):
            tar_dir(self.dist / f"xmesh-{version}-linux-{arch}.tar.gz", f"xmesh-{version}-linux-{arch}", kernel)
        top = f"xmesh-webui-{version}-linux"
        tar_dir(self.dist / f"{top}.tar.gz", top, [("xmesh-webui.js", "// fake\n", 0o755), ("public/index.html", "<html></html>", 0o644), ("VERSION", version[1:] + "\n", 0o644)])
        manifest = fixture(self.dist, version)
        self.manifest = manifest

    def env(self, **extra):
        env = dict(os.environ, HOME=str(self.home), FAKE_DIR=str(self.state), XMESH_INSTALL_POLL_SECONDS="0",
                   PATH=str(self.commands) + os.pathsep + os.environ["PATH"], XDG_CONFIG_HOME=str(self.home / ".config"),
                   XDG_DATA_HOME=str(self.home / ".local/share"), XDG_CACHE_HOME=str(self.home / ".cache"))
        env.pop("XMESH_BIN", None)
        env.update(extra)
        return env

    def run_script(self, *args, stdin=None, **env):
        command = ["bash", str(ROOT / "scripts/install-linux.sh"), "--offline", str(self.dist), "--version", LINUX_VERSION, "--arch", "amd64", *args]
        return subprocess.run(command, capture_output=True, text=True, env=self.env(**env), input=stdin if stdin is not None else "", timeout=60)

    @property
    def prefix(self): return self.home / ".local/share/xmesh"

    def calls(self, name="calls.log"):
        file = self.state / name
        return file.read_text(encoding="utf-8").splitlines() if file.exists() else []

    def keyfile(self):
        file = self.root / "web.key"; file.write_text(KEY + "\n", encoding="utf-8"); return file

    def test_manifest_lists_the_web_panel_archive(self):
        webui = [a for a in self.manifest["assets"] if a["kind"] == "webui"]
        self.assertEqual(len(webui), 1)
        self.assertEqual((webui[0]["os"], webui[0]["arch"], webui[0]["name"]), ("linux", "any", f"xmesh-webui-{LINUX_VERSION}-linux.tar.gz"))
        self.assertEqual(len([a for a in self.manifest["assets"] if a["kind"] == "cli" and a["os"] == "linux"]), 2)
        for field, value in (("os", "darwin"), ("arch", "amd64")):
            changed = copy.deepcopy(self.manifest)
            next(a for a in changed["assets"] if a["kind"] == "webui")[field] = value
            with self.assertRaises(ValueError): validate(changed)

    def test_installs_kernel_and_web_panel_as_two_services(self):
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", "--node-name", "box",
                                 "--web-key-from", str(self.keyfile()), "--linger")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        xmesh = self.prefix / "bin/xmesh"
        self.assertTrue(os.access(xmesh, os.X_OK))
        self.assertEqual(os.readlink(self.home / ".local/bin/xmesh"), str(xmesh))
        self.assertTrue((self.prefix / "webui/xmesh-webui.js").is_file())
        self.assertTrue((self.prefix / "webui/public/index.html").is_file())
        # the kernel goes through the supported setup flow, with exactly the access that was asked for
        self.assertEqual([c.split(" --")[0] for c in self.calls() if c.startswith("setup")], ["setup inspect", "setup plan", "setup apply", "setup status"])
        plan = json.loads((self.state / "plan-input.json").read_text(encoding="utf-8"))
        self.assertEqual((plan["purpose"], plan["node_name"], plan["workspace"], plan["permission"], plan["clients"]), ("local", "box", str(self.workspace), "readonly", []))
        # the panel is installed by its own `install` command, with the key on stdin only
        line = self.calls("node.log")[0]
        self.assertIn(f"{self.prefix}/webui/xmesh-webui.js install --listen 127.0.0.1:7480 --xmesh-bin {xmesh} --linger --key-stdin", line)
        self.assertEqual((self.state / "node.stdin").read_text(encoding="utf-8").strip(), KEY)
        self.assertNotIn(KEY, line)
        self.assertNotIn(KEY, result.stdout + result.stderr)
        self.assertIn("xmesh-webui", result.stdout)

    def test_a_generated_key_is_left_to_the_panel(self):
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "development")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        line = self.calls("node.log")[0]
        self.assertNotIn("--key-stdin", line)
        self.assertNotIn("--rotate-key", line)
        self.assertFalse((self.state / "node.stdin").exists())

    def test_non_interactive_runs_must_name_the_access_they_grant(self):
        for args in ([], ["--workspace", str(self.workspace)], ["--permission", "development"]):
            with self.subTest(args=args):
                result = self.run_script("--yes", *args)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("--workspace 和 --permission", result.stderr)
                self.assertFalse(self.prefix.exists(), "nothing is installed before the questions are settled")
                self.assertEqual(self.calls(), [])

    def test_an_existing_node_is_kept_and_restarted_without_questions(self):
        config = self.home / ".config/xmesh/old/xmesh.yaml"
        config.parent.mkdir(parents=True); config.write_text("node:\n  name: old\n", encoding="utf-8")
        (self.state / "node").write_text("old\n", encoding="utf-8")
        result = self.run_script("--yes", "--no-webui")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual([c.split(" --")[0] for c in self.calls() if not c.startswith("version")], ["setup inspect", "restart"])
        self.assertIn("restart --name xmesh-old", " ".join(self.calls()))
        self.assertEqual(self.calls("node.log"), [])
        self.assertEqual(config.read_text(encoding="utf-8"), "node:\n  name: old\n")

    def test_a_stopped_existing_node_is_not_started_behind_your_back(self):
        (self.home / ".config/xmesh/old").mkdir(parents=True)
        (self.home / ".config/xmesh/old/xmesh.yaml").write_text("x", encoding="utf-8")
        (self.state / "node").write_text("old\n", encoding="utf-8")
        result = self.run_script("--yes", "--no-webui", FAKE_NODE_RUNNING="false")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(c.startswith("restart") for c in self.calls()))
        self.assertIn("systemctl --user start xmesh-old", result.stdout)

    def test_a_kernel_that_does_not_come_up_is_reported_with_its_next_step(self):
        (self.state / "fail-status").write_text("", encoding="utf-8")
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", "--no-webui")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("install: failed unit refused check systemd", result.stderr)
        self.assertIn("setup resume --job job456", result.stderr)
        self.assertEqual(self.calls("node.log"), [])

    def test_node_must_be_recent_enough_for_the_panel_unless_it_is_skipped(self):
        for version in ("20.11.0", "22.17.9"):
            result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", FAKE_NODE_VERSION=version)
            self.assertNotEqual(result.returncode, 0, version)
            self.assertIn("22.18", result.stderr)
            self.assertFalse(self.prefix.exists())
        ok = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", FAKE_NODE_VERSION="22.18.0")
        self.assertEqual(ok.returncode, 0, ok.stdout + ok.stderr)
        (self.root / "nonode").mkdir()
        skipped = self.run_script("--yes", "--no-webui", "--workspace", str(self.workspace), "--permission", "readonly", PATH=f"{self.commands}:{os.environ['PATH']}", FAKE_NODE_VERSION="14.0.0")
        self.assertEqual(skipped.returncode, 0, skipped.stdout + skipped.stderr)

    def test_without_a_systemd_user_session_nothing_is_installed(self):
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", FAKE_NO_SYSTEMD="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("systemd 用户会话", result.stderr)
        self.assertFalse(self.prefix.exists())

    def test_the_panel_needs_tls_or_an_explicit_choice_off_localhost(self):
        base = ["--yes", "--workspace", str(self.workspace), "--permission", "readonly"]
        refused = self.run_script(*base, "--web-listen", "0.0.0.0:7480")
        self.assertNotEqual(refused.returncode, 0)
        self.assertIn("没有 TLS", refused.stderr)
        self.assertFalse(self.prefix.exists())
        cert = self.root / "c.pem"; key = self.root / "c.key"
        cert.write_text("x"); key.write_text("x")
        tls = self.run_script(*base, "--web-listen", "0.0.0.0:7480", "--web-tls-cert", str(cert), "--web-tls-key", str(key))
        self.assertEqual(tls.returncode, 0, tls.stdout + tls.stderr)
        self.assertIn(f"--tls-cert {cert} --tls-key {key}", self.calls("node.log")[0])
        self.assertIn("https://0.0.0.0:7480/", tls.stdout)
        (self.state / "node").unlink(); (self.state / "node.log").unlink()
        shutil.rmtree(self.prefix)
        insecure = self.run_script(*base, "--web-listen", "0.0.0.0:7480", "--web-insecure")
        self.assertEqual(insecure.returncode, 0, insecure.stdout + insecure.stderr)
        self.assertIn("--insecure", self.calls("node.log")[0])

    def test_tampered_archives_and_unsafe_paths_are_refused_before_anything_is_installed(self):
        archive = self.dist / f"xmesh-{LINUX_VERSION}-linux-amd64.tar.gz"
        archive.write_bytes(archive.read_bytes() + b"x")
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("校验和", result.stderr)
        self.assertFalse(self.prefix.exists())
        # an archive whose checksum matches the manifest but that escapes its directory
        self.build(kernel_extra=[("../../escape.txt", "x", 0o644)])
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", "--no-webui")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("越界路径", result.stderr)
        self.assertFalse((self.root / "escape.txt").exists())
        self.assertFalse(self.prefix.exists())
        # a symlink could point anywhere once extracted
        import io, tarfile
        self.build()
        archive = self.dist / f"xmesh-{LINUX_VERSION}-linux-amd64.tar.gz"
        with tarfile.open(archive, "w:gz") as bundle:
            data = FAKE_XMESH.replace("@VERSION@", LINUX_VERSION).encode("utf-8")
            info = tarfile.TarInfo("top/xmesh"); info.size = len(data); info.mode = 0o755
            bundle.addfile(info, io.BytesIO(data))
            link = tarfile.TarInfo("top/link"); link.type = tarfile.SYMTYPE; link.linkname = "/etc/passwd"
            bundle.addfile(link)
        (self.dist / "manifest.json").write_text(json.dumps(build(self.dist, LINUX_VERSION, "tzf1003/xmesh")), encoding="utf-8")
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", "--no-webui")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("链接或特殊文件", result.stderr)
        self.assertFalse(self.prefix.exists())

    def test_the_installed_binary_must_report_the_release_version(self):
        self.build(version=LINUX_VERSION)
        for arch in ("amd64",):
            tar_dir(self.dist / f"xmesh-{LINUX_VERSION}-linux-{arch}.tar.gz", f"xmesh-{LINUX_VERSION}-linux-{arch}", [("xmesh", FAKE_XMESH.replace("@VERSION@", "v9.9.9"), 0o755)])
        (self.dist / "manifest.json").write_text(json.dumps(build(self.dist, LINUX_VERSION, "tzf1003/xmesh")), encoding="utf-8")
        result = self.run_script("--yes", "--workspace", str(self.workspace), "--permission", "readonly", "--no-webui")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("xmesh v9.9.9", result.stderr)

    def test_download_only_verifies_and_installs_nothing(self):
        result = self.run_script("--download-only")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(f"xmesh-{LINUX_VERSION}-linux-amd64.tar.gz", result.stdout)
        self.assertFalse(self.prefix.exists())
        self.assertEqual(self.calls(), [])

    def test_argument_errors(self):
        for args, text in ((["--permission", "root"], "development 或 readonly"), (["--purpose", "cloud"], "local 或 multi-device"),
                           (["--workspace", "relative/dir"], "绝对路径"), (["--web-tls-cert", "a.pem"], "同时给出"),
                           (["--lang", "fr"], "zh 或 en"), (["--no-kernel", "--no-webui"], "没有可安装的内容"), (["--web-listen", "nope"], "host:port")):
            with self.subTest(args=args):
                result = self.run_script("--yes", *args)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(text, result.stderr)
        self.assertEqual(self.run_script("--bogus").returncode, 2)
        self.assertEqual(self.run_script("--help").returncode, 0)

    def test_questions_are_asked_in_a_terminal_and_the_key_is_confirmed(self):
        import pty, select, time
        master, slave = pty.openpty()
        command = ["bash", str(ROOT / "scripts/install-linux.sh"), "--offline", str(self.dist), "--version", LINUX_VERSION, "--arch", "amd64"]
        process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave, env=self.env(), close_fds=True)
        os.close(slave)
        transcript = ""
        def answer(prompt, text, timeout=30):
            nonlocal transcript
            deadline = time.time() + timeout
            while prompt not in transcript:
                if time.time() > deadline: self.fail(f"no prompt {prompt!r}; got:\n{transcript}")
                if select.select([master], [], [], 0.2)[0]:
                    try: transcript += os.read(master, 4096).decode("utf-8", "replace")
                    except OSError: break
            transcript = transcript.split(prompt, 1)[1]
            os.write(master, (text + "\n").encode("utf-8"))
        try:
            answer("用途：", "")
            answer("节点名", "mybox")
            answer("工作目录", str(self.workspace))
            answer("权限", "")
            answer("监听地址", "")
            answer("管理密钥（留空", "short")
            answer("请重新输入", KEY)
            answer("再次输入以确认", "does-not-match-at-all")
            answer("两次输入不一致", KEY)
            answer("再次输入以确认", KEY)
            answer("退出登录后继续运行", "n")
            answer("按此计划创建节点", "y")
            deadline = time.time() + 30
            while process.poll() is None and time.time() < deadline:
                if select.select([master], [], [], 0.2)[0]:
                    try: transcript += os.read(master, 4096).decode("utf-8", "replace")
                    except OSError: break
            self.assertEqual(process.wait(timeout=30), 0, transcript)
        finally:
            if process.poll() is None: process.kill()
            os.close(master)
        plan = json.loads((self.state / "plan-input.json").read_text(encoding="utf-8"))
        self.assertEqual((plan["node_name"], plan["permission"], plan["purpose"]), ("mybox", "development", "local"))
        self.assertEqual((self.state / "node.stdin").read_text(encoding="utf-8").strip(), KEY)
        self.assertNotIn(KEY, transcript, "typed secrets are not echoed")
        self.assertNotIn("--linger", self.calls("node.log")[0])
        self.assertIn("未启用 linger", transcript)

if __name__=='__main__':unittest.main()
