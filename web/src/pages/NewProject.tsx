import { PRODUCT } from '../brand'
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

// The new-project wizard: next → next → done, ONE
// decision per screen, defaults that are almost always right.
//   1. Description — a working name and address follow, with optional overrides.
//   2. Way of working — "pick for me" leads; the card wall only appears if
//      someone asks to choose themselves.
//   3. Repository — every project GETS a repo (not a question); the only
//      real choice is whether a GitLab copy is created too (default: yes).
// Then Create runs the whole assembly with a progress log and lands on the
// manifest — the receipt of what was wired.
//
// The wizard's state is one draft (newProject/draft.ts); each step is its
// own component; the assembly is newProject/create.ts.

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
    <fieldset disabled={!identity.current} style={{ border: 0, padding: 0, margin: 0 }}>
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
  const [showManifest, setShowManifest] = useState(false)
  const [manifest, setManifest] = useState<Manifest | null>(null)
  const { run, busy, err } = useAction()

  const create = (where: { remote: boolean; namespaceId: string }) => {
    setProgress([])
    void run(async () => {
      try { setManifest(await createProject(d, where, (line) => setProgress((p) => [...p, line]), context())) }
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
    setSaved(null); setFile(null); setProgress([]); setManifest(null); setShowManifest(false)
  })
  const buildV0 = () => run(async () => {
    try {
      const result = await buildFirstVersion(context())
      nav(`/projects/${result.slug}/console?change=${result.change.change.id}&kickoff=1`)
    } finally { refreshSaved() }
  })
  if (storageError) return <div className="error">{storageError} The saved setup has not been replaced.</div>
  if (manifest && showManifest) {
    return <>
      <ManifestView m={manifest} onBuildV0={() => void buildV0()} onConsole={() => nav(`/projects/${manifest.project.slug}/console`)} />
      {err && <div className="error">{err}</div>}
    </>
  }
  if (manifest) {
    return (
      <div style={{ maxWidth: 760 }}>
        <h1>{manifest.project.name || manifest.project.slug} <span className="chip pass">created</span></h1>
        <div className="card mt-3">
          <ProgressLog lines={progress} />
        </div>
        <div className="row mt-4">
          <button className="primary" style={{ fontSize: 'var(--fs-base)', padding: '10px 26px' }}
            onClick={() => setShowManifest(true)}>Continue →</button>
        </div>
      </div>
    )
  }

  if (saved) return <div style={{ maxWidth: 720 }}>
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
    <div style={{ maxWidth: 720 }}>
      <h1>New project</h1>
      <fieldset disabled={busy} style={{ border: 0, padding: 0, margin: 0 }}>
      <StepChips step={d.step} />
      {d.step === 1 && <NameStep d={d} act={act} />}
      {d.step === 2 && <WayOfWorkingStep d={d} act={act} />}
      {d.step === 3 && (
        <RepositoryStep d={d} act={act} busy={busy} onCreate={create}>
          {progress.length > 0 && (
            <div className="card mt-3">
              <ProgressLog lines={progress} />
            </div>
          )}
        </RepositoryStep>
      )}
      </fieldset>
      {err && <div className="error">{err}</div>}
    </div>
  )
}

// The assembly log: one line per step, a skipped step drawn as a warning.
// The three steps of making a project, the current one marked.
function StepChips({ step }: { step: number }) {
  return (
    <div className="row" style={{ marginBottom: 18 }}>
      {['Software', 'Way of working', 'Repository'].map((label, i) => (
        <span key={label} className={`chip ${step === i + 1 ? 'running' : step > i + 1 ? 'pass' : ''}`}>
          {step > i + 1 ? '✓ ' : `${i + 1} · `}{label}
        </span>
      ))}
    </div>
  )
}

function ProgressLog({ lines }: { lines: { text: string; warn?: boolean }[] }) {
  return <>{lines.map((l, i) => <div key={i} className={'small' + (l.warn ? ' warn-line' : '')}>{l.text}</div>)}</>
}

// The landing view: everything the project is wired to, pinned. Reads like a
// receipt because it is one: the same wiring a run records when it is
// admitted (what was available then, not what the run read).
export function ManifestView({ m, onBuildV0, onConsole }: { m: Manifest; onBuildV0: () => void; onConsole: () => void }) {
  return (
    <div style={{ maxWidth: 760 }}>
      <h1>{m.project.name} <span className="chip pass">created</span></h1>

      <div className="card">
        <strong>Repository</strong>
        <div className="muted small mt-1">
          {m.repo.backed
            ? <>branch <code>{m.repo.defaultBranch}</code>{m.repo.remote && <> · remote <code>{m.repo.remote}</code></>}</>
            : 'not repo-backed'}
        </div>
      </div>

      <div className="card">
        <strong>Way of working</strong>
        {m.packs.length === 0 && <div className="muted small mt-1">no pack · bare project</div>}
        {m.packs.map((p) => <div key={p.name} className="muted small mt-1">pack <code>{p.name}</code></div>)}
        <div className="row" style={{ marginTop: 'var(--space-2)', gap: 'var(--space-1)' }}>
          {m.workflows.map((w) => (
            <span key={w.name} className="chip" title={w.origin}>{w.name} v{w.version}</span>
          ))}
        </div>
        {m.guidance.length > 0 && (
          <div className="muted small mt-2">
            {m.guidance.length} guidance blocks pinned · compiled sha256:{m.guidanceHash.slice(0, 12)}…
          </div>
        )}
      </div>

      <div className="card">
        <strong>Policies</strong>
        {m.policies.length === 0 && <div className="muted small mt-1">none · add a pack that includes them, or add them under Settings › Way of working</div>}
        {m.policies.map((p) => (
          <div key={p.id} className="muted small mt-1">
            <span className="chip pass">✓</span> <strong>{p.name}</strong> — {p.description || p.rule}{p.origin ? ` · ${p.origin}` : ''}
          </div>
        ))}
      </div>

      <div className="card">
        <strong>Bindings</strong>
        <div className="row" style={{ marginTop: 'var(--space-2)', gap: 'var(--space-1)' }}>
          {m.bindings.map((b) => (
            <span key={`${b.capability}/${b.binding}`} className="chip" title={`config sha256:${b.configHash}`}>
              {b.capability} → {b.binding} v{b.version}
            </span>
          ))}
        </div>
        {/* Which integration each kind would use, not whether it is set up:
            a trial lists GitLab though no GitLab is configured. */}
        <div className="muted small mt-1">Whether each is set up: <Link to="/integrations">Integrations</Link></div>
      </div>

      <div className="card tone-accent" style={{ marginTop: 'var(--space-5)' }}>
        <strong>Next</strong>
        <div className="small" style={{ marginTop: 'var(--space-2)', lineHeight: 1.5 }}>
          <strong>Build the first version</strong> opens a chat on a new change with what you wrote
          about {m.project.name}, and the helper starts building. Try it when the helper says it is
          ready; {PRODUCT} checks the work before it goes live.
        </div>
      </div>

      <div className="row mt-4">
        <button className="primary" style={{ fontSize: 'var(--fs-md)', padding: '12px 30px' }}
          onClick={onBuildV0}>🔨 Build the first version →</button>
        <button onClick={onConsole}>Open the chat</button>
        <Link to={`/projects/${m.project.slug}`}><button className="subtle">project page</button></Link>
      </div>
    </div>
  )
}
