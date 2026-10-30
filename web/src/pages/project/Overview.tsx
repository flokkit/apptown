import { PRODUCT } from '../../brand'
import { runtimeDescription } from '../../runtime'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Release, get, openAfter, post } from '../../api'
import { Modal } from '../../Modal'
import { QuietConfirm } from '../../QuietConfirm'
import { SplitButton } from '../../SplitButton'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { NewChangeButton } from './NewChange'
import { fmtWhen } from '../../format'
import type { DeploymentView, PullPlan, ReleasePlan, ReleasePreviewStarted, CatalogApplied, CreatedChange, CatalogChangeDetail, CatalogDef, CatalogEntry as GeneratedCatalogEntry, CatalogList, ProjectDetail as ProjectT } from '../../wire.gen'

// The project overview's top: the two big doors and the catalog of prepared
// changes behind one of them.

// The two doors: CHANGE and RELEASE — the whole loop in two big
// verbs. Each is a split button: the face is the house default (chat change /
// release to the selected environment), the chevron holds the advanced paths
// (catalog change, detailed form, branch import / other targets). Advanced
// never crowds default; default never hides advanced.
export function BigChangeBox({ slug, inline = false }: { slug: string; inline?: boolean }) {
  const nav = useNavigate()
  const [busy, setBusy] = useState(false)
  const [catalogOpen, setCatalogOpen] = useState(false)
  const [detailedOpen, setDetailedOpen] = useState(false)
  const envsQ = useGet<DeploymentView[]>(`/projects/${slug}/deployments`)
  const projectQ = useGet<ProjectT>(`/projects/${slug}`)
  const envs = envsQ.data ?? []
  const p = projectQ.data
  const blurb = p?.blurb ?? ''
  // The current plan identifies the configured release destination.
  const planQ = useGet<ReleasePlan>(`/projects/${slug}/release/plan`)
  const plan = planQ.current ? planQ.data : undefined
  const canRelease = plan?.configured === true
  const releaseLabel = plan?.deploymentLabel || plan?.deployment || 'the selected environment'
  // The project's own read is shown by the page around this box.
  const [err, setErr] = useState('')

  // Prefer the selected destination by identity when its address can be opened.
  // Otherwise show an addressable alternative by its own customer label.
  const addressed = envs.filter((e) => !!e.url && !e.hostOnly)
  const best = addressed.find((e) => !!plan?.deploymentId && e.id === plan.deploymentId) ?? addressed.find((e) => e.running) ?? addressed[0]

  // The Build-the-first-version door:
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
    <div className="card overview-actions" style={inline ? undefined : { padding: 18, margin: '14px 0 18px', maxWidth: 980 }}>
      <div className="row" style={{ gap: 14, alignItems: 'stretch', flexWrap: 'wrap' }}>
        {best && !inline && (
          <SplitButton icon="▶" label="Open" green
            sub={(best.displayLabel || best.name) + ' · ' + best.url.replace(/^https?:\/\//, '')}
            onMain={() => window.open(best.url, '_blank')}
            items={[
              ...envs.map((e) => ({
                label: (e.displayLabel || e.name) + (e.url ? ' · ' + e.url.replace(/^https?:\/\//, '') : ''),
                hint: !e.url ? 'no address yet · nothing has been released here' : runtimeDescription(e),
                onClick: () => { if (e.url) window.open(e.url, '_blank'); else nav('/deployments') },
              })),
              { label: 'All deployments', onClick: () => nav('/deployments') },
            ]} />
        )}
        {buildFirst && (
          <SplitButton icon="🔨" label="Build the first version" green
            sub={blurb ? `"${blurb.slice(0, 60)}${blurb.length > 60 ? '…' : ''}"` : 'no app here yet · start it in a building chat'}
            busy={busy} onMain={buildV0} items={[]} />
        )}
        <SplitButton icon="💬" label="Change" sub="describe what you want changed"
          primary={!buildFirst} busy={busy} onMain={startChat}
          items={[
            { label: '📋 Catalog change', hint: 'run one of the prepared changes', onClick: () => setCatalogOpen(true) },
            { label: '✏️ New change, detailed', hint: 'pick the title and workflow yourself', onClick: () => setDetailedOpen(true) },
          ]} />
        {canRelease && (
          <SplitButton icon="↑" label="Release"
            sub={'publish the latest finished version to ' + releaseLabel}
            onMain={() => nav(`/projects/${slug}/release`)}
            items={[]} />
        )}
      </div>
      <ReadStatus q={envsQ} what="the deployments" className="small" />
      <ReadStatus q={planQ} what={`the ${PRODUCT} release destination`} className="small" />
      <StaleNote q={[envsQ, planQ]} />
      {err && <div className="error" style={{ marginTop: 10 }}>{err}</div>}
      {catalogOpen && <CatalogPicker slug={slug} onClose={() => setCatalogOpen(false)} />}
      {detailedOpen && <NewChangeButton slug={slug} external={{ open: true, onClose: () => setDetailedOpen(false) }} />}
    </div>
  )
}

// The catalog picker: the prepared changes the release ships, one click
// plus params.
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
            </div>
            <div className="small" style={{ marginTop: 4 }}>{e.description}</div>
            <div className="small" style={{ marginTop: 6 }}>
              needs the <code>{e.missingWorkflow}</code> workflow
              {e.fromPack ? <> — it comes with the latest <code>{e.fromPack}</code> pack.{' '}
                <QuietConfirm label="Pull latest pack…" className="subtle" yes="Pull it"
                  ask={`Pull the latest ${e.fromPack} pack?`}
                  prepare={() => pullPlan(e.fromPack!)}
                  onYes={() => pull(e.fromPack!)} />
              </> : " · not part of this project's pack; an amender can install it from the workflow library."}
            </div>
          </div>
        ) : (
          <div key={e.name} className="card" style={{
            cursor: 'pointer', borderColor: picked?.name === e.name ? 'var(--accent)' : undefined,
          }} onClick={() => setPicked(picked?.name === e.name ? null : e)}>
            <div className="row">
              <strong><code>{e.name}</code></strong>
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

// Release previews retain the same action as the overview's release history.
export function ReleaseRows({ slug, releases }: { slug: string; releases: Release[] }) {
  const nav = useNavigate()
  const live = releases.find(r => r.serving)
  return <>
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
                  }}>▶ #{r.number} <span className="muted small">{fmtWhen(r.releasedAt || r.createdAt)}{r.origin === 'observed-after-restore' ? ' · observed after restore' : ''}</span></button>
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
  </>
}
