import { PRODUCT } from './brand'
import { NavLink, Navigate, Route, Routes, useLocation, useParams } from 'react-router-dom'
import { ErrorBoundary } from './ErrorBoundary'
import { StaleScope } from './ReadGate'
import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { currentUser, setCurrentUser } from './api'
import Dashboard from './pages/Dashboard'
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
import { InfrastructureHome } from './pages/InfrastructureHome'
import { InfrastructureNew } from './pages/InfrastructureNew'
import Conformance from './pages/Conformance'
import UsagePage from './pages/Usage'
import People from './pages/People'
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
import { InstanceGate, useInstancePages } from './instancePages'
import TopBar, { Find, ThemeSwitch, UserLine } from './TopBar'
import { useNarrow } from './chrome'
import { withChunkReload } from './chunkReload'
import { identityChanged, useGet, useOnline } from './data'
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
  const cls = ({ isActive }: { isActive: boolean }) => 'nav' + (isActive ? ' active' : '')
  // Pages about the whole forge are left out for someone who cannot read
  // them; the menu is otherwise the same for everyone.
  const mayOpen = useInstancePages()
  const wow = /^\/(packs|workflow-library|guidance|policies)(\/|$)/.test(pathname)

  if (slug) {
    return (
      <>
        <NavLink to="/projects" className="nav muted small" style={{ paddingBottom: 2 }}>‹ all projects</NavLink>
        <div className="muted small" style={{ padding: '4px 10px 8px', textTransform: 'uppercase', letterSpacing: 1 }}>{slug}</div>
        <NavLink to={`/projects/${slug}`} end className={cls}>Overview</NavLink>
        <NavLink to={`/projects/${slug}/console`} className={cls}>Chat ▸</NavLink>
        <NavLink to={`/projects/${slug}/security`} className={cls}>Security</NavLink>
        <NavLink to={`/projects/${slug}/settings`} className={cls}>Settings</NavLink>
      </>
    )
  }
  return (
    <>
      <NavLink to="/" end className={cls}>Home</NavLink>
      <NavLink to="/projects" end className={cls}>Projects</NavLink>
      <NavLink to="/deployments" className={cls}>Deployments</NavLink>
      {mayOpen('infrastructure') && <NavLink to="/infrastructure" className={cls}>Infrastructure</NavLink>}
      {mayOpen('conformance') && <NavLink to="/conformance" className={cls}>Conformance</NavLink>}
      <div className="nav-group">setup</div>
      <NavLink to="/packs" className={() => 'nav' + (wow ? ' active' : '')}>Ways of working</NavLink>
      {mayOpen('evals') && <NavLink to="/evals" className={cls}>Evals</NavLink>}
      {mayOpen('integrations') && <NavLink to="/integrations" className={cls}>Integrations</NavLink>}
      {mayOpen('people') && <NavLink to="/people" className={cls}>People</NavLink>}
      <NavLink to="/usage" className={cls}>Usage</NavLink>
      {mayOpen('complaints') && <NavLink to="/complaints" className={cls}>Complaints</NavLink>}
      <NavLink to="/docs" className={cls}>Docs</NavLink>
    </>
  )
}

// A target's page lives under Infrastructure now.
function ToInfrastructureTarget() {
  const { name } = useParams()
  return <Navigate to={`/infrastructure/${name}`} replace />
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
    return <Login needsBootstrap={!!me.needsBootstrap} onSignedIn={loadMe} />
  }
  return (
    <div className="layout">
      <TopBar me={me} narrow={narrow} menuOpen={menuOpen} onMenu={() => setMenuOpen((o) => !o)} />
      {narrow && menuOpen && <div className="sidebar-backdrop" onClick={() => setMenuOpen(false)} />}
      <nav id="sidebar" className={'sidebar' + (narrow ? ' narrow' : '') + (menuOpen ? ' open' : '')}>
        <div className="sidebar-inner">
          {narrow && routePath !== '/projects' && <Find />}
          <SidebarNav />
          <div className="spacer" />
          {narrow && (
            <div className="sidebar-foot">
              <ThemeSwitch />
              <UserLine me={me} />
            </div>
          )}
          {me && me.mode === 'open' && (
            <div className="user">
              acting as
              <input
                value={user}
                onChange={(e) => { setUser(e.target.value); setCurrentUser(e.target.value) }}
              />
            </div>
          )}
        </div>
      </nav>
      <main className="main">
        {meQ.err && <div className="error small" style={{ marginBottom: 10 }}>{PRODUCT} could not say who you are: {meQ.err}</div>}
        {!online && <div className="muted small" style={{ marginBottom: 10 }}>You are offline. Pages keep what they last read and refresh when the connection returns.</div>}
        {/* Keyed by identity as well: another person starts from fresh pages,
            with no draft or local state left from the one before. */}
        <ErrorBoundary key={`${identity ?? ''} ${routePath}`}>
        <StaleScope>
        <Suspense fallback={<div className="muted">Opening the page…</div>}>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/projects" element={<Projects />} />
          <Route path="/deployments" element={<Deployments />} />
          <Route path="/deployments/:gtab" element={<Deployments />} />
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
          <Route path="/conformance/what-ifs/:id" element={<InstanceGate page="conformance"><WhatIfPage /></InstanceGate>} />
          <Route path="/conformance/remediations" element={<InstanceGate page="conformance"><Conformance tab="remediations" /></InstanceGate>} />
          <Route path="/conformance/ways" element={<InstanceGate page="conformance"><Conformance tab="ways" /></InstanceGate>} />
          <Route path="/conformance/remediations/:remId" element={<InstanceGate page="conformance"><Conformance tab="remediation" /></InstanceGate>} />
          <Route path="/projects/_new" element={<NewProject />} />
          <Route path="/projects/:slug" element={<Project />} />
          <Route path="/operations/:id" element={<OperationPage />} />
          <Route path="/projects/:slug/:tab" element={<Project />} />
          <Route path="/projects/:slug/:tab/:stab" element={<Project />} />
          <Route path="/changes/:id" element={<ChangePage />} />
          <Route path="/items/:id" element={<ChangePage />} />
          <Route path="/adrs/:id" element={<AdrPage />} />
          <Route path="/guidance" element={<WaysOfWorking tab="guidance" />} />
          <Route path="/guidance/:id" element={<GuidanceBlockPage />} />
          <Route path="/workflow-library" element={<WaysOfWorking tab="workflows" />} />
          <Route path="/projects/:slug/workflows/*" element={<ToProjectWorking />} />
          <Route path="/projects/:slug/console" element={<Console />} />
          <Route path="/integrations" element={<InstanceGate page="integrations"><Integrations /></InstanceGate>} />
          <Route path="/infrastructure" element={<InstanceGate page="infrastructure"><InfrastructureHome /></InstanceGate>} />
          <Route path="/infrastructure/jobs" element={<InstanceGate page="infrastructure"><InfrastructureHome /></InstanceGate>} />
          <Route path="/infrastructure/new" element={<InstanceGate page="infrastructure"><InfrastructureNew /></InstanceGate>} />
          <Route path="/infrastructure/:name" element={<InstanceGate page="infrastructure"><TargetPage /></InstanceGate>} />
          <Route path="/integrations/targets/:name" element={<InstanceGate page="infrastructure"><ToInfrastructureTarget /></InstanceGate>} />
          <Route path="/integrations/:name" element={<InstanceGate page="integrations"><Integrations /></InstanceGate>} />
          <Route path="/policies" element={<WaysOfWorking tab="policies" />} />
          <Route path="/packs" element={<WaysOfWorking tab="packs" />} />
          <Route path="/packs/:name" element={<PackPage />} />
          <Route path="/people" element={<InstanceGate page="people"><People /></InstanceGate>} />
          <Route path="/usage" element={<UsagePage />} />
          <Route path="/evals" element={<InstanceGate page="evals"><EvalsPage /></InstanceGate>} />
          <Route path="/evals/new" element={<InstanceGate page="evals"><EvalStartPage /></InstanceGate>} />
          <Route path="/evals/:id" element={<InstanceGate page="evals"><EvalScorecardPage /></InstanceGate>} />
          <Route path="/docs" element={<DocsPage />} />
          <Route path="/docs/:section/:slug" element={<DocsPage />} />
        </Routes>
        </Suspense>
        </StaleScope>
        </ErrorBoundary>
      </main>
    </div>
  )
}
