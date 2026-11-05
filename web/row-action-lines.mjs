// Browser and camera share the same measurement. Padding and hint lines are
// irrelevant: measure only text ranges, and coalesce inline fragments by line.
export function wrappedRowActions(root = document) {
  const selector = '.list-action button, .list-action a, .project-action button, .project-action a, .row-action button, .row-action a, .split-button > button'
  return [...root.querySelectorAll(selector)].filter(button => !button.closest('.popover')).flatMap(button => {
    const label = button.querySelector('.action-label') ?? button
    const walker = document.createTreeWalker(label, NodeFilter.SHOW_TEXT)
    const tops = []
    while (walker.nextNode()) {
      const node = walker.currentNode
      if (!node.textContent.trim() || node.parentElement.closest('.btn-sub, [data-action-hint], .muted.small, [aria-hidden="true"]')) continue
      const range = document.createRange()
      range.selectNodeContents(node)
      for (const rect of range.getClientRects()) {
        if (rect.width && rect.height && !tops.some(top => Math.abs(top - rect.top) < 2)) tops.push(rect.top)
      }
    }
    return tops.length > 1 ? [{ label: label.textContent.trim(), lines: tops.length }] : []
  })
}
