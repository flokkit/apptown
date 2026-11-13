import { useEffect, useLayoutEffect, useReducer, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useAction, useGet } from '../data'
import type { AuthzHoldings, ProjectManifestView } from '../wire.gen'
import { draftReducer, newDraft } from './newProject/draft'
import { buildFirstVersion, continueCreation, createProject, leaveSubmittedCreation, type CreationContext, type ProgressLine } from './newProject/create'
import { readCreation, type RetainedCreation } from './newProject/retained'
import { ReadStatus } from '../ReadGate'
import { RepositoryAttachment } from './project/settings/RepositoryAttachment'
import { NameStep } from './newProject/NameStep'
import { WayOfWorkingStep } from './newProject/WayOfWorkingStep'
import { RepositoryStep } from './newProject/RepositoryStep'
import { PlacementStep } from './newProject/PlacementStep'
import './NewProject.css'

// One retained draft carries the four decisions through creation and recovery.
type Manifest = ProjectManifestView

export default function NewProject() {
  const identity = useGet<AuthzHoldings>('/authz/me')
  const [confirmed, setConfirmed] = useState<string | null>(null)
  useEffect(() => { if (identity.current) setConfirmed(identity.data?.actorId ?? null) }, [identity.current, identity.data?.actorId])
  const actorId = identity.current ? identity.data?.actorId : confirmed && confirmed === identity.data?.actorId ? confirmed : null
  if (!actorId) return <>
    {!identity.current && identity.data && !identity.err && <p role="status">Reading your current account…</p>}
    <ReadStatus q={identity} what="your current account" />
    {identity.current && <div className="error">Sign in as a person before creating a project.</div>}
  </>
  return <>
    {!identity.current && <ReadStatus q={identity} what="your current account" />}
    <fieldset disabled={!identity.current} className="new-project-fieldset">
      <ProjectSetup key={actorId} actorId={actorId} identityCurrent={identity.current} />
    </fieldset>
  </>
}

function ProjectSetup({ actorId, identityCurrent }: { actorId: string; identityCurrent: boolean }) {
  const nav = useNavigate()
  const [initial] = useState(() => {
    try { return { saved: readCreation(sessionStorage, actorId), error: '' } }
    catch (e) { return { saved: null, error: (e as Error).message } }
  })
  const [saved, setSaved] = useState<RetainedCreation | null>(initial.saved)
  const [storageError, setStorageError] = useState(initial.error)
  const [file, setFile] = useState<File | null>(null)
  const [d, act] = useReducer(draftReducer, undefined, () => initial.saved ? { ...initial.saved.draft, bundleFile: null } : newDraft())
  const active = useRef(false)
  useLayoutEffect(() => { active.current = identityCurrent; return () => { active.current = false } }, [identityCurrent])
  const context = (): CreationContext => ({ actorId, storage: sessionStorage, file: file ?? d.bundleFile, assertActive: () => {
    if (!active.current) throw new Error('Read your current account again before continuing setup.')
  } })
  const refreshSaved = () => {
    if (!active.current) return
    try { setSaved(readCreation(sessionStorage, actorId)) }
    catch (e) { setStorageError((e as Error).message) }
  }
  // The assembly log: what was wired (ok) and what was skipped (warn).
  const [progress, setProgress] = useState<ProgressLine[]>([])
  const [manifest, setManifest] = useState<Manifest | null>(null)
  const { run, busy, err } = useAction()

  const create = (where: { remote: boolean; namespaceId: string }) => {
    setProgress([])
    void run(async () => {
      try {
        let skipped = false
        const m = await createProject(d, where, line => { skipped ||= !!line.warn; setProgress(p => [...p, line]) }, context())
        setManifest(m)
        if (!skipped) {
          const result = await buildFirstVersion(context())
          nav(`/changes/${result.change.change.id}?tab=conversation&kickoff=1`)
        }
      }
      finally { refreshSaved() }
    })
  }
  const resume = () => void run(async () => {
    try { setManifest(await continueCreation(context(), (line) => setProgress((p) => [...p, line]))) }
    finally { refreshSaved() }
  })
  const another = () => void run(async () => {
    await leaveSubmittedCreation(context())
    const fresh = newDraft()
    act({ type: 'restore', draft: saved?.pending === 'rejected'
      ? { ...saved.draft, creationRequestId: fresh.creationRequestId, attachmentRequestId: fresh.attachmentRequestId, step: 1, bundleFile: file ?? d.bundleFile }
      : fresh })
    setSaved(null); setFile(null); setProgress([]); setManifest(null)
  })
  const buildV0 = () => run(async () => {
    try {
      const result = await buildFirstVersion(context())
      nav(`/changes/${result.change.change.id}?tab=conversation&kickoff=1`)
    } finally { refreshSaved() }
  })
  if (storageError) return <div className="error">{storageError} The saved setup has not been replaced.</div>
  if (manifest) {
    return (
      <div className="new-project-project-setup-div">
        <h1>{manifest.project.name || manifest.project.slug} <span className="badge">created</span></h1>
        <div className="card mt-3">
          <ProgressLog lines={progress} />
        </div>
        <div className="row mt-4">
          <button className="primary new-project-project-setup-primary"
            disabled={busy} onClick={() => void buildV0()}>Build the first version →</button>
          <Link to={`/projects/${manifest.project.slug}`}>Project page</Link>
        </div>
        {err && <div className="error">{err}</div>}
      </div>
    )
  }

  if (saved) return <div className="new-project-project-setup-div-2">
    <h1>Saved project setup</h1>
    <p><strong>{saved.draft.name}</strong> — {saved.draft.description}</p>
    {saved.project ? <p>Project <Link to={`/projects/${saved.project.slug}`}>{saved.project.slug}</Link> exists.</p>
      : <p>{saved.pending === 'rejected' ? 'Creation was refused. Your submitted choices are kept.' : 'Check the original creation request before starting another project.'}</p>}
    {saved.pending && !['project', 'canonical', 'manifest', 'services-read', 'rejected'].includes(saved.pending) && <p>Check {saved.pending} setup in the project. Its outcome will not be guessed or repeated automatically.</p>}
    {saved.draft.origin === 'bundle' && (!saved.project || saved.pending === 'canonical') && <label>Original Git bundle
      <input type="file" disabled={busy} accept=".bundle,application/x-git-bundle" aria-label="Original Git bundle" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
    </label>}
    {saved.project && saved.pending === 'upstream' && <RepositoryAttachment slug={saved.project.slug} />}
    <ProgressLog lines={progress} />
    <div className="row mt-3">
      {saved.pending !== 'rejected' && <button className="primary" disabled={busy} onClick={resume}>Check saved setup</button>}
      {(saved.project || saved.pending === 'rejected' || saved.pending === null) && <button disabled={busy} onClick={another}>{saved.pending === 'rejected' ? 'Edit choices for a new request' : 'Start another project'}</button>}
    </div>
    {err && <div className="error">{err}</div>}
  </div>

  return (
    <div className="new-project-project-setup-div-2">
      <h1 className="new-project-project-setup-h1">Create project</h1>
      <p>Start a new app or import existing code.{d.step < 3 && ' Choose the code in the Code step.'}</p>
      <fieldset disabled={busy} className="new-project-fieldset">
      <StepChips step={d.step} />
      {d.step === 1 && <NameStep d={d} act={act} />}
      {d.step === 2 && <WayOfWorkingStep d={d} act={act} />}
      {d.step === 3 && (
        <RepositoryStep d={d} act={act} busy={busy} onNext={where => { act({ type: 'repo', set: { remoteChoice: where.remote, namespaceId: where.namespaceId } }); act({ type: 'step', step: 4 }) }} />
      )}
      {d.step === 4 && <>
        <PlacementStep d={d} act={act} busy={busy} onCreate={() => create({ remote: !!d.remoteChoice, namespaceId: d.namespaceId })} />
        {progress.length > 0 && <div className="card mt-3"><ProgressLog lines={progress} /></div>}
      </>}
      </fieldset>
      {err && <div className="error">{err}</div>}
    </div>
  )
}

// The assembly log: one line per step, a skipped step drawn as a warning.
// The four steps of making a project, the current one marked.
function StepChips({ step }: { step: number }) {
  return (
    <ol className="wiz-steps">
      {['What it does', 'Way of working', 'Code', 'Where it runs'].map((label, i) => (
        <li key={label} className={step === i + 1 ? 'current' : step > i + 1 ? 'done' : ''} aria-current={step === i + 1 ? 'step' : undefined}>
          {label}{step > i + 1 && <span className="sr-only"> completed</span>}
        </li>
      ))}
    </ol>
  )
}

function ProgressLog({ lines }: { lines: { text: string; warn?: boolean }[] }) {
  return <>{lines.map((l, i) => <div key={i} className={'small' + (l.warn ? ' warn-line' : '')}>{l.text}</div>)}</>
}
