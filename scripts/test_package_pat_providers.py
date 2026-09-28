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
        for name in ("cpa-provider-codebuddy", "cpa-provider-copilot", "cpa-provider-qoder", "zcode-coding-plan"):
            (self.native / f"{name}.so").write_bytes(b"fixture-not-executable")

    def package(self, arch: str = "amd64"):
        return packager.package(self.native, self.output, arch, "a" * 40, "test")

    def test_layout_hashes_and_repeatability(self):
        manifest = self.package()
        self.assertEqual(len(manifest["assets"]), 4)
        self.assertEqual(set(manifest["plugins"]), {"codebuddy", "copilot", "qoder", "zcode-coding-plan"})
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
        self.assertEqual(manifest["transport"], "mixed")
        self.assertEqual(manifest["plugin_transports"], {
            "codebuddy": "direct_openai", "copilot": "direct_openai", "qoder": "direct_openai",
            "zcode-coding-plan": "direct_anthropic",
        })

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
            stem = name if name == "zcode-coding-plan" else f"cpa-provider-{name}"
            self.assertTrue(any(asset.startswith(f"{stem}_{expected}_") for asset in manifest["assets"]))

    def test_coding_plan_archive_preserves_identity_and_only_example_config(self):
        manifest = self.package()
        name = next(name for name in manifest["assets"] if name.startswith("zcode-coding-plan_"))
        with zipfile.ZipFile(self.output / name) as archive:
            self.assertEqual(set(archive.namelist()), {
                "zcode-coding-plan.so", "README.md", "SPEC.md", "LICENSE",
                "config.example.json", "auth.example.json",
            })
            config = json.loads(archive.read("config.example.json"))
            self.assertFalse(config["host_logging_disabled"])
            self.assertEqual(config["credential"], {"api_key_env": "CP_API_KEY"})
            self.assertEqual(config["identity"]["device_id_env"], "CP_DEVICE_ID")
            self.assertEqual(json.loads(archive.read("auth.example.json"))["type"], "zcode-coding-plan")

    def test_missing_coding_plan_library_is_rejected(self):
        (self.native / "zcode-coding-plan.so").unlink()
        with self.assertRaises(ValueError):
            self.package()
        self.assertFalse(self.output.exists())

    def test_missing_library_is_rejected(self):
        (self.native / "cpa-provider-qoder.so").unlink()
        with self.assertRaises(ValueError):
            self.package()
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main()
