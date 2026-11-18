import { PRODUCT } from '../brand'
import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useGet, useAction } from '../data'
import { currentUser, post } from '../api'
import { DecisionForm } from '../DecisionForm'
import { DecisionChoices, type DecisionLayout } from '../DecisionChoices'
import { useHolds } from '../instancePages'
import { changedFiles } from './janitor/files'
import { ReadStatus, StaleNote } from '../ReadGate'
import { Section } from '../Section'
import { ListTable, type ListColumn } from '../ListTable'
import { fmtWhen } from '../format'
import { PageHead } from '../PageHead'
import { RunNow } from './janitor/RunNow'
import { TryAgain } from './janitor/TryAgain'
import type { JanitorView, JanitorProject, JanitorHistoryView, JanitorLandedChange, QueueStatus } from '../wire.gen'

const projectTo = (p: JanitorProject) => `/projects/${p.slug}/settings/general#janitor`
const date = (value?: string) => value && !value.startsWith('0001-') ? fmtWhen(value) : 'Not known'

export function summary(data: JanitorView, items: Need[]) {
 const c = data.choices?.choices
 if (!c) return ''
 if (c.dependencies === 'off' && !c.securityFixes && !c.baseImages && !c.toolchainPatches && !c.packs) return 'Janitor is off'
 const count = new Set(items.map(n => n.project.projectId)).size
 if (count) return count === 1 ? '1 project needs you' : `${count} projects need you`
 const kept = data.projects.filter(p => p.queues.length > 0 && p.state.word !== 'off' && p.state.word !== 'no repository').length
 return `Keeping ${kept} ${kept === 1 ? 'project' : 'projects'} up to date`
}

type Need = { key: string; project: JanitorProject; what: string; reason: string; to: string; queue?: QueueStatus; scheduleProblem?: boolean }
export function needs(data: JanitorView): Need[] {
  return [...data.projects.filter(p => p.queues.length > 0).flatMap(project => [
    ...project.queues.filter(q => ['decide', 'stopped'].includes(q.state)).map(queue => ({ key: `${project.projectId}/${queue.queue}`, project, what: queue.title, reason: [queue.state === 'decide' ? queue.changeSummary || changedFiles(queue.packages, queue.files) : '', queue.words].filter(Boolean).join(' · '), to: queue.changeId ? `/changes/${queue.changeId}` : projectTo(project), queue })),
    ...(project.scheduleProblem ? [{ key: `${project.projectId}/unknown`, project, what: 'Updates', reason: `${PRODUCT} could not read this project's update schedule · ${project.scheduleProblem}`, to: projectTo(project), scheduleProblem: true }] : []),
    ...data.deployments.filter(d => d.projectId === project.projectId && d.hold).map(d => ({ key: d.deploymentId!, project, what: d.displayLabel, reason: `Release ${d.hold!.releaseNumber} to ${d.displayLabel} ${d.hold!.reason === 'release failed' ? 'failed' : d.hold!.reason}`, to: `/projects/${project.slug}/deployments/${d.name}?tab=history` })),
  ])]
}

function History({ projects }: { projects: JanitorProject[] }) {
  const [page, setPage] = useState({ older: false, before: '', offset: 0 })
  const params = new URLSearchParams({ period: page.older ? 'older' : 'recent', offset: String(page.offset) })
  if (page.before) params.set('before', page.before)
  const q = useGet<JanitorHistoryView>(`/janitor/history?${params}`, { poll: page.older ? undefined : 10000 })
  const data = q.data
  const columns: ListColumn<JanitorLandedChange>[] = [
    { key: 'project', label: 'Project', phone: 'name', render: c => <Link to={`/changes/${c.changeId}?tab=changes`}>{c.project}</Link> },
    { key: 'summary', label: 'What changed', render: c => c.packVersions.length ? `Ways of working: ${c.packVersions.map(p => p.pack).join(', ')}` : c.packages.length ? c.packages.map(p => `${p.package} ${p.from} → ${p.to}`).join(', ') : projects.find(p => p.projectId === c.projectId)?.queues.find(q => q.queue === c.queue)?.title || c.title },
    { key: 'when', label: 'When', kind: 'time', render: c => date(c.landedAt) },
  ]
  return <>
    <h3>{page.older ? 'Older history' : 'Last 7 days'}</h3>
    <ReadStatus q={q} what="landed janitor Changes" /><StaleNote q={q} />
    {data && <>{data.history.changes.length ? <ListTable label="Landed janitor Changes" columns={columns} rows={data.history.changes} rowKey={c => c.changeId!} rowTo={c => `/changes/${c.changeId}?tab=changes`} /> : <p>{page.older ? 'No older janitor Changes.' : 'No janitor Changes landed in the last 7 days.'}</p>}
      <div className="row row-2">
        {page.offset > 0 && <button disabled={!q.current} onClick={() => setPage({ ...page, offset: Math.max(0, page.offset - 20) })}>Previous</button>}
        {data.history.more && <button disabled={!q.current} onClick={() => setPage({ ...page, before: data.until, offset: page.offset + 20 })}>Next 20</button>}
        {!page.older && <button disabled={!q.current} onClick={() => setPage({ older: true, before: data.since, offset: 0 })}>Older history</button>}
        {page.older && <button onClick={() => setPage({ older: false, before: '', offset: 0 })}>Last 7 days</button>}
      </div>
    </>}
  </>
}

export function NeedsYou() {
  const q = useGet<JanitorView>('/janitor', { poll: 10000 })
  const data = q.data
  const items = data ? needs(data) : []
  return <div>
    <PageHead page="maintenance" sub="Needs you" line={data && summary(data, items)} />
    <ReadStatus q={q} what="janitor" /><StaleNote q={q} />
    {data && <>
      {/* Decisions share state across the prose and action cells, so this
          table owns its rows and uses the shared record-table conventions. */}
      {items.length === 0 ? <p>Nothing needs you.</p> : <Section title="Needs you"><table className="record-table record-table-links" aria-label="Needs you">
        <thead><tr><th scope="col">Project</th><th scope="col" className="record-fit">What it was doing</th><th scope="col">Why</th><th scope="col" className="record-fit record-action" /></tr></thead>
        <tbody>{items.map(n => <NeedRow key={`${n.key}/${n.queue?.changeId}/${n.queue?.decide?.stepId}`} need={n} current={q.current} reload={q.reload} />)}</tbody>
      </table></Section>}

    </>}
  </div>
}

function NeedRow({ need, current, reload }: { need: Need; current: boolean; reload: () => Promise<void> }) {
  const nav = useNavigate()
  const row = (details?: ReactNode, actions?: ReactNode) => <tr data-linked onClick={e => {
    if (!(e.target as Element).closest('a, button, input, select, [role=dialog]')) nav(need.to)
  }}>
    <td className="phone-name"><Link to={need.to}>{need.project.name}</Link></td>
    <td className="record-fit phone-value" data-label="What it was doing">{need.what}</td>
    <td className="phone-value" data-label="Why">{need.reason}{details}</td>
    <td className="record-fit record-action phone-action">{actions}</td>
  </tr>
  return need.queue ? <QueueAction queue={need.queue} slug={need.project.slug} current={current} reload={reload}>{row}</QueueAction> : row(undefined, need.scheduleProblem ? <RunNow slug={need.project.slug} queues={need.project.queues} current={current} reload={reload} /> : undefined)
}

function QueueAction({ queue, slug, current, reload, children }: { queue: QueueStatus; slug: string; current: boolean; reload: () => void; children: DecisionLayout }) {
  const action = useAction(reload)
  const canClose = useHolds('change:abandon', slug)
  const canAccept = useHolds(queue.decide?.act === 'approve' ? 'change:approve' : 'change:override', slug)
  const [note, setNote] = useState('')
  if (queue.state === 'stopped') return <TryAgain queue={queue} slug={slug} current={current} reload={reload}>{children}</TryAgain>
  const close = { label: 'Close', disabled: !canClose, run: () => action.run(() => post(`/projects/${encodeURIComponent(slug)}/janitor/${queue.queue}/skip`, { changeId: queue.changeId })) }
  const layout = (details: ReactNode, actions: ReactNode) => children(<>{details}{action.err && <div className="error">{action.err}</div>}</>, actions)
  return queue.decide?.act === 'approve' ? <DecisionForm key={queue.decide.stepId} stepId={queue.decide.stepId!} approveLabel="Accept" close={close} externalBusy={!current || action.busy} approveDisabled={!canAccept} onDone={reload}>{layout}</DecisionForm> :
    <DecisionChoices note={note} setNote={setNote} busy={!current || action.busy} accept={queue.decide ? {
      label: 'Accept', disabled: !canAccept, run: () => {}, prompt: {
        label: 'Accept anyway',
        ask: "Accept the change although the check did not pass? This records an exception in your name, with your reason; the check's result stays on the record.",
        run: reason => action.run(() => post(`/steps/${queue.decide!.stepId}/override`, { user: currentUser(), reason })),
      },
    } : undefined}>{(details, actions) => layout(details && <div className="ch-forward-reason">{details}</div>, <div className="row row-2">
      {actions}<button disabled={!current || action.busy || !canClose} onClick={close.run}>Close</button>
    </div>)}</DecisionChoices>
}

export function UpdatesHistory() {
  const q = useGet<JanitorView>('/janitor', { poll: 10000 })
  return <div>
    <PageHead page="maintenance" sub="Updates" />
    <Section title="Recent updates"><History projects={q.data?.projects || []} /></Section>
  </div>
}
