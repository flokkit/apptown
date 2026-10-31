import { Tabs } from '../Tabs'
import { PhoneDetails } from '../GlanceRow'
import { Status } from '../Status'
import { PRODUCT } from '../brand'
import { AppAddress, RuntimeStatus } from '../runtime'
import { NavLink, useParams } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { ApplicationOverview } from './project/ApplicationOverview'
import { UsageView } from './Usage'
import type { DeploymentView } from '../wire.gen'
import { NewReleasePage, ReleasePermalinkPage } from './project/Release'
import { FindingsTab, SuppressionsTab } from './project/Security'
import { GeneralTab, LifecycleNote } from './project/settings/General'
import { GuidanceTab } from './project/settings/Working'
import { ServicesTab, ProjectIntegrationsTab } from './project/settings/Services'
import { MembersTab } from './project/settings/Members'
import { SecretsTab } from './project/settings/Secrets'
import { DeploymentsTab } from './project/settings/Deployments'
import { AdrsTab } from './project/settings/Decisions'
import { SECURITY_TABS, SETTINGS_TABS, SETTINGS_LABEL, resolveProjectTabs } from './project/tabs'
import type { ProjectDetail as ProjectT, MainStanding } from '../wire.gen'

// Where main stands against AppTown's record. A plain note, not an alarm:
// it says what differs, never who moved main (an AppTown move whose record has
// not settled reads the same as a commit made elsewhere).
export function MainNote({ main }: { main?: MainStanding | null }) {
  const notes = [main?.note, main?.upstream?.note].filter((n): n is string => !!n)
  if (notes.length === 0) return null
  return (
    <div className="card muted small" style={{ marginBottom: 14 }} data-testid="main-note">
      {notes.map((n) => <div key={n}>{n[0].toUpperCase() + n.slice(1)}.</div>)}
    </div>
  )
}

// Out-of-band edits: someone changed the project's stored files without
// going through AppTown. There is no authorized way to do that, so this
// shows for everyone — tampering hides best when only the tamperer knows to
// look. The two buttons are the only sanctioned exits (admin, audited).
function IntegrityBanner({ slug, files, onResolved }: {
  slug: string; files?: string[]; onResolved: () => void
}) {
  const { run, busy, err } = useAction(onResolved)
  if (!files || files.length === 0) return null
  const resolve = (action: 'adopt' | 'discard') => run(() => post(`/projects/${slug}/repo/integrity`, { action }))
  return (
    <div className="card" style={{ border: '1px solid var(--bad)', marginBottom: 14 }}>
      <div style={{ fontWeight: 600 }}>Someone changed this project's stored files directly, outside {PRODUCT}.</div>
      <div className="muted small" style={{ marginTop: 4 }}>
        There is no authorized way to do that · until an admin explains it, treat it as tampering.
        {PRODUCT} will not merge or ship over it. Changed: {files.join(', ')}.
        The audit log holds what was found.
      </div>
      <div className="row" style={{ marginTop: 8, gap: 8 }}>
        <button disabled={busy} onClick={() => resolve('adopt')}>Keep the edits (recorded as your decision)</button>
        <button className="subtle" disabled={busy} onClick={() => resolve('discard')}>Discard the edits</button>
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

export default function Project() {
  const { slug, tab: rawTab, stab: rawStab } = useParams()
  const { tab, secTab, setTab } = resolveProjectTabs(rawTab, rawStab)
  const q = useGet<ProjectT>(slug ? `/projects/${slug}` : null, { poll: 5000 })
  const { data: project, reload: load, current: projectCurrent } = q

  // A failed refresh keeps the page, and the settings form's draft, mounted
  // under a stale note; only a project never read shows the error instead.
  if (!project) return <ReadStatus q={q} what="the project" />

  return (
    <div>
      <StaleNote q={q} />
      {tab !== 'overview' && <ProjectHeader project={project} />}
      <LifecycleNote slug={project.slug} lifecycle={project.lifecycle} />
      <IntegrityBanner slug={project.slug} files={project.outOfBandEdits} onResolved={load} />
      <MainNote main={project.main} />
      {tab === 'overview' && <ApplicationOverview project={project} />}
      {tab === 'security' && (
        <>
          <Tabs className="tabs">
            {SECURITY_TABS.map((t) => (
              <NavLink key={t} to={`/projects/${slug}/security/${t}`}
                className={({ isActive }) => 'tab' + (isActive || (t === 'findings' && !rawStab) ? ' active' : '')}>
                {t[0].toUpperCase() + t.slice(1)}
              </NavLink>
            ))}
          </Tabs>
          {secTab === 'findings' && <FindingsTab slug={slug!} />}
          {secTab === 'suppressions' && <SuppressionsTab slug={slug!} />}
        </>
      )}
      {tab === 'release' && <NewReleasePage slug={slug!} />}
      {tab === 'releases' && <ReleasePermalinkPage slug={slug!} number={rawStab} />}
      {tab === 'settings' && (
        <>
          <Tabs className="tabs">
            {SETTINGS_TABS.map((t) => (
              <NavLink key={t} to={`/projects/${slug}/settings/${t}`}
                className={() => 'tab' + (setTab === t ? ' active' : '')}>
                {SETTINGS_LABEL[t]}
              </NavLink>
            ))}
          </Tabs>
          {setTab === 'general' && <GeneralTab project={project} current={projectCurrent} onChanged={load} />}
          {setTab === 'working' && <GuidanceTab slug={slug!} />}
          {setTab === 'services' && (
            <>
              <h2 style={{ marginTop: 0 }}>Services</h2>
              <ServicesTab slug={slug!} />
              <h2>Connections</h2>
              <ProjectIntegrationsTab slug={slug!} />
            </>
          )}
          {setTab === 'deployments' && <DeploymentsTab slug={slug!} />}
          {setTab === 'secrets' && <SecretsTab slug={slug!} />}
          {setTab === 'members' && <MembersTab slug={slug!} />}
          {setTab === 'decisions' && <AdrsTab slug={slug!} />}
          {setTab === 'usage' && <UsageView project={slug!} />}
        </>
      )}
    </div>
  )
}

function ProjectHeader({ project }: { project: ProjectT }) {
  const q = useGet<DeploymentView[]>(`/projects/${project.slug}/deployments`)
  const envs = q.data ?? []

  return (
      <>
      <h1>
        {project.icon && <span style={{ marginRight: 8 }} aria-hidden>{project.icon}</span>}
        {project.name}
        {project.namespace && <span className="chip" style={{ marginLeft: 10 }} title="namespace (display grouping)">{project.namespace}</span>}
      </h1>
      <PhoneDetails>
      <div className="row small project-status-strip" style={{ marginBottom: 10 }}>
        {envs.map((e) => <span key={e.name} className="row" style={{ gap: 4 }}>
          <RuntimeStatus observation={e} />
          <AppAddress url={e.url} hostOnly={e.hostOnly}>{e.displayLabel}</AppAddress>
        </span>)}
        {!q.data && q.err && <Status word="unknown" tone="unknown" help={q.err} />}
      </div>
      </PhoneDetails>
      </>
  )
}
