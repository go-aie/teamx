"""A timeout/failed stop must not discard unresolved execution ownership."""
import json
import subprocess
import unittest
from unittest.mock import patch

import run


class CleanupOwnershipTests(unittest.TestCase):
    def inspect(self, running, pid):
        return subprocess.CompletedProcess([], 0, stdout=json.dumps({"Running": running, "Pid": pid}))

    def test_stop_timeout_still_requires_independent_exit_evidence(self):
        with patch.object(run.subprocess, "run", side_effect=subprocess.TimeoutExpired("docker stop", 25)), \
                patch.object(run, "command", return_value=self.inspect(True, 42)) as command:
            receipt = run.settle("owned-probe")
        self.assertFalse(receipt["settled"])
        self.assertFalse(receipt["removed"])
        self.assertEqual(command.call_count, 1)
        self.assertEqual(command.call_args.args[:3], ("docker", "inspect", "--format"))

    def test_exited_flag_with_live_pid_does_not_authorize_removal(self):
        with patch.object(run.subprocess, "run"), \
                patch.object(run, "command", return_value=self.inspect(False, 42)) as command:
            receipt = run.settle("owned-probe")
        self.assertFalse(receipt["settled"])
        self.assertFalse(receipt["removed"])
        self.assertEqual(command.call_count, 1)

    def test_inspection_failure_retains_ownership(self):
        with patch.object(run.subprocess, "run"), \
                patch.object(run, "command", side_effect=subprocess.CalledProcessError(1, "inspect")):
            receipt = run.settle("owned-probe")
        self.assertFalse(receipt["settled"])
        self.assertFalse(receipt["removed"])

    def test_failed_stop_can_settle_after_verified_exit(self):
        with patch.object(run.subprocess, "run", return_value=subprocess.CompletedProcess([], 1)), \
                patch.object(run, "command", side_effect=[self.inspect(False, 0), None]) as command:
            receipt = run.settle("owned-probe")
        self.assertTrue(receipt["settled"])
        self.assertTrue(receipt["removed"])
        self.assertEqual(command.call_args.args, ("docker", "rm", "owned-probe"))

    def test_remove_failure_retains_object_and_data(self):
        with patch.object(run.subprocess, "run"), \
                patch.object(run, "command", side_effect=[self.inspect(False, 0), subprocess.CalledProcessError(1, "rm")]):
            receipt = run.settle("owned-probe")
        self.assertTrue(receipt["settled"])
        self.assertFalse(receipt["removed"])


if __name__ == "__main__":
    unittest.main()
