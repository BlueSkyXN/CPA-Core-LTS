import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location("packager", Path(__file__).with_name("package-pat-providers.py"))
packager = importlib.util.module_from_spec(spec)
spec.loader.exec_module(packager)


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.native, self.output = [self.root / name for name in ("native", "out")]
        self.native.mkdir()
        for name in ("codebuddy", "copilot", "qoder"):
            (self.native / f"cpa-provider-{name}.so").write_bytes(b"fixture-not-executable")

    def package(self, arch: str = "amd64"):
        return packager.package(self.native, self.output, arch, "a" * 40, "test")

    def test_layout_hashes_and_repeatability(self):
        manifest = self.package()
        self.assertEqual(len(manifest["assets"]), 3)
        for name, sha in manifest["assets"].items():
            self.assertEqual(packager.hashlib.sha256((self.output / name).read_bytes()).hexdigest(), sha)
            with zipfile.ZipFile(self.output / name) as archive:
                libraries = [item for item in archive.namelist() if item.endswith(".so")]
                self.assertEqual(len(libraries), 1)
                self.assertNotIn("/", libraries[0])
                if name.startswith("cpa-provider-qoder_"):
                    self.assertIn("THIRD_PARTY_NOTICES.md", archive.namelist())
        self.assertEqual(self.package(), manifest)

    def test_native_package_has_no_runner_dependency(self):
        manifest = self.package()
        self.assertIsNone(manifest["runner"])
        self.assertIsNone(manifest["node_major"])
        self.assertFalse(manifest["runner_required"])
        self.assertEqual(manifest["runtime"], "native-go")
        self.assertEqual(manifest["transport"], "direct_openai")
        self.assertTrue(all(name.startswith("cpa-provider-") for name in manifest["assets"]))

    def test_arch_metadata_and_distinct_assets(self):
        manifest = self.package("arm64")
        self.assertEqual(manifest["platform"], "linux/arm64")
        for name in manifest["assets"]:
            self.assertIn("linux_arm64", name)
        checksums = (self.output / "provider-checksums.txt").read_text()
        bundle = json.loads((self.output / "pat-provider-bundle.json").read_text())
        self.assertEqual(sorted(bundle["assets"]), sorted(line.split("  ")[1].strip() for line in checksums.splitlines()))

    def test_plugin_versions_come_from_source(self):
        manifest = self.package()
        for name, expected in manifest["plugins"].items():
            self.assertRegex(expected, r"^[0-9][0-9A-Za-z.+-]*$")
            self.assertTrue(any(asset.startswith(f"cpa-provider-{name}_{expected}_") for asset in manifest["assets"]))

    def test_missing_library_is_rejected(self):
        (self.native / "cpa-provider-qoder.so").unlink()
        with self.assertRaises(ValueError):
            self.package()
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
