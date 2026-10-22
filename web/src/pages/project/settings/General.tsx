import { PRODUCT } from '../../../brand'
import { type ReactNode, useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { HttpError, Project as ProjectT, del, patch, post } from '../../../api'
import { FieldRow } from '../../../FieldRow'
import { firstErr, getKey, useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import type { OverviewData } from '../../../parts/runActivity'
import type { DeploymentView } from '../../../wire.gen'
import type { ChangeEnvelope, Lifecycle, ProjectDeletionState, ProjectRow } from '../../../wire.gen'
import { fmtDay, fmtStamp } from '../../../format'
import { QuietConfirm } from '../../../QuietConfirm'

// Deleting a project, as its deletion stands: facts and the acts each
// state allows, a greyed act saying in one line why not. The lifecycle is
// described once, in the docs (/docs/guides/container-lifecycle).
export function DangerZone({ slug }: { slug: string }) {
  const [confirm, setConfirm] = useState('')
  const [open, setOpen] = useState(false)
  const statusQ = useGet<ProjectDeletionState>(`/projects/${slug}/deletion`, { poll: 5000 })
  const envsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`)
  const { run, busy, err } = useAction(() => { setOpen(false); setConfirm('') }, { affects: [`/projects/${slug}`, '/projects'] })
  const st = statusQ.data
  // The deployments the deletion would take down; null while unknown
  // (still being read, its read failed, or its latest refresh failed and
  // only an older answer is held), never shown as none.
  const envs = envsQ.data && !envsQ.refreshFailed ? envsQ.data.filter((e) => e.name).map((e) => e.name) : null
  if (!st) return <div className="card tone-bad" style={{ marginTop: 28 }}><ReadStatus q={statusQ} what="where its deletion stands" /></div>
  const askDelete = () => run(() => del(`/projects/${slug}`, { confirm: slug }))
  const row = (k: string, v: ReactNode, cls = '') => <tr><td className="kv-key">{k}</td><td className={cls}>{v}</td></tr>
  if (st.state === 'deleting') {
    const stopped = !!st.stalled || !!st.bindingRevoked
    return (
      <div className="card tone-bad" style={{ marginTop: 28 }}>
        <div className="row"><strong>Deleting</strong> <span className="chip pending">{stopped ? 'stopped' : 'in progress'}</span></div>
        <table className="kv" style={{ marginTop: 8 }}>
          <tbody>
            {row('Asked', <>{st.requestedAt ? fmtStamp(st.requestedAt) : ''} by {st.requestedBy}</>)}
            {row('Still to take down', st.remaining && st.remaining.length > 0 ? st.remaining.join(', ') : 'nothing')}
            {st.takeDown && row('Latest take-down', <Link className="tap" to={`/operations/${st.takeDown.id}`}>{String(st.takeDown.input?.deployment ?? '')} · {st.takeDown.state}</Link>)}
            {st.stalled && row('Stopped', st.stalled, 'error')}
            {st.bindingRevoked && row('Authority', 'withdrawn', 'error')}
          </tbody>
        </table>
        <div className="row" style={{ marginTop: 10 }}>
          {stopped && <button className="subtle danger" disabled={busy} onClick={askDelete}>Delete again</button>}
          <button className="subtle" disabled={busy || st.takeDownsStarted} onClick={() => run(() => post(`/projects/${slug}/deletion/cancel`, {}))}>Cancel deletion</button>
          {st.takeDownsStarted && <span className="muted small">a take-down has started</span>}
          {err && <span className="error small">{err}</span>}
        </div>
      </div>
    )
  }
  if (st.state === 'deleted') {
    const purging = !!st.purgeStartedAt
    return (
      <div className="card tone-bad" style={{ marginTop: 28 }}>
        <div className="row"><strong>Deleted</strong> <span className="chip fail">{purging ? 'purging' : 'kept'}</span></div>
        <table className="kv" style={{ marginTop: 8 }}>
          <tbody>
            {row('Deleted', <>{st.deletedAt ? fmtDay(st.deletedAt) : ''} by {st.requestedBy}</>)}
            {row('Kept until', st.keptUntil ? fmtDay(st.keptUntil) : 'unknown')}
            {row('Backups', <Link className="tap" to="/backups">kept</Link>)}
            {st.purge && row('Purge', <><Link className="tap" to={`/operations/${st.purge.id}`}>{st.purge.state}</Link>{st.purge.detail && <div className="muted small">{st.purge.detail}</div>}</>)}
            {st.bindingRevoked && row('Authority', 'withdrawn: no scheduled purge', 'error')}
          </tbody>
        </table>
        <div className="row" style={{ marginTop: 10 }}>
          <button className="subtle" disabled={busy || purging} onClick={() => run(() => post(`/projects/${slug}/restore`, {}))}>Restore project</button>
          {purging && <span className="muted small">purge has started; restoring is no longer available</span>}
          <QuietConfirm label="Purge now" ask={`Purge ${slug} now? Its backups, repository and records go for good; its GitLab copy is archived.`}
            onYes={() => run(() => post(`/projects/${slug}/purge`, {}))} yes="Purge now" className="subtle danger" title="administrators only" />
          {err && <span className="error small">{err}</span>}
        </div>
      </div>
    )
  }
  if (st.state !== 'active') {
    return <div className="card" style={{ marginTop: 28 }}><strong>{st.state}</strong></div>
  }
  return (
    <div className="card tone-bad" style={{ marginTop: 28 }}>
      <div className="row">
        <strong>Delete project</strong>
        <span style={{ flex: 1 }} />
        {!open && <button className="subtle danger" onClick={() => setOpen(true)}>Delete…</button>}
      </div>
      {open && (
        <>
          <StaleNote q={envsQ} />
          <table className="kv" style={{ marginTop: 8 }}>
            <tbody>
              {row('New work', 'stops at once')}
              {row('Can be cancelled', 'for 10 minutes')}
              {row('Taken down', envs === null ? (envsQ.reason ? `unknown: ${envsQ.reason}` : 'reading…') : envs.length > 0 ? [...envs.filter((n) => n !== 'main'), ...envs.filter((n) => n === 'main')].join(', then ') : 'no deployments', envs === null && envsQ.reason ? 'error' : '')}
              {row('Kept, restorable', 'for 30 days')}
              {row('Then purged', 'backups, repository, records')}
            </tbody>
          </table>
          <div className="row" style={{ marginTop: 10 }}>
            <input value={confirm} onChange={(e) => setConfirm(e.target.value)}
              placeholder={`type "${slug}" to confirm`} style={{ width: 260, maxWidth: '100%' }} />
            <button className="subtle danger" disabled={busy || confirm !== slug || envs === null} onClick={askDelete}>Delete {slug}</button>
            <button className="subtle" onClick={() => { setOpen(false); setConfirm('') }}>Cancel</button>
            {err && <span className="error small">{err}</span>}
          </div>
        </>
      )}
    </div>
  )
}

// LifecycleNote says, on every page of a project that is not active, where
// it stands, with the way to its deletion's detail.
export function LifecycleNote({ slug, lifecycle }: { slug: string; lifecycle?: Lifecycle }) {
  if (!lifecycle || lifecycle.state === 'active') return null
  const said = lifecycle.state === 'deleted'
    ? <>Deleted — kept until {lifecycle.keptUntil ? fmtDay(lifecycle.keptUntil) : 'unknown'}. Read-only.</>
    : lifecycle.state === 'deleting'
    ? <>Being deleted{lifecycle.stalled ? ' — stopped' : ''}. No new work.</>
    : <>Being imported. No new work until it ends.</>
  return (
    <div className="card tone-bad small" style={{ margin: '8px 0' }}>
      {said} <Link className="tap" to={`/projects/${slug}/settings/general`}>Details</Link>
    </div>
  )
}

// Settings › General: the project's name and face, its repository, adopting
// a branch, and deleting it. Everything about the project AS a thing, in
// one place — the header stays a header.

type Face = { name: string; namespace: string; icon: string; blurb: string }
const faceOf = (p: ProjectT): Face => ({ name: p.name, namespace: p.namespace ?? '', icon: p.icon ?? '', blurb: p.blurb ?? '' })
// said quotes a value for a sentence, and names an empty one.
const said = (v: string) => (v === '' ? 'empty' : `“${v}”`)
const faceLabels: Record<keyof Face, string> = { name: 'Name', icon: 'Icon', blurb: 'One line about it', namespace: 'Group' }

// The form follows the project as the page reads it until someone edits a
// field; from then on the draft is theirs and survives refreshes and failed
// saves. It starts only from an answer this page read itself (`current`),
// never from one another view held, and it remembers the settings revision
// it started from. Save sends only the fields that differ from that start,
// with the revision, so the server refuses the save if someone else saved in
// between rather than letting it overwrite their changes.
export function GeneralTab({ project, current = true, onChanged }: { project: ProjectT; current?: boolean; onChanged: () => void }) {
  const nav = useNavigate()
  const client = useQueryClient()
  // base is the reading the draft started from; while nothing is edited it
  // follows each newer reading, never an older one that lands late.
  const [base, setBase] = useState<{ face: Face; revision: number } | null>(null)
  const [draft, setDraft] = useState<Face | null>(null)
  const [conflict, setConflict] = useState(false)
  const [msg, setMsg] = useState('')
  useEffect(() => {
    if (!current || draft) return
    setBase((held) => (held && held.revision > project.settingsRevision) ? held : { face: faceOf(project), revision: project.settingsRevision })
  }, [project, current, draft])
  const shown = draft ?? base?.face ?? null
  const edit = (field: keyof Face) => (v: string) => {
    if (!base) return
    setMsg('')
    setDraft((d) => ({ ...(d ?? base.face), [field]: v }))
  }
  const changed = base && draft ? (Object.keys(draft) as (keyof Face)[]).filter((k) => draft[k] !== base.face[k]) : []

  const [branch, setBranch] = useState('')
  const { run: runSave, busy: saving, err: saveErr, failure, setErr: setSaveErr } = useAction(undefined, { affects: ['/projects'] })
  const { run, err: actErr } = useAction()
  const projectsQ = useGet<ProjectRow[]>('/projects', { kind: 'config' })
  const namespaces = [...new Set((projectsQ.data ?? []).map((p) => p.namespace ?? '').filter(Boolean))]
  const err = firstErr(actErr, projectsQ.err)

  const save = async () => {
    if (!base || !draft || changed.length === 0) return
    setMsg('')
    setConflict(false)
    const body: Record<string, unknown> = { expectedRevision: base.revision }
    for (const k of changed) body[k] = draft[k]
    const p = await runSave(() => patch<ProjectT>(`/projects/${project.slug}`, body))
    if (!p) return
    // The answer is the committed record: it is the new starting point, and
    // the project's view shows it without waiting for a refresh.
    client.setQueryData(getKey(`/projects/${p.slug}`), p)
    setBase({ face: faceOf(p), revision: p.settingsRevision })
    setDraft(null)
    setMsg('saved')
    onChanged()
  }
  // A refused save means the page's reading is old: read the settings again
  // so the person sees what changed before choosing.
  const reread = useRef(onChanged)
  reread.current = onChanged
  useEffect(() => {
    if (failure instanceof HttpError && failure.code === 'settings-moved') { setConflict(true); reread.current() }
  }, [failure])
  const newer = !!base && project.settingsRevision > base.revision
  // Keep my edits: the draft moves onto the settings as they are now, and the
  // next save carries their revision. The person has seen what changed.
  const keepMine = () => {
    const now = { face: faceOf(project), revision: project.settingsRevision }
    setBase(now)
    setDraft((d) => d && { ...now.face, ...Object.fromEntries((Object.keys(d) as (keyof Face)[]).filter((k) => base && d[k] !== base.face[k]).map((k) => [k, d[k]])) })
    setConflict(false); setSaveErr('')
  }
  const discard = () => { setDraft(null); setBase({ face: faceOf(project), revision: project.settingsRevision }); setConflict(false); setSaveErr('') }

  const importBranch = async () => {
    if (!branch.trim()) return
    const r = await run(() => post<ChangeEnvelope>(`/projects/${project.slug}/import`, { branch: branch.trim() }))
    if (r) nav(`/changes/${r.change.id}`)
  }

  return (
    <div style={{ maxWidth: 760 }}>
      <div className="card">
        <strong>Name and icon</strong>
        <fieldset disabled={!shown || saving} style={{ border: 'none', margin: 0, padding: 0 }}>
        <div className="form-grid" style={{ marginTop: 10 }}>
          <FieldRow label="Name"><input value={shown?.name ?? ''} onChange={(e) => edit('name')(e.target.value)} /></FieldRow>
          <FieldRow label="Icon" help="an emoji"><input value={shown?.icon ?? ''} onChange={(e) => edit('icon')(e.target.value)} maxLength={8} style={{ width: 90 }} /></FieldRow>
          <FieldRow label="One line about it"><input value={shown?.blurb ?? ''} onChange={(e) => edit('blurb')(e.target.value)} maxLength={140} placeholder="one sentence" /></FieldRow>
          <FieldRow label="Group" help="how projects are grouped on the front page">
            <input value={shown?.namespace ?? ''} onChange={(e) => edit('namespace')(e.target.value)} list="known-namespaces" placeholder="(none)" style={{ width: 200 }} />
          </FieldRow>
          <FieldRow label="Address" help="the short name in links, fixed when the project was made">
            <code>{project.slug}</code>
          </FieldRow>
        </div>
        </fieldset>
        <datalist id="known-namespaces">{namespaces.map((n) => <option key={n} value={n} />)}</datalist>
        {conflict && base && draft && (
          <div className="card tone-warn" style={{ marginTop: 10 }}>
            <div>Someone saved these settings after you started editing. Your edits are kept here, unsaved.</div>
            {(Object.keys(faceLabels) as (keyof Face)[]).filter((k) => faceOf(project)[k] !== base.face[k]).map((k) => (
              <div key={k} className="muted small" style={{ marginTop: 4 }}>
                {faceLabels[k]} is now {said(faceOf(project)[k])} (it was {said(base.face[k])} when you started{changed.includes(k) ? `; you have ${said(draft[k])}` : ''}).
              </div>
            ))}
            <div className="row" style={{ marginTop: 8 }}>
              <button onClick={keepMine} disabled={!newer}>Keep my edits on top of theirs</button>
              {!newer && <span className="muted small">reading the current settings…</span>}
              <button className="subtle" onClick={discard}>Discard my edits</button>
            </div>
          </div>
        )}
        <div className="row" style={{ marginTop: 10 }}>
          <button className="primary" onClick={save} disabled={changed.length === 0 || saving || conflict}>{saving ? 'Saving…' : 'Save'}</button>
          {!shown && <span className="muted small">reading the current settings before editing…</span>}
          {draft && changed.length > 0 && !conflict && <span className="muted small">unsaved: {changed.map((k) => faceLabels[k].toLowerCase()).join(', ')}</span>}
          {msg && <span className="muted small">{msg}</span>}
          {saveErr && !conflict && <span className="error small">{saveErr}</span>}
        </div>
      </div>

      <RepoCard slug={project.slug} />

      <div className="card">
        <div className="row">
          <strong>Adopt a branch</strong>
          <span className="muted small">turn work that already exists into a change; its history is marked as reconstructed</span>
        </div>
        <div className="row" style={{ marginTop: 8 }}>
          <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="branch name" style={{ width: 260 }}
            onKeyDown={(e) => e.key === 'Enter' && importBranch()} />
          <button disabled={!branch.trim()} onClick={importBranch}>Adopt as a change</button>
        </div>
      </div>
      {err && <div className="error">{err}</div>}

      <DangerZone slug={project.slug} />
    </div>
  )
}

// The repository card (Settings › General): where the code lives, its
// head, and the clone command for anyone working outside AppTown.
export function RepoCard({ slug }: { slug: string }) {
  const q = useGet<OverviewData>(`/projects/${slug}/overview`)
  const data = q.data
  const [copied, setCopied] = useState(false)
  if (!data) return <ReadStatus q={q} what="the repository" />
  if (!data.repo) return <StaleNote q={q} />
  return (
    <div className="card">
      <StaleNote q={q} />
      <div className="row">
        <strong>Repository</strong>
        <span className="chip">{data.repo.defaultBranch}</span>
        <span className="muted small">{data.repo.commits} commits</span>
        {data.repo.webUrl && <a href={data.repo.webUrl} target="_blank" rel="noreferrer">GitLab ↗</a>}
        <span style={{ flex: 1 }} />
        <span className="muted small">
          {data.repo.head.sha} {data.repo.head.subject.slice(0, 60)} · {data.repo.head.when}
        </span>
      </div>
      <div className="row small" style={{ marginTop: 8, gap: 8, flexWrap: 'wrap' }}>
        <a className="btn" href={`/api/v1/projects/${slug}/repo/bundle`} download={`${slug}.bundle`}>Download code</a>
        <span className="muted">the {data.repo.defaultBranch} branch and tags as a Git bundle; another {PRODUCT} can start a project from it</span>
      </div>
      {data.repo.cloneCmd && (
        <div className="row" style={{ marginTop: 8 }}>
          <code style={{ background: 'var(--code-bg)', padding: '6px 10px', borderRadius: 6, flex: 1 }}>
            {data.repo.cloneCmd}
          </code>
          <button className="subtle" onClick={() => {
            navigator.clipboard.writeText(data.repo!.cloneCmd!)
            setCopied(true); setTimeout(() => setCopied(false), 1500)
          }}>{copied ? 'copied ✓' : 'copy'}</button>
        </div>
      )}
    </div>
  )
}
