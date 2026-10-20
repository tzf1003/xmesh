#!/usr/bin/env python3
"""Regression cases for CI coverage and conservative fallback behavior."""

import subprocess
import unittest

from ci_changes import SUITES, plan, select


class CIChanges(unittest.TestCase):
    def test_documentation_only_uses_contract_job(self):
        self.assertFalse(any(select(["README.md", "docs/operations.md"]).values()))

    def test_go_package_runs_platform_and_network_checks(self):
        selected = select(["internal/fs/download.go"])
        self.assertTrue(selected["go"] and selected["headscale"])
        self.assertFalse(selected["ui"] or selected["computer"] or selected["extended"])

    def test_go_embedded_assets_and_native_sources_run_go(self):
        for path in ["internal/config/testdata/config.json", "internal/exec/helper.s",
                     "cmd/xmesh/assets/setup.html", "meshadmin/testdata/policy.hujson"]:
            with self.subTest(path=path):
                selected = select([path])
                self.assertTrue(selected["go"] and selected["headscale"])

    def test_renderer_runs_ui_and_mcp(self):
        selected = select(["ui/src/renderer/settings.tsx"])
        self.assertTrue(selected["ui"] and selected["ui_mcp"])
        self.assertFalse(selected["go"] or selected["computer"])

    def test_schema_runs_consumers(self):
        selected = select(["docs/contracts/schemas/control.schema.json"])
        self.assertTrue(all(selected[s] for s in ("go", "headscale", "ui", "ui_mcp")))

    def test_native_shared_and_ci_inputs(self):
        for path in ["computer-native/src/main.rs", "ui/scripts/test-native-desktop.mjs",
                     "ui/src/main/update/native.ts", "internal/desktopbroker/ipc.go",
                     "scripts/package_computer.py"]:
            with self.subTest(path=path):
                self.assertTrue(select([path])["computer"])
        for path in ["go.sum", ".github/workflows/ui.yml", "scripts/release_gate.py"]:
            with self.subTest(path=path):
                self.assertTrue(all(select([path])[s] for s in SUITES))

    def test_tag_manual_and_schedule_always_run_full(self):
        for env in [{"GITHUB_REF": "refs/tags/v1.2.3", "GITHUB_EVENT_NAME": "push"},
                    {"GITHUB_EVENT_NAME": "workflow_dispatch"}, {"GITHUB_EVENT_NAME": "schedule"}]:
            with self.subTest(env=env):
                self.assertTrue(all(plan(env).values()))

    def test_diff_base_and_deleted_file_are_preserved(self):
        base = "a" * 40
        seen = []
        def diff(value):
            seen.append(value)
            return ["internal/fs/deleted_test.go"]
        selected = plan({"GITHUB_EVENT_NAME": "pull_request", "CI_BASE_SHA": base}, diff)
        self.assertEqual(seen, [base])
        self.assertTrue(selected["go"])
        self.assertFalse(selected["extended"])

    def test_unknown_base_runs_full(self):
        for base in ["", "0" * 40, "not-a-sha"]:
            with self.subTest(base=base):
                self.assertTrue(all(plan({"CI_BASE_SHA": base}).values()))
        def missing(_):
            raise subprocess.CalledProcessError(128, "git diff")
        self.assertTrue(all(plan({"CI_BASE_SHA": "a" * 40}, missing).values()))


if __name__ == "__main__":
    unittest.main()
