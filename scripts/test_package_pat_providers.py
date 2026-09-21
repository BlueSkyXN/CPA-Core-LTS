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
        self.native, self.runner, self.output = [self.root / name for name in ("native", "runner", "out")]
        self.native.mkdir()
        (self.runner / "dist").mkdir(parents=True)
        (self.runner / "node_modules/example").mkdir(parents=True)
        (self.runner / "node_modules/example/index.js").write_text("export {}")
        (self.runner / "dist/index.js").write_text("export {}")
        (self.runner / "package.json").write_text(json.dumps({"version": "0.1.0", "type": "module"}))
        for name in ("codebuddy", "qoder"):
            (self.native / f"cpa-provider-{name}.so").write_bytes(b"fixture-not-executable")

    def package(self):
        return packager.package(self.native, self.runner, self.output, "amd64", "a" * 40, "test")

    def test_layout_hashes_and_repeatability(self):
        manifest = self.package()
        self.assertEqual(len(manifest["assets"]), 3)
        for name, sha in manifest["assets"].items():
            self.assertEqual(packager.hashlib.sha256((self.output / name).read_bytes()).hexdigest(), sha)
            with zipfile.ZipFile(self.output / name) as archive:
                if name.startswith("cpa-provider-"):
                    libraries = [item for item in archive.namelist() if item.endswith(".so")]
                    self.assertEqual(len(libraries), 1)
                    self.assertNotIn("/", libraries[0])
                    if name.startswith("cpa-provider-qoder_"):
                        self.assertIn("THIRD_PARTY_NOTICES.md", archive.namelist())
                else:
                    self.assertIn("dist/index.js", archive.namelist())
                    self.assertIn("node_modules/example/index.js", archive.namelist())
        self.assertEqual(self.package(), manifest)

    def test_native_package_has_no_runner_dependency(self):
        manifest = packager.package(self.native, None, self.output, "amd64", "a" * 40, "test")
        self.assertEqual(len(manifest["assets"]), 2)
        self.assertIsNone(manifest["runner"])
        self.assertIsNone(manifest["node_major"])
        self.assertFalse(manifest["runner_required"])
        self.assertEqual(manifest["runtime"], "native-go")
        self.assertTrue(all(name.startswith("cpa-provider-") for name in manifest["assets"]))

    def test_missing_runner_fails_before_output(self):
        (self.runner / "dist/index.js").unlink()
        with self.assertRaises(ValueError):
            self.package()
        self.assertFalse(self.output.exists())

    def test_unexpected_symlink_is_rejected(self):
        (self.runner / "node_modules/example/link").symlink_to(self.runner / "package.json")
        with self.assertRaises(ValueError):
            self.package()

    def test_missing_library_is_rejected(self):
        (self.native / "cpa-provider-qoder.so").unlink()
        with self.assertRaises(ValueError):
            self.package()
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
