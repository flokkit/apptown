// The links in Markdown files, as the Docs page renders them, for
// scripts/docs-links.py. It renders each file with the page's own renderer
// (web/src/md.ts: marked, then DOMPurify with the page's rules), parses the
// result as HTML, and reads each a[href], so the check and the page agree on
// what is a link: a fenced example at any indent, or an HTML comment, is not
// one; a bare or parenthesised address, an HTML anchor and a reference link
// are.
//
//   node scripts/docs-links-extract.mjs <file>...
//
// prints one JSON object per link: {"file", "line", "href"}. line is where
// the target is first written in the file at or after the previous link's
// (0 when the source spells it differently, as an entity or reference).
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'

// The renderer and its dependencies are the web package's; resolve them there.
const web = new URL('../web/', import.meta.url)
const fromWeb = createRequire(new URL('package.json', web))
let JSDOM
try {
  ;({ JSDOM } = await import(pathToFileURL(fromWeb.resolve('jsdom')).href))
} catch {
  console.error('docs-links: the web package\'s dependencies are not installed (cd web && npm ci)')
  process.exit(2)
}
// DOMPurify binds to the window it finds when it loads.
const window = new JSDOM('').window
globalThis.window = window
const { renderMd } = await import(new URL('src/md.ts', web).href)

for (const file of process.argv.slice(2)) {
  const src = readFileSync(file, 'utf8')
  const body = new window.DOMParser().parseFromString(renderMd(src), 'text/html').body
  let from = 0
  for (const a of body.querySelectorAll('a[href]')) {
    const href = a.getAttribute('href')
    const at = src.indexOf(href, from)
    let line = 0
    if (at >= 0) {
      line = src.slice(0, at).split('\n').length
      from = at + href.length
    }
    console.log(JSON.stringify({ file, line, href }))
  }
}
