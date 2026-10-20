"""scripts/names-check.py: an old name above its baseline fails, a count that
fell is recorded only with --tighten, and allowlisted kept names and history
files are not counted."""
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("names_check", HERE / "names-check.py")
names = importlib.util.module_from_spec(spec)
spec.loader.exec_module(names)


class NamesCheck(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "scripts").mkdir()
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)

    def write(self, path, text):
        p = self.root / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        subprocess.run(["git", "-C", str(self.root), "add", path], check=True)

    def baseline(self, **counts):
        self.write("scripts/names-baseline.json", json.dumps({s: counts.get(s, 0) for s in names.SURFACES}))

    def run_check(self, *args):
        out = StringIO()
        with redirect_stdout(out):
            code = names.main(["--root", str(self.root), *args])
        return code, out.getvalue()

    # Each surface catches its old name: one planted per surface, all at a
    # zero baseline, fails on every surface.
    def test_a_new_old_name_in_each_surface_fails(self):
        planted = {
            "command": "run forged -addr :7777",
            "cli-invocation": "then run forge check",
            "env": "export FORGE_URL=x",
            "repo-folder": "edit .forge/app.yaml",
            "api-version": "apiVersion: forge.flokkit.io/v1",
            "static-path": "load /_forge/env.js",
            "header": "set X-Forge-Actor",
            "trailer-or-header": "Forge-Operation: 1",
            "refs": "push refs/forge/x",
            "branch": "on forge/change-12",
            "install-path": "data in /var/lib/forge",
            "token-or-cookie": "cookie forge_session",
            "metric": "forge_http_requests_total",
            "scope": "grant forge:user:read",
            "storage-key": "localStorage forge-theme",
            "export-and-ticket": "user x-forge-ticket",
            "gate": "GATE_FORGE=1",
            "skill": ".claude/skills/forge",
            "agent-recipe": "build deploy/forge-agent",
            "quoted-name": 'exec.Command("forge", "help")',
            "commit-subject": "forge: update dependencies",
            "mount-and-builder": "/forge/in/token",
            "public-field": "forgeFault",
        }
        self.assertEqual(set(planted), set(names.SURFACES))
        self.baseline()
        self.write("doc.md", "\n".join(planted.values()))
        code, out = self.run_check()
        self.assertEqual(code, 1, out)
        for s in names.SURFACES:
            self.assertRegex(out, rf"names {s} +[1-9]\d*  baseline +0  WORSE", s)

    # History (plans, reviews, earlier release notes) is not counted.
    def test_history_is_not_counted(self):
        self.baseline()
        self.write("private/design/plans/old-2026-09-01.md", "run forged with FORGE_DATA")
        self.write("releases/v1.0.0-rc.5.md", "forge check")
        code, out = self.run_check()
        self.assertEqual(code, 0, out)

    # A count below its baseline passes; --tighten records it, so the
    # baseline only ever falls.
    def test_a_fall_is_recorded_only_with_tighten(self):
        self.baseline(env=3)
        self.write("doc.md", "FORGE_URL")
        code, out = self.run_check()
        self.assertEqual(code, 0, out)
        self.assertIn("lower", out)
        self.assertEqual(json.loads((self.root / "scripts/names-baseline.json").read_text())["env"], 3)
        code, out = self.run_check("--tighten")
        self.assertEqual(code, 0, out)
        self.assertEqual(json.loads((self.root / "scripts/names-baseline.json").read_text())["env"], 1)

    # An allowlisted kept name is not counted; the same name elsewhere is.
    def test_an_allowlisted_kept_name_passes(self):
        saved = list(names.ALLOWED)
        self.addCleanup(lambda: names.ALLOWED.__setitem__(slice(None), saved))
        names.ALLOWED[:] = [("trailer-or-header", r"^history/", r"Forge-Operation", "old commits keep their trailers")]
        self.baseline()
        self.write("history/reader.go", "Forge-Operation")
        code, out = self.run_check()
        self.assertEqual(code, 0, out)
        self.write("other.go", "Forge-Operation")
        code, out = self.run_check()
        self.assertEqual(code, 1, out)

    def test_each_table_name_fails_at_zero(self):
        # Independent examples from the plan, rather than one per regex key.
        examples = [
            'exec.Command("forge", "help")',
            "command = 'forge'",
            'window.__FORGE_ENV__',
            'forge: update dependencies',
            "prefix = 'forge_'",
            'x-forge-needs: [redis]',
            'x-forge-used-by: cli',
            '/forge/in/token',
            '/forge/state/out',
            '/forge-cache',
            'source=forge-local',
            'actor=forge-system',
            'forgeFault',
            'store=forge-disk',
            'trusts-forge-front-door',
            'capacity.forge',
        ]
        for example in examples:
            with self.subTest(example=example):
                self.baseline()
                self.write("surface.txt", example)
                code, out = self.run_check()
                self.assertEqual(code, 1, example + "\n" + out)

    def test_kept_container_names_are_not_debt(self):
        self.baseline()
        # Existing kept runtime names, copied from their production owners.
        self.write("internal/console/pi/runtime.go",
                   'func ContainerName(id string) string { return "forge-console-" + id }')
        self.write("internal/worker/checks.go", 'Name: "forge-check-" + id,')
        code, out = self.run_check()
        self.assertEqual(code, 0, out)

    def test_an_old_path_fails_at_zero(self):
        for path in [".claude/skills/forge/SKILL.md", "deploy/forge-agent/Dockerfile",
                     "cmd/forged/main.go", "cmd/forge/main.go", ".forge/app.yaml"]:
            with self.subTest(path=path):
                self.baseline()
                self.write(path, "# AppTown\n")
                code, out = self.run_check()
                self.assertEqual(code, 1, path + "\n" + out)
                (self.root / path).unlink()
                subprocess.run(["git", "-C", str(self.root), "rm", "--cached", "-q", path], check=True)

    def test_kept_database_and_package_names_are_not_debt(self):
        examples = {
            "internal/forge/dbboot.go": 'builtinDBName = "forge"\n',
            "scripts/codebase-shape.py": 'parts = ["internal", "forge", *parts[1:]]\n',
        }
        for path, text in examples.items():
            with self.subTest(path=path):
                self.baseline()
                self.write(path, text)
                code, out = self.run_check()
                try:
                    self.assertEqual(code, 0, out)
                finally:
                    (self.root / path).unlink()
                    subprocess.run(["git", "-C", str(self.root), "rm", "--cached", "-q", path], check=True)

    def test_kept_package_assertion_is_not_debt(self):
        self.baseline()
        self.write("scripts/shape-check_test.py",
                   'self.assertEqual(self.shape.top("flokkit.io/forge/internal/forge"), "forge")')
        code, out = self.run_check()
        self.assertEqual(code, 0, out)

    def test_frozen_release_history_is_not_debt(self):
        self.baseline()
        self.write("internal/forge/testdata/upgrade/1.0.0-rc.5/release-schema.sql",
                   "about TEXT NOT NULL DEFAULT 'forge',\n")
        self.write("internal/forge/testdata/upgrade/1.0.0-rc.5/reads.json",
                   '{"branch":"forge/change-123", "path":".forge/app.yaml"}')
        code, out = self.run_check()
        self.assertEqual(code, 0, out)


if __name__ == "__main__":
    unittest.main()
