import Features from './pages/Features'
import { CoreArea, FeatureGate, ProjectFeatureGate, settingsAreas, useFeature } from './features'
import { ReleaseLogPage } from './pages/project/Release'
import { ChangeRecordPage } from './pages/change/Details'
import { GeneratedGuidance } from './pages/GuidanceLibrary'
import { PastChatsPage } from './pages/change/Conversation'
import WorkflowLibrary from './pages/WorkflowLibrary'
import PoliciesPage, { PolicyNewPage } from './pages/PoliciesPage'
import { CheckProjects, GuidanceProjects, PolicyProjects, PackProjects, IntegrationProjects } from './pages/DependentProjects'

import { DeploymentPage } from './pages/project/settings/Deployments'
import WorkRecordPage from './pages/change/WorkRecord'
import { MEMORY_TABS, SECURITY_TABS, SETTINGS_TABS, SETTINGS_LABEL } from './pages/project/tabs'
import { ProjectMark } from './ProjectListRow'
import { Blocks, BookOpen, BookText, ChartColumn, ChevronDown, ChevronRight, FlaskConical, FolderKanban, GitPullRequest, House, LayoutDashboard, MessageSquare, MessageSquareWarning, Plug, RefreshCw, Rocket, Settings, Shield, ShieldCheck, Users } from 'lucide-react'
import { createChatChange } from './pages/project/ChangeButton'
import { PRODUCT } from './brand'
import { Link, NavLink, Navigate, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ErrorBoundary } from './ErrorBoundary'
import { UpdateNote } from './UpdateNote'
import { UpdatesPage, ReleaseAvailabilityNote } from './pages/Updates'
import { StaleScope } from './ReadGate'
import { lazy, Suspense, useEffect, useRef, useState, type ReactNode } from 'react'
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
import { TargetResourcesPage } from './pages/integrations/TargetResources'
import { TargetPage } from './pages/integrations/TargetPage'
import { INTEGRATION_MENU } from './pages/integrations/kinds'
import { ModelProfilePage } from './pages/integrations/ModelSlots'
import { JobsPage } from './pages/Jobs'
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
const TextRoles = lazy(withChunkReload(() => import('./pages/TextRoles')))
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
  const permission = useInstancePages()
 const feature = useFeature()
 const mayOpen = (page: Parameters<typeof permission>[0]) => permission(page) && (page === 'infrastructure' || page === 'integrations' || page === 'people' || page === 'complaints' || feature(page))
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
          <ProjectMark name={name} slug={slug} icon={project?.icon} />
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
        <Drawer label="Settings" icon={<Settings aria-hidden size={16} />} to={`${base}/settings`} active={pathname.startsWith(`${base}/settings`)} items={SETTINGS_TABS.filter(t => ['projects', 'people', 'ways', 'usage'].includes(settingsAreas[t]) || feature(settingsAreas[t])).map(t => (
          { to: `${base}/settings/${t}`, label: SETTINGS_LABEL[t], active: pathname === `${base}/settings/${t}` || (t === 'general' && pathname === `${base}/settings`) }))} />
        {!feature('memory') && <NavLink to={`${base}/memory/decisions`} className={cls}><BookOpen aria-hidden size={16} />Decisions</NavLink>}
        {feature('memory') && <Drawer label="Memory" icon={<BookOpen aria-hidden size={16} />} to={`${base}/memory`} active={pathname.startsWith(`${base}/memory`)} items={MEMORY_TABS.map(t => (
          { to: `${base}/memory/${t}`, label: t[0].toUpperCase() + t.slice(1), active: pathname === `${base}/memory/${t}` || (t === 'notes' && pathname === `${base}/memory`) }))} />}
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
          ...(feature('backups') ? [{ to: '/backups', label: 'Backups', active: pathname === '/backups' }] : []),
        ] : []),
      ]} />
      {mayOpen('conformance') && <Drawer label="Conformance" icon={<ShieldCheck aria-hidden size={16} />} to={feature('conformance-estate') ? '/conformance' : '/conformance/reviews'} active={pathname.startsWith('/conformance')} items={[
        ...(feature('conformance-estate') ? [{ to: '/conformance', label: 'Projects', active: pathname === '/conformance' || pathname.startsWith('/conformance/projects') }] : []),
        { to: '/conformance/reviews', label: 'Reviews', active: pathname.startsWith('/conformance/reviews') },
        ...(feature('conformance-estate') ? [{ to: '/conformance/checks', label: 'Checks', active: pathname.startsWith('/conformance/checks') }] : []),
        { to: '/conformance/remediations', label: 'Remediations', active: pathname.startsWith('/conformance/remediations') },
        ...(feature('what-ifs') ? [{ to: '/conformance/what-ifs', label: 'Pack upgrades', active: pathname.startsWith('/conformance/what-ifs') }] : []),
      ]} />}
      <div className="nav-group">Setup</div>
 <NavLink to="/features" className={cls}><Settings aria-hidden size={16} />Features</NavLink>
      <Drawer label="Ways of working" icon={<BookOpen aria-hidden size={16} />} to="/packs" active={wow} items={[
        { to: '/packs', label: 'Packs', active: pathname.startsWith('/packs') },
        { to: '/workflows', label: 'Workflows', active: pathname.startsWith('/workflows') },
        { to: '/guidance', label: 'Guidance', active: pathname.startsWith('/guidance') },
        { to: '/policies', label: 'Policies', active: pathname.startsWith('/policies') },
      ]} />
      {mayOpen('integrations') && INTEGRATION_MENU.map((g) => (
        <Drawer key={g.key} label={g.heading} icon={g.key === 'services' ? <Blocks aria-hidden size={16} /> : <Plug aria-hidden size={16} />} to={`/${g.key}`}
          active={new RegExp(`^/${g.key}(/|$)`).test(pathname)}
          items={g.items.filter(i => i.key !== 'cloud-account' || feature('app-platform')).map((i) => ({ to: `/${g.key}/${i.key}`, label: i.title, active: pathname === `/${g.key}/${i.key}` }))} />))}
      {mayOpen('evals') && <NavLink to="/evals" className={cls}><FlaskConical aria-hidden size={16} />Evals</NavLink>}
      {mayOpen('people') && <Drawer label="People" icon={<Users aria-hidden size={16} />} to="/people/accounts" active={pathname.startsWith('/people')} items={PEOPLE_PAGES.map(([key, label]) => ({ to: `/people/${key}`, label, active: pathname.startsWith(`/people/${key}`) }))} />}
      <NavLink to="/usage" className={cls}><ChartColumn aria-hidden size={16} />Usage</NavLink>
      {mayUpdate && feature('updates') && <NavLink to="/updates" className={cls}><RefreshCw aria-hidden size={16} />Updates</NavLink>}
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
  if (invite) {
    const page = <InvitePage token={invite[1] ? decodeURIComponent(invite[1]) : ''} />
    return <><TopBar me={null} standalone narrow={narrow} /><main className="standalone-page"><Routes>
      <Route path="/invite" element={<CoreArea area="people">{page}</CoreArea>} />
      <Route path="/invite/:token" element={<CoreArea area="people">{page}</CoreArea>} />
    </Routes></main></>
  }
  if (me && me.mode === 'internal' && !me.user) {
    return <><TopBar me={null} standalone narrow={narrow} /><main className="standalone-page"><Login needsBootstrap={!!me.needsBootstrap} onSignedIn={loadMe} external={me.external} /></main></>
  }
  return (
    <div className="layout">
      <TopBar me={me} narrow={narrow} menuOpen={menuOpen} onMenu={() => setMenuOpen((o) => !o)} />
      {narrow && menuOpen && <div className="sidebar-backdrop" onClick={() => setMenuOpen(false)} />}
      <nav id="sidebar" className={'sidebar' + (narrow ? ' narrow' : '') + (menuOpen ? ' open' : '')}>
        <div className="sidebar-inner">
          <SidebarNav />
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
 <Route path="/features" element={<CoreArea area="setup"><Features /></CoreArea>} />
          <Route path="/" element={<CoreArea area="setup"><Dashboard /></CoreArea>} />
          <Route path="/attention/checks" element={<CoreArea area="setup"><AttentionPage checks /></CoreArea>} />
          <Route path="/attention/checks/:checkKey" element={<CoreArea area="setup"><AttentionPage checks /></CoreArea>} />
          <Route path="/attention" element={<CoreArea area="setup"><AttentionPage /></CoreArea>} />
 <Route path="/updates" element={<FeatureGate area="updates"><UpdatesPage /></FeatureGate>} />
          <Route path="/projects" element={<CoreArea area="projects"><Projects /></CoreArea>} />
          <Route path="/deployments" element={<CoreArea area="deployments"><Deployments /></CoreArea>} />
          <Route path="/releases" element={<CoreArea area="releases"><Deployments page="releases" /></CoreArea>} />
          <Route path="/backups" element={<FeatureGate area="backups"><InstanceGate page="infrastructure"><Deployments page="backups" /></InstanceGate></FeatureGate>} />
          <Route path="/complaints" element={<CoreArea area="complaints"><InstanceGate page="complaints"><Complaints tab="new" /></InstanceGate></CoreArea>} />
          <Route path="/complaints/answered" element={<CoreArea area="complaints"><InstanceGate page="complaints"><Complaints tab="answered" /></InstanceGate></CoreArea>} />
          <Route path="/complaints/archive" element={<CoreArea area="complaints"><InstanceGate page="complaints"><Complaints tab="archived" /></InstanceGate></CoreArea>} />
          <Route path="/conformance" element={<FeatureGate area="conformance-estate"><InstanceGate page="conformance"><Conformance tab="projects" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/checks/:checkKey/projects" element={<FeatureGate area="conformance-estate"><InstanceGate page="conformance"><CheckProjects /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/reviews/:reviewId/checks/:checkKey/projects" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><CheckProjects /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/checks/:checkKey" element={<FeatureGate area="conformance-estate"><InstanceGate page="conformance"><Conformance tab="checks" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/reviews/:reviewId/checks/:checkKey" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><Conformance tab="checks" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/checks" element={<FeatureGate area="conformance-estate"><InstanceGate page="conformance"><Conformance tab="checks" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/reviews" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><Conformance tab="reviews" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/reviews/:reviewId" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><Conformance tab="projects" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/reviews/:reviewId/checks" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><Conformance tab="checks" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/reviews/:reviewId/projects/:slug" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><Conformance tab="project" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/projects/:slug" element={<FeatureGate area="conformance-estate"><InstanceGate page="conformance"><Conformance tab="project" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/what-ifs" element={<FeatureGate area="what-ifs"><InstanceGate page="conformance"><Conformance tab="what-ifs" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/what-ifs/:id/projects/:slug" element={<FeatureGate area="what-ifs"><InstanceGate page="conformance"><WhatIfPage /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/what-ifs/:id" element={<FeatureGate area="what-ifs"><InstanceGate page="conformance"><WhatIfPage /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/remediations" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><Conformance tab="remediations" /></InstanceGate></FeatureGate>} />
          <Route path="/conformance/remediations/:remId" element={<FeatureGate area="conformance"><InstanceGate page="conformance"><Conformance tab="remediation" /></InstanceGate></FeatureGate>} />
          <Route path="/projects/_new" element={<CoreArea area="projects"><NewProject /></CoreArea>} />
          <Route path="/projects/:slug/releases/:number/log" element={<CoreArea area="releases"><ReleaseLogPage /></CoreArea>} />
          <Route path="/projects/:slug/deployments/:name" element={<CoreArea area="projects"><DeploymentPage /></CoreArea>} />
          <Route path="/projects/:slug" element={<CoreArea area="projects"><Project /></CoreArea>} />
          <Route path="/operations/:id" element={<CoreArea area="record"><OperationPage /></CoreArea>} />
          <Route path="/projects/:slug/:tab" element={<ProjectFeatureGate><Project /></ProjectFeatureGate>} />
          <Route path="/projects/:slug/:tab/:stab" element={<ProjectFeatureGate><Project /></ProjectFeatureGate>} />
          <Route path="/changes/:id/records/:recordId" element={<CoreArea area="record"><ChangeRecordPage /></CoreArea>} />
          <Route path="/changes/:id/work/:stepId/request" element={<CoreArea area="record"><WorkRecordPage section="request" /></CoreArea>} />
          <Route path="/changes/:id/work/:stepId/workflow" element={<CoreArea area="record"><WorkRecordPage section="workflow" /></CoreArea>} />
          <Route path="/changes/:id/work/:stepId/report" element={<CoreArea area="record"><WorkRecordPage section="report" /></CoreArea>} />
          <Route path="/changes/:id/work/:stepId/records/:artifactId" element={<CoreArea area="record"><WorkRecordPage section="record" /></CoreArea>} />
          <Route path="/changes/:id/work/:stepId" element={<CoreArea area="record"><WorkRecordPage /></CoreArea>} />
          <Route path="/changes/:id" element={<CoreArea area="record"><ChangePage /></CoreArea>} />
          <Route path="/adrs/:id" element={<CoreArea area="record"><AdrPage /></CoreArea>} />
          <Route path="/projects/:slug/chats" element={<CoreArea area="record"><PastChatsPage /></CoreArea>} />
          <Route path="/projects/:slug/chats/:sessionId" element={<CoreArea area="record"><PastChatsPage /></CoreArea>} />
          <Route path="/guidance/generated" element={<CoreArea area="ways"><GeneratedGuidance page /></CoreArea>} />
          <Route path="/guidance/generated/:name" element={<CoreArea area="ways"><GeneratedGuidance page /></CoreArea>} />
          <Route path="/workflows/:name" element={<CoreArea area="ways"><WorkflowLibrary page /></CoreArea>} />
          <Route path="/policies/new" element={<CoreArea area="ways"><PolicyNewPage /></CoreArea>} />
          <Route path="/policies/:name" element={<CoreArea area="ways"><PoliciesPage page /></CoreArea>} />
          <Route path="/packs/:name/versions/:version" element={<CoreArea area="ways"><PackPage /></CoreArea>} />
          <Route path="/guidance/:name/projects" element={<CoreArea area="ways"><GuidanceProjects /></CoreArea>} />
          <Route path="/policies/:name/projects" element={<CoreArea area="ways"><PolicyProjects /></CoreArea>} />
          <Route path="/packs/:name/projects" element={<CoreArea area="ways"><PackProjects /></CoreArea>} />
          <Route path="/integrations/profiles/:profile/projects" element={<CoreArea area="models"><InstanceGate page="integrations"><ModelProfilePage projects /></InstanceGate></CoreArea>} />
          <Route path="/integrations/:name/projects" element={<CoreArea area="setup"><InstanceGate page="integrations"><IntegrationProjects /></InstanceGate></CoreArea>} />
          <Route path="/services/:name/projects" element={<CoreArea area="setup"><InstanceGate page="integrations"><IntegrationProjects /></InstanceGate></CoreArea>} />
          <Route path="/guidance" element={<CoreArea area="ways"><WaysOfWorking tab="guidance" /></CoreArea>} />
          <Route path="/guidance/:name" element={<CoreArea area="ways"><GuidanceBlockPage /></CoreArea>} />
          <Route path="/workflows" element={<CoreArea area="ways"><WaysOfWorking tab="workflows" /></CoreArea>} />
          <Route path="/projects/:slug/workflows/*" element={<CoreArea area="projects"><ToProjectWorking /></CoreArea>} />
          <Route path="/projects/:slug/console" element={<CoreArea area="chats"><Console /></CoreArea>} />
          <Route path="/integrations" element={<CoreArea area="setup"><InstanceGate page="integrations"><Integrations /></InstanceGate></CoreArea>} />
          <Route path="/integrations/profiles/:profile" element={<CoreArea area="models"><InstanceGate page="integrations"><ModelProfilePage /></InstanceGate></CoreArea>} />
          <Route path="/jobs" element={<CoreArea area="record"><InstanceGate page="infrastructure"><JobsPage /></InstanceGate></CoreArea>} />
          <Route path="/targets" element={<CoreArea area="deployments"><InstanceGate page="infrastructure"><InfrastructureHome /></InstanceGate></CoreArea>} />
          <Route path="/targets/new" element={<CoreArea area="deployments"><InstanceGate page="infrastructure"><InfrastructureNew /></InstanceGate></CoreArea>} />
          <Route path="/targets/:name/resources" element={<CoreArea area="deployments"><InstanceGate page="infrastructure"><TargetResourcesPage /></InstanceGate></CoreArea>} />
          <Route path="/targets/:name/resources/:id" element={<CoreArea area="deployments"><InstanceGate page="infrastructure"><TargetResourcesPage detail /></InstanceGate></CoreArea>} />
          <Route path="/targets/:name/capacity" element={<CoreArea area="deployments"><InstanceGate page="infrastructure"><TargetPage tab="capacity" /></InstanceGate></CoreArea>} />
          <Route path="/targets/:name/history" element={<CoreArea area="deployments"><InstanceGate page="infrastructure"><TargetPage tab="history" /></InstanceGate></CoreArea>} />
          <Route path="/targets/:name" element={<CoreArea area="deployments"><InstanceGate page="infrastructure"><TargetPage /></InstanceGate></CoreArea>} />
          <Route path="/integrations/:name" element={<CoreArea area="setup"><InstanceGate page="integrations"><Integrations /></InstanceGate></CoreArea>} />
          <Route path="/services" element={<CoreArea area="setup"><InstanceGate page="integrations"><Integrations /></InstanceGate></CoreArea>} />
          <Route path="/services/:name" element={<CoreArea area="setup"><InstanceGate page="integrations"><Integrations /></InstanceGate></CoreArea>} />
          <Route path="/policies" element={<CoreArea area="ways"><WaysOfWorking tab="policies" /></CoreArea>} />
          <Route path="/packs" element={<CoreArea area="ways"><WaysOfWorking tab="packs" /></CoreArea>} />
          <Route path="/packs/:name" element={<CoreArea area="ways"><PackPage /></CoreArea>} />
          <Route path="/account" element={<CoreArea area="people"><MyExternalSignIn /></CoreArea>} />
          <Route path="/people/accounts" element={<CoreArea area="people"><InstanceGate page="people"><People view="accounts" /></InstanceGate></CoreArea>} />
          <Route path="/people/accounts/:name" element={<CoreArea area="people"><InstanceGate page="people"><People view="accounts" /></InstanceGate></CoreArea>} />
          <Route path="/people/groups" element={<CoreArea area="people"><InstanceGate page="people"><People view="groups" /></InstanceGate></CoreArea>} />
          <Route path="/people/groups/:name" element={<CoreArea area="people"><InstanceGate page="people"><People view="groups" /></InstanceGate></CoreArea>} />
          <Route path="/people/roles" element={<CoreArea area="people"><InstanceGate page="people"><People view="roles" /></InstanceGate></CoreArea>} />
          <Route path="/people/sign-in" element={<CoreArea area="people"><InstanceGate page="people"><People view="sign-in" /></InstanceGate></CoreArea>} />
          <Route path="/people/agents-and-tokens" element={<CoreArea area="people"><InstanceGate page="people"><People view="agents-and-tokens" /></InstanceGate></CoreArea>} />
          <Route path="/usage" element={<CoreArea area="usage"><UsagePage /></CoreArea>} />
          <Route path="/evals" element={<FeatureGate area="evals"><InstanceGate page="evals"><EvalsPage /></InstanceGate></FeatureGate>} />
          <Route path="/evals/new" element={<FeatureGate area="evals"><InstanceGate page="evals"><EvalStartPage /></InstanceGate></FeatureGate>} />
          <Route path="/evals/:id" element={<FeatureGate area="evals"><InstanceGate page="evals"><EvalScorecardPage /></InstanceGate></FeatureGate>} />
          <Route path="/docs" element={<CoreArea area="setup"><DocsPage /></CoreArea>} />
          <Route path="/docs/:section/:slug" element={<CoreArea area="setup"><DocsPage /></CoreArea>} />
          <Route path="/text-roles" element={<CoreArea area="setup"><TextRoles /></CoreArea>} />
          <Route path="*" element={<CoreArea area="setup"><div><h1>Page not found</h1><p>This address does not identify a page. Release links include the project name.</p><Link to="/projects">Find your project</Link></div></CoreArea>} />

        </Routes>
        </Suspense>
        </StaleScope>
        </ErrorBoundary>
      </main>
    </div>
  )
}
