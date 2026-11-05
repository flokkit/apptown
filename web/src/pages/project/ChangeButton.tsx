import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { get, post } from '../../api'
import { Modal } from '../../Modal'
import { QuietConfirm } from '../../QuietConfirm'
import { SplitButton } from '../../SplitButton'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import { NewChangeButton } from './NewChange'
import type { PullPlan, CatalogApplied, CreatedChange, CatalogChangeDetail, CatalogDef, CatalogEntry as GeneratedCatalogEntry, CatalogList } from '../../wire.gen'

export function ChangeButton({ slug, chatAvailable, compact = false, primary = false }: {
  slug: string; chatAvailable: boolean; compact?: boolean; primary?: boolean
}) {
  const nav = useNavigate()
  const [catalogOpen, setCatalogOpen] = useState(false)
  const [detailedOpen, setDetailedOpen] = useState(false)
  const { run, busy, err } = useAction()
  const startChat = async () => {
    const r = await run(() => createChatChange(slug))
    if (r) nav(`/projects/${slug}/console?change=${r.change.id}`)
  }
  return <>
    <SplitButton icon="" label="Change" compact={compact}
      sub={compact ? undefined : 'describe what you want changed'}
      primary={primary} busy={busy} onMain={chatAvailable ? startChat : () => setDetailedOpen(true)}
      items={[
        { label: 'Catalog change', hint: 'run one of the prepared changes', onClick: () => setCatalogOpen(true) },
        ...(chatAvailable ? [{ label: 'New change, detailed', hint: 'pick the title and workflow yourself', onClick: () => setDetailedOpen(true) }] : []),
      ]} />
    {err && <div className="error" style={{ marginTop: 10 }}>{err}</div>}
    {catalogOpen && <CatalogPicker slug={slug} onClose={() => setCatalogOpen(false)} />}
    {detailedOpen && <NewChangeButton slug={slug} external={{ open: true, onClose: () => setDetailedOpen(false) }} />}
  </>
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
          }}>
            <button className="catalog-entry subtle" aria-expanded={picked?.name === e.name} onClick={() => setPicked(picked?.name === e.name ? null : e)}>
            <div className="row">
              <strong><code>{e.name}</code></strong>
            </div>
            <div className="small" style={{ marginTop: 4 }}>{e.description}</div>
            </button>
            {picked?.name === e.name && <ReadStatus q={defQ} what="the change's definition" className="small" />}
            {picked?.name === e.name && def && (
              <div style={{ marginTop: 10 }} onClick={(ev) => ev.stopPropagation()}>
                {(def.params ?? []).map((p) => (
                  <div className="row" key={p.name} style={{ marginBottom: 6 }}>
                    <label className="small" style={{ minWidth: 110 }}>{p.name}{p.required ? ' *' : ''}</label>
                    <input aria-label={p.name} value={params[p.name] ?? ''} placeholder={p.description ?? p.default ?? ''}
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

// Both navigation and the overview start the same ordinary change.
export function createChatChange(slug: string) {
  const stamp = new Date().toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  return post<CreatedChange>(`/projects/${slug}/changes`, {
    title: `Chat change (${stamp})`, titleFromChat: true, type: 'feature', body: '', workflow: '',
  })
}
