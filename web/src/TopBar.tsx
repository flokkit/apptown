import { routeAncestors } from './breadcrumbs'
import { Disclosure } from './Popover'
import { useEffect, useRef, useState } from 'react'
import { TechnicalDetailMark, TechnicalDetailSwitch } from './pages/change/TechnicalDetail'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { post } from './api'
import { usePageContext } from './chrome'
import { AttentionLights } from './attention'
import { PRODUCT } from './brand'
import { useProjectScope } from './projectScope'
import { useGet } from './data'
import { type Size, applySize, currentSize } from './size'
import { Theme, applyTheme, currentTheme } from './theme'
import type { ProjectDetail, ProjectPage } from './wire.gen'

// One trail links the route's ancestor pages, ending at the parent.
// The current page is named by its heading in main.


type Me = { user?: string; role?: string; mode: string }

// On a narrow screen (chrome.ts useNarrow) the bar is the menu button, the
// name and where you are; App puts finding, the theme and who you are in
// the menu instead.
export default function TopBar({ me, narrow = false, menuOpen = false, onMenu, standalone = false }: {
  me: Me | null; narrow?: boolean; menuOpen?: boolean; onMenu?: () => void; standalone?: boolean
}) {
  const { pathname, search } = useLocation()
  const page = usePageContext()

  const slug = usePageProject()
  // The crumb falls back to the slug, which is known, when the project
  // itself cannot be read.
  const proj = useGet<ProjectDetail>(slug ? `/projects/${slug}` : null).data

  const crumbs = routeAncestors(pathname, {
    ...page.names,
    ...(slug ? { [`/projects/${slug}`]: proj?.name ?? slug } : {}),
  }, search).filter(c => c.to !== '/')

  // ⌘K / Ctrl-K, registered once: focuses whichever find box the page has —
  // the front page's big one or the bar's small one (same id, never both).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        const input = document.getElementById(FIND_BOX_ID) as HTMLInputElement | null
        if (input) input.focus()
        else document.getElementById('open-find')?.click()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <header className="topbar">
      {narrow && !standalone && (
        <button className="subtle topbar-menu" aria-label={menuOpen ? 'close the menu' : 'open the menu'}
          aria-expanded={menuOpen} aria-controls="sidebar" onClick={onMenu}>{menuOpen ? '✕' : '☰'}</button>
      )}
      <nav className="topbar-trail" aria-label="Breadcrumb">
      <Link to="/" className="topbar-logo">{PRODUCT}</Link>
      {narrow && crumbs.length > 1 && <span className="topbar-ellipsis" aria-label="More ancestors">› …</span>}
      {crumbs.map((c, i) => (
        <span key={i} className={'topbar-crumb' + (i === crumbs.length - 1 ? ' last' : '')}>
          <span className="topbar-sep">›</span>
          <Link to={c.to}>{c.label}</Link>
        </span>
      ))}
      </nav>
      <span className="grow" />
      {!standalone && <>
      {me && <AttentionLights />}
      <Disclosure className="account-menu topbar-search" closeOnAction={false} trigger={<button id="open-find" className="subtle" aria-label="Search">Find</button>}>
        {close => <Find onNavigate={close} />}
      </Disclosure>
      <Link to="/projects">Projects</Link>
      </>}
    </header>
  )
}

// AccountCorner is you, at the foot of the menu: your name, and under it
// your account page, the theme and the way out. One place, not two.
export function AccountCorner({ me }: { me: Me | null }) {
  const name = me?.user || 'Account'
  return (
    <Disclosure className="account-corner" placement="above" closeOnAction={false} trigger={<button className="subtle" aria-label="Account menu"><span className="account-avatar" aria-hidden>{name.slice(0, 1).toUpperCase()}</span><span className="account-name" title={name}>{name}</span><TechnicalDetailMark /></button>}>
      <div>
        <Link to="/account">Your account</Link>
        <div className="account-switches"><ThemeSwitch /><SizeSwitch /><TechnicalDetailSwitch /></div>
        {me?.mode === 'internal' && <button className="subtle" onClick={() => post('/auth/logout').then(() => location.reload())}>Sign out</button>}
      </div>
    </Disclosure>
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
export function Find({ onNavigate }: { onNavigate?: () => void }) {
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
  const go = (to: string) => { setQ(''); setOpen(false); onNavigate?.(); nav(to) }

  return (
    <div className="topbar-find">
      <input ref={ref} id={FIND_BOX_ID} value={q} placeholder="Find a project or #change" aria-label="find"
        onFocus={() => setOpen(true)}
        onChange={(e) => { setQ(e.target.value); setOpen(true) }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && rows[0]) { e.preventDefault(); go(rows[0].to) }
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

// One shared scale, chosen by the person reading on this browser.
export function SizeSwitch() {
  const [size, setSize] = useState<Size>(currentSize)
  const pick = (next: Size) => { applySize(next); setSize(next) }
  return (
    <div className="theme-switch size-switch" role="group" aria-label="Text size">
      {(['small', 'medium', 'large'] as const).map((option) => (
        <button key={option} className={'subtle' + (size === option ? ' active' : '')}
          aria-pressed={size === option} onClick={() => pick(option)}>
          {option[0].toUpperCase() + option.slice(1)}
        </button>
      ))}
    </div>
  )
}
