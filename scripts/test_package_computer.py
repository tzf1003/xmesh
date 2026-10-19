import pathlib
import plistlib
import tempfile
import unittest

from package_computer import package


class NativePackageTests(unittest.TestCase):
    def test_macos_application_identity_and_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            binary = root / "native"
            binary.write_bytes(b"native executor")
            output = root / "xmesh-computer.app"
            target = package(binary, output, "darwin", "v1.2.3-beta.1")
            self.assertEqual(target.read_bytes(), binary.read_bytes())
            with (output / "Contents/Info.plist").open("rb") as stream:
                info = plistlib.load(stream)
            self.assertEqual(info["CFBundleIdentifier"], "app.xmesh.computer")
            self.assertEqual(info["LSMinimumSystemVersion"], "14.0")
            self.assertEqual(info["CFBundleShortVersionString"], "1.2.3")
            self.assertEqual(info["CFBundleVersion"], "1.2.3")
            self.assertEqual(info["XmeshReleaseVersion"], "1.2.3-beta.1")
            notices=(output / "Contents/Resources/ThirdPartyNotices.txt").read_text()
            self.assertIn("Copyright (c) 2026 Leo",notices)
            self.assertIn("Copyright (c) 2025 Cua AI, Inc.",notices)
            self.assertIn("Copyright (c) 2026 Matthew Lam",notices)
            with self.assertRaises(ValueError):
                package(binary, output, "darwin", "v1.2.3")

    def test_windows_executable_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            binary = root / "native.exe"
            binary.write_bytes(b"native executor")
            target = package(binary, root / "bundle/xmesh-computer.exe", "windows", "v1.2.3")
            self.assertEqual(target.read_bytes(), binary.read_bytes())

    def test_macos_release_metadata_is_numeric_for_stable_beta_and_rc(self):
        for version in ["v0.1.0", "0.1.0-beta.7", "v0.1.0-rc.2"]:
            with self.subTest(version=version), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                binary = root / "native"
                binary.write_bytes(b"fixture")
                output = root / "helper.app"
                package(binary, output, "darwin", version)
                info = plistlib.loads((output / "Contents/Info.plist").read_bytes())
                self.assertEqual(info["CFBundleVersion"], "0.1.0")
                self.assertEqual(info["CFBundleShortVersionString"], "0.1.0")
                self.assertEqual(info["XmeshReleaseVersion"], version.removeprefix("v"))


if __name__ == "__main__":
    unittest.main()
