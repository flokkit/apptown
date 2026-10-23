"""Regressions for scripts/docs-links.py: shipped guides' and release notes' links work on GitHub and on the Docs page.

Each run of the check starts the web package's renderer (about a second), so
a test puts its cases in one tree as separate files and runs the check once.
"""
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CHECK = Path(__file__).with_name("docs-links.py")

# A small tree: two guides, an integration pair, a design guide.
TREE = {
    "stock/docs/guides/quickstart.md": "# Quickstart\n",
    "stock/docs/guides/secrets.md": "# Secrets\n",
    "stock/docs/integrations/llm-openrouter.admin.md": "# Admin\n",
    "stock/docs/integrations/llm-openrouter.developer.md": "# Developer\n",
    "design/writing.md": "# Writing\n",
}


def run(files):
    """The check's exit code and, per file it names, its problem lines."""
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        for name, text in {**TREE, **files}.items():
            p = root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(text)
        r = subprocess.run([sys.executable, str(CHECK), str(root)], capture_output=True, text=True)
    found = {}
    for line in r.stdout.splitlines():
        found.setdefault(line.split(":", 1)[0], []).append(line)
    return r.returncode, found, r.stdout + r.stderr


def probes(bodies, where="stock/docs/guides/probe"):
    """One file per body: {path: body}."""
    return {f"{where}{i}.md": body for i, body in enumerate(bodies)}


class Check(unittest.TestCase):
    def assert_each_fails(self, files, said=""):
        code, found, out = run(files)
        self.assertEqual(code, 1, out)
        for path in files:
            self.assertIn(path, found, f"{files[path]!r} passed\n{out}")
            self.assertTrue(all(said in p for p in found[path]), found[path])

    def assert_all_pass(self, files):
        code, _, out = run(files)
        self.assertEqual(code, 0, out)


class ShippedDocs(Check):
    def test_links_to_shipped_docs_by_file_and_web_addresses_pass(self):
        self.assert_all_pass({
            "stock/docs/guides/probe.md":
                "[a](quickstart.md) [b](<secrets.md>) [c](./quickstart.md \"title\")\n"
                "[d](../integrations/llm-openrouter.admin.md) [e](https://example.com/x)\n"
                "[f](https://github.com/flokkit/apptown/blob/main/design/writing.md) [g](mailto:a@example.com)\n"
                "[h][ref]\n\n[ref]: secrets.md\n"
                "Connect at {apptown:mcp}.\n\n"
                "<a href=\"secrets.md\">S</a> <https://example.com/x> "
                "and https://github.com/flokkit/apptown/blob/main/design/writing.md.\n\n"
                "[i](https://github.com/flokkit/apptown/tree/main/design/)\n",
            "stock/docs/integrations/llm-openrouter.developer.md":
                "[a](../guides/secrets.md) [b](llm-openrouter.admin.md)\n",
        })

    # What the renderer does not make a link is not checked: code spans,
    # fences at any indent or length, indented code, an HTML comment.
    def test_what_is_not_rendered_as_a_link_is_not_checked(self):
        self.assert_all_pass(probes([
            "`[a](/docs/guides/x)`\n\n```\n[b](/docs/guides/x)\n```\n\n~~~md\n<a href=\"/x\">c</a>\n~~~\n",
            "Indented:\n\n    [d](/docs/x)\n",
            "1. Example:\n\n    ```md\n    [Example](/docs/guides/quickstart)\n    ```\n",
            "````md\n```md\n[Example](/docs/guides/quickstart)\n```\n````\n",
            '<!-- <a href="/docs/guides/quickstart">Draft</a> -->\n',
        ]))

    # Every form the renderer makes a link from is read, as the renderer
    # reads it: a list item's indented paragraph is prose, not code.
    def test_rendered_link_forms_cannot_bypass_the_github_rule(self):
        self.assert_each_fails(probes([
            '1. Read this:\n\n    [Start](/docs/guides/quickstart)\n',
            '<a href="/docs/guides/quickstart">Start</a>\n',
            "<A class=x HREF='/docs/guides/quickstart'>Start</A>\n",
            '<https://github.com/flokkit/apptown/blob/main/gone.md>\n',
            'See https://github.com/flokkit/apptown/blob/main/gone.md.\n',
            "See (https://github.com/flokkit/apptown/blob/main/gone.md).\n",
            '<a title="Build > Deploy" href="https://github.com/flokkit/apptown/blob/main/gone.md">Guide</a>\n',
        ]))

    # The check and the Docs page agree on what a shipped doc is.
    def test_a_shipped_doc_the_docs_page_cannot_route_fails(self):
        self.assert_each_fails({"stock/docs/guides/Read_Me.md": "# X\n", "stock/docs/integrations/notes.md": "# X\n"},
                               "no route for")

    # The links the guides had before: right on the Docs page, a 404 on GitHub.
    def test_a_root_path_fails(self):
        files = probes(["[a](/docs/guides/quickstart)\n", "[a](/docs/llm/openrouter)\n", "[a](/projects)\n",
                        "[a][r]\n\n[r]: /docs/guides/quickstart\n"])
        self.assert_each_fails(files, "a root path")
        _, found, _ = run({"stock/docs/guides/probe.md": "# P\n\nSee [a](/docs/guides/quickstart).\n"})
        self.assertIn("stock/docs/guides/probe.md:3:", found["stock/docs/guides/probe.md"][0])

    def test_a_relative_link_to_anything_but_a_shipped_doc_fails(self):
        self.assert_each_fails(probes(["[a](../../../design/writing.md)\n", "[a](../api/openapi.json)\n"]), "does not serve")
        self.assert_each_fails(probes(["[a](missing.md)\n", "[a](../integrations/llm-other.admin.md)\n"]), "does not exist")

    def test_an_anchor_fails(self):
        self.assert_each_fails(probes(["[a](quickstart.md#install)\n", "[a](#install)\n", "[a](quickstart.md?x=1)\n"]), "anchor")

    def test_a_live_address_as_a_link_target_fails(self):
        self.assert_each_fails(probes(["[MCP]({apptown:mcp})\n"]), "live address")

    def test_a_repository_address_must_name_a_file_in_the_tree(self):
        self.assert_each_fails(probes(["[a](https://github.com/flokkit/apptown/blob/main/design/gone.md)\n"]),
                               "design/gone.md, which is not in the repository")


class ReleaseNotes(Check):
    def test_full_repository_addresses_pass(self):
        self.assert_all_pass({"releases/v1.0.0.md":
                              "[promises](https://github.com/flokkit/apptown/blob/main/stock/docs/guides/quickstart.md)\n"})

    # The notes are a GitHub Release's text too, where a relative link does
    # not resolve; history (rc.1 to rc.6) is not exempt from a dead link.
    def test_relative_root_and_missing_links_fail(self):
        self.assert_each_fails({"releases/v1.0.0-rc.1.md": "[a](../stock/docs/guides/quickstart.md)\n",
                                "releases/v1.0.0-rc.2.md": "[a](/docs/guides/quickstart)\n"}, "GitHub Release")
        self.assert_each_fails({
            "releases/v1.0.0.md": "[a](https://github.com/flokkit/apptown/blob/main/stock/docs/guides/gone.md)\n",
            "releases/v1.0.1.md": "See (https://github.com/flokkit/apptown/blob/main/gone.md).\n",
            "releases/v1.0.2.md": '<a title="Build > Deploy" href="https://github.com/flokkit/apptown/blob/main/gone.md">G</a>\n',
        }, "not in the repository")


if __name__ == "__main__":
    unittest.main()
