#!/usr/bin/env python3
"""Unit tests for conformance_check.py and release_gate.py (stdlib only)."""

import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import conformance_check  # noqa: E402
import release_gate  # noqa: E402

MATRIX = """| 编号 | 观察点 |
| --- | --- |
| CON-ARCH-02 CTR-FS-01 CTR-FS-02 | x |
| INV-01 RUL-03 | y |
"""


def tree(files):
    root = pathlib.Path(tempfile.mkdtemp())
    (root / "docs").mkdir()
    (root / "docs" / "conformance.md").write_text(MATRIX, encoding="utf-8")
    for name, text in files.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
    return root


class ConformanceCheck(unittest.TestCase):
    def run_check(self, files, *extra):
        root = tree(files)
        done = subprocess.run([sys.executable, str(HERE / "conformance_check.py"), "--root", str(root), *extra],
                              capture_output=True, text=True)
        return done.returncode, done.stdout + done.stderr

    def test_name_and_marker_claims(self):
        code, out = self.run_check({
            "internal/a/a_test.go": "package a\nfunc Test_CONARCH02_Build(t *testing.T) {}\n"
                                    "func Test_CTRFS01_02_Pair(t *testing.T) {}\n"
                                    "func Test_CONARCH02_20Builds(t *testing.T) {}\n"
                                    "// conformance: RUL-03, INV-01\nfunc TestOther(t *testing.T) {}\n"})
        self.assertEqual(code, 0, out)
        self.assertIn("5/5", out)

    def test_missing_id_fails(self):
        code, out = self.run_check({"internal/a/a_test.go": "package a\nfunc Test_CONARCH02_Build(t *testing.T) {}\n"})
        self.assertEqual(code, 1)
        self.assertIn("error: INV-01 has no test", out)

    def test_marker_for_unknown_id_fails_but_unknown_name_only_warns(self):
        files = {"internal/a/a_test.go": "package a\nfunc Test_CONARCH02_Build(t *testing.T) {}\n"
                 "func Test_CTRFS01_02_Pair(t *testing.T) {}\nfunc Test_CTRCFG02_X(t *testing.T) {}\n"
                 "// conformance: INV-01 RUL-03 CON-FAKE-99\n"}
        code, out = self.run_check(files)
        self.assertEqual(code, 1)
        self.assertIn("CON-FAKE-99", out)
        files["internal/a/a_test.go"] = files["internal/a/a_test.go"].replace(" CON-FAKE-99", "")
        code, out = self.run_check(files)
        self.assertEqual(code, 0, out)
        self.assertIn("warning:", out)
        code, _ = self.run_check(files, "--strict")
        self.assertEqual(code, 1)

    def test_marker_must_be_a_comment(self):
        code, _ = self.run_check({"internal/a/a_test.go": 'package a\nvar s = "conformance: INV-01 RUL-03"\n'
                                  "func Test_CONARCH02_Build(t *testing.T) {}\nfunc Test_CTRFS01_02_P(t *testing.T) {}\n"})
        self.assertEqual(code, 1)

    def test_hidden_directories_are_not_evidence(self):
        code, out = self.run_check({".claude/worktrees/x/a_test.go": "package a\n// conformance: INV-01\n"})
        self.assertEqual(code, 1, out)

    def test_this_repository_matrix_parses(self):
        ids = conformance_check.matrix_ids(HERE.parent / "docs" / "conformance.md")
        self.assertGreaterEqual(len(ids), 100)
        self.assertEqual(len(ids), len(set(ids)))


def run(path, status, conclusion=None):
    return {"path": path, "status": status, "conclusion": conclusion}


class ReleaseGate(unittest.TestCase):
    ALL = release_gate.REQUIRED

    def test_all_green(self):
        runs = [run(p, "completed", "success") for p in self.ALL]
        self.assertEqual(release_gate.judge(runs)[0], "ok")

    def test_one_success_among_reruns_is_enough(self):
        runs = [run(p, "completed", "success") for p in self.ALL] + [run(self.ALL[0], "completed", "failure")]
        self.assertEqual(release_gate.judge(runs)[0], "ok")

    def test_waits_for_missing_and_running(self):
        runs = [run(self.ALL[0], "completed", "success"), run(self.ALL[1], "in_progress")]
        verdict, reasons = release_gate.judge(runs)
        self.assertEqual(verdict, "wait")
        self.assertEqual(len(reasons), 2)

    def test_failure_is_final(self):
        runs = [run(self.ALL[0], "completed", "failure"), run(self.ALL[1], "queued")]
        self.assertEqual(release_gate.judge(runs)[0], "fail")

    def test_main_polls_then_gives_up(self):
        calls = []
        fetches = iter([[run(self.ALL[0], "in_progress")], [run(p, "completed", "success") for p in self.ALL]])
        code = release_gate.main(["--repo", "r", "--sha", "s", "--interval", "1"],
                                 fetch=lambda repo, sha: next(fetches), sleep=calls.append)
        self.assertEqual((code, calls), (0, [1]))
        code = release_gate.main(["--repo", "r", "--sha", "s", "--wait", "2", "--interval", "1"],
                                 fetch=lambda repo, sha: [], sleep=calls.append)
        self.assertEqual(code, 1)


from test_accept_scripts import AcceptCollect, AcceptConfig, GatewayTokenDiscovery, PtyBudget  # noqa: E402,F401  (picked up by unittest.main)


if __name__ == "__main__":
    unittest.main()
