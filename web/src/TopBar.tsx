import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { post } from './api'
import { usePageContext } from './chrome'
import { AttentionBadge } from './attention'
import { PRODUCT } from './brand'
import { useProjectScope } from './projectScope'
import { useGet } from './data'
import { Theme, applyTheme, currentTheme } from './theme'
import type { ProjectDetail, ProjectPage } from './wire.gen'

// The top bar (design/subsystems/ui-coherence.md § Top bar): one fixed strip that says
// what you are doing, on every page, in the same place — AppTown › project ›
// page · state. It replaced two ad-hoc sticky headers (the change page's
// context strip, the chat's room header). Right side: find anything, the
// theme, who you are. The sidebar is navigation and nothing else.

const INSTANCE: [RegExp, string][] = [
  [/^\/$/, 'Home'],
  [/^\/attention$/, 'Attention'],
  [/^\/projects$/, 'Projects'],
  [/^\/deployments/, 'Deployments'],
  [/^\/(packs|workflow-library|guidance|policies)/, 'Ways of working'],
  [/^\/integrations/, 'Integrations'],
  [/^\/people/, 'People'],
  [/^\/usage/, 'Usage'],
  [/^\/evals/, 'Evals'],
  [/^\/complaints/, 'Complaints'],
  [/^\/docs/, 'Docs'],
  [/^\/projects\/_new/, 'New project'],
]
const PROJECT_PAGE: Record<string, string> = {
  '': 'Overview', console: 'Chat', security: 'Security', settings: 'Settings',
  release: 'Release', releases: 'Release', workflows: 'Workflows', board: 'Overview', usage: 'Settings',
}

type Me = { user?: string; role?: string; mode: string }

// On a narrow screen (chrome.ts useNarrow) the bar is the menu button, the
// name and where you are; App puts finding, the theme and who you are in
// the menu instead.
export default function TopBar({ me, narrow = false, menuOpen = false, onMenu }: {
  me: Me | null; narrow?: boolean; menuOpen?: boolean; onMenu?: () => void
}) {
  const { pathname } = useLocation()
  const page = usePageContext()
  const m = pathname.match(/^\/projects\/([^/]+)(?:\/([^/]+))?(?:\/([^/]+))?/)
  const slug = usePageProject()
  // The crumb falls back to the slug, which is known, when the project
  // itself cannot be read.
  const proj = useGet<ProjectDetail>(slug ? `/projects/${slug}` : null).data

  // The sentence: AppTown › project › page · state.
  const crumbs: { label: string; to?: string }[] = []
  if (slug) crumbs.push({ label: `${proj?.icon ? proj.icon + ' ' : ''}${proj?.name ?? slug}`, to: `/projects/${slug}` })
  if (page.crumbs && page.crumbs.length > 0) {
    crumbs.push(...page.crumbs)
  } else if (m && m[1] !== '_new') {
    const tab = m[2] ?? ''
    let label = PROJECT_PAGE[tab] ?? tab
    if (tab === 'releases' && m[3]) label = `Release #${m[3]}`
    if (tab !== '') crumbs.push({ label })
  } else {
    const hit = INSTANCE.find(([re]) => re.test(pathname))
    if (hit) crumbs.push({ label: hit[1] })
  }

  // ⌘K / Ctrl-K, registered once: focuses whichever find box the page has —
  // the front page's big one or the bar's small one (same id, never both).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault(); (document.getElementById(FIND_BOX_ID) as HTMLInputElement | null)?.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <header className="topbar">
      {narrow && (
        <button className="subtle topbar-menu" aria-label={menuOpen ? 'close the menu' : 'open the menu'}
          aria-expanded={menuOpen} aria-controls="sidebar" onClick={onMenu}>{menuOpen ? '✕' : '☰'}</button>
      )}
      <Link to="/" className="topbar-logo">⚒ <span>{PRODUCT}</span></Link>
      {crumbs.map((c, i) => (
        <span key={i} className={'topbar-crumb' + (i === crumbs.length - 1 ? ' last' : '')}>
          <span className="topbar-sep">›</span>
          {c.to ? <Link to={c.to}>{c.label}</Link> : <span className="topbar-here">{c.label}</span>}
        </span>
      ))}
      {page.chip && <span className={`chip topbar-chip ${page.chip.cls ?? ''}`}>{page.chip.text}</span>}
      <span style={{ flex: 1 }} />
      {me && <AttentionBadge />}
      {!narrow && <>
        <Find />
        <ThemeSwitch />
        <UserLine me={me} />
      </>}
    </header>
  )
}

// Who you are, and the way out when there is a sign-in.
export function UserLine({ me }: { me: Me | null }) {
  if (!me || me.mode === 'open' || !me.user) return null
  return (
    <span className="topbar-user">
      <span className="topbar-user-name" title={me.user}>{me.user}</span>
      {me.mode === 'internal' && (
        <button className="subtle" onClick={() => post('/auth/logout').then(() => location.reload())}>sign out</button>
      )}
    </span>
  )
}

// Find anything: a project by name, a change by number, from any page.
// ⌘K / Ctrl-K focuses it. Enter goes to the first match. On the front page
// there is no small find — the page's big box IS the find (it filters the
// grid), and the last row here hands a query over to it.
// usePageProject is the project a page is in: its URL's, or the one a
// change or decision page set as its scope.
function usePageProject(): string | null {
  const { pathname } = useLocation()
  const scope = useProjectScope()
  const m = pathname.match(/^\/projects\/([^/]+)/)
  return m && m[1] !== '_new' ? m[1] : /^\/(changes|items|adrs)\//.test(pathname) ? scope : null
}

export const FIND_BOX_ID = 'find-projects'
export function Find() {
  const nav = useNavigate()
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLInputElement>(null)


  const here = usePageProject()
  const t = q.trim().toLowerCase()
  // A change by its friendly form: "<slug>#<n>", or "#<n>" in the project
  // the page is in. The API resolves it wherever a change route takes an id.
  const friendly = t.match(/^([^\s#]+)#(\d+)$/)
  const bare = t.match(/^#?(\d+)$/)?.[1]
  const changeRef = friendly ? `${friendly[1]}#${friendly[2]}` : bare && here ? `${here}#${bare}` : ''
  const { data: page } = useGet<ProjectPage>(open && t ? `/projects?limit=6&q=${encodeURIComponent(t)}` : null, { poll: 0 })
  const hits = page?.projects ?? []
  const rows: { label: string; hint?: string; to: string }[] = [
    ...(changeRef ? [{ label: `Change ${changeRef}`, hint: 'go to the change', to: `/changes/${encodeURIComponent(changeRef)}` }] : []),
    ...hits.map((p) => ({ label: `${p.icon ? p.icon + ' ' : ''}${p.name}`, hint: p.blurb, to: `/projects/${p.slug}` })),
    ...(t && !changeRef && !bare ? [{ label: `All projects matching “${q.trim()}” →`, to: `/projects?q=${encodeURIComponent(q.trim())}` }] : []),
  ]
  const go = (to: string) => { setQ(''); setOpen(false); ref.current?.blur(); nav(to) }

  return (
    <div className="topbar-find">
      <input ref={ref} id={FIND_BOX_ID} value={q} placeholder="Find a project or #change" aria-label="find"
        onFocus={() => setOpen(true)}
        onBlur={() => setTimeout(() => setOpen(false), 120)}
        onChange={(e) => { setQ(e.target.value); setOpen(true) }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && rows[0]) go(rows[0].to)
          if (e.key === 'Escape') { setQ(''); setOpen(false); ref.current?.blur() }
        }} />
      {open && t && (
        <div className="topbar-find-menu">
          {rows.length === 0 && <div className="muted small" style={{ padding: '6px 10px' }}>{!page ? 'looking…' : 'nothing matches'}</div>}
          {rows.map((r) => (
            <button key={r.to} className="subtle" onMouseDown={(e) => e.preventDefault()} onClick={() => go(r.to)}>
              {r.label}{r.hint && <span className="muted small" style={{ marginLeft: 8 }}>{r.hint}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// Light / dark / follow the system — one small control, same for everyone.
export function ThemeSwitch() {
  const [t, setT] = useState<Theme>(currentTheme())
  const pick = (n: Theme) => { setT(n); applyTheme(n) }
  const opts: { k: Theme; label: string; title: string }[] = [
    { k: 'system', label: 'auto', title: 'follow the system setting' },
    { k: 'light', label: '☀', title: 'light' },
    { k: 'dark', label: '☾', title: 'dark' },
  ]
  return (
    <div className="theme-switch" role="group" aria-label="theme">
      {opts.map((o) => (
        <button key={o.k} className={'subtle' + (t === o.k ? ' active' : '')} title={o.title} aria-label={o.title}
          aria-pressed={t === o.k} onClick={() => pick(o.k)}>{o.label}</button>
      ))}
    </div>
  )
}
