"""Verifier regressions; no Docker, credentials or model calls."""

import hashlib
import json
from pathlib import Path
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch
import subprocess

from http.server import ThreadingHTTPServer
from agent_sandbox_acceptance import pixel_code, verify_trials, settle_container
from agent_sandbox_fixture import Fixture


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.expected = {"1": {"reading": "dynamic-secret", "challenge": "form-secret", "receipt": "receipt-secret", "bytes": 5, "sha256": "digest"}}
        self.dsh = {"turns": [{"name": "trial-1", "tool_calls": 4, "result": {"text": "dynamic-secret receipt-secret", "stopReason": "end_turn"}}]}
        self.events = [{"run": "1", "kind": kind} for kind in ("dynamic", "complete")]
        self.events += [{"run": "1", "kind": "submission", "matched": True, "answer": "form-secret"}, {"run": "1", "kind": "download", "sha256": "digest"}]
        self.downloads = [{"name": "trial-1.txt", "bytes": 5, "sha256": "digest"}]

    def test_model_claims_require_independent_receipts_and_bytes(self):
        self.assertTrue(verify_trials(self.expected, self.events, self.dsh, self.downloads)[0]["passed"])
        self.assertFalse(verify_trials(self.expected, [], self.dsh, self.downloads)[0]["passed"])
        self.assertFalse(verify_trials(self.expected, self.events, self.dsh, [])[0]["passed"])
        self.downloads[0]["sha256"] = "corrupt"
        self.assertFalse(verify_trials(self.expected, self.events, self.dsh, self.downloads)[0]["passed"])

    def test_cross_trial_receipts_and_failed_turn_cannot_pass(self):
        self.events[2]["run"] = "2"
        self.assertFalse(verify_trials(self.expected, self.events, self.dsh, self.downloads)[0]["passed"])
        self.events[2]["run"] = "1"
        self.dsh["turns"][0]["error"] = "timeout"
        self.assertFalse(verify_trials(self.expected, self.events, self.dsh, self.downloads)[0]["passed"])

    def test_stop_timeout_requires_independent_stopped_state(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch("agent_sandbox_acceptance.command", side_effect=[subprocess.TimeoutExpired("docker stop", 30),
                       json.dumps({"Running": False, "Pid": 0}).encode(), b"removed", b""]) as docker:
                result = settle_container("owned-id", Path(directory) / "state.json")
                self.assertTrue(result["settled"])
                self.assertTrue(result["removed"])
                self.assertTrue(result["stop_command_failed"])
                self.assertEqual(docker.call_args_list[1].args[1], "inspect")
            with patch("agent_sandbox_acceptance.command", side_effect=[b"stopped", json.dumps({"Running": True, "Pid": 123}).encode()]) as docker:
                result = settle_container("owned-id", Path(directory) / "live.json")
                self.assertFalse(result["settled"])
                self.assertFalse(result["removed"])
                self.assertEqual(docker.call_count, 2)

    def test_fixture_dynamic_form_redirect_and_download(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            payload = b"fixed-per-run-download"
            (root / "expected.json").write_text(json.dumps(self.expected))
            (root / "trial-1.txt").write_bytes(payload)
            (root / "pixels-1.png").write_bytes(pixel_code("12345678"))
            handler = type("TestFixture", (Fixture,), {"root": root})
            server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            base = f"http://127.0.0.1:{server.server_port}"
            try:
                with urllib.request.urlopen(base + "/1/start") as response:
                    page = response.read()
                self.assertNotIn(b"dynamic-secret", page)
                with urllib.request.urlopen(base + "/1/dynamic") as response:
                    self.assertEqual(json.load(response)["reading"], "dynamic-secret")
                with self.assertRaises(urllib.error.HTTPError) as failed:
                    urllib.request.urlopen(base + "/1/submit", data=b"answer=wrong")
                self.assertEqual(failed.exception.code, 422)
                failed.exception.close()
                with urllib.request.urlopen(base + "/1/submit", data=b"answer=form-secret") as response:
                    self.assertTrue(response.url.endswith("/1/complete"))
                    page = response.read()
                    self.assertIn(b"receipt-secret", page)
                    self.assertNotIn(b"12345678", page)
                with urllib.request.urlopen(base + "/1/download") as response:
                    self.assertEqual(response.read(), payload)
                records = [json.loads(line) for line in (root / "receipts.jsonl").read_text().splitlines()]
                submissions = [r["matched"] for r in records if r["kind"] == "submission"]
                self.assertEqual(submissions, [False, True])
                self.assertEqual(records[-1]["sha256"], hashlib.sha256(payload).hexdigest())
            finally:
                server.shutdown()
                server.server_close()
                thread.join()


if __name__ == "__main__":
    unittest.main()
