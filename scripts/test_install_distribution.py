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
    def test_versioned_site_links_and_prompt(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp);dist=directory/'dist';dist.mkdir();manifest=fixture(dist)
            site=directory/'site'
            version_dir=build_site(ROOT,site,manifest['version'],'tzf1003/xmesh','https://tzf1003.github.io/xmesh',manifest)
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

if __name__=='__main__':unittest.main()
