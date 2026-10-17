import sys
sys.path.insert(0, sys.path[0] + "/..")
from kit import check, finish, run_bin

STAGE = int(sys.argv[1]) if len(sys.argv) > 1 else 1


def conv(md, args=None):
    ec, out, err, sym = run_bin(args or [], md)
    return ec, out, sym


def eq(name, md, want, args=None):
    ec, out, sym = conv(md, args)
    check(name, ec == 0 and out == want, sym + f" · wanted {want!r}")


def stage1():
    eq("paragraph join", "hello\nworld\n", "<p>hello world</p>\n")
    eq("two paragraphs", "a\n\nb\n", "<p>a</p>\n<p>b</p>\n")
    eq("heading levels", "# One\n\n### Three\n", "<h1>One</h1>\n<h3>Three</h3>\n")
    eq("h7 is paragraph", "####### nope\n", "<p>####### nope</p>\n")
    eq("hash without space is paragraph", "#nope\n", "<p>#nope</p>\n")
    eq("list basic", "- a\n- b\n", "<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n")
    eq("list ends at blank", "- a\n\npara\n", "<ul>\n<li>a</li>\n</ul>\n<p>para</p>\n")
    eq("code block literal + escaped",
       "```\nif a < b && c:\n  *x*\n```\n",
       "<pre><code>if a &lt; b &amp;&amp; c:\n  *x*\n</code></pre>\n")
    eq("unclosed fence runs out", "```\nraw\n", "<pre><code>raw\n</code></pre>\n")
    eq("escaping in text", "a < b & c > d\n", "<p>a &lt; b &amp; c &gt; d</p>\n")


def stage2():
    eq("bold", "**hi** there\n", "<p><strong>hi</strong> there</p>\n")
    eq("italic", "*hi*\n", "<p><em>hi</em></p>\n")
    eq("code span wins over bold", "`**not bold**`\n", "<p><code>**not bold**</code></p>\n")
    eq("code span escapes", "`a<b>`\n", "<p><code>a&lt;b&gt;</code></p>\n")
    eq("bold before italic", "***x***\n", "<p><strong><em>x</em></strong></p>\n")
    eq("unmatched marker literal", "a * b\n", "<p>a * b</p>\n")
    eq("unmatched bold literal", "**dangling\n", "<p>**dangling</p>\n")
    eq("link", "[go](http://x/)\n", '<p><a href="http://x/">go</a></p>\n')
    eq("link text parsed, url literal",
       "[*em*](http://x/*)\n", '<p><a href="http://x/*"><em>em</em></a></p>\n')
    eq("quote in url", '[a](http://x/"y)\n', '<p><a href="http://x/%22y">a</a></p>\n')
    eq("inline in heading", "# a `b`\n", "<h1>a <code>b</code></h1>\n")
    eq("inline in list", "- **b**\n", "<ul>\n<li><strong>b</strong></li>\n</ul>\n")


def stage3():
    eq("ordered list", "1. a\n2. b\n", "<ol>\n<li>a</li>\n<li>b</li>\n</ol>\n")
    eq("ordered numbers ignored", "7. a\n3. b\n", "<ol>\n<li>a</li>\n<li>b</li>\n</ol>\n")
    eq("blockquote", "> quoted\n> more\n", "<blockquote><p>quoted more</p></blockquote>\n")
    eq("hr", "---\n", "<hr>\n")
    eq("almost hr is paragraph", "----\n", "<p>----</p>\n")
    eq("smart dashes", "a -- b\n", "<p>a &mdash; b</p>\n", args=["--smart"])
    eq("smart ellipsis", "wait...\n", "<p>wait&hellip;</p>\n", args=["--smart"])
    eq("smart spares code", "`a -- b`\n", "<p><code>a -- b</code></p>\n", args=["--smart"])
    eq("no smart without flag", "a -- b\n", "<p>a -- b</p>\n")


stage1()
if STAGE >= 2:
    stage2()
if STAGE >= 3:
    stage3()
finish()
