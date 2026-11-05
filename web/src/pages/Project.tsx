import { PRODUCT } from '../brand'
import { useParams } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { ApplicationOverview } from './project/ApplicationOverview'
import { UsageView } from './Usage'
import { NewReleasePage, ReleasePermalinkPage } from './project/Release'
import { FindingsTab, SuppressionsTab } from './project/Security'
import { GeneralTab, LifecycleNote } from './project/settings/General'
import { GuidanceTab } from './project/settings/Working'
import { ServicesTab, ProjectIntegrationsTab } from './project/settings/Services'
import { MembersTab } from './project/settings/Members'
import { SecretsTab } from './project/settings/Secrets'
import { DeploymentsTab } from './project/settings/Deployments'
import { AdrsTab } from './project/settings/Decisions'
import { SETTINGS_LABEL, resolveProjectTabs } from './project/tabs'
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
      <div className="muted small mt-1" >
        There is no authorized way to do that · until an admin explains it, treat it as tampering.
        {PRODUCT} will not merge or ship over it. Changed: {files.join(', ')}.
        The audit log holds what was found.
      </div>
      <div className="row mt-2" style={{ gap: 8 }}>
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
          {/* The menu's Security drawer holds these pages; the heading says which. */}
          <SubPageHead area="Security" label={secTab[0].toUpperCase() + secTab.slice(1)} />
          {secTab === 'findings' && <FindingsTab slug={slug!} />}
          {secTab === 'suppressions' && <SuppressionsTab slug={slug!} />}
        </>
      )}
      {tab === 'release' && <NewReleasePage slug={slug!} />}
      {tab === 'releases' && <ReleasePermalinkPage slug={slug!} number={rawStab} />}
      {tab === 'settings' && (
        <>
          {/* The menu's Settings drawer holds these pages; the heading says which. */}
          <SubPageHead area="Settings" label={SETTINGS_LABEL[setTab]} />
          {setTab === 'general' && <GeneralTab project={project} current={projectCurrent} onChanged={load} />}
          {setTab === 'working' && <GuidanceTab slug={slug!} />}
          {setTab === 'services' && (
            <>
              <ServicesTab slug={slug!} />
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

// The project's name above every sub-page. Its deployments are not repeated
// here: the overview has a Deployments section, Settings › Deployments the rest.
function ProjectHeader({ project }: { project: ProjectT }) {
  return (
    <h1>
      {project.icon && <span style={{ marginRight: 8 }} aria-hidden>{project.icon}</span>}
      {project.name}
      {project.namespace && <span className="badge" style={{ marginLeft: 10 }} title="namespace (display grouping)">{project.namespace}</span>}
    </h1>
  )
}

function SubPageHead({ area, label }: { area: string; label: string }) {
  return <div className="sub-page-head"><div className="page-area">{area}</div><h2>{label}</h2></div>
}
