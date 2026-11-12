import { LastScan } from './project/Janitor'
import { PageHead } from '../PageHead'
import { PRODUCT } from '../brand'
import { Link, useParams } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { ProjectDeployments } from './project/ProjectDeployments'
import { ApplicationOverview, ProjectChanges } from './project/ApplicationOverview'
import { UsageView } from './Usage'
import { ReleasePermalinkPage } from './project/Release'
import { FindingsTab, SuppressionsTab } from './project/Security'
import { ModelsTab } from './project/settings/Models'
import { GeneralTab, LifecycleNote } from './project/settings/General'
import { GuidanceTab } from './project/settings/Working'
import { ServicesTab, ProjectIntegrationsTab } from './project/settings/Services'
import { MembersTab } from './project/settings/Members'
import { SecretsTab } from './project/settings/Secrets'
import { AdrsTab } from './project/Decisions'
import { NotesTab } from './project/ProjectNotes'
import { OLD_SETTINGS, SETTINGS_LABEL, resolveProjectTabs } from './project/tabs'
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
      <div className="text-name">Someone changed this project's stored files directly, outside {PRODUCT}.</div>
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
  const { tab, secTab, setTab, memTab } = resolveProjectTabs(rawTab, rawStab)
  const q = useGet<ProjectT>(slug ? `/projects/${slug}` : null, { poll: 5000 })
  const { data: project, reload: load, current: projectCurrent } = q

  // A failed refresh keeps the page, and the settings form's draft, mounted
  // under a stale note; only a project never read shows the error instead.
  if (rawTab === 'settings' && rawStab && !(rawStab in SETTINGS_LABEL) && !(rawStab in OLD_SETTINGS)) return <><h1>Page not found</h1><Link to={`/projects/${slug}/deployments`}>Deployments</Link></>
  if (!project) return <ReadStatus q={q} what="the project" />

  return (
    <div>
      <StaleNote q={q} />
      {tab === 'changes' && <h1>Changes</h1>}
      {tab === 'deployments' && <PageHead page="projectDeployments" />}
      {tab === 'releases' && <h1>Release #{rawStab}</h1>}
      <LifecycleNote slug={project.slug} lifecycle={project.lifecycle} />
      <IntegrityBanner slug={project.slug} files={project.outOfBandEdits} onResolved={load} />
      <MainNote main={project.main} />
      {tab === 'overview' && <><ApplicationOverview project={project} /><div id="janitor"><LastScan slug={project.slug} janitor /></div></>}
      {tab === 'changes' && <ProjectChanges project={project} />}
      {tab === 'deployments' && <ProjectDeployments slug={slug!} />}
      {tab === 'security' && (
        <>
          {/* The menu's Security drawer holds these pages; the heading says which. */}
          <PageHead key={secTab} page="projectSecurity" sub={secTab === 'findings' ? 'Security findings' : 'Suppressions'} />
          {secTab === 'findings' && <FindingsTab slug={slug!} />}
          {secTab === 'suppressions' && <SuppressionsTab slug={slug!} />}
        </>
      )}
      {tab === 'releases' && <ReleasePermalinkPage slug={slug!} number={rawStab} />}
      {tab === 'memory' && <>
        <PageHead key={memTab} page="projectMemory" sub={memTab === 'notes' ? 'Notes' : 'Decisions'} />
        {memTab === 'notes' && <NotesTab project={project} />}
        {memTab === 'decisions' && <AdrsTab slug={slug!} />}
      </>}
      {tab === 'settings' && (
        <>
          {/* The menu's Settings drawer holds these pages; the heading says which. */}
          <PageHead key={setTab} page="projectSettings" sub={SETTINGS_LABEL[setTab]} />
          {setTab === 'general' && <GeneralTab project={project} current={projectCurrent} onChanged={load} />}
          {setTab === 'models' && <ModelsTab key={slug} slug={slug!} />}
          {setTab === 'working' && <GuidanceTab slug={slug!} />}
          {setTab === 'services' && (
            <>
              <ServicesTab slug={slug!} />
              <ProjectIntegrationsTab slug={slug!} />
            </>
          )}
          {setTab === 'secrets' && <SecretsTab slug={slug!} />}
          {setTab === 'members' && <MembersTab slug={slug!} />}
          {setTab === 'usage' && <UsageView project={slug!} />}
        </>
      )}
    </div>
  )
}

// The project's name above every sub-page. Its deployments are not repeated
// here: the overview has a Deployments section, Settings › Deployments the rest.
// The page title owns the heading; the project name is context.
