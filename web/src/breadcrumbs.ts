import { PRODUCT } from './brand'
import { matchPath } from 'react-router-dom'
import { INTEGRATION_MENU } from './pages/integrations/kinds'

// Actual pages, including literal ancestors of parameterised routes.
export const PAGE_ROUTES = [
  "/invite",
  "/invite/:token",
  "/features",
  "/",
  "/attention/checks",
  "/attention/checks/:checkKey",
  "/attention",
  "/updates",
  "/projects",
  "/deployments",
  "/releases",
  "/backups",
  "/complaints",
  "/complaints/answered",
  "/complaints/archive",
  "/conformance",
  "/conformance/checks/:checkKey/projects",
  "/conformance/reviews/:reviewId/checks/:checkKey/projects",
  "/conformance/checks/:checkKey",
  "/conformance/reviews/:reviewId/checks/:checkKey",
  "/conformance/checks",
  "/conformance/reviews",
  "/conformance/reviews/:reviewId",
  "/conformance/reviews/:reviewId/checks",
  "/conformance/reviews/:reviewId/projects/:slug",
  "/conformance/projects/:slug",
  "/conformance/what-ifs",
  "/conformance/what-ifs/:id/projects/:slug",
  "/conformance/what-ifs/:id",
  "/conformance/remediations",
  "/conformance/remediations/:remId",
  "/projects/_new",
  "/projects/:slug/releases/:number/log",
  "/projects/:slug/deployments/:name",
  "/projects/:slug",
  "/operations/:id",
  "/projects/:slug/:tab",
  "/projects/:slug/:tab/:stab",
  "/changes/:id/records/:recordId",
  "/changes/:id/work/:stepId/request",
  "/changes/:id/work/:stepId/workflow",
  "/changes/:id/work/:stepId/report",
  "/changes/:id/work/:stepId/records/:artifactId",
  "/changes/:id/work/:stepId",
  "/changes/:id",
  "/adrs/:id",
  "/projects/:slug/chats",
  "/projects/:slug/chats/:sessionId",
  "/guidance/generated",
  "/guidance/generated/:name",
  "/workflows/:name",
  "/policies/new",
  "/policies/:name",
  "/packs/:name/versions/:version",
  "/guidance/:name/projects",
  "/policies/:name/projects",
  "/packs/:name/projects",
  "/integrations/profiles/:profile/projects",
  "/integrations/:name/projects",
  "/services/:name/projects",
  "/guidance",
  "/guidance/:name",
  "/workflows",
  "/projects/:slug/console",
  "/integrations",
  "/integrations/profiles/:profile",
  "/jobs",
  "/targets",
  "/targets/new",
  "/targets/:name/resources",
  "/targets/:name/resources/:id",
  "/targets/:name/capacity",
  "/targets/:name/history",
  "/targets/:name",
  "/integrations/:name",
  "/services",
  "/services/:name",
  "/policies",
  "/janitor",
  "/packs",
  "/packs/:name",
  "/account",
  "/people/accounts",
  "/people/accounts/:name",
  "/people/groups",
  "/people/groups/:name",
  "/people/roles",
  "/people/sign-in",
  "/people/agents-and-tokens",
  "/usage",
  "/evals",
  "/evals/new",
  "/evals/:id",
  "/docs",
  "/docs/:section/:slug",
  "/text-roles",
] as const

const LABELS: Record<string, string> = {
  ...Object.fromEntries(['projects','changes','deployments','releases','targets','backups','packs','workflows','guidance','policies','conformance','checks','reviews','remediations','services','integrations','memory','security','settings','general','models','secrets','members','usage','notes','decisions','chats'].map(s => [s, s.charAt(0).toUpperCase() + s.slice(1)])),
  accounts: 'Accounts', groups: 'Groups', roles: 'Roles', 'sign-in': 'Sign-in', 'agents-and-tokens': 'Agents and tokens', chats: 'Past chats', generated: 'Automatic guidance', _new: 'New project', scm: 'Source control', 'what-ifs': 'Pack upgrades',
  working: 'Way of working', llm: 'AI models', adrs: 'Decisions',
  ...Object.fromEntries(INTEGRATION_MENU.flatMap(g => g.items.map(i => [i.key, i.title]))),
}

// Tabs of one page that each have their own address. A tab has the page's
// own parent, so every tab of the page shows the same way back.
export const TAB_ROUTES = [
  "/complaints/answered",
  "/complaints/archive",
  "/targets/:name/resources",
  "/targets/:name/capacity",
  "/targets/:name/history",
] as const

export type Breadcrumb = { to: string; label: string }
// A grouping only appears when it is itself a page. Routes such as
// /docs/guides/:slug have no /docs/guides page to link to.
export function routeAncestors(pathname: string, names: Record<string, string> = {}, search = ''): Breadcrumb[] {
  const parts = pathname.split('/').filter(Boolean)
  const paths = ['/']
  for (let n = 1; n < parts.length; n++) {
    const path = '/' + parts.slice(0, n).join('/')
    // Release records are read under the project, but it has no releases
    // collection page: its deployments own release history.
    if (/^\/projects\/[^/]+\/releases$/.test(path)) continue
    if (PAGE_ROUTES.some(pattern => matchPath({ path: pattern, end: true }, path))) paths.push(path)
  }
  if (TAB_ROUTES.some(pattern => matchPath({ path: pattern, end: true }, pathname))) {
    const home = '/' + parts.slice(0, -1).join('/')
    const at = paths.indexOf(home)
    if (at >= 0) paths.splice(at, 1)
  }
  // Targets, releases and backups are pages in the Deployments area.
  if (['targets', 'releases', 'backups'].includes(parts[0])) paths.splice(1, 0, '/deployments')
  return paths.filter(path => path !== pathname).map(to => {
    const segment = decodeURIComponent(to.split('/').at(-1) || '')
    const pattern = PAGE_ROUTES.find(pattern => matchPath({ path: pattern, end: true }, to))
    const dynamic = pattern?.split('/').at(-1)?.startsWith(':')
    const identity = new URLSearchParams()
    if (/^\/changes\/[^/]+\/work\/[^/]+$/.test(to)) {
      const params = new URLSearchParams(search)
      for (const key of ['run', 'attempt']) if (params.has(key)) identity.set(key, params.get(key)!)
    }
    return { to: to + (identity.size ? `?${identity}` : ''), label: (dynamic ? names[to] : undefined) ?? (to === '/' ? PRODUCT : LABELS[segment] ?? (dynamic ? segment : segment.charAt(0).toUpperCase() + segment.slice(1).replaceAll('-', ' '))) }
  })
}
