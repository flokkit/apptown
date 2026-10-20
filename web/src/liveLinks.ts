import { PRODUCT } from './brand'
import type { DocLinks } from './wire.gen'

// Live links: a shipped guide writes {apptown:mcp} where this AppTown's own
// address belongs, and the Docs page shows the address. AppTown sends the
// values with the guide (DocLinks: its field names are the whole
// vocabulary); this file only places them, after the markdown is rendered,
// because only then is it known whether a placeholder sits in prose (where
// it becomes a link) or in code (where it stays text to copy).
//
// An AppTown that was not told its public address sends no URLs. The page then
// says "your AppTown's address" in words, never a link to a guessed address.
// An unknown name is left as written; docsLinks.test.ts fails on it, so a
// typo cannot ship.

export const livePlaceholder = /\{apptown:([a-z-]+)\}/g

const unknownAddress = `${PRODUCT}'s address`

function isName(links: DocLinks, name: string): name is keyof DocLinks {
  return Object.prototype.hasOwnProperty.call(links, name)
}

// fillLiveLinks replaces each known placeholder under root with this
// AppTown's address.
export function fillLiveLinks(root: HTMLElement, links: DocLinks): void {
  const doc = root.ownerDocument
  const walker = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const texts: Text[] = []
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if ((n.nodeValue ?? '').includes('{apptown:')) texts.push(n as Text)
  }
  for (const text of texts) {
    const parent = text.parentElement
    const inCode = !!parent?.closest('code, pre')
    const inLink = !!parent?.closest('a')
    const value = text.nodeValue ?? ''
    const out = doc.createDocumentFragment()
    let last = 0
    for (const m of value.matchAll(livePlaceholder)) {
      const name = m[1]
      if (!isName(links, name)) continue
      const { url, path } = links[name]
      out.append(value.slice(last, m.index))
      last = (m.index ?? 0) + m[0].length
      if (!url) {
        out.append(inCode ? `<${unknownAddress}>${path}` : path ? `${unknownAddress} followed by ${path}` : unknownAddress)
      } else if (inCode || inLink) {
        out.append(url)
      } else {
        const a = doc.createElement('a')
        a.href = url
        a.textContent = url
        out.append(a)
      }
    }
    if (last === 0) continue
    out.append(value.slice(last))
    text.replaceWith(out)
  }
}
