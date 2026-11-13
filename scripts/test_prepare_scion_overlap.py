"""Offline native-CLI contract and uncertain-write checks; no real remote access."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
PREPARE = ROOT / ".teamx/teams/scion-parallel-development/overlap/prepare.py"
SOURCE = "c1" + "1" * 64
HEAD = "c1" + "2" * 64

FAKE = r'''
import json,os,pathlib,sys
config=pathlib.Path(os.environ["SCION_CONFIG_FILE"])
state=json.loads(config.read_text());args=sys.argv[1:]
state.setdefault("calls",[]).append(args)
state["model_secret_inherited"]="EXPERIMENT_API_KEY" in os.environ
config.write_text(json.dumps(state))
def emit(value):print(json.dumps(value))
if args==["--version"]:
 print("scn 0.1.13");sys.exit(0)
assert args[:7]==["--endpoint","https://scion.opencsg.com","--hub","https://scion.opencsg.com","--repo","OpenCSG/teamx-demo","--json"],args
args=args[7:];command=args[0]
if command=="whoami":emit({"subject":{"identity":"other","anonymous":"anonymous"}.get(state.get("mode"),"csgship"),"endpoint":"https://other.invalid" if state.get("mode")=="instance" else "https://scion.opencsg.com","auth_required":True})
elif args[:2]==["repo","info"]:emit({"name":"OpenCSG/teamx-demo","archived":False})
elif args[:2]==["branch","ls"]:
 branches=[{"name":"main","changeset":state["source"]}]
 branch=state.get("branch")
 if state.get("mode")=="existing":branch="teamx/overlap-base/offline"
 if branch:branches.append({"name":branch,"changeset":state["head"] if state.get("published") else state["source"]})
 emit({"branches":branches})
elif command=="clone":
 p=pathlib.Path(args[2]);(p/".scion").mkdir(parents=True)
 (p/".scion/state.json").write_text(json.dumps({"endpoint":"https://scion.opencsg.com","repo":"OpenCSG/teamx-demo","base":state["source"]}))
 (p/"README.md").write_text("original repository file\n");emit({"cloned":True})
elif command=="status":emit({"ref":"main","base":state["source"],"pending":None,"added":[],"modified":[],"deleted":[],"conflicted":[]})
elif command=="switch":
 assert args[1]=="-c";state["branch"]=args[2];emit({"created":args[2]})
elif command=="commit":
 assert args[-3:]==["overlap_todo.py","test_overlap_todo.py","OVERLAP-TODO.md"]
 state["files"]={name:pathlib.Path(name).read_text() for name in args[-3:]};emit({"committed":state["head"]})
elif command=="land":
 state["published"]=True;config.write_text(json.dumps(state))
 if state.get("mode")=="unknown":print("controlled lost response",file=sys.stderr);sys.exit(4)
 emit({"landed":state["head"]})
elif command=="show":emit({"commit":{"id":state["head"],"author":"TeamX overlap base","email":"overlap-base@teamx.local","parents":[state["source"]]}})
elif command=="reflog":emit({"entries":[{"changeset":state["head"],"actor":"csgship","move":"fast-forward"}]})
elif command=="cat":
 head,name=args[1].split(":",1);content=state["files"][name]
 if state.get("mode")=="content" and name=="overlap_todo.py":content+="\n# changed remotely\n"
 emit({"revision":head,"path":name,"content":content,"binary":False})
else:raise AssertionError(args)
config.write_text(json.dumps(state))
'''


class PrepareOverlap(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.output = self.root / "output"
        self.config = self.root / "fake-scion.json"
        self.config.write_text(json.dumps({"source": SOURCE, "head": HEAD}))
        self.scn = self.root / "scn"
        self.scn.write_text(f"#!{sys.executable}\n" + FAKE)
        self.scn.chmod(0o700)

    def run_prepare(self, *, publish=True, mode=""):
        state = json.loads(self.config.read_text())
        state["mode"] = mode
        self.config.write_text(json.dumps(state))
        env = {**os.environ, "SCION_CONFIG_FILE": str(self.config), "EXPERIMENT_API_KEY": "must-not-be-forwarded"}
        return subprocess.run([sys.executable, str(PREPARE), "--scn", str(self.scn),
                               "--run-id", "offline", "--output", str(self.output),
                               *(["--publish"] if publish else [])],
                              env=env, capture_output=True, text=True, timeout=60)

    def state(self):
        return json.loads(self.config.read_text())

    def report(self):
        return json.loads((self.output / "report.json").read_text())

    def test_local_preparation_runs_real_base_tests_without_cli_or_remote(self):
        result = self.run_prepare(publish=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.report()["status"], "local_only")
        self.assertNotIn("calls", self.state())
        self.assertIn("Ran 7 tests", (self.output / "base-tests.txt").read_text())
        self.assertIn("当前仅本地准备", (self.output / "prompt.txt").read_text())

    def test_publish_checks_exact_remote_contents_and_generates_resolved_prompt(self):
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.report()["head"], HEAD)
        self.assertEqual(self.report()["status"], "published")
        prompt = (self.output / "prompt.txt").read_text()
        self.assertIn(HEAD, prompt)
        self.assertNotIn("$head", prompt)
        self.assertIn("teamx/overlap-base/offline", prompt)
        self.assertIn("Ran 7 tests", (self.output / "published-tests.txt").read_text())
        self.assertFalse(self.state()["model_secret_inherited"])
        calls = [args[7:] for args in self.state()["calls"] if args != ["--version"]]
        self.assertEqual([args for args in calls if args[0] == "switch"], [["switch", "-c", "teamx/overlap-base/offline"]])
        self.assertEqual(sum(args == ["land"] for args in calls), 1)
        self.assertFalse(any(args[0] in ("review", "login", "clean", "reset") for args in calls))

    def test_identity_mismatch_stops_before_clone_or_branch_creation(self):
        result = self.run_prepare(mode="identity")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.report()["remote_writes_may_have_started"])
        self.assertEqual(self.report()["whoami"]["subject"], "other")
        self.assertIn("expected 'csgship', got 'other'", result.stderr)
        self.assertFalse(any("clone" in call or "switch" in call for call in self.state()["calls"]))

    def test_anonymous_is_reported_as_logged_out_with_reuse_guidance(self):
        result = self.run_prepare(mode="anonymous")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not logged in", result.stderr)
        self.assertIn("--credentials-file", result.stderr)
        self.assertEqual(self.report()["whoami"]["subject"], "anonymous")
        self.assertFalse(self.report()["remote_writes_may_have_started"])
        self.assertFalse(any("clone" in call or "switch" in call for call in self.state()["calls"]))

    def test_instance_mismatch_reports_actual_instance(self):
        result = self.run_prepare(mode="instance")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("instance mismatch", result.stderr)
        self.assertEqual(self.report()["whoami"]["endpoint"], "https://other.invalid")
        self.assertFalse(any("clone" in call or "switch" in call for call in self.state()["calls"]))

    def test_existing_remote_branch_is_not_reused(self):
        result = self.run_prepare(mode="existing")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("already exists", result.stderr)
        self.assertFalse(any("switch" in call for call in self.state()["calls"]))

    def test_unknown_land_is_not_retried_and_local_state_is_preserved(self):
        result = self.run_prepare(mode="unknown")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.report()["phase"], "publishing")
        self.assertTrue(self.report()["remote_writes_may_have_started"])
        self.assertTrue((self.output / "workspace/overlap_todo.py").is_file())
        self.assertFalse((self.output / "prompt.txt").exists())
        self.assertEqual(sum(call[-1:] == ["land"] for call in self.state()["calls"]), 1)
        before = self.state()["calls"]
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.assertEqual(self.state()["calls"], before)

    def test_changed_remote_file_never_produces_success_prompt(self):
        result = self.run_prepare(mode="content")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.report()["status"], "failed")
        self.assertIn("remote content mismatch", result.stderr)
        self.assertFalse((self.output / "prompt.txt").exists())


if __name__ == "__main__":
    unittest.main()
