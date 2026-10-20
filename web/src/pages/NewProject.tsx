import { PRODUCT } from '../brand'
import { useReducer, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { post } from '../api'
import { useAction } from '../data'
import type { CreatedChange, ProjectManifestView } from '../wire.gen'
import { draftReducer, newDraft, slugOf } from './newProject/draft'
import { createProject, type ProgressLine } from './newProject/create'
import { NameStep } from './newProject/NameStep'
import { WayOfWorkingStep } from './newProject/WayOfWorkingStep'
import { RepositoryStep } from './newProject/RepositoryStep'

// The new-project wizard: next → next → done, ONE
// decision per screen, defaults that are almost always right.
//   1. Name — the slug derives quietly (editable, but never looks required).
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
  const nav = useNavigate()
  const [d, act] = useReducer(draftReducer, undefined, () => newDraft())
  // The assembly log: what was wired (ok) and what was skipped (warn).
  const [progress, setProgress] = useState<ProgressLine[]>([])
  const [showManifest, setShowManifest] = useState(false)
  const [manifest, setManifest] = useState<Manifest | null>(null)
  const { run, busy, err } = useAction()
  const slug = slugOf(d)

  const create = (where: { remote: boolean; namespaceId: string }) => {
    setProgress([])
    void run(async () => setManifest(await createProject(d, where, (line) => setProgress((p) => [...p, line]))))
  }

  // The assembly log is worth reading (what got wired, what got skipped) —
  // don't yank it away the moment the manifest arrives; let the person say
  // when they're done with it.
  // "Build the first version": the wizard's own description IS the v0 intent —
  // open a Change carrying it, drop into its workshop, and kick off, so the
  // distance from "created" to "try it" is one click.
  const buildV0 = () => run(async () => {
    const s = manifest!.project.slug
    const r = await post<CreatedChange>(`/projects/${s}/changes`, {
      title: 'First version', type: 'feature', body: d.description.trim(), workflow: '',
    })
    nav(`/projects/${s}/console?change=${r.change.id}&kickoff=1`)
  })
  if (manifest && showManifest) {
    return <>
      <ManifestView m={manifest} onBuildV0={() => void buildV0()} onConsole={() => nav(`/projects/${manifest.project.slug}/console`)} />
      {err && <div className="error">{err}</div>}
    </>
  }
  if (manifest) {
    return (
      <div style={{ maxWidth: 760 }}>
        <h1>{d.name || slug} <span className="chip pass">created</span></h1>
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

  return (
    <div style={{ maxWidth: 720 }}>
      <h1>New project</h1>
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
      {err && <div className="error">{err}</div>}
    </div>
  )
}

// The assembly log: one line per step, a skipped step drawn as a warning.
// The three steps of making a project, the current one marked.
function StepChips({ step }: { step: number }) {
  return (
    <div className="row" style={{ marginBottom: 18 }}>
      {['Name', 'Way of working', 'Repository'].map((label, i) => (
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
        {m.packs.length === 0 && <div className="muted small mt-1">no pack — bare project</div>}
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
        {m.policies.length === 0 && <div className="muted small mt-1">none — add a pack that includes them, or add them under Settings › Way of working</div>}
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
