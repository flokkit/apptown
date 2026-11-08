import { ListRow } from '../../ListRow'
import { PageHead } from '../../PageHead'
import './list-migrations.css'
import { QuietPrompt } from '../../QuietConfirm'
import './RemediationPage.css'
import { Status } from '../../Status'
import { Link } from 'react-router-dom'
import { post } from '../../api'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { Item, RemediationDetail } from '../../wire.gen'
import { fmtWhen } from '../../format'
import { JobBar, OccupancyCard } from './Remediations'

function ItemReason({ item }: { item: Item }) {
  const waiting = item.state === 'queued' ? item.waiting : undefined
  const reason = item.state === 'queued' ? waiting?.reason : item.reason
  if (!reason) return null
  return <div className={`small mt-1 remediation-page-item-reason-div ${item.state === 'stuck' ? 'remediation-stuck' : 'muted'}`}>
    {reason}
    {waiting?.changes?.map((change) => <span key={change.id}> · <Link to={`/changes/${change.id}`}>change {change.number}</Link></span>)}
    {waiting?.remediationId && <span> · <Link to={`/conformance/remediations/${waiting.remediationId}`}>remediation {waiting.remediationNumber}</Link></span>}
  </div>
}

export function RemediationPage({ id }: { id: string }) {
  const q = useGet<RemediationDetail>(`/conformance/remediations/${id}`, { poll: 5000 })
  const data = q.data
  const { run, busy, err } = useAction()
  const act = (path: string, body?: unknown) => run(() => post(path, body ?? {}))
  const record = async (path: string, body: unknown) => {
    const saved = await run(async () => { await post(path, body); return true })
    if (!saved) throw new Error('The action could not be saved. Your words are kept here; try again.')
  }
  if (!data) return <ReadStatus q={q} what="the remediation" className="small" />
  const m = data.remediation
  const byProject: Record<string, Item[]> = {}
  for (const it of data.items) (byProject[it.projectSlug] ??= []).push(it)
  return (
    <div>
      <StaleNote q={q} />
      <PageHead page="conformance" title={<>Remediation #{m.number} <Status word={m.statusLabel} help={m.status.replaceAll('_', ' ')} /></>} actions={<span className="row remediation-page-span">
        {m.status === 'running' && <button disabled={busy} onClick={() => act(`/conformance/remediations/${m.id}/pause`)}>Pause</button>}
        {m.status === 'paused' && <button disabled={busy} onClick={() => act(`/conformance/remediations/${m.id}/resume`)}>Resume</button>}
        {(m.status === 'running' || m.status === 'paused') && <button disabled={busy} onClick={() => act(`/conformance/remediations/${m.id}/cancel`)}>Cancel</button>}
      </span>} />
      <div className="row remediation-page-div-2"><JobBar counts={m.counts} /><span className="muted small">{m.sentence} · {m.actor} · {fmtWhen(m.createdAt)} · {m.mode}{m.cost ? ` · $${m.cost.toFixed(3)} so far` : ''}</span></div>
      {err && <div className="error small mb-3">{err}</div>}
      {Object.entries(byProject).map(([slug, items]) => (
        <div key={slug} className="card mb-3 remediation-page-card">
          <div className="remediation-page-div-3">
            <Link to={`/conformance/projects/${slug}`} className="remediation-page-link">{slug}</Link>
          </div>
          {items.map((it) => {
            const tries = data.attempts[it.id ?? ''] ?? []
            return (
              <ListRow key={it.id} className="remediation-page-div-4 remediation-item-row" identity={
                  <span className="row remediation-page-span-2">
                    <Status word={it.statusLabel} help={it.state === 'queued' ? it.waiting?.reason : (it.reason || it.state)} />
                    <span className="mono remediation-page-mono">{it.check}</span>
                    {it.changeId ? <Link to={`/changes/${it.changeId}`} className="subtle small">{it.changeNumber ? `change ${it.projectSlug}#${it.changeNumber}` : 'the change'}</Link> : null}
                    {it.attempt > 1 && <span className="muted small">attempt {it.attempt}</span>}
                    {it.doing && <span className="muted small">· on {it.doing}</span>}
                  </span>
                  } action={<span className="row remediation-page-span">
                    {it.state === 'stuck' && <QuietPrompt label="Retry" yes="Retry" disabled={busy}
                      ask={`Retry ${it.check} on ${slug}. Add a note for the agent if needed.`}
                      fields={[{ key: 'note', label: 'Retry note (optional)', placeholder: 'Note (optional)' }]}
                      onYes={({ note }) => record(`/conformance/remediations/${m.id}/items/${it.id}/retry`, { note })} />}
                    {(it.state === 'stuck' || it.state === 'queued') && <QuietPrompt label="Skip" yes="Skip" disabled={busy}
                      ask={`Skip ${it.check} on ${slug}. Why should this item be skipped?`}
                      fields={[{ key: 'reason', label: 'Reason for skipping', placeholder: 'Reason (required)', required: true }]}
                      onYes={({ reason }) => record(`/conformance/remediations/${m.id}/items/${it.id}/skip`, { reason: reason.trim() })} />}
                  </span>} detail={<><ItemReason item={it} />
                {it.state === 'accepting' && it.changeId ? <div className="small mt-1">The fix is ready: <Link to={`/changes/${it.changeId}`}>accept it on the change page</Link> and it lands itself.</div> : null}
                {tries.length > 1 && <div className="muted small mt-1">{tries.map((t) => `#${t.n}: ${t.outcome || 'in progress'}${t.reason ? ` (${t.reason.slice(0, 80)})` : ''}`).join(' · ')}</div>}</>} />
            )
          })}
        </div>
      ))}
      {/* The work itself first; how many run at once, below it. */}
      <OccupancyCard occ={data.occupancy} />
    </div>
  )
}
