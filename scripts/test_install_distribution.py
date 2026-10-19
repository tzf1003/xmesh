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
import tempfile
import unittest
import zipfile

from release_manifest import build, bundles, channel_for, validate
from build_install_site import build as build_site

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
                    for name in (asset["name"],"manifest.json","scripts/install.sh","scripts/install.ps1","install.sh","install.ps1","docs/agent/setup.md","llms.txt","docs/operations.md","docs/validation/real-environment-runbook.md"):
                        self.assertIn(name,bundle.namelist())
                    self.assertEqual(json.loads(bundle.read("manifest.json")),manifest)

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
            for page in (site/'index.html',version_dir/'index.html'):
                text=page.read_text(encoding="utf-8");self.assertNotIn('@@',text)
                parser=Links();parser.feed(text)
                for link in parser.links:
                    if link.startswith(('https:','#')):continue
                    self.assertTrue((page.parent/link).is_file(),f'{page}: {link}')
            prompt=(version_dir/'prompt.txt').read_text(encoding="utf-8")
            self.assertIn('/versions/v1.2.3-beta.2/agent/README.md',prompt)
            self.assertIn('plan/job ID',prompt)
            self.assertIn(manifest['version'],(version_dir/'agent/install.md').read_text(encoding="utf-8"))
            for navigation in (site/'llms.txt',version_dir/'llms.txt'):
                for link in re.findall(r'\]\(([^)]+)\)',navigation.read_text(encoding="utf-8")):
                    if not link.startswith('https:'):self.assertTrue((navigation.parent/link).is_file())
            self.assertNotIn('arch.value="amd64"', (ROOT/'docs/site/install.js').read_text(encoding="utf-8").split('if(navigator.userAgentData')[0])

    def test_site_requires_manifest_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp);manifest=fixture(directory)
            with self.assertRaises(ValueError):build_site(ROOT,directory/'site','v9.9.9','tzf1003/xmesh','https://example.org',manifest)

class BootstrapTests(unittest.TestCase):
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
