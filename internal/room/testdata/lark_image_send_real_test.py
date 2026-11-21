"""Offline verification of the real-write bound; never invokes lark-cli."""
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import lark_image_send_real as guard


class WriteBound(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = self.tmp.name
        self.cfg = dict(profile="test", ledger=root + "/ledger", dispatch_dir=root,
                        user_id="ou_test", operation="message.send_self", receive_id="ou_test",
                        receive_id_type="open_id", self_chat_id="oc_self", group_id="oc_group",
                        group_name="QA")
        self.case = dict(id="single", key="a" * 32, input={"images": [{"message_seq": 1,
                         "attachment_id": "img_" + "1" * 32}]},
                         hashes=[hashlib.sha256(b"original").hexdigest()])
        self.cfg["cases"] = [self.case]
        os.makedirs(self.cfg["ledger"] + "/records")
        self.action = dict(status="unknown", attempts=1, idempotency_key=self.case["key"],
                           request=dict(id="single", operation=self.cfg["operation"], input=self.case["input"]),
                           access=dict(account=dict(id=dict(value="ou_test"))),
                           inspection=dict(content_digest="d" * 64))
        self.ledger()
        self.addCleanup(os.chdir, os.getcwd())
        os.chdir(root)
        with open("image-1.png", "wb") as stream:
            stream.write(b"original")
        self.env = patch.dict(os.environ, {"LARKSUITE_CLI_CONFIG_DIR": root + "/pinned"})
        self.env.start()
        self.addCleanup(self.env.stop)

    def ledger(self):
        with open(self.cfg["ledger"] + "/records/one.json", "w") as stream:
            json.dump({"action": self.action}, stream)

    def upload(self):
        return ["--profile", "test", "im", "images", "create", "--data",
                '{"image_type":"message"}', "--file", "image=./image-1.png",
                "--as", "user", "--format", "json"]

    def message(self, **changes):
        payload = dict(receive_id="ou_test", uuid=self.case["key"], msg_type="image",
                       content=json.dumps({"image_key": "img_uploaded"}))
        payload.update(changes)
        return ["--profile", "test", "api", "POST", "/open-apis/im/v1/messages", "--params",
                '{"receive_id_type":"open_id"}', "--data", json.dumps(payload),
                "--as", "user", "--format", "json"]

    def uploaded(self):
        _, record = guard.authorize(self.cfg, self.upload())
        guard.save_once(record + ".response", {"image_key": "img_uploaded"})

    def test_single_upload_and_dispatch_survive_reinvocation(self):
        self.uploaded()
        with self.assertRaises(FileExistsError):
            guard.authorize(self.cfg, self.upload())
        _, record = guard.authorize(self.cfg, self.message())
        self.assertEqual(guard.load(record)["status"], "unknown")
        with self.assertRaises(FileExistsError):
            guard.authorize(self.cfg, self.message())

    def test_upload_requires_durable_frozen_attempt(self):
        for field, value in [("attempts", 0), ("status", "succeeded")]:
            old = self.action[field]
            self.action[field] = value
            self.ledger()
            with self.assertRaises(AssertionError):
                guard.authorize(self.cfg, self.upload())
            self.action[field] = old
        self.action["request"]["input"] = {"body": "replaced"}
        self.ledger()
        with self.assertRaises(AssertionError):
            guard.authorize(self.cfg, self.upload())

    def test_changed_bytes_and_unpinned_account_rejected_before_upload(self):
        with open("image-1.png", "wb") as stream:
            stream.write(b"changed")
        with self.assertRaises(AssertionError):
            guard.authorize(self.cfg, self.upload())
        os.environ.pop("LARKSUITE_CLI_CONFIG_DIR")
        with self.assertRaises(AssertionError):
            guard.authorize(self.cfg, self.upload())

    def test_missing_upload_and_wrong_content_never_dispatch(self):
        with self.assertRaises(FileNotFoundError):
            guard.authorize(self.cfg, self.message())
        self.uploaded()
        for change in [dict(receive_id="oc_other"), dict(uuid="b" * 32),
                       dict(content='{"image_key":"img_other"}'), dict(msg_type="text")]:
            with self.assertRaises(AssertionError):
                guard.authorize(self.cfg, self.message(**change))
        self.assertFalse(os.path.exists(self.cfg["dispatch_dir"] + "/" + self.case["key"] + ".message"))

    def test_no_other_business_write_or_identity(self):
        for args in [["api", "DELETE", "/open-apis/im/v1/messages/om_other"],
                     ["im", "+messages-send", "--text", "extra"], ["auth", "login"],
                     ["api", "GET", "/open-apis/im/v1/messages/om_other", "--as", "user", "--format", "json"]]:
            with self.assertRaises(AssertionError):
                guard.authorize(self.cfg, ["--profile", "test"] + args)

    def test_caption_and_order_are_checked_as_one_post(self):
        case = dict(input=dict(body="中文\n\n**literal**\n", images=[{}, {}]))
        kind, payload = guard.content(case, ["img_one", "img_two"])
        self.assertEqual(kind, "post")
        rows = payload["zh_cn"]["content"]
        self.assertEqual([r[0]["text"] for r in rows[:-2]], ["中文", "", "**literal**", ""])
        self.assertEqual([r[0]["image_key"] for r in rows[-2:]], ["img_one", "img_two"])

    def test_real_subprocess_forwarding_and_guard_before_second_invocation(self):
        root = self.tmp.name
        native = root + "/native"
        with open(native, "w") as stream:
            stream.write("#!/usr/bin/env python3\nimport json\n"
                         "with open('native-calls','a') as f:f.write('one\\n')\n"
                         "print(json.dumps({'ok':True,'identity':'user','data':{'image_key':'img_uploaded'}}))\n")
        os.chmod(native, 0o700)
        self.cfg["binary"] = native
        config = root + "/config.json"
        with open(config, "w") as stream:
            json.dump(self.cfg, stream)
        with open(guard.__file__) as stream:
            script = stream.read().replace('"__CONFIG_PATH__"', json.dumps(config), 1)
        wrapper = root + "/wrapper.py"
        with open(wrapper, "w") as stream:
            stream.write(script)
        first = subprocess.run([sys.executable, "-I", wrapper] + self.upload(), capture_output=True)
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(json.loads(first.stdout)["data"]["image_key"], "img_uploaded")
        response = guard.load(root + "/" + self.case["key"] + ".upload-1.response")
        self.assertEqual(response["image_key"], "img_uploaded")
        second = subprocess.run([sys.executable, "-I", wrapper] + self.upload(), capture_output=True)
        self.assertNotEqual(second.returncode, 0)
        with open("native-calls") as stream:
            self.assertEqual(stream.read(), "one\n")


if __name__ == "__main__":
    unittest.main()
