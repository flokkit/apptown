import { routeAncestors } from './breadcrumbs'
import { Disclosure } from './Popover'
import { createContext, useCallback, useContext, useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Modal } from './Modal'
import { TechnicalDetailMark, TechnicalDetailSwitch } from './pages/change/TechnicalDetail'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { Search } from 'lucide-react'
import { post } from './api'
import { useNarrow, usePageContext } from './chrome'
import { AttentionLights } from './attention'
import { PRODUCT } from './brand'
import { useProjectScope } from './projectScope'
import { useGet } from './data'
import { type Size, applySize, currentSize } from './size'
import { Theme, applyTheme, currentTheme } from './theme'
import type { ProjectDetail, ProjectPage } from './wire.gen'

// One bar at the top of every page (design/subsystems/ui-coherence.md). The
// shell owns the row and its controls: the menu button when the nav is
// folded, search and the attention lights. The page on screen fills the
// middle with its title, inline facts and actions through PageBar. The
// controls are there before the page has read anything, while it loads, when
// it fails and on "Page not found"; the title joins them when it arrives.

type Me = { user?: string; role?: string; mode: string }

// The row's middle: undefined with no shell above (a page rendered on its
// own draws its row in place), null until the shell's row has mounted.
const Slot = createContext<HTMLElement | null | undefined>(undefined)

// ShellBar is the row and everything under it that may fill it. lead is the
// menu button when the nav is folded, or the wordmark on a page with no nav.
export function ShellBar({ lead, controls = true, children }: { lead?: ReactNode; controls?: boolean; children: ReactNode }) {
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  return <>
    <header className="page-bar">
      {lead && <div className="page-bar-lead">{lead}</div>}
      <div className="page-bar-slot" ref={setSlot} />
      {controls && <div className="page-bar-trail"><ShellControls /></div>}
    </header>
    <Slot.Provider value={slot}>{children}</Slot.Provider>
  </>
}

// PageBar is a page's header row: its h1 (the only place one is drawn), the
// "?" beside it, facts that sit on the title's line, and the page's actions.
// Only the page on screen renders it: a part kept mounted while hidden
// renders its bar only while shown.
export function PageBar({ title, titleClass, help, after, actions, className }: {
  title: ReactNode; titleClass?: string; help?: ReactNode; after?: ReactNode; actions?: ReactNode; className?: string
}) {
  const slot = useContext(Slot)
  const row = (
    <div className={'page-bar-page' + (className ? ' ' + className : '')}>
      {/* The title and its "?" stay together; the facts follow on its line. */}
      <div className="page-bar-title">
        <h1 className={titleClass}>{title}</h1>
        {help}
        {after}
      </div>
      {/* The actions keep to the right, on the title's line or, wrapped, on their own. */}
      {actions && <div className="page-bar-actions">{actions}</div>}
    </div>
  )
  if (slot === undefined) return row
  return slot ? createPortal(row, slot) : null
}

// The wordmark: Home, at the top of the nav, or leading the row of a page
// that has no nav (sign-in, an invite).
export function Wordmark() {
  return <Link to="/" className="wordmark">{PRODUCT}</Link>
}

// The row's right end: the attention lights and search, for a signed-in
// person. On a phone the lights are one dot and search is a dialog.
function ShellControls() {
  const narrow = useNarrow()
  // ⌘K / Ctrl-K, registered once: focuses whichever find box the page has —
  // the front page's big one or the bar's — or opens the bar's.
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
  const trigger = <button id="open-find" className="subtle page-bar-icon" aria-label="Search" title="Search (⌘K)"><Search aria-hidden size={18} /></button>
  return <>
    <AttentionLights compact={narrow} />
    {narrow ? <PhoneFind trigger={trigger} /> : <Disclosure className="account-menu page-bar-search" closeOnAction={false} trigger={trigger}>
      {close => <Find onNavigate={close} />}
    </Disclosure>}
  </>
}

// ParentLink is the first item of the folded menu: the page above this one,
// the way back up on a phone or tablet. Home has none.
// A parent the menu already links to (a project's Overview, its Settings) is
// not repeated: the menu already offers the way there.
export function ParentLink() {
  const parent = useParentPage()
  const link = useRef<HTMLAnchorElement>(null)
  const [inMenu, setInMenu] = useState(false)
  // The menu's items arrive with their reads (a project's areas, the
  // features in use), so the check follows the menu as it changes.
  const to = parent?.to
  useLayoutEffect(() => {
    const menu = link.current?.closest('nav')
    if (!menu || !to) { setInMenu(false); return }
    const check = () => setInMenu([...menu.querySelectorAll('a[href]')].some(a => a !== link.current && a.getAttribute('href') === to))
    check()
    const watch = new MutationObserver(check)
    watch.observe(menu, { childList: true, subtree: true, attributes: true, attributeFilter: ['href'] })
    return () => watch.disconnect()
  }, [to])
  if (!parent) return null
  return <Link ref={link} to={parent.to} className="nav sidebar-parent" hidden={inMenu}><span aria-hidden>‹</span>{parent.label}</Link>
}

function useParentPage(): { to: string; label: string } | undefined {
  const { pathname, search } = useLocation()
  const page = usePageContext()
  const slug = usePageProject()
  // The project's name falls back to its slug, which is known, when the
  // project itself cannot be read.
  const proj = useGet<ProjectDetail>(slug ? `/projects/${slug}` : null).data
  const projectCrumb = slug ? { to: `/projects/${slug}`, label: proj?.name ?? slug } : null
  return routeAncestors(pathname, {
    ...page.names,
    ...(projectCrumb ? { [projectCrumb.to]: projectCrumb.label } : {}),
  }, search, slug ? { slug, name: proj?.name ?? slug } : undefined).filter(c => c.to !== '/').at(-1)
}

// PhoneFind: on a phone, Find opens as a dialog sized to its results, as
// help and status details do there. A box hung under the bar would lie over
// the page's heading and leave its figures without their labels.
function PhoneFind({ trigger }: { trigger: React.ReactElement<React.ButtonHTMLAttributes<HTMLButtonElement>> }) {
  const [open, setOpen] = useState(false)
  const id = useId()
  const close = useCallback(() => setOpen(false), [])
  return <div className="account-menu page-bar-search">
    <button {...trigger.props} aria-haspopup="dialog" aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => setOpen(true)} />
    {open && <Modal fit title="Find" onClose={close}><div id={id}><Find onNavigate={close} /></div></Modal>}
  </div>
}

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
  return m && m[1] !== '_new' ? m[1] : /^\/(changes|items|adrs|operations)\//.test(pathname) ? scope : null
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
    <div className="find-box">
      <input ref={ref} id={FIND_BOX_ID} value={q} placeholder="Find a project or #change" aria-label="find"
        onFocus={() => setOpen(true)}
        onChange={(e) => { setQ(e.target.value); setOpen(true) }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && rows[0]) { e.preventDefault(); go(rows[0].to) }
        }} />
      {open && t && (
        <div className="find-box-menu">
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
