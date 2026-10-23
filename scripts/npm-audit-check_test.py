"""Regressions for scripts/npm-audit-check.py: no unexcused high or critical npm advisory."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CHECK = Path(__file__).with_name("npm-audit-check.py")

# A stand-in npm: records its arguments and prints the report.json (or
# raw.txt) beside the lockfile it was run for.
STUB = """#!/usr/bin/env python3
import sys
from pathlib import Path
Path("args.txt").write_text(" ".join(sys.argv[1:]))
raw = Path("raw.txt")
print(raw.read_text() if raw.exists() else Path("report.json").read_text())
sys.exit(1)
"""


def report(*advisories):
    vulns = {}
    for ghsa, name, severity in advisories:
        vulns[name] = {"name": name, "severity": severity, "via": [
            {"source": 1, "name": name, "title": f"{name} is broken", "severity": severity,
             "url": f"https://github.com/advisories/{ghsa}"}]}
        # A dependent's entry names the package as a string, not an advisory.
        vulns[name + "-dependent"] = {"name": name + "-dependent", "severity": severity, "via": [name]}
    return json.dumps({"auditReportVersion": 2, "vulnerabilities": vulns, "metadata": {}})


HIGH = "GHSA-2222-3333-4444"
CRIT = "GHSA-5555-6666-7777"


class NpmAuditCheckTest(unittest.TestCase):
    def setUp(self):
        self.tree = tempfile.TemporaryDirectory(prefix="apptown-npm-audit-check-")
        self.addCleanup(self.tree.cleanup)
        self.root = Path(self.tree.name)
        self.write("stub-npm", STUB).chmod(0o755)
        for d in ("web", "stock/seeds/game"):
            self.write(d + "/package-lock.json", "{}")
            self.write(d + "/report.json", report())
        self.write("scripts/npm-audit-exceptions.txt", "# header\n")

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        return p

    def run_check(self):
        env = dict(os.environ, APPTOWN_NPM=str(self.root / "stub-npm"))
        return subprocess.run([sys.executable, str(CHECK), str(self.root)], capture_output=True, text=True, env=env)

    def assertPasses(self):
        out = self.run_check()
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    def assertFails(self, *want):
        out = self.run_check()
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        for w in want:
            self.assertIn(w, out.stdout)

    def test_clean_reports_pass(self):
        self.assertPasses()

    # Kills: web audited with dev dependencies (failing on tools that do not
    # ship) or a seed audited without them (missing what new projects get).
    def test_web_omits_dev_and_seeds_include_it(self):
        self.assertPasses()
        self.assertIn("--omit=dev", (self.root / "web/args.txt").read_text())
        self.assertNotIn("--omit=dev", (self.root / "stock/seeds/game/args.txt").read_text())
        for d in ("web", "stock/seeds/game"):
            self.assertIn("--package-lock-only", (self.root / d / "args.txt").read_text())

    # Kills: the severity threshold dropped, or npm's non-zero exit on any
    # finding read as failure.
    def test_high_and_critical_fail_and_lower_pass(self):
        self.write("web/report.json", report(("GHSA-8888-9999-cccc", "lodash", "moderate"), ("GHSA-ffff-gggg-hhhh", "ms", "low")))
        self.assertPasses()
        self.write("web/report.json", report((HIGH, "dompurify", "high")))
        self.write("stock/seeds/game/report.json", report((CRIT, "vite", "critical")))
        self.assertFails(f"web/package-lock.json: dompurify: high: {HIGH}",
                         f"stock/seeds/game/package-lock.json: vite: critical: {CRIT}")

    def test_a_dated_exception_lets_its_advisory_pass(self):
        self.write("web/report.json", report((HIGH, "dompurify", "high")))
        self.write("scripts/npm-audit-exceptions.txt", f"{HIGH}  2026-10-23  no fixed version; we do not call the affected API\n")
        self.assertPasses()

    # Kills: an exception outliving its advisory.
    def test_a_stale_exception_fails(self):
        self.write("scripts/npm-audit-exceptions.txt", f"{HIGH}  2026-10-23  no fixed version\n")
        self.assertFails("scripts/npm-audit-exceptions.txt:1: no audited lockfile reports")

    # Kills: a blanket or undated, unexplained line accepted.
    def test_a_malformed_exception_fails(self):
        self.write("web/report.json", report((HIGH, "dompurify", "high")))
        for line in ("*", HIGH, f"{HIGH} 2026-10-23", f"{HIGH} yesterday because", "dompurify 2026-10-23 because"):
            self.write("scripts/npm-audit-exceptions.txt", line + "\n")
            self.assertFails("scripts/npm-audit-exceptions.txt:1: a line is one advisory's GHSA id", f"dompurify: high: {HIGH}")

    # Kills: an unreachable advisory service, or output npm could not
    # finish, read as a clean audit.
    def test_could_not_check_fails(self):
        self.write("web/raw.txt", json.dumps({"message": "request to https://registry.npmjs.org/-/npm/v1/security/advisories/bulk failed", "error": {"summary": ""}}))
        self.assertFails("web/package-lock.json: could not check: request to https://registry.npmjs.org")
        self.write("web/raw.txt", "not json")
        self.assertFails("web/package-lock.json: could not check")
        self.write("web/raw.txt", json.dumps({"vulnerabilities": {}}))
        self.assertFails("web/package-lock.json: could not check")

    # Kills: an exception called stale because an unchecked lockfile is
    # where its advisory would have been reported.
    def test_no_stale_verdict_when_a_lockfile_was_not_checked(self):
        self.write("scripts/npm-audit-exceptions.txt", f"{HIGH}  2026-10-23  no fixed version\n")
        self.write("web/raw.txt", "not json")
        out = self.run_check()
        self.assertEqual(out.returncode, 1)
        self.assertNotIn("any more", out.stdout)


if __name__ == "__main__":
    unittest.main()
