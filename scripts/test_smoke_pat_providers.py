import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("smoke", Path(__file__).with_name("smoke-pat-providers.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class SmokeFixtureTests(unittest.TestCase):
    def test_coding_plan_private_fixture_is_explicit_and_separate_from_auths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private = smoke.write_coding_plan_fixture(root)
            self.assertEqual(private, root / "coding-plan")
            config = json.loads((private / "config.json").read_text())
            self.assertEqual(config["credential"], {"api_key_file": "api-key.txt"})
            self.assertEqual(config["identity"]["device_id_file"], "device-id.txt")
            self.assertEqual((private / "api-key.txt").read_text(), "synthetic-key.synthetic-secret")
            self.assertTrue(config["host_logging_disabled"])
            self.assertFalse((root / "auths").exists())

    def test_internal_network_endpoint_does_not_require_published_port(self):
        info = {"State": {"Running": True}, "NetworkSettings": {
            "Ports": {}, "Networks": {"fixture": {"IPAddress": "172.30.0.2"}},
        }}
        self.assertEqual(smoke.fixture_management_url(info, "fixture"), "http://172.30.0.2:8317/v0/management")
        info["State"]["Running"] = False
        with self.assertRaises(RuntimeError):
            smoke.fixture_management_url(info, "fixture")
        info["State"]["Running"] = True
        info["NetworkSettings"]["Networks"]["fixture"]["IPAddress"] = ""
        with self.assertRaises(ValueError):
            smoke.fixture_management_url(info, "fixture")

    def test_readiness_requires_selected_auth_and_no_secret_echo(self):
        smoke.check_coding_plan_status({"Ready": False, "Checks": [{"Level": "auth_ready", "State": "unknown"}]}, selected=False)
        smoke.check_coding_plan_status({"Ready": True, "Checks": [{"Level": "auth_ready", "State": "ready"}]}, selected=True)
        for status in (
            {"Ready": True, "Checks": []},
            {"Ready": False, "Checks": [{"Level": "auth_ready", "State": "ready"}]},
            {"Ready": True, "Checks": [{"Level": "auth_ready", "State": "ready", "Message": "synthetic-key.synthetic-secret"}]},
        ):
            with self.assertRaises(AssertionError):
                smoke.check_coding_plan_status(status, selected=True)


if __name__ == "__main__":
    unittest.main()
