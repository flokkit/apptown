// Compare the same text role across sibling rows of a shared component.
// A title and a description may have different sizes; two titles may not.
export function rowFontDifferences() {
  const differences = []
  for (const [row, roles] of [
    ['.project-row', ['.project-name', '.project-description']],
    ['.list-row', ['.list-identity', '.list-state', '.list-action']],
    ['.int-row', ['.int-name']],
    ['.record-table tbody tr', Array.from({length: 8}, (_, i) => `td:nth-child(${i + 1})`)],
  ]) {
    const rows = [...document.querySelectorAll(row)]
    const group = el => el.closest('[role="list"]') || el.parentElement
    for (const parent of new Set(rows.map(group))) {
      for (const role of roles) {
        const cells = rows.filter(el => group(el) === parent).flatMap(el => [...el.querySelectorAll(role)]).filter(el => el.getClientRects().length && el.textContent.trim())
        const sizes = [...new Set(cells.map(el => getComputedStyle(el).fontSize))]
        if (sizes.length > 1) differences.push({row, role, sizes})
      }
    }
  }
  return differences
}
