#!/usr/bin/env python3
"""Unit tests for agent_desktop_upstream.py. Stdlib only; no network access."""

import base64
import contextlib
import hashlib
import io
import json
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import unittest
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import agent_desktop_upstream as adu  # noqa: E402

PINNED_MANIFEST = {
    "schema_version": 1,
    "package": "agent-desktop",
    "version": "1.0.2",
    "license": "Apache-2.0",
    "tarball": {
        "url": "https://registry.npmjs.org/agent-desktop/-/agent-desktop-1.0.2.tgz",
        "integrity": "sha512-old",
        "published_at": "2026-10-30T21:18:45.761Z",
    },
    "platforms": {"old": True},
    "validation": {"validated_versions": ["1.0.2"], "validated_platforms": ["darwin-arm64"], "record": None},
    "engine_note": "fields this script does not own must survive",
}


def package_files(version, skip=(), extra=None):
    """Files of a fake npm tarball laid out like agent-desktop's. The contents are not real binaries."""
    files = {
        "package.json": json.dumps({"name": "agent-desktop", "version": version, "license": "Apache-2.0"}).encode(),
        "bin/agent-desktop": b"#!/usr/bin/env node\n",
    }
    for binary, helpers in adu.PLATFORMS.values():
        for name in (binary, *helpers):
            if name in skip:
                continue
            data = f"{version}:{name}".encode()
            files[f"bin/{name}"] = data
            files[f"bin/{name}.sha256"] = (hashlib.sha256(data).hexdigest() + "\n").encode()
    files.update(extra or {})
    return files


def write_tarball(path, files, raw=None):
    """`files` are placed under package/. `raw` maps full member names, used for unsafe-name tests."""
    entries = [("package/" + name, data) for name, data in files.items()]
    entries += list((raw or {}).items())
    with tarfile.open(path, "w:gz") as archive:
        for name, data in entries:
            info = tarfile.TarInfo(name)
            info.size = len(data)
            info.mode = 0o755 if "/bin/" in name else 0o644
            archive.addfile(info, io.BytesIO(data))


def sha512_integrity(path):
    return "sha512-" + base64.b64encode(hashlib.sha512(path.read_bytes()).digest()).decode("ascii")


def packument_for(version, integrity, latest=None):
    return {
        "dist-tags": {"latest": latest or version},
        "time": {version: "2026-11-03T04:15:14.065Z"},
        "versions": {
            version: {
                "dist": {
                    "tarball": f"https://registry.npmjs.org/agent-desktop/-/agent-desktop-{version}.tgz",
                    "integrity": integrity,
                }
            }
        },
    }


class DecideTest(unittest.TestCase):
    def test_same_version_is_not_proposed(self):
        self.assertFalse(adu.decide("1.0.2", "1.0.2", [])[0])

    def test_older_upstream_never_moves_the_pin_back(self):
        self.assertFalse(adu.decide("1.1.0", "1.0.2", [])[0])

    def test_newer_upstream_is_proposed(self):
        self.assertTrue(adu.decide("1.0.2", "1.1.0", [])[0])

    def test_versions_compare_numerically(self):
        self.assertTrue(adu.decide("1.9.0", "1.10.0", [])[0])

    def test_one_open_proposal_at_a_time(self):
        pulls = [{"number": 7, "state": "OPEN", "headRefName": "automation/agent-desktop-1.1.0"}]
        self.assertFalse(adu.decide("1.0.2", "1.1.1", pulls)[0])

    def test_closed_proposal_is_not_reopened(self):
        pulls = [{"number": 8, "state": "CLOSED", "headRefName": "automation/agent-desktop-1.1.0"}]
        propose, reason = adu.decide("1.0.2", "1.1.0", pulls)
        self.assertFalse(propose)
        self.assertIn("run this workflow by hand with version 1.1.0", reason)

    def test_closed_proposal_can_be_requested_again(self):
        pulls = [{"number": 8, "state": "CLOSED", "headRefName": "automation/agent-desktop-1.1.0"}]
        self.assertTrue(adu.decide("1.0.2", "1.1.0", pulls, requested=True)[0])

    def test_open_proposal_still_blocks_a_requested_version(self):
        pulls = [{"number": 7, "state": "OPEN", "headRefName": "automation/agent-desktop-1.1.0"}]
        self.assertFalse(adu.decide("1.0.2", "1.1.0", pulls, requested=True)[0])

    def test_unrelated_pull_requests_are_ignored(self):
        pulls = [{"number": 9, "state": "OPEN", "headRefName": "claude/project-thread-x"}]
        self.assertTrue(adu.decide("1.0.2", "1.1.0", pulls)[0])


class VersionTest(unittest.TestCase):
    def test_rejects_anything_but_plain_semver(self):
        for text in ("1.0", "v1.0.0", "1.0.0-beta.1", "01.0.0", ""):
            with self.subTest(text=text), self.assertRaises(adu.UpstreamError):
                adu.parse_version(text)

    def test_tarball_must_come_from_the_npm_registry(self):
        packument = packument_for("1.1.0", "sha512-x")
        packument["versions"]["1.1.0"]["dist"]["tarball"] = "https://evil.example/agent-desktop-1.1.0.tgz"
        with self.assertRaises(adu.UpstreamError):
            adu.select_target(packument)

    def test_requested_version_must_be_published(self):
        with self.assertRaises(adu.UpstreamError):
            adu.select_target(packument_for("1.1.0", "sha512-x"), requested="9.9.9")

    def test_integrity_must_be_sha512(self):
        with self.assertRaises(adu.UpstreamError):
            adu.select_target(packument_for("1.1.0", "sha1-abc"))


class ProposalTest(unittest.TestCase):
    def setUp(self):
        self._repo = tempfile.TemporaryDirectory()
        self._fixtures = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self._repo.name)
        self.fixtures = pathlib.Path(self._fixtures.name)
        self.manifest = self.root / adu.DEFAULT_MANIFEST
        self.manifest.parent.mkdir(parents=True)
        self.manifest.write_text(json.dumps(PINNED_MANIFEST, indent=2) + "\n", encoding="utf-8")
        (self.root / adu.RECORD_DIR).mkdir(parents=True)

    def tearDown(self):
        self._repo.cleanup()
        self._fixtures.cleanup()

    def publish(self, version="1.1.0", files=None, raw=None, integrity=None, latest=None):
        """Write a fake tarball and packument into the fixtures directory."""
        tarball = self.fixtures / f"agent-desktop-{version}.tgz"
        write_tarball(tarball, files if files is not None else package_files(version), raw)
        packument = self.fixtures / "packument.json"
        document = packument_for(version, integrity or sha512_integrity(tarball), latest)
        packument.write_text(json.dumps(document), encoding="utf-8")
        return tarball, packument

    def run_tool(self, tarball, packument, requested_version="", pulls=None):
        output = self.root / "github-output.txt"
        output.write_text("", encoding="utf-8")
        argv = [
            "--repo-root", str(self.root),
            "--packument", str(packument),
            "--tarball", str(tarball),
            "--github-output", str(output),
            "--requested-version", requested_version,
        ]
        if pulls is not None:
            listing = self.fixtures / "pulls.json"
            listing.write_text(json.dumps(pulls), encoding="utf-8")
            argv += ["--pulls", str(listing)]
        with contextlib.redirect_stdout(io.StringIO()):
            code = adu.main(argv)
        values = dict(line.split("=", 1) for line in output.read_text(encoding="utf-8").splitlines() if line)
        return code, values

    def test_proposes_new_version_and_keeps_fields_it_does_not_own(self):
        tarball, packument = self.publish("1.1.0")
        code, values = self.run_tool(tarball, packument)
        self.assertEqual(code, 0)
        self.assertEqual(values["changed"], "true")
        self.assertEqual(values["branch"], "automation/agent-desktop-1.1.0")
        self.assertEqual(values["version"], "1.1.0")

        manifest = json.loads(self.manifest.read_text(encoding="utf-8"))
        self.assertEqual(manifest["version"], "1.1.0")
        self.assertEqual(manifest["tarball"]["integrity"], sha512_integrity(tarball))
        self.assertEqual(manifest["tarball"]["published_at"], "2026-11-03T04:15:14.065Z")
        self.assertEqual(manifest["upstream"], {"latest": "1.1.0"})
        self.assertEqual(manifest["validation"], {
            "validated_versions": ["1.0.2"],
            "validated_platforms": ["darwin-arm64"],
            "record": "docs/validation/agent-desktop-1.1.0.md",
        })
        self.assertEqual(manifest["engine_note"], PINNED_MANIFEST["engine_note"])
        for key, (binary, helpers) in adu.PLATFORMS.items():
            entry = manifest["platforms"][key]
            self.assertEqual(entry["binary"]["member"], f"bin/{binary}")
            self.assertEqual(entry["binary"]["sha256"], hashlib.sha256(f"1.1.0:{binary}".encode()).hexdigest())
            self.assertEqual([h["member"] for h in entry["helpers"]], [f"bin/{h}" for h in helpers])

    def test_writes_a_pending_validation_record(self):
        tarball, packument = self.publish("1.1.0")
        self.run_tool(tarball, packument)
        record = (self.root / "docs/validation/agent-desktop-1.1.0.md").read_text(encoding="utf-8")
        self.assertIn("待验收（pending）", record)
        self.assertIn("| win32-x64 |", record)
        self.assertIn("没有运行任何上游二进制", record)

    def test_existing_record_is_never_overwritten(self):
        record = self.root / "docs/validation/agent-desktop-1.1.0.md"
        record.write_text("human notes", encoding="utf-8")
        tarball, packument = self.publish("1.1.0")
        code, values = self.run_tool(tarball, packument)
        self.assertEqual(code, 0)
        self.assertEqual(values["changed"], "true")
        self.assertEqual(record.read_text(encoding="utf-8"), "human notes")

    def test_no_change_when_the_pin_is_current(self):
        before = self.manifest.read_text(encoding="utf-8")
        tarball, packument = self.publish("1.0.2")
        code, values = self.run_tool(tarball, packument)
        self.assertEqual((code, values["changed"]), (0, "false"))
        self.assertEqual(self.manifest.read_text(encoding="utf-8"), before)

    def test_missing_manifest_is_a_no_op(self):
        self.manifest.unlink()
        tarball, packument = self.publish("1.1.0")
        code, values = self.run_tool(tarball, packument)
        self.assertEqual((code, values["changed"]), (0, "false"))

    def test_refuses_tarball_that_does_not_match_registry_integrity(self):
        before = self.manifest.read_text(encoding="utf-8")
        tarball, packument = self.publish("1.1.0", integrity="sha512-" + base64.b64encode(bytes(64)).decode("ascii"))
        with self.assertRaisesRegex(adu.UpstreamError, "does not match the registry integrity"):
            self.run_tool(tarball, packument)
        self.assertEqual(self.manifest.read_text(encoding="utf-8"), before)
        self.assertFalse((self.root / "docs/validation/agent-desktop-1.1.0.md").exists())

    def test_refuses_binary_that_does_not_match_its_sidecar(self):
        files = package_files("1.1.0", extra={"bin/agent-desktop-linux-x64.sha256": b"00" * 32 + b"\n"})
        tarball, packument = self.publish("1.1.0", files=files)
        with self.assertRaisesRegex(adu.UpstreamError, "does not match its .sha256 sidecar"):
            self.run_tool(tarball, packument)

    def test_refuses_missing_platform_binary(self):
        tarball, packument = self.publish("1.1.0", files=package_files("1.1.0", skip=("agent-desktop-win32-x64.exe",)))
        with self.assertRaisesRegex(adu.UpstreamError, "has no bin/agent-desktop-win32-x64.exe"):
            self.run_tool(tarball, packument)

    def test_refuses_missing_helper(self):
        helper = "agent-desktop-linux-clipboard-owner-linux-x64"
        tarball, packument = self.publish("1.1.0", files=package_files("1.1.0", skip=(helper,)))
        with self.assertRaisesRegex(adu.UpstreamError, f"has no bin/{helper}"):
            self.run_tool(tarball, packument)

    def test_refuses_sidecar_that_names_another_file(self):
        files = package_files("1.1.0")
        digest = hashlib.sha256(b"1.1.0:agent-desktop-linux-x64").hexdigest()
        files["bin/agent-desktop-linux-x64.sha256"] = f"{digest}  agent-desktop-linux-arm64\n".encode()
        tarball, packument = self.publish("1.1.0", files=files)
        with self.assertRaisesRegex(adu.UpstreamError, "names another file"):
            self.run_tool(tarball, packument)

    def test_refuses_unsafe_archive_names(self):
        for name in ("package/../escape.txt", "/etc/evil", "other/root.txt"):
            with self.subTest(name=name):
                tarball, packument = self.publish("1.1.0", raw={name: b"x"})
                with self.assertRaises(adu.UpstreamError):
                    self.run_tool(tarball, packument)

    def test_refuses_package_json_for_another_package_or_version(self):
        files = package_files("1.1.0")
        files["package.json"] = json.dumps({"name": "agent-desktop", "version": "1.0.9", "license": "Apache-2.0"}).encode()
        tarball, packument = self.publish("1.1.0", files=files)
        with self.assertRaisesRegex(adu.UpstreamError, "package.json reports"):
            self.run_tool(tarball, packument)

    def test_unmapped_binaries_are_reported_not_recorded(self):
        extra = {
            "bin/agent-desktop-linux-riscv64": b"riscv",
            "bin/agent-desktop-linux-riscv64.sha256": hashlib.sha256(b"riscv").hexdigest().encode() + b"\n",
        }
        tarball, packument = self.publish("1.1.0", files=package_files("1.1.0", extra=extra))
        code, values = self.run_tool(tarball, packument)
        self.assertEqual((code, values["changed"]), (0, "true"))
        record = (self.root / "docs/validation/agent-desktop-1.1.0.md").read_text(encoding="utf-8")
        self.assertIn("`bin/agent-desktop-linux-riscv64`：不写入清单", record)
        manifest = json.loads(self.manifest.read_text(encoding="utf-8"))
        self.assertNotIn("riscv", json.dumps(manifest["platforms"]))

    def test_requested_version_that_is_older_is_not_proposed(self):
        tarball, packument = self.publish("1.0.1")
        code, values = self.run_tool(tarball, packument, requested_version="1.0.1")
        self.assertEqual((code, values["changed"]), (0, "false"))
        self.assertIn("the pin never moves backwards", values["reason"])

    def test_requested_version_is_not_recorded_as_upstream_latest(self):
        tarball, packument = self.publish("1.1.0", latest="1.2.0")
        code, values = self.run_tool(tarball, packument, requested_version="1.1.0")
        self.assertEqual((code, values["changed"], values["upstream"]), (0, "true", "1.2.0"))
        manifest = json.loads(self.manifest.read_text(encoding="utf-8"))
        self.assertEqual(manifest["version"], "1.1.0")
        self.assertEqual(manifest["upstream"], {"latest": "1.2.0"})

    def test_closed_proposal_blocks_the_daily_run_until_requested_by_hand(self):
        tarball, packument = self.publish("1.1.0")
        pulls = [{"number": 8, "state": "CLOSED", "headRefName": "automation/agent-desktop-1.1.0"}]
        code, values = self.run_tool(tarball, packument, pulls=pulls)
        self.assertEqual((code, values["changed"]), (0, "false"))
        self.assertIn("run this workflow by hand", values["reason"])
        code, values = self.run_tool(tarball, packument, requested_version="1.1.0", pulls=pulls)
        self.assertEqual((code, values["changed"]), (0, "true"))

    def test_cli_reports_failures_as_annotations_and_exits_nonzero(self):
        tarball, packument = self.publish("1.1.0", integrity="sha512-" + base64.b64encode(bytes(64)).decode("ascii"))
        done = subprocess.run(
            [sys.executable, str(HERE / "agent_desktop_upstream.py"), "--repo-root", str(self.root),
             "--packument", str(packument), "--tarball", str(tarball)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(done.returncode, 1)
        self.assertIn("::error::", done.stdout)


class RedirectTest(unittest.TestCase):
    def test_redirect_to_another_host_or_scheme_is_refused(self):
        request = urllib.request.Request("https://registry.npmjs.org/agent-desktop/-/agent-desktop-1.1.0.tgz")
        for location in ("https://cdn.example.com/agent-desktop-1.1.0.tgz",
                         "http://registry.npmjs.org/agent-desktop/-/agent-desktop-1.1.0.tgz"):
            with self.subTest(location=location), self.assertRaisesRegex(adu.UpstreamError, "refusing redirect"):
                adu.SameHostRedirects().redirect_request(request, None, 302, "Found", {}, location)

    def test_redirect_within_the_registry_host_is_followed(self):
        request = urllib.request.Request("https://registry.npmjs.org/agent-desktop/-/agent-desktop-1.1.0.tgz")
        moved = "https://registry.npmjs.org/agent-desktop/-/moved-1.1.0.tgz"
        followed = adu.SameHostRedirects().redirect_request(request, None, 302, "Found", {}, moved)
        self.assertEqual(followed.full_url, moved)


if __name__ == "__main__":
    unittest.main()
