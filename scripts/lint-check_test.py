"""Regressions for scripts/lint-check.go and the rules in .golangci.yml; no database required."""
import json
import os
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent
ROOT = SCRIPTS.parent
LINT = os.environ.get("GOLANGCI_LINT", "golangci-lint")


class LintCheckTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.work = tempfile.TemporaryDirectory(prefix="forge-lint-check-")
        cls.addClassCleanup(cls.work.cleanup)
        cls.binary = Path(cls.work.name) / "lint-check"
        subprocess.run(["go", "build", "-o", str(cls.binary), str(SCRIPTS / "lint-check.go")], check=True)

    def setUp(self):
        self.tree = tempfile.TemporaryDirectory(prefix="forge-lint-tree-")
        self.addCleanup(self.tree.cleanup)
        self.root = Path(self.tree.name)
        self.write("go.mod", "module example.com/m\n\ngo 1.26\n")
        # The repository's configuration, but its allow-listed sites, which
        # a fixture tree does not have.
        config = (ROOT / ".golangci.yml").read_text().split("      # Allow-listed sites")[0]
        (self.root / ".golangci.yml").write_text(config)
        self.baseline({})

    def write(self, rel, source):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(source))

    def baseline(self, counts):
        self.write("scripts/lint-baseline.json", json.dumps(counts))

    def stored(self):
        return json.loads((self.root / "scripts/lint-baseline.json").read_text())

    def check(self, *args, issues=None):
        extra = []
        if issues is not None:
            self.write("issues.json", json.dumps({"Issues": issues}))
            extra = ["-issues", str(self.root / "issues.json")]
        # The allow-list's own names (Holds' own tests) are not in a fixture
        # tree; the allow-list's tests ask for its check.
        return subprocess.run([str(self.binary), "-lint", LINT, "-stale=false", *extra, *args], cwd=self.root,
                              capture_output=True, text=True, timeout=300)

    @staticmethod
    def issue(file, text="use of `time.Now` forbidden because \"clock rule: read the time through the owner's clock\"", linter="forbidigo"):
        return {"FromLinter": linter, "Text": text, "Pos": {"Filename": file, "Line": 3}}

    SLEEP = "use of `time.Sleep` forbidden because \"sleep rule: time.Sleep ignores cancellation\""

    def test_a_new_violation_fails_in_a_package_that_has_old_ones(self):
        self.baseline({"sleep": {"internal/a": 1}})
        out = self.check(issues=[self.issue("internal/a/x.go", text=self.SLEEP)])
        self.assertEqual(out.returncode, 0, out.stdout)
        out = self.check(issues=[self.issue("internal/a/x.go", text=self.SLEEP)] * 2)
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertIn("WORSE", out.stdout)

    def test_a_violation_fails_in_a_package_with_no_count_and_rules_are_apart(self):
        self.baseline({"sleep": {"internal/a": 1}})
        out = self.check(issues=[self.issue("internal/b/x.go", text=self.SLEEP)])
        self.assertEqual(out.returncode, 1, out.stdout)
        # A package's sleep count does not admit a clock read.
        out = self.check(issues=[self.issue("internal/a/x.go")])
        self.assertEqual(out.returncode, 1, out.stdout)

    def test_tighten_lowers_and_drops_but_never_raises(self):
        self.baseline({"sleep": {"internal/a": 3, "internal/gone": 1}})
        out = self.check(issues=[self.issue("internal/a/x.go", text=self.SLEEP)])
        self.assertEqual(out.returncode, 0, out.stdout)
        self.assertIn("-tighten", out.stdout)
        self.assertEqual(self.stored()["sleep"]["internal/a"], 3, "a plain run writes nothing")
        self.assertEqual(self.check("-tighten", issues=[self.issue("internal/a/x.go", text=self.SLEEP)]).returncode, 0)
        self.assertEqual(self.stored()["sleep"], {"internal/a": 1})
        out = self.check("-tighten", issues=[self.issue("internal/a/x.go", text=self.SLEEP)] * 2)
        self.assertEqual(out.returncode, 1, out.stdout)
        self.assertEqual(self.stored()["sleep"], {"internal/a": 1})

    def test_the_clock_rule_admits_no_baseline(self):
        # Any clock read fails, and a count for the rule is refused rather
        # than honoured, so one cannot be written back in.
        out = self.check(issues=[self.issue("internal/a/x.go")])
        self.assertEqual(out.returncode, 1, out.stdout)
        self.baseline({"clock": {"internal/a": 1}})
        out = self.check(issues=[self.issue("internal/a/x.go")])
        self.assertEqual(out.returncode, 2, out.stdout + out.stderr)
        self.assertIn("admits no baseline", out.stderr)
        self.baseline({"sleep": {"internal/a": 1}})
        self.assertEqual(self.check("-tighten", issues=[]).returncode, 0)
        self.assertNotIn("clock", self.stored(), "tighten writes no count for a hard rule")

    CONTEXT = "use of `context.Background` forbidden because \"context rule: use the caller's context\""

    def test_the_context_rule_admits_no_baseline(self):
        # A fresh context outside an allow-listed entry point fails, and a
        # count for the rule is refused, as for the clock.
        out = self.check(issues=[self.issue("internal/a/x.go", text=self.CONTEXT)])
        self.assertEqual(out.returncode, 1, out.stdout)
        self.baseline({"context": {"internal/a": 1}})
        out = self.check(issues=[self.issue("internal/a/x.go", text=self.CONTEXT)])
        self.assertEqual(out.returncode, 2, out.stdout + out.stderr)
        self.assertIn("admits no baseline", out.stderr)

    def test_a_run_that_could_not_see_the_code_fails(self):
        out = self.check(issues=[self.issue("internal/a/x.go", text="undefined: time.Sleep", linter="typecheck")])
        self.assertEqual(out.returncode, 2, out.stdout)
        out = self.check(issues=[self.issue("internal/a/x.go", text="use of `fmt.Println` forbidden")])
        self.assertEqual(out.returncode, 2, out.stdout)

    def fake_lint(self, report, status):
        """A golangci-lint that writes report where it is asked to and exits with status."""
        self.write("report.json", json.dumps(report))
        fake = self.root / "fake-lint"
        fake.write_text("#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in --output.json.path=*) cp '%s' \"${a#*=}\";; esac; done\n"
                        "echo 'analysis failed' >&2\nexit %d\n" % (self.root / "report.json", status))
        fake.chmod(0o755)
        return str(fake)

    def test_a_failed_analysis_is_never_fewer_violations(self):
        # A report golangci-lint wrote before failing, or one that names its
        # own failure, is not a count: the check fails and -tighten writes
        # nothing, whatever the report holds.
        self.baseline({"sleep": {"internal/a": 1}})
        before = self.stored()
        for report, status in [({"Issues": [], "Report": {"Error": "analysis failed"}}, 3),
                               ({"Issues": []}, 3),
                               ({"Issues": [], "Report": {"Error": "analysis failed"}}, 1)]:
            with self.subTest(report=report, status=status):
                lint = self.fake_lint(report, status)
                for args in ([], ["-tighten"]):
                    out = subprocess.run([str(self.binary), "-lint", lint, "-stale=false", *args], cwd=self.root,
                                         capture_output=True, text=True, timeout=60)
                    self.assertEqual(out.returncode, 2, out.stdout + out.stderr)
                    self.assertEqual(self.stored(), before)
        # The control: the same fake reporting success is read.
        out = subprocess.run([str(self.binary), "-lint", self.fake_lint({"Issues": []}, 0), "-stale=false"], cwd=self.root,
                             capture_output=True, text=True, timeout=60)
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)

    def test_two_violations_on_one_line_count_twice(self):
        self.write("internal/a/a.go", """\
            package a

            import "time"

            func Wait() { time.Sleep(time.Until(time.Now())) }
            """)
        self.baseline({"sleep": {"internal/a": 1}})
        out = self.check()
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        lines = [" ".join(l.split()) for l in out.stdout.splitlines()]
        self.assertIn("lint clock internal/a 2 baseline 0 WORSE", lines)

    def test_the_rules_as_configured(self):
        # One golangci-lint run over a small module with the repository's
        # configuration: what each rule catches, and where it does not apply.
        self.write("internal/a/a.go", """\
            package a

            import (
                "context"
                clockless "time"
            )

            func Renamed() clockless.Time { return clockless.Now() }
            func Elapsed(t clockless.Time) clockless.Duration { return clockless.Since(t) }
            func Nap(ctx context.Context) { clockless.Sleep(clockless.Millisecond); _ = ctx }
            """)
        self.write("internal/a/a_test.go", """\
            package a

            import (
                "testing"
                "time"
            )

            func TestNap(t *testing.T) { _ = time.Now(); time.Sleep(time.Millisecond) }
            """)
        # A test's clock under another import name is still a test's clock;
        # its sleep is still a sleep.
        self.write("internal/a/renamed_test.go", """\
            package a

            import clockless "time"

            var Started = clockless.Now
            var Doze = clockless.Sleep
            """)
        self.write("internal/a/atest/renamed.go", """\
            package atest

            import clockless "time"

            var Started = clockless.Since
            """)
        self.write("internal/a/atest/helper.go", """\
            package atest

            import "time"

            func Deadline() time.Time { return time.Now().Add(time.Second) }
            """)
        self.write("internal/clock/clock.go", """\
            package clock

            import "time"

            var Real = time.Now
            """)
        out = self.check("-v")
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        lines = [" ".join(l.split()) for l in out.stdout.splitlines()]
        self.assertIn("lint sleep internal/a 3 baseline 0 WORSE", lines)
        self.assertTrue(any("renamed_test.go:6" in l and "clockless.Sleep" in l for l in lines), out.stdout)
        self.assertFalse(any("renamed_test.go:5" in l or "atest/renamed.go" in l for l in lines), out.stdout)
        self.assertIn("lint clock internal/a 2 baseline 0 WORSE", lines)
        self.assertTrue(any("internal/a/a_test.go:8" in l and "time.Sleep" in l for l in lines), out.stdout)
        self.assertFalse(any("a_test.go:8" in l and "time.Now" in l for l in lines), out.stdout)
        self.assertFalse(any("atest" in l or "internal/clock" in l for l in lines), out.stdout)


    HOLDS = """\
        package polltest

        import (
            "context"
            "time"
        )

        func Holds(ctx context.Context, d time.Duration, cond func() bool) bool { return true }
        """

    ALLOWED = """
      # The fixture's one site of unowned asynchrony.
      - path: '^internal/a/a_test\\.go$'
        linters: [forbidigo]
        text: 'forbidden because "absence rule:'
        source: 'polltest\\.Holds\\(ctx, time\\.Second'
"""

    def holds_tree(self, allow=""):
        self.write("internal/poll/polltest/polltest.go", self.HOLDS)
        self.write("internal/poll/polltest/polltest_test.go", """\
            package polltest

            import (
                "context"
                "testing"
            )

            func TestHolds(t *testing.T) { Holds(context.Background(), 0, nil) }
            """)
        self.write("internal/a/a_test.go", """\
            package a

            import (
                "context"
                "testing"
                "time"

                "example.com/m/internal/poll/polltest"
            )

            func TestQuiet(t *testing.T) {
                ctx := context.Background()
                _ = polltest.Holds(ctx, time.Second, func() bool { return false })
                _ = polltest.Holds(ctx, time.Minute, func() bool { return false })
            }
            """)
        with open(self.root / ".golangci.yml", "a") as f:
            f.write(allow)

    def test_a_wait_for_nothing_is_held_to_its_allow_list(self):
        # Every call of Holds is a violation but the allow-listed site and
        # Holds' own tests.
        self.holds_tree(self.ALLOWED)
        out = self.check("-v", "-stale=true")
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        lines = [" ".join(l.split()) for l in out.stdout.splitlines()]
        self.assertIn("lint absence internal/a 1 baseline 0 WORSE", lines)
        self.assertTrue(any("a_test.go:14" in l for l in lines), out.stdout)
        self.assertFalse(any("a_test.go:13" in l or "polltest" in l.split(":")[0] for l in lines if l.startswith("internal")), out.stdout)

    def test_an_allow_list_entry_names_a_live_site_with_its_reason(self):
        # A site that is gone, and an entry with no reason, both fail before
        # any lint runs.
        self.holds_tree(self.ALLOWED.replace("time\\.Second", "time\\.Hour"))
        out = self.check("-stale=true")
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        self.assertIn("matches 0 calls of polltest.Holds", out.stdout)
        self.setUp()
        self.holds_tree(self.ALLOWED.replace("      # The fixture's one site of unowned asynchrony.\n", ""))
        out = self.check("-stale=true")
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        self.assertIn("no reason above it", out.stdout)

    CONTEXT_ALLOWED = """
      # The fixture's process root.
      - path: '^internal/a/a\\.go$'
        linters: [forbidigo]
        text: 'forbidden because "context rule:'
        source: 'root := context\\.Background\\(\\)'
"""

    def context_tree(self, allow=""):
        # The configuration's own directory entry (Holds' tests) must exist.
        self.write("internal/poll/polltest/polltest.go", self.HOLDS)
        self.write("internal/a/a.go", """\
            package a

            import "context"

            func Serve() {
                root := context.Background()
                Work(root)
            }

            func Work(ctx context.Context) { _ = ctx; Later() }

            func Later() { _ = context.TODO() }
            """)
        self.write("internal/a/a_test.go", """\
            package a

            import (
                "context"
                "testing"
            )

            func TestWork(t *testing.T) { Work(context.Background()) }
            """)
        self.write("internal/a/atest/helper.go", """\
            package atest

            import "context"

            func Ctx() context.Context { return context.Background() }
            """)
        with open(self.root / ".golangci.yml", "a") as f:
            f.write(allow)

    def test_a_fresh_context_is_held_to_its_allow_list(self):
        # A context made mid-operation is counted; the allow-listed entry
        # point, tests and test helper packages are not.
        self.context_tree(self.CONTEXT_ALLOWED)
        out = self.check("-v", "-stale=true")
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        lines = [" ".join(l.split()) for l in out.stdout.splitlines()]
        self.assertIn("lint context internal/a 1 baseline 0 WORSE", lines)
        self.assertTrue(any("internal/a/a.go:12" in l and "context.TODO" in l for l in lines), out.stdout)
        self.assertFalse(any("a.go:6" in l or "a_test.go" in l or "atest" in l for l in lines), out.stdout)
        # A gone entry point fails before any lint runs.
        self.setUp()
        self.context_tree(self.CONTEXT_ALLOWED.replace("root :=", "base :="))
        out = self.check("-stale=true")
        self.assertEqual(out.returncode, 1, out.stdout + out.stderr)
        self.assertIn("matches 0 calls of context.Background( or context.TODO(", out.stdout)


if __name__ == "__main__":
    unittest.main()
