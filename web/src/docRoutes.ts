// A shipped doc links to another shipped doc by its file, relatively
// ("split-install.md", "../integrations/registry-registry.admin.md"), so the
// link works where the repository is read, on GitHub. The Docs page serves
// the same docs at its own routes; this maps such a link to the route
// (scripts/docs-links.py keeps any other relative link from shipping).
//
// file is the doc's path under stock/docs ("guides/server-setup.md").

const integration = /^integrations\/([a-z0-9]+)-([a-z0-9-]+)\.(?:admin|developer)\.md$/
const guide = /^guides\/([a-z0-9-]+)\.md$/

// docRoute is the Docs page route a link in file names, or null when the
// link is not a relative link to a shipped doc.
export function docRoute(file: string, href: string): string | null {
  if (!href || href.startsWith('/') || href.startsWith('#') || /^[a-z][a-z0-9+.-]*:/i.test(href)) return null
  const parts = file.split('/').slice(0, -1)
  for (const p of href.split('/')) {
    if (p === '..') {
      if (!parts.length) return null
      parts.pop()
    } else if (p !== '.' && p !== '') parts.push(p)
  }
  const to = parts.join('/')
  const g = guide.exec(to)
  if (g) return `/docs/guides/${g[1]}`
  const i = integration.exec(to)
  return i ? `/docs/${i[1]}/${i[2]}` : null
}

// routeDocLinks points each relative link to a shipped doc under root at its
// Docs page route.
export function routeDocLinks(root: HTMLElement, file: string): void {
  root.querySelectorAll<HTMLAnchorElement>('a[href]').forEach((a) => {
    const route = docRoute(file, a.getAttribute('href') ?? '')
    if (route) a.setAttribute('href', route)
  })
}
