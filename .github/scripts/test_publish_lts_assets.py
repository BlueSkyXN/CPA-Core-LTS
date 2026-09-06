import copy
import hashlib
import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
import subprocess

spec = importlib.util.spec_from_file_location("publisher", Path(__file__).with_name("publish-lts-assets.py"))
pub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pub)

class FakeGitHub:
    def __init__(self, exists=False, draft=False, assets=None):
        self.record = {"id": 1, "draft": draft} if exists else None
        self.data = dict(assets or {})
        self.calls = []
        self.fail = None
    def release(self, tag): return copy.deepcopy(self.record)
    def assets(self, rid): return [{"name": n} for n in self.data]
    def digest(self, tag, asset): return hashlib.sha256(self.data[asset["name"]]).hexdigest()
    def create(self, *args):
        self.calls.append("create-draft"); self.record = {"id": 1, "draft": True}
    def upload(self, tag, path):
        self.calls.append("upload:" + path.name)
        if path.name == self.fail: raise pub.PublicationError("network failed")
        assert path.name not in self.data, "overwritten asset"
        self.data[path.name] = path.read_bytes()
    def edit_notes(self, *args): self.calls.append("edit-notes")
    def publish(self, tag): self.calls.append("publish"); self.record["draft"] = False

class PublishTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name);self.files={n:root/n for n in ("a.zip", "b.zip")}
        for n,p in self.files.items():p.write_bytes(n.encode())
        self.notes=root/"notes.md";self.notes.write_text("Release notes")
    def run_publish(self,api,rewrite=False):pub.publish(api,"v1-lts-0.0.22",self.files,"Title",self.notes,rewrite)
    def test_new_is_draft_until_all_verified(self):
        api=FakeGitHub();self.run_publish(api)
        self.assertEqual(api.calls,["create-draft","upload:a.zip","upload:b.zip","publish"])
    def test_conflicting_rebuild_makes_no_mutation(self):
        api=FakeGitHub(True,assets={"a.zip":b"old"})
        with self.assertRaises(pub.PublicationError):self.run_publish(api,True)
        self.assertEqual(api.calls,[]);self.assertEqual(api.data["a.zip"],b"old")
    def test_unknown_asset_makes_no_mutation(self):
        api=FakeGitHub(True,assets={"unknown":b"x"})
        with self.assertRaises(pub.PublicationError):self.run_publish(api)
        self.assertEqual(api.calls,[])
    def test_identical_rebuild_skips_assets_and_preserves_notes(self):
        api=FakeGitHub(True,assets={n:p.read_bytes() for n,p in self.files.items()})
        self.run_publish(api);self.assertEqual(api.calls,[])
    def test_partial_failure_stays_draft_and_resumes_without_overwrite(self):
        api=FakeGitHub();api.fail="b.zip"
        with self.assertRaises(pub.PublicationError):self.run_publish(api)
        self.assertTrue(api.record["draft"]);self.assertNotIn("publish",api.calls)
        api.fail=None;api.calls.clear();self.run_publish(api)
        self.assertEqual(api.calls,["upload:b.zip","publish"])
    def test_existing_upload_failure_preserves_old_bytes_and_notes(self):
        api=FakeGitHub(True,assets={"a.zip":b"a.zip"});api.fail="b.zip"
        with self.assertRaises(pub.PublicationError):self.run_publish(api,True)
        self.assertEqual(api.data,{"a.zip":b"a.zip"});self.assertNotIn("edit-notes",api.calls)
    def test_network_failure_is_not_interpreted_as_absent_release(self):
        api=pub.GitHub("owner/repo")
        for stderr in ["HTTP 500", "gh: Requires authentication (HTTP 401)"]:
            with patch.object(pub.subprocess,"run",return_value=subprocess.CompletedProcess([],1,"",stderr)):
                with self.assertRaises(pub.PublicationError):api.release("v1-lts-0.0.22")
        with patch.object(pub.subprocess,"run",return_value=subprocess.CompletedProcess([],1,"","gh: Not Found (HTTP 404)")):
            self.assertIsNone(api.release("v1-lts-0.0.22"))
    def test_upload_command_never_clobbers(self):
        api=pub.GitHub("owner/repo")
        with patch.object(pub.subprocess,"run",return_value=subprocess.CompletedProcess([],0,"","")) as run:
            api.upload("v1-lts-0.0.22",self.files["a.zip"])
            self.assertNotIn("--clobber",run.call_args.args[0])

if __name__ == "__main__":unittest.main()
