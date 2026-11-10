// Words in running text that a line break splits inside ("Productio" then
// "n"). Runs in the page. A word counts by the distinct lines its letters sit
// on, not by its rects, since inline markup can give one line several rects.
// Code, technical values and mono text may break anywhere and are not
// counted, nor is a word wider than the page's line (an unbroken name too
// long for the screen must break somewhere), nor a word joined by a hyphen,
// which breaks after the hyphen. A name marked as one (.ident) counts whole.
export function splitWords() {
  const found = []
  const root = document.querySelector('main') || document.body
  // A word wider than the page's own line must break somewhere; one narrower
  // than it that breaks was squeezed by its column.
  const page = getComputedStyle(root)
  const line = root.clientWidth - parseFloat(page.paddingLeft) - parseFloat(page.paddingRight)
  // Text kept only for screen readers (clipped to a pixel) is not read on screen.
  const hidden = new Map()
  const clipped = el => {
    if (!el || el === root) return false
    if (!hidden.has(el)) hidden.set(el, getComputedStyle(el).clipPath !== 'none' || clipped(el.parentElement))
    return hidden.get(el)
  }
  const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let node; (node = walk.nextNode());) {
    const el = node.parentElement
    if (!el || el.closest('pre, code, kbd, samp, svg, .technical, .tech-line, .xterm, .diff, .log, [aria-hidden="true"]')) continue
    if (!el.getClientRects().length || clipped(el)) continue
    const style = getComputedStyle(el)
    if (style.visibility === 'hidden' || /mono/i.test(style.fontFamily)) continue
    for (const m of node.data.matchAll(/[A-Za-z]{3,}/g)) {
      const end = m.index + m[0].length
      if (node.data[m.index - 1] === '-' || node.data[end] === '-') continue
      const range = document.createRange()
      range.setStart(node, m.index); range.setEnd(node, end)
      const rects = [...range.getClientRects()].filter(r => r.width > 0)
      if (new Set(rects.map(r => Math.round(r.top))).size < 2) continue
      if (rects.reduce((sum, r) => sum + r.width, 0) > line) continue
      found.push({ word: m[0], text: node.data.trim().slice(0, 80), in: String(el.className).slice(0, 60) })
    }
  }
  // A name (Ident) is one unit: one narrower than its block's line that sits
  // on two lines was split, hyphen or not.
  for (const el of root.querySelectorAll('.ident')) {
    if (!el.getClientRects().length || clipped(el)) continue
    const range = document.createRange(); range.selectNodeContents(el)
    const rects = [...range.getClientRects()].filter(r => r.width > 0)
    if (new Set(rects.map(r => Math.round(r.top))).size < 2) continue
    let block = el.parentElement
    while (block && block !== root && /^(inline|contents)/.test(getComputedStyle(block).display)) block = block.parentElement
    const s = getComputedStyle(block)
    if (rects.reduce((sum, r) => sum + r.width, 0) > block.clientWidth - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight)) continue
    found.push({ word: el.textContent, text: el.textContent, in: 'ident' })
  }
  return found
}
