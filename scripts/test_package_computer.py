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
            with self.assertRaises(ValueError):
                package(binary, output, "darwin", "v1.2.3")

    def test_windows_executable_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            binary = root / "native.exe"
            binary.write_bytes(b"native executor")
            target = package(binary, root / "bundle/xmesh-computer.exe", "windows", "v1.2.3")
            self.assertEqual(target.read_bytes(), binary.read_bytes())


if __name__ == "__main__":
    unittest.main()
