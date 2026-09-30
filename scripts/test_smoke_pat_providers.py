import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("smoke", Path(__file__).with_name("smoke-pat-providers.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class SmokeFixtureTests(unittest.TestCase):
    def test_inline_coding_plan_payload_is_single_file_self_contained(self):
        payload = smoke.inline_coding_plan_payload()
        self.assertEqual(payload["type"], "zcode-coding-plan")
        self.assertIn("api_key", payload)
        self.assertIn("device_id", payload)
        self.assertNotIn("config_file", payload)
        self.assertTrue(payload["api_key"].count(".") == 1)

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
