// @vitest-environment jsdom
// @ts-expect-error -- this package has no Node types; only these two reads are used.
import { readdirSync, readFileSync } from 'node:fs'
import { matchPath } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import appSource from './App.tsx?raw'
import { renderMd } from './md'
import { fillLiveLinks } from './liveLinks'
import type { DocLinks } from './wire.gen'

// The Docs page renders each shipped doc with renderMd at
// /docs/guides/<slug> or /docs/<capability>/<provider>, and a link in it is a
// plain <a href> the browser resolves against that page's address. So a link
// written for the repository ("toolchains.md", "../../../design/...")
// resolves to a page the product does not serve, and a heading anchor
// points nowhere: the rendered headings have no ids. This test renders every
// shipped doc the way the page does and checks each rendered link: a web
// address, a shipped doc's route, or a route the app defines (App.tsx).

// Every shipped doc, as the binary embeds them (stock/embed.go:
// docs/guides/*.md and docs/integrations/*.md). Read from disk: the docs
// live outside the web package, where Vite's imports may not reach. Vitest
// runs from web/ (npm test), so the path is relative to it.
const stock = '../stock/docs/'
const files: Record<string, string> = {}
for (const dir of ['guides', 'integrations']) {
  for (const f of readdirSync(stock + dir) as string[]) {
    if (f.endsWith('.md')) files[`${dir}/${f}`] = readFileSync(`${stock}${dir}/${f}`, 'utf8') as string
  }
}

// Shipped docs another change is rewriting; each names what is wrong so it
// leaves this list with that change.
const knownBroken: Record<string, string> = {}

type Doc = { file: string; route: string; body: string }

function shippedDocs(): Doc[] {
  return Object.entries(files).map(([file, body]) => {
    const name = file.split('/')[1].replace(/\.md$/, '')
    if (file.startsWith('guides/')) return { file, route: `/docs/guides/${name}`, body }
    const key = name.replace(/\.(admin|developer)$/, '')
    const dash = key.indexOf('-')
    return { file, route: `/docs/${key.slice(0, dash)}/${key.slice(dash + 1)}`, body }
  })
}

// The app's routes, read from App.tsx, except the doc page itself: a link
// into /docs must name a doc that exists.
const appRoutes = [...appSource.matchAll(/<Route\s+path="([^"]+)"/g)]
  .map((m) => m[1])
  .filter((p) => !p.startsWith('/docs/'))

const origin = 'http://forge.test'

// A guide's live links ({apptown:<name>}, liveLinks.ts) as an AppTown with a
// public address fills them. Typed as AppTown's DocLinks, so a name the
// AppTown adds or drops fails the type check until this follows.
const liveLinks: DocLinks = {
  origin: { url: 'https://forge.example', path: '' },
  mcp: { url: 'https://forge.example/mcp/user', path: '/mcp/user' },
  git: { url: 'https://forge.example/git', path: '/git' },
}

// linkProblems renders one doc the way the page does and returns what is
// wrong with each link, and any live link AppTown would not fill: an
// unknown name in a guide, or any live link in an integration doc (those
// are served without AppTown's addresses).
function linkProblems(doc: Doc, served: Set<string>): string[] {
  const html = new DOMParser().parseFromString(renderMd(doc.body), 'text/html')
  if (doc.route.startsWith('/docs/guides/')) fillLiveLinks(html.body, liveLinks)
  const problems: string[] = []
  for (const m of (html.body.textContent ?? '').matchAll(/\{apptown:[^}]*\}/g)) {
    problems.push(`${m[0]}: not a live link AppTown fills here (names: ${Object.keys(liveLinks).join(', ')})`)
  }
  for (const a of html.querySelectorAll('a[href]')) {
    const href = a.getAttribute('href') ?? ''
    const url = new URL(href, origin + doc.route)
    if (url.origin !== origin) {
      if (!['http:', 'https:', 'mailto:'].includes(url.protocol)) problems.push(`${href}: not a web address`)
      continue
    }
    if (url.hash) {
      problems.push(`${href}: the Docs page's headings have no ids, so an anchor points nowhere`)
      continue
    }
    const path = url.pathname
    if (path.startsWith('/docs/')) {
      if (!served.has(path)) problems.push(`${href}: resolves to ${path}, which no shipped doc serves`)
    } else if (!appRoutes.some((r) => matchPath(r, path))) {
      problems.push(`${href}: resolves to ${path}, which is not a route of the app`)
    }
  }
  return problems
}

describe('shipped docs', () => {
  const docs = shippedDocs()
  const served = new Set(docs.map((d) => d.route))

  it('are all read', () => {
    expect(docs.filter((d) => d.file.startsWith('guides/')).length).toBeGreaterThan(10)
  })

  // One test per doc: rendering and parsing every shipped doc in one test
  // took ~0.25 s alone and over 5 s inside a loaded push gate (11 October),
  // and the corpus only grows; each doc's own test stays well inside the limit.
  it.each(docs.filter((d) => !(d.file in knownBroken)).map((d) => [d.file, d] as const))('%s links only to pages the product serves', (_, d) => {
    expect(linkProblems(d, served)).toEqual([])
  })

  // The check reads links as the renderer makes them: valid Markdown forms
  // pass, broken ones in any form fail, and code is not a link.
  it('reads links the way the renderer does', () => {
    const page = (body: string) => linkProblems({ file: 'probe.md', route: '/docs/guides/probe', body }, served)
    expect(page('[a](/docs/guides/quickstart) [b](</docs/guides/quickstart>) [c](/docs/guides/quickstart?from=x) [d](quickstart)')).toEqual([])
    expect(page('[e](https://example.com/x.md) [f](/projects/demo) [g](/docs/llm/openrouter)')).toEqual([])
    expect(page('`[h](relative.md)`\n\n```\n[i](relative.md)\n```')).toEqual([])
    expect(page('[Missing][t]\n\n[t]: /docs/guides/does-not-exist')).toHaveLength(1)
    expect(page('[j](toolchains.md)')).toHaveLength(1)
    expect(page('[k](/docs/guides/quickstart#make-something)')).toHaveLength(1)
    expect(page('[l](../../../design/subsystems/x.md)')).toHaveLength(1)
    expect(page('[m](/no-such-product-page)')).toHaveLength(1)
  })

  // Live links are filled before links are checked; a name AppTown does
  // not fill is a problem wherever it appears, and integration docs get none.
  it('reject live links AppTown does not fill', () => {
    const page = (body: string, route = '/docs/guides/probe') => linkProblems({ file: 'probe.md', route, body }, served)
    expect(page('Connect at {apptown:mcp}; `APPTOWN_URL={apptown:origin}`\n\n```\ngit clone {apptown:git}/demo\n```')).toEqual([])
    expect(page('Connect at {apptown:mpc}')).toHaveLength(1)
    expect(page('`{apptown:url}`')).toHaveLength(1)
    expect(page('```\n{apptown:Origin}\n```')).toHaveLength(1)
    expect(page('Connect at {apptown:mcp}', '/docs/llm/openrouter')).toHaveLength(1)
  })
})
