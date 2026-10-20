"""Regressions for scripts/docs-current.py: current guides carry no dated names, status lines or public links into private/."""
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CHECK = Path(__file__).with_name("docs-current.py")


def run(files):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        for name, text in files.items():
            p = root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(text)
        r = subprocess.run([sys.executable, str(CHECK), str(root)], capture_output=True, text=True)
        return r.returncode, r.stdout


class DocsCurrent(unittest.TestCase):
    def test_a_current_guide_passes(self):
        code, out = run({
            "design/subsystems/jobs.md": "# Jobs\n\nJobs run on River ([decision](../decisions/x.md)).\n",
            "design/decisions/x.md": "# X\n",
            "CLAUDE.md": "# Guide\n",
        })
        self.assertEqual(code, 0, out)

    def test_working_papers_may_be_dated_and_say_anything(self):
        code, out = run({
            "private/design/plans/thing-2026-10-19.md": "Status: proposed\n[r](../reviews/thing-2026-10-19.md)\n",
            "private/design/reviews/thing-2026-10-19.md": "> Superseded by later work\n",
        })
        self.assertEqual(code, 0, out)

    def test_a_dated_name_fails(self):
        for name in ("design/decisions/policy-2026-09.md", "design/operations/rollout-20260910/README.md",
                     "design/qualification/proof-2026-10-01.md"):
            code, out = run({name: "# A\n"})
            self.assertEqual(code, 1, name)
            self.assertIn("has a date in it", out)

    def test_a_link_into_private_fails(self):
        code, out = run({
            "design/subsystems/a.md": "See the [plan](../../private/design/plans/a-2026-10-01.md).\n",
            "design/architecture/b.md": "[earlier](../../private/design/reviews/b.md#why)\n",
            "design/operations/c.md": "[demo](../../private/design/operations/do-demo.md)\n",
            "CLAUDE.md": "[work](private/design/plans/future-stuff.md)\n",
        })
        self.assertEqual(code, 1)
        self.assertEqual(out.count("links into private/"), 4, out)

    def test_a_link_into_private_inside_a_private_block_passes(self):
        # CLAUDE.md keeps the maintainers' navigation in private blocks, which
        # the public export drops.
        code, out = run({"CLAUDE.md": "# Guide\n<!-- private -->\n[position](private/design/position/position.md)\n<!-- /private -->\n"})
        self.assertEqual(code, 0, out)

    def test_ordinary_markdown_link_forms_into_working_papers_fail(self):
        cases = {
            "inline with title": '[plan](../../private/design/plans/example.md "Plan")',
            "reference": '[plan][policy]\n\n[policy]: ../../private/design/plans/example.md',
        }
        for name, link in cases.items():
            with self.subTest(name=name):
                code, out = run({"design/subsystems/a.md": link + "\n"})
                self.assertEqual(code, 1, out)
                self.assertIn("links into private/", out)

    def test_a_status_line_fails(self):
        code, out = run({"design/subsystems/a.md": "# A\n\nStatus: current. Checked on 18 September.\n",
                         "design/decisions/b.md": "> Superseded in part, 17 October.\n"})
        self.assertEqual(code, 1)
        self.assertEqual(out.count("a status line"), 2, out)

    def test_outside_links_and_status_words_mid_line_pass(self):
        code, out = run({"design/subsystems/a.md":
                         "A step's status: done. See [the docs](https://example.com/plans/x) and "
                         "[stock](../../stock/docs/guides/a.md).\n"})
        self.assertEqual(code, 0, out)

    def test_a_shipped_guide_is_held_to_the_same_rules(self):
        code, out = run({"stock/docs/guides/a.md": "See the [plan](../../../private/design/plans/a.md).\n",
                         "stock/docs/guides/b-2026-10-19.md": "# B\n"})
        self.assertEqual(code, 1)
        self.assertIn("links into private/", out)
        self.assertIn("has a date in it", out)

    def test_a_shipped_guide_calling_the_product_forge_fails(self):
        cases = {
            "the forge": "AppTown hosts it, and the forge restarts it.\n",
            "across a line": "Ask this\nforge for its address.\n",
            "possessive": "Read forge's log.\n",
            "capital": "Forge keeps the record.\n",
            "an article": "On a forge with a worker.\n",
        }
        for name, text in cases.items():
            with self.subTest(name=name):
                code, out = run({"stock/docs/guides/a.md": "# A\n\n" + text})
                self.assertEqual(code, 1, out)
                self.assertIn("says forge for the product", out)

    def test_code_names_in_a_shipped_guide_pass(self):
        text = ("Run `apptown wait` and `the forge` in code. The `apptown` command, `apptownd`, "
                "apptown-worker and {apptown:mcp} are names.\n\n```sh\nthe forge\n```\n\n"
                "    Forge in indented code\n\n[guide](/docs/guides/running-a-forge)\n")
        code, out = run({"stock/docs/guides/a.md": text})
        self.assertEqual(code, 0, out)

    def test_prose_after_indented_code_is_still_checked(self):
        code, out = run({"stock/docs/guides/a.md":
                         "    apptown help\n\nForge restarts the app.\n"})
        self.assertEqual(code, 1, out)
        self.assertIn("a.md:3: says forge for the product", out)

    # The guidance agents read is held to the product name too, in its body
    # and in its checks (titles, expects and the messages they print), and
    # its code names pass as in a guide.
    def test_guidance_calling_the_product_forge_fails(self):
        block = ('---\nname: a\nchecks: [{"name": "trusts-front-door", '
                 '"title": "%s", "expects": "x"}]\n---\n%s')
        for name, (title, body) in {
            "body": ("Front door", "The forge serves the files.\n"),
            "check title": ("Trust the forge's front door", "Run `apptown dev`.\n"),
        }.items():
            with self.subTest(name=name):
                code, out = run({"stock/guidance/deploy/a.md": block % (title, body)})
                self.assertEqual(code, 1, out)
                self.assertIn("stock/guidance/deploy/a.md", out)
        code, out = run({"stock/guidance/deploy/a.md": block % ("Front door", "Run `apptown dev` in `.apptown/`.\n")})
        self.assertEqual(code, 0, out)

    def test_design_guides_are_not_held_to_the_product_name(self):
        code, out = run({"design/subsystems/a.md": "The forge's engine runs steps.\n"})
        self.assertEqual(code, 0, out)

    def test_product_names_before_sentence_punctuation_are_checked(self):
        for text in ("Install Forge.\n", "This is the forge.\n", "Configure Forge:\n"):
            with self.subTest(text=text):
                code, out = run({"stock/docs/guides/a.md": text})
                self.assertEqual(code, 1, out)
                self.assertIn("says forge for the product", out)


if __name__ == "__main__":
    unittest.main()
