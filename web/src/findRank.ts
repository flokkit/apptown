// Ranking for finding a project by what you typed: a name that STARTS
// with it, then a name that contains it, then a blurb that mentions it.
// 'sour' should find the sourdough apps before a project whose blurb says
// 'source'. Shared by the front page's big find and the bar's small one.
export function rankProjects<T extends { name: string; slug: string; blurb?: string; namespace?: string; icon?: string }>(items: T[], q: string): T[] {
  const t = q.trim().toLowerCase()
  if (!t) return items
  const score = (p: T): number => {
    const name = p.name.toLowerCase(), slug = p.slug.toLowerCase()
    if (name.startsWith(t) || slug.startsWith(t)) return 0
    if (name.includes(t) || slug.includes(t)) return 1
    if ((p.blurb ?? '').toLowerCase().includes(t)) return 2
    if ((p.namespace ?? '').toLowerCase().includes(t) || (p.icon ?? '').includes(t)) return 3
    return -1
  }
  return items.map((p) => [score(p), p] as const).filter(([s]) => s >= 0)
    .sort((a, b) => a[0] - b[0] || a[1].name.localeCompare(b[1].name)).map(([, p]) => p)
}
