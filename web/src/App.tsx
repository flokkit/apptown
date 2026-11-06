import { DeploymentPage } from './pages/project/settings/Deployments'
import WorkRecordPage from './pages/change/WorkRecord'
import { MEMORY_TABS, SECURITY_TABS, SETTINGS_TABS, SETTINGS_LABEL } from './pages/project/tabs'
import { projectHue } from './ProjectListRow'
import { Blocks, BookOpen, BookText, ChartColumn, ChevronDown, ChevronRight, FlaskConical, FolderKanban, GitPullRequest, House, LayoutDashboard, MessageSquare, MessageSquareWarning, Plug, RefreshCw, Rocket, Settings, Shield, ShieldCheck, Users } from 'lucide-react'
import { createChatChange } from './pages/project/ChangeButton'
import { PRODUCT } from './brand'
import { Link, NavLink, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ErrorBoundary } from './ErrorBoundary'
import { UpdateNote } from './UpdateNote'
import { UpdatesPage, ReleaseAvailabilityNote } from './pages/Updates'
import { StaleScope } from './ReadGate'
import { lazy, Suspense, useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { currentUser, setCurrentUser } from './api'
import Dashboard from './pages/Dashboard'
import AttentionPage from './pages/Attention'
import Projects from './pages/Projects'
import Deployments from './pages/Deployments'
import WaysOfWorking from './pages/WaysOfWorking'
import Complaints from './pages/Complaints'
import Project from './pages/Project'
import ChangePage from './pages/ChangePage'
import AdrPage from './pages/AdrPage'
import GuidanceBlockPage from './pages/GuidanceBlockPage'
import PackPage from './pages/PackPage'
import WhatIfPage from './pages/WhatIfPage'
import NewProject from './pages/NewProject'
import Integrations from './pages/Integrations'
import { TargetPage } from './pages/integrations/TargetPage'
import { INTEGRATION_MENU } from './pages/integrations/kinds'
import { ModelProfilePage } from './pages/integrations/ModelSlots'
import { InfrastructureHome } from './pages/InfrastructureHome'
import { InfrastructureNew } from './pages/InfrastructureNew'
import Conformance from './pages/Conformance'
import UsagePage from './pages/Usage'
import People, { PEOPLE_PAGES } from './pages/People'
import { MyExternalSignIn, externalSignInFailure } from './pages/ExternalSignIn'
import Login from './pages/Login'
import InvitePage from './pages/Invite'
// Docs (mermaid and its diagram libraries) and the console (xterm) are the
// heaviest pages by far; loading them on first visit keeps every other
// page's first load small. A tab left open across a deploy reloads once
// rather than failing to find the previous build's chunk.
const DocsPage = lazy(withChunkReload(() => import('./pages/Docs')))
const Console = lazy(withChunkReload(() => import('./pages/Console')))
import { useProjectScope } from './projectScope'
import { EvalScorecardPage, EvalStartPage, EvalsPage } from './pages/Evals'
import { OperationPage } from './pages/DataProtection'
import { InstanceGate, useInstancePages, useHolds } from './instancePages'
import TopBar, { AccountCorner } from './TopBar'
import { useNarrow } from './chrome'
import { withChunkReload } from './chunkReload'
import { identityChanged, useAction, useGet, useOnline } from './data'
import type { AuthMe } from './wire.gen'

// Contextual sidebar (the GitLab pattern): inside a project, the menu IS the
// project menu — instance navigation is one click up. Above the line is the
// loop everyone runs every day (find a project, build, try, finish up,
// approve); below "setup" is what you touch once in a while. Same menu for
// everyone (design/subsystems/ui-coherence.md): weight differs, words don't.
export function SidebarNav() {
  const { pathname } = useLocation()
  const scope = useProjectScope()
  const m = pathname.match(/^\/projects\/([^/]+)/)
  // Inside a project by URL, or inside one of its changes/decisions by scope.
  const slug = m ? (m[1] !== '_new' ? m[1] : null)
    : /^\/(changes|items|adrs)\//.test(pathname) ? scope : null
  const nav = useNavigate()
  const chat = useAction()
  const canCreate = useHolds('change:create', slug ?? undefined)
  const canChat = useHolds('change:invoke-lane', slug ?? undefined)
  const project = useGet<import('./wire.gen').ProjectDetail>(slug ? `/projects/${slug}` : null).data
  const cls = ({ isActive }: { isActive: boolean }) => 'nav' + (isActive ? ' active' : '')
  // Pages about the whole AppTown are left out for someone who cannot read
  // them; the menu is otherwise the same for everyone.
  const mayOpen = useInstancePages()
  const me = useGet<AuthMe>('/auth/me').data
 const mayUpdate = useHolds("instance:updates")
  const wow = /^\/(packs|workflows|guidance|policies)(\/|$)/.test(pathname)

  if (slug) {
    const base = `/projects/${slug}`
    const name = project?.name ?? slug
    return (
      <>
        {/* The project, as the app rows show it: its colour and its name. */}
        <div className="sidebar-project" title={name}>
          <span className="project-avatar project-letter" aria-hidden style={{ '--project-hue': projectHue(slug) } as CSSProperties}>{name.slice(0, 1).toUpperCase()}</span>
          <strong>{name}</strong>
        </div>
        <NavLink to={base} end className={cls}><LayoutDashboard aria-hidden size={16} />Overview</NavLink>
        <NavLink to={`${base}/changes`} className={cls}><GitPullRequest aria-hidden size={16} />Changes</NavLink>
        <NavLink to={`${base}/deployments`} className={cls}><Rocket aria-hidden size={16} />Deployments</NavLink>
        {project?.chatAvailable && canCreate && canChat && <button className="nav" disabled={chat.busy} onClick={() => void chat.run(async () => {
          const r = await createChatChange(slug)
          nav(`${base}/console?change=${r.change.id}`)
        })}><MessageSquare aria-hidden size={16} />Chat</button>}
        {chat.err && <p className="error small">{chat.err}</p>}
        <Drawer label="Security" icon={<Shield aria-hidden size={16} />} to={`${base}/security`} active={pathname.startsWith(`${base}/security`)} items={SECURITY_TABS.map(t => (
          { to: `${base}/security/${t}`, label: t[0].toUpperCase() + t.slice(1), active: pathname === `${base}/security/${t}` || (t === 'findings' && pathname === `${base}/security`) }))} />
        <Drawer label="Settings" icon={<Settings aria-hidden size={16} />} to={`${base}/settings`} active={pathname.startsWith(`${base}/settings`)} items={SETTINGS_TABS.map(t => (
          { to: `${base}/settings/${t}`, label: SETTINGS_LABEL[t], active: pathname === `${base}/settings/${t}` || (t === 'general' && pathname === `${base}/settings`) }))} />
        <Drawer label="Memory" icon={<BookOpen aria-hidden size={16} />} to={`${base}/memory`} active={pathname.startsWith(`${base}/memory`)} items={MEMORY_TABS.map(t => (
          { to: `${base}/memory/${t}`, label: t[0].toUpperCase() + t.slice(1), active: pathname === `${base}/memory/${t}` || (t === 'notes' && pathname === `${base}/memory`) }))} />
        <div className="spacer" />
        <NavBottom cls={cls} complaints={mayOpen('complaints')} me={me ?? null} />
      </>
    )
  }
  const dep = /^\/(releases|deployments|targets|backups)(\/|$)/.test(pathname)
  const infra = mayOpen('infrastructure')
  return (
    <>
      <NavLink to="/" end className={cls}><House aria-hidden size={16} />Home</NavLink>
      <NavLink to="/projects" end className={cls}><FolderKanban aria-hidden size={16} />Projects</NavLink>
      <Drawer label="Deployments" icon={<Rocket aria-hidden size={16} />} to="/deployments" active={dep} items={[
        { to: '/releases', label: 'Releases', active: pathname === '/releases' },
        { to: '/deployments', label: 'Deployments', active: pathname === '/deployments' },
        ...(infra ? [
          { to: '/targets', label: 'Targets', active: pathname.startsWith('/targets') },
          { to: '/backups', label: 'Backups', active: pathname === '/backups' },
        ] : []),
      ]} />
      {mayOpen('conformance') && <NavLink to="/conformance" className={cls}><ShieldCheck aria-hidden size={16} />Conformance</NavLink>}
      <div className="nav-group">Setup</div>
      <Drawer label="Ways of working" icon={<BookOpen aria-hidden size={16} />} to="/packs" active={wow} items={[
        { to: '/packs', label: 'Packs', active: pathname.startsWith('/packs') },
        { to: '/workflows', label: 'Workflows', active: pathname.startsWith('/workflows') },
        { to: '/guidance', label: 'Guidance', active: pathname.startsWith('/guidance') },
        { to: '/policies', label: 'Policies', active: pathname.startsWith('/policies') },
      ]} />
      {mayOpen('integrations') && INTEGRATION_MENU.map((g) => (
        <Drawer key={g.key} label={g.heading} icon={g.key === 'services' ? <Blocks aria-hidden size={16} /> : <Plug aria-hidden size={16} />} to={`/${g.key}`}
          active={new RegExp(`^/${g.key}(/|$)`).test(pathname)}
          items={g.items.map((i) => ({ to: `/${g.key}/${i.key}`, label: i.title, active: pathname === `/${g.key}/${i.key}` }))} />))}
      {mayOpen('evals') && <NavLink to="/evals" className={cls}><FlaskConical aria-hidden size={16} />Evals</NavLink>}
      {mayOpen('people') && <Drawer label="People" icon={<Users aria-hidden size={16} />} to="/people/accounts" active={pathname.startsWith('/people')} items={PEOPLE_PAGES.map(([key, label]) => ({ to: `/people/${key}`, label, active: pathname.startsWith(`/people/${key}`) }))} />}
      <NavLink to="/usage" className={cls}><ChartColumn aria-hidden size={16} />Usage</NavLink>
      {mayUpdate && <NavLink to="/updates" className={cls}><RefreshCw aria-hidden size={16} />Updates</NavLink>}
      <div className="spacer" />
      <NavBottom cls={cls} complaints={mayOpen('complaints')} me={me ?? null} />
    </>
  )
}

// NavBottom is the same on every menu: help, and you.
function NavBottom({ cls, complaints, me }: { cls: (a: { isActive: boolean }) => string; complaints: boolean | undefined; me: AuthMe | null }) {
  return (
    <div className="nav-bottom">
      <NavLink to="/docs" className={cls}><BookText aria-hidden size={16} />Docs</NavLink>
      {complaints && <NavLink to="/complaints" className={cls}><MessageSquareWarning aria-hidden size={16} />Complaints</NavLink>}
      <AccountCorner me={me} />
    </div>
  )
}

// Drawer is an area of the product with pages of its own: the area opens
// where you are in it, and its pages are the menu's map of the area.
function Drawer({ label, icon, to, active, items }: { label: string; icon: ReactNode; to: string; active: boolean; items: { to: string; label: string; active: boolean }[] }) {
  const { pathname } = useLocation()
  const [choice, setChoice] = useState({ pathname, open: active })
  // Manual choices belong to this page, including closing its current area.
  const open = choice.pathname === pathname ? choice.open : active
  if (choice.pathname !== pathname) setChoice({ pathname, open: active })
  return (
    <div className={'nav-drawer' + (open ? ' open' : '')}>
      <div className="nav-drawer-head">
        <NavLink to={to} className={() => 'nav' + (active ? ' active-area' : '')}>{icon}{label}</NavLink>
        <button className="nav-drawer-toggle" aria-expanded={open} aria-label={`${open ? 'Close' : 'Open'} ${label}`} onClick={() => setChoice({ pathname, open: !open })}>
          {open ? <ChevronDown aria-hidden size={14} /> : <ChevronRight aria-hidden size={14} />}
        </button>
      </div>
      {open && <div className="nav-drawer-items">
        {items.map(i => <NavLink key={i.to} to={i.to} className={() => 'nav nav-sub' + (i.active ? ' active' : '')}>{i.label}</NavLink>)}
      </div>}
    </div>
  )
}

// Old per-project workflow pages land on the project's Way of working.
function ToProjectWorking() {
  const { slug } = useParams()
  return <Navigate to={`/projects/${slug}/settings/working`} replace />
}



export default function App() {
  const { pathname: routePath } = useLocation()
  const [user, setUser] = useState(currentUser())
  // /auth/me is reachable without a session; it says whether a login
  // door exists (internal mode) and who is behind it if signed in.
  const meQ = useGet<AuthMe>('/auth/me')
  const me = meQ.data
  const loadMe = () => { void meQ.reload() }
  // Who the page reads for. When that changes (signing in or out, a session
  // expiring, another name in open mode) every held answer is dropped and any
  // read still in flight is discarded, so one person's views never show what
  // was read for another.
  const client = useQueryClient()
  const identity = me ? `${me.mode}:${me.user ?? ''}:${me.mode === 'open' ? user : ''}` : null
  const lastIdentity = useRef<string | null>(null)
  useEffect(() => {
    if (identity === null) return
    if (lastIdentity.current !== null && lastIdentity.current !== identity) identityChanged(client)
    lastIdentity.current = identity
  }, [identity, client])
  const online = useOnline()
  // Under the shell's breakpoint the sidebar is a menu that opens over the
  // page; going anywhere closes it.
  const narrow = useNarrow()
  const [menuOpen, setMenuOpen] = useState(false)
  useEffect(() => { setMenuOpen(false) }, [routePath, narrow])

  // An invite link is opened by someone with no account yet: its page
  // stands on its own, signed in or not.
  // The page moves the token out of the address bar once read, leaving /invite.
  const invite = routePath.match(/^\/invite(?:\/([^/]+))?\/?$/)
  if (invite) return <InvitePage token={invite[1] ? decodeURIComponent(invite[1]) : ''} />
  if (me && me.mode === 'internal' && !me.user) {
    return <Login needsBootstrap={!!me.needsBootstrap} onSignedIn={loadMe} external={me.external} />
  }
  return (
    <div className="layout">
      <TopBar me={me} narrow={narrow} menuOpen={menuOpen} onMenu={() => setMenuOpen((o) => !o)} />
      {narrow && menuOpen && <div className="sidebar-backdrop" onClick={() => setMenuOpen(false)} />}
      <nav id="sidebar" className={'sidebar' + (narrow ? ' narrow' : '') + (menuOpen ? ' open' : '')}>
        <div className="sidebar-inner">
          <SidebarNav />
          <div className="spacer" />
          {me && me.mode === 'open' && (
            <div className="user">
              acting as
              <input
                aria-label="Acting as" value={user}
                onChange={(e) => { setUser(e.target.value); setCurrentUser(e.target.value) }}
              />
            </div>
          )}
        </div>
      </nav>
      <main className="main">
        {externalSignInFailure(new URLSearchParams(window.location.search).get('signin')) && <p className="error">{externalSignInFailure(new URLSearchParams(window.location.search).get('signin'))}</p>}
        {meQ.err && <div className="error small" style={{ marginBottom: 10 }}>{PRODUCT} could not say who you are: {meQ.err}</div>}
        <UpdateNote />
 <ReleaseAvailabilityNote />
        {!online && <div className="muted small" style={{ marginBottom: 10 }}><span title="Pages refresh when the connection returns.">Offline</span></div>}
        {/* Keyed by identity as well: another person starts from fresh pages,
            with no draft or local state left from the one before. */}
        <ErrorBoundary key={`${identity ?? ''} ${routePath}`}>
        <StaleScope>
        <Suspense fallback={<div className="muted">Loading…</div>}>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/attention" element={<AttentionPage />} />
 <Route path="/updates" element={<UpdatesPage />} />
          <Route path="/projects" element={<Projects />} />
          <Route path="/deployments" element={<Deployments />} />
          <Route path="/releases" element={<Deployments page="releases" />} />
          <Route path="/backups" element={<InstanceGate page="infrastructure"><Deployments page="backups" /></InstanceGate>} />
          <Route path="/complaints" element={<InstanceGate page="complaints"><Complaints tab="new" /></InstanceGate>} />
          <Route path="/complaints/answered" element={<InstanceGate page="complaints"><Complaints tab="answered" /></InstanceGate>} />
          <Route path="/complaints/archive" element={<InstanceGate page="complaints"><Complaints tab="archived" /></InstanceGate>} />
          <Route path="/conformance" element={<InstanceGate page="conformance"><Conformance tab="projects" /></InstanceGate>} />
          <Route path="/conformance/checks" element={<InstanceGate page="conformance"><Conformance tab="checks" /></InstanceGate>} />
          <Route path="/conformance/reviews" element={<InstanceGate page="conformance"><Conformance tab="reviews" /></InstanceGate>} />
          <Route path="/conformance/reviews/:reviewId" element={<InstanceGate page="conformance"><Conformance tab="projects" /></InstanceGate>} />
          <Route path="/conformance/reviews/:reviewId/checks" element={<InstanceGate page="conformance"><Conformance tab="checks" /></InstanceGate>} />
          <Route path="/conformance/reviews/:reviewId/projects/:slug" element={<InstanceGate page="conformance"><Conformance tab="project" /></InstanceGate>} />
          <Route path="/conformance/projects/:slug" element={<InstanceGate page="conformance"><Conformance tab="project" /></InstanceGate>} />
          <Route path="/conformance/what-ifs" element={<InstanceGate page="conformance"><Conformance tab="what-ifs" /></InstanceGate>} />
          <Route path="/conformance/what-ifs/:id" element={<InstanceGate page="conformance"><WhatIfPage /></InstanceGate>} />
          <Route path="/conformance/remediations" element={<InstanceGate page="conformance"><Conformance tab="remediations" /></InstanceGate>} />
          <Route path="/conformance/ways" element={<InstanceGate page="conformance"><Conformance tab="ways" /></InstanceGate>} />
          <Route path="/conformance/remediations/:remId" element={<InstanceGate page="conformance"><Conformance tab="remediation" /></InstanceGate>} />
          <Route path="/projects/_new" element={<NewProject />} />
          <Route path="/projects/:slug/deployments/:name" element={<DeploymentPage />} />
          <Route path="/projects/:slug" element={<Project />} />
          <Route path="/operations/:id" element={<OperationPage />} />
          <Route path="/projects/:slug/:tab" element={<Project />} />
          <Route path="/projects/:slug/:tab/:stab" element={<Project />} />
          <Route path="/changes/:id/work/:stepId" element={<WorkRecordPage />} />
          <Route path="/changes/:id" element={<ChangePage />} />
          <Route path="/adrs/:id" element={<AdrPage />} />
          <Route path="/guidance" element={<WaysOfWorking tab="guidance" />} />
          <Route path="/guidance/:id" element={<GuidanceBlockPage />} />
          <Route path="/workflows" element={<WaysOfWorking tab="workflows" />} />
          <Route path="/projects/:slug/workflows/*" element={<ToProjectWorking />} />
          <Route path="/projects/:slug/console" element={<Console />} />
          <Route path="/integrations" element={<InstanceGate page="integrations"><Integrations /></InstanceGate>} />
          <Route path="/integrations/profiles/:profile" element={<InstanceGate page="integrations"><ModelProfilePage /></InstanceGate>} />
          <Route path="/targets" element={<InstanceGate page="infrastructure"><InfrastructureHome /></InstanceGate>} />
          <Route path="/targets/new" element={<InstanceGate page="infrastructure"><InfrastructureNew /></InstanceGate>} />
          <Route path="/targets/:name" element={<InstanceGate page="infrastructure"><TargetPage /></InstanceGate>} />
          <Route path="/integrations/:name" element={<InstanceGate page="integrations"><Integrations /></InstanceGate>} />
          <Route path="/services" element={<InstanceGate page="integrations"><Integrations /></InstanceGate>} />
          <Route path="/services/:name" element={<InstanceGate page="integrations"><Integrations /></InstanceGate>} />
          <Route path="/policies" element={<WaysOfWorking tab="policies" />} />
          <Route path="/packs" element={<WaysOfWorking tab="packs" />} />
          <Route path="/packs/:name" element={<PackPage />} />
          <Route path="/account" element={<MyExternalSignIn />} />
          <Route path="/people/accounts" element={<InstanceGate page="people"><People view="accounts" /></InstanceGate>} />
          <Route path="/people/accounts/:name" element={<InstanceGate page="people"><People view="accounts" /></InstanceGate>} />
          <Route path="/people/groups" element={<InstanceGate page="people"><People view="groups" /></InstanceGate>} />
          <Route path="/people/groups/:name" element={<InstanceGate page="people"><People view="groups" /></InstanceGate>} />
          <Route path="/people/roles" element={<InstanceGate page="people"><People view="roles" /></InstanceGate>} />
          <Route path="/people/sign-in" element={<InstanceGate page="people"><People view="sign-in" /></InstanceGate>} />
          <Route path="/people/agents-and-tokens" element={<InstanceGate page="people"><People view="agents-and-tokens" /></InstanceGate>} />
          <Route path="/usage" element={<UsagePage />} />
          <Route path="/evals" element={<InstanceGate page="evals"><EvalsPage /></InstanceGate>} />
          <Route path="/evals/new" element={<InstanceGate page="evals"><EvalStartPage /></InstanceGate>} />
          <Route path="/evals/:id" element={<InstanceGate page="evals"><EvalScorecardPage /></InstanceGate>} />
          <Route path="/docs" element={<DocsPage />} />
          <Route path="/docs/:section/:slug" element={<DocsPage />} />
          <Route path="*" element={<div><h1>Page not found</h1><p>This address does not identify a page. Release links include the project name.</p><Link to="/projects">Find your project</Link></div>} />
        </Routes>
        </Suspense>
        </StaleScope>
        </ErrorBoundary>
      </main>
    </div>
  )
}
