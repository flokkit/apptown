import { runtimeDescription } from '../../runtime'
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Release, get, openAfter, post } from '../../api'
import { Modal } from '../../Modal'
import { QuietConfirm } from '../../QuietConfirm'
import { SplitButton } from '../../SplitButton'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { NewChangeButton } from './NewChange'
import { ScheduleRows } from './Schedules'
import { PastChats } from '../change/Conversation'
import { fmtWhen } from '../../format'
import { ActiveRunList, ChangeHistoryTable, type OverviewData } from '../../parts/runActivity'
import type { DeploymentView, PullPlan, ReleasePreviewStarted, CatalogApplied, CreatedChange, CatalogChangeDetail, CatalogDef, CatalogEntry as GeneratedCatalogEntry, CatalogList, ProjectDetail as ProjectT } from '../../wire.gen'

// The project overview's top: the two big doors and the catalog of prepared
// changes behind one of them.

// The two doors: CHANGE and RELEASE — the whole loop in two big
// verbs. Each is a split button: the face is the house default (chat change /
// release to the intranet), the chevron holds the advanced paths
// (catalog change, detailed form, branch import / other targets). Advanced
// never crowds default; default never hides advanced.
export function BigChangeBox({ slug }: { slug: string }) {
  const nav = useNavigate()
  const [busy, setBusy] = useState(false)
  const [catalogOpen, setCatalogOpen] = useState(false)
  const [detailedOpen, setDetailedOpen] = useState(false)
  const envsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`)
  const projectQ = useGet<ProjectT>(`/projects/${slug}`)
  const envs = envsQ.data ?? []
  const p = projectQ.data
  const blurb = p?.blurb ?? ''
  const intranetReady = !p || p.intranetReady !== false || p.runtimeForm === 'no app yet'
  // The project's own read is shown by the page around this box.
  const [err, setErr] = useState('')

  // The most common act on a shipped project is USING it, so the
  // deployments get the biggest call to action — the intranet copy when
  // released, else the main copy. Always "Open", never "Play": the
  // word must suit ordinary institutional software.
  // The door opens an address, so only deployments that have one qualify:
  // an intranet deployment that was bound but never released has none
  // (it once sent Duck Storm's Open to about:blank). An address that
  // answers on the forge host only is no door for anyone elsewhere.
  const addressed = envs.filter((e) => !!e.url && !e.hostOnly)
  const best = addressed.find((e) => e.name === 'lan') ?? addressed.find((e) => e.running) ?? addressed[0]

  // The Build-the-first-version door (design/decisions/interactive-vs-pipeline.md):
  // while a project has no running app, building v1 is overwhelmingly the
  // likely intent — a standing primary action, not a hint. Not a workflow:
  // a Change + workshop with the project's remembered intent (blurb) seeded.
  // Only while the project has no app at all: a project whose repository
  // already declares one (a pack's seed may carry a working starter, read as
  // the runtime form) is changed, not built, even before its first deploy.
  const buildFirst = !best && p?.runtimeForm === 'no app yet'
  const buildV0 = async () => {
    if (busy) return
    setBusy(true); setErr('')
    try {
      // No blurb, no brief: say so in the body rather than sending nothing
      // (complaint #38) — the helper then asks before it builds.
      const body = blurb.trim() || 'No description was written for this project yet. Ask what the first version should do before building anything.'
      const r = await post<CreatedChange>(`/projects/${slug}/changes`, {
        title: 'First version', type: 'feature', body, workflow: '',
      })
      nav(`/projects/${slug}/console?change=${r.change.id}&kickoff=1`)
    } catch (e) { setErr((e as Error).message); setBusy(false) }
  }

  const startChat = async () => {
    if (busy) return
    setBusy(true); setErr('')
    try {
      const stamp = new Date().toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
      const r = await post<CreatedChange>(`/projects/${slug}/changes`, {
        title: `Chat change (${stamp})`, titleFromChat: true, type: 'feature', body: '', workflow: '',
      })
      nav(`/projects/${slug}/console?change=${r.change.id}`)
    } catch (e) { setErr((e as Error).message); setBusy(false) }
  }

  return (
    <div className="card" style={{ padding: 18, margin: '14px 0 18px', maxWidth: 980 }}>
      <div className="row" style={{ gap: 14, alignItems: 'stretch', flexWrap: 'wrap' }}>
        {best && (
          <SplitButton icon="▶" label="Open" green
            sub={best.name === 'lan' ? best.url.replace(/^https?:\/\//, '') + ' — for everyone on the intranet'
              : 'the ' + best.name + ' copy — ' + best.url.replace(/^https?:\/\//, '')}
            onMain={() => window.open(best.url, '_blank')}
            items={[
              ...envs.map((e) => ({
                label: (e.name === 'lan' ? '🏢 intranet' : e.name) + (e.url ? ' — ' + e.url.replace(/^https?:\/\//, '') : ''),
                hint: !e.url ? 'no address yet — nothing has been released here' : runtimeDescription(e),
                onClick: () => { if (e.url) window.open(e.url, '_blank'); else nav('/deployments') },
              })),
              { label: 'All deployments', onClick: () => nav('/deployments') },
            ]} />
        )}
        {buildFirst && (
          <SplitButton icon="🔨" label="Build the first version" green
            sub={blurb ? `"${blurb.slice(0, 60)}${blurb.length > 60 ? '…' : ''}"` : 'no app here yet — start it in a building chat'}
            busy={busy} onMain={buildV0} items={[]} />
        )}
        <SplitButton icon="💬" label="Change" sub="describe what you want changed"
          primary={!buildFirst} busy={busy} onMain={startChat}
          items={[
            { label: '📋 Catalog change', hint: 'run one of the catalogued changes', onClick: () => setCatalogOpen(true) },
            { label: '✏️ New change, detailed', hint: 'pick the title and workflow yourself', onClick: () => setDetailedOpen(true) },
          ]} />
        <SplitButton icon="🏢" label="Release"
          sub={intranetReady ? 'publish the latest finished version to the intranet'
            : 'needs packaging first — the release page explains'}
          onMain={() => nav(`/projects/${slug}/release`)}
          items={[]} />
      </div>
      <ReadStatus q={envsQ} what="the deployments" className="small" />
      <StaleNote q={envsQ} />
      {err && <div className="error" style={{ marginTop: 10 }}>{err}</div>}
      {catalogOpen && <CatalogPicker slug={slug} onClose={() => setCatalogOpen(false)} />}
      {detailedOpen && <NewChangeButton slug={slug} external={{ open: true, onClose: () => setDetailedOpen(false) }} />}
    </div>
  )
}

// The catalog picker: our canned changes, one click plus params. Drafts are
// runnable but say so (a draft run is an evaluation).
export type CatalogEntry = GeneratedCatalogEntry

export type CatalogDefT = CatalogDef

export function CatalogPicker({ slug, onClose }: { slug: string; onClose: () => void }) {
  const nav = useNavigate()
  const entriesQ = useGet<CatalogList>(`/catalog?project=${slug}`)
  const entries = entriesQ.data?.catalog ?? []
  const [picked, setPicked] = useState<CatalogEntry | null>(null)
  const defQ = useGet<CatalogChangeDetail>(picked ? `/catalog/${picked.name}` : null)
  const def = picked ? defQ.data?.definition ?? null : null
  const [params, setParams] = useState<Record<string, string>>({})
  useEffect(() => { setParams({}) }, [picked])
  const { run, busy, err } = useAction()

  const apply = async () => {
    if (!picked) return
    const r = await run(() => post<CatalogApplied>(`/catalog-apply/${picked.name}`,
      { project: slug, params }))
    if (r) nav(`/changes/${r.change.id}`)
  }
  // What pulling the latest pack would change, then the pull itself.
  const pullPlan = async (pack: string) => {
    const plan = await get<PullPlan>(`/projects/${slug}/pack/pull-plan?pack=${pack}`)
    const lines = [
      plan.additions.length ? `adds: ${plan.additions.join(', ')}` : '',
      plan.removals.length ? `removes: ${plan.removals.join(', ')}` : '',
      plan.keptReferenced.length ? `keeps (a workflow still calls it): ${plan.keptReferenced.join(', ')}` : '',
      plan.deprecated.length ? `skips (deprecated): ${plan.deprecated.join(', ')}` : '',
      plan.guidance.length ? `guidance to add under Guidance: ${plan.guidance.join(', ')}` : '',
      plan.conflicting.length ? `not adopted (your choice): ${plan.conflicting.join('; ')}` : '',
    ].filter(Boolean).join('\n')
    return lines || 'no changes'
  }
  // The action re-reads the catalog when the pull lands.
  const pull = (pack: string) => run(() => post(`/projects/${slug}/pack/pull`, { pack }))

  return (
    <Modal onClose={onClose} maxWidth={640}
      title={<><h2 style={{ margin: 0 }}>Catalog change</h2><span className="muted small">prepared changes, run the same way every time</span></>}>
      <div style={{ fontSize: 'var(--fs-base)', fontWeight: 'normal' }}>
        <ReadStatus q={entriesQ} what="the catalog" className="small" />
        <StaleNote q={entriesQ} />
        {entriesQ.data && entries.length === 0 && <div className="muted small">The catalog is empty.</div>}
        {entries.map((e) => e.missingWorkflow ? (
          <div key={e.name} className="card" style={{ opacity: 0.6 }}>
            <div className="row">
              <strong><code>{e.name}</code></strong>
              <span className="muted small">v{e.latestVersion}</span>
            </div>
            <div className="small" style={{ marginTop: 4 }}>{e.description}</div>
            <div className="small" style={{ marginTop: 6 }}>
              needs the <code>{e.missingWorkflow}</code> workflow
              {e.fromPack ? <> — it comes with the latest <code>{e.fromPack}</code> pack.{' '}
                <QuietConfirm label="Pull latest pack…" className="subtle" yes="Pull it"
                  ask={`Pull the latest ${e.fromPack} pack?`}
                  prepare={() => pullPlan(e.fromPack!)}
                  onYes={() => pull(e.fromPack!)} />
              </> : " — not part of this project's pack; an amender can install it from the workflow library."}
            </div>
          </div>
        ) : (
          <div key={e.name} className="card" style={{
            cursor: 'pointer', borderColor: picked?.name === e.name ? 'var(--accent)' : undefined,
          }} onClick={() => setPicked(picked?.name === e.name ? null : e)}>
            <div className="row">
              <strong><code>{e.name}</code></strong>
              <span className={`chip ${e.status === 'catalogued' ? 'pass' : 'pending'}`}>
                {e.status === 'catalogued' ? 'catalogued' : 'draft — this run is an evaluation'}
              </span>
              <span className="muted small">v{e.latestVersion}</span>
            </div>
            <div className="small" style={{ marginTop: 4 }}>{e.description}</div>
            {picked?.name === e.name && <ReadStatus q={defQ} what="the change's definition" className="small" />}
            {picked?.name === e.name && def && (
              <div style={{ marginTop: 10 }} onClick={(ev) => ev.stopPropagation()}>
                {(def.params ?? []).map((p) => (
                  <div className="row" key={p.name} style={{ marginBottom: 6 }}>
                    <label className="small" style={{ minWidth: 110 }}>{p.name}{p.required ? ' *' : ''}</label>
                    <input value={params[p.name] ?? ''} placeholder={p.description ?? p.default ?? ''}
                      onChange={(ev) => setParams({ ...params, [p.name]: ev.target.value })} style={{ flex: 1 }} />
                  </div>
                ))}
                <div className="row">
                  <button className="primary" disabled={busy ||
                    (def.params ?? []).some((p) => p.required && !(params[p.name] ?? '').trim())}
                    onClick={apply}>
                    {busy ? 'Starting…' : 'Run it'}
                  </button>
                  <span className="muted small">runs the {def.workflow} workflow; then opens the change</span>
                </div>
              </div>
            )}
          </div>
        ))}
        {err && <div className="error">{err}</div>}
      </div>
    </Modal>
  )
}

// The project's Overview tab: what is running, then the history of changes
// and releases.
export function OverviewTab({ slug }: { slug: string }) {
  const nav = useNavigate()
  const q = useGet<OverviewData>(`/projects/${slug}/overview`, { poll: 5000 })
  const releasesQ = useGet<Release[]>(`/projects/${slug}/releases`)
  const data = q.data
  const releases = releasesQ.data ?? []

  if (!data) return <ReadStatus q={q} what="the project's activity" />

  // live means a running deployment is serving it, not merely that it was released
  const live = releases.find((r) => r.serving)
  return (
    <div>
      <StaleNote q={[q, releasesQ]} />
      {/* A releases read that failed must not read as "not live". */}
      {!releasesQ.data && <ReadStatus q={releasesQ} what="the releases" />}
      {releases.length > 0 && (
        <div className="card">
          <div className="row">
            <strong>Releases</strong>
            {live && <span className="chip pass">{live.deployment === 'lan' ? '🏢 ' : ''}live: #{live.number}</span>}
            <span className="row" style={{ gap: 6 }}>
              {releases.slice(0, 5).map((r) => (
                <button key={r.number} className="subtle" title="Open this release"
                  onClick={async () => {
                    try {
                      await openAfter(async () => (await post<ReleasePreviewStarted>(`/projects/${slug}/releases/${r.number}/preview`, {})).url)
                    } catch { nav(`/projects/${slug}/releases/${r.number}`) }
                  }}>▶ #{r.number} <span className="muted small">{fmtWhen(r.releasedAt || r.createdAt)}</span></button>
              ))}
            </span>
            {live && (live.changes ?? []).length > 0 && (
              <span className="muted small">— {(live.changes ?? []).map((c) => c.title).join(', ').slice(0, 80)}</span>
            )}
          </div>
          {live?.notes && (
            <div className="muted small" style={{ marginTop: 6, maxWidth: 720 }} title="Release notes, composed from the change's approved narrative.">
              {live.notes.length > 260 ? live.notes.slice(0, 260) + '…' : live.notes}
            </div>
          )}
        </div>
      )}
      <ScheduleRows slug={slug} />
      {(data.active.length > 0 || (data.open ?? []).length > 0) && <h2>Happening now</h2>}
      {(data.open ?? []).map((c) => (
        <Link key={c.changeId} to={`/changes/${c.changeId}`} className="card row" style={{ display: 'flex' }}>
          {c.state === 'landing'
            ? <span className="chip pending">landing</span>
            : c.editing > 0
              ? <span className="chip running">being edited</span>
              : <span className="chip">open — waiting on someone</span>}
          <strong><span className="muted" style={{ fontWeight: 400 }}>#{c.changeNumber} </span>{c.title}</strong>
          <span className="muted small">
            {c.editing > 0 ? `${c.editing} file${c.editing === 1 ? '' : 's'} in progress` : 'no run in progress'}
          </span>
          <span style={{ flex: 1 }} />
          <span className="muted small">open →</span>
        </Link>
      ))}
      {data.active.length > 0 && <ActiveRunList runs={data.active} />}

      <h2>History</h2>
      <ChangeHistoryTable changes={data.history} releases={releases} slug={slug} />
      <PastChats slug={slug} />
    </div>
  )
}
