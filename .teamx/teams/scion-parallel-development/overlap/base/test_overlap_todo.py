import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("overlap_todo.py")


class TodoCLI(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.data = Path(self.directory.name) / "tasks.json"

    def run_cli(self, *args, success=True):
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--file", str(self.data), *args],
            capture_output=True, text=True,
        )
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result

    def test_empty_list_does_not_create_file(self):
        self.assertEqual(self.run_cli("list").stdout, "")
        self.assertFalse(self.data.exists())

    def test_add_and_list_chinese_title(self):
        self.assertEqual(self.run_cli("add", "买牛奶").stdout.strip(), "1")
        self.assertEqual(self.run_cli("list").stdout.strip(), "1 [ ] 买牛奶")

    def test_identifiers_are_unique_across_processes(self):
        self.run_cli("add", "first")
        self.assertEqual(self.run_cli("add", "second").stdout.strip(), "2")
        self.assertEqual(len(self.run_cli("list").stdout.splitlines()), 2)

    def test_done_is_persistent_and_idempotent(self):
        self.run_cli("add", "finish")
        self.run_cli("done", "1")
        self.run_cli("done", "1")
        self.assertEqual(self.run_cli("list").stdout.strip(), "1 [x] finish")

    def test_unknown_id_does_not_modify_data(self):
        self.run_cli("add", "keep")
        before = self.data.read_bytes()
        self.run_cli("done", "9", success=False)
        self.assertEqual(self.data.read_bytes(), before)

    def test_list_preserves_existing_fields_and_data(self):
        original = {"version": 1, "next_id": 3, "tasks": [
            {"id": 2, "title": "second", "done": False, "note": "keep"},
            {"id": 1, "title": "first", "done": True},
        ]}
        self.data.write_text(json.dumps(original), encoding="utf-8")
        before = self.data.read_bytes()
        self.assertEqual(self.run_cli("list").stdout.splitlines(), ["1 [x] first", "2 [ ] second"])
        self.assertEqual(self.data.read_bytes(), before)

    def test_invalid_arguments_fail(self):
        for arguments in (("add", "  "), ("done", "oops"), ("unknown",)):
            with self.subTest(arguments=arguments):
                self.run_cli(*arguments, success=False)
        self.assertFalse(self.data.exists())


if __name__ == "__main__":
    unittest.main()
