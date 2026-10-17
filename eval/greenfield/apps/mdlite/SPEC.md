# mdlite — a small, predictable markdown subset

Full CommonMark is a monster. I want a tiny subset that renders exactly
the same way every time. A binary `mdlite` that reads markdown on stdin
(or a file argument) and writes HTML to stdout. No options in stage 1.

Output style, to make results comparable: each block element on its own
line, no indentation, no wrapping. A final trailing newline.

## Stage 1 — blocks

- Paragraphs: consecutive non-blank lines join with a single space into
  one <p>...</p>. Blank line(s) separate blocks.
- Headings: 1-6 leading #'s then a space. More #'s than 6, or no space →
  it's just a paragraph. Trailing #'s are literal (we're not CommonMark).
  <h1>Title</h1>
- Unordered lists: lines starting with "- ". Consecutive ones form
  <ul><li>...</li>...</ul> (one line per <li>, <ul> and </ul> on their
  own lines). No nesting in stage 1 — a deeper-indented "- " is literal
  text inside the item? No: keep it simple, it starts a new item too.
- Code blocks: fenced with ``` on its own line. Everything inside is
  LITERAL (no inline parsing), HTML-escaped, in <pre><code>...</code></pre>.
  Every content line keeps its newline — including the last one — so
  </code></pre> always sits after a newline.
  An unclosed fence runs to end of input.
- Escape & < > in all text output (and " inside attribute values).

## Stage 2 — inline

Inside paragraphs, headings, and list items:

- **bold** and *italic*, `code` spans, [text](url) links.
- Code spans win: nothing inside `...` is parsed, and it's HTML-escaped.
- Bold before italic when ambiguous; no nesting of the same kind.
  Unmatched markers are literal text. Precedence, exactly:

      scan left to right:
        ` starts a code span if a closing ` exists → consume
        ** starts bold if a closing ** exists → consume, parse inside (no bold)
        *  starts italic if a closing * exists → consume, parse inside (no italic)
        [ starts a link if ](...) completes it → text parsed, url literal
        else: literal character

- Link urls: escape " as %22 in the href attribute; no other mangling.

## Stage 3 — a few conveniences

- Ordered lists: "1. " style (any number, we renumber implicitly by
  emitting plain <ol><li>).
- Blockquotes: "> " prefix, one level only, containing paragraphs only.
  <blockquote><p>...</p></blockquote>
- Horizontal rule: a line of exactly --- → <hr>.
- `--smart` flag: turns -- into &mdash; and ... into &hellip; in plain
  text (never inside code spans/blocks or urls).
