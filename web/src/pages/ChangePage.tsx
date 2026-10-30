import { useEffect } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { setProjectScope } from '../projectScope'
import { setPageContext } from '../chrome'
import { ChangeView } from '../api'
import { useGet } from '../data'
import { ChangeBrief, ChangeHeader, ChangesTab } from './change/Head'
import { Evidence, RunEvidence } from './change/Evidence'
import { StatePanel } from './change/StatePanel'
import { Disagreement } from './change/Disagreement'
import { ConversationLog } from './change/Conversation'
import { HistoryTab, ReleasesTab, WorkTab, useRunDetail } from './change/Tabs'
import { changeChip, nextStep } from './change/next'
import { ChangeWorkerChats } from './WorkerChat'

// The change page, in the order a person asks:
//   1. what this change is — title, project, when and how it started, one line;
//   2. where it is, with ONE next action for whoever is looking (or who it
//      waits on), and anything that needs a person — a decision, a failure,
//      the disagreement, a chat's refused save — on the first screen;
//   3. the evidence as receipts, one ruled line per recorded fact;
//   4. tabs for the rest, addressable by URL (?tab=work&step=…).

export const CHANGE_TABS = ['changes', 'work', 'conversation', 'releases', 'history', 'about'] as const
export type ChangeTab = typeof CHANGE_TABS[number]

export default function ChangePage() {
  const { id } = useParams()
  const [params] = useSearchParams()
  const rawTab = params.get('tab') ?? ''
  const tab: ChangeTab = (CHANGE_TABS as readonly string[]).includes(rawTab) ? rawTab as ChangeTab : 'changes'
  const runParam = params.get('run') || undefined
  const stepParam = params.get('step') || undefined

  // The address names the change by its id or its friendly form (slug#n).
  const read = useGet<ChangeView>(`/changes/${encodeURIComponent(id ?? '')}`, { poll: 3000 })
  const { data: view, err, reload, current } = read

  useEffect(() => { if (view) setProjectScope(view.projectSlug) }, [view?.projectSlug]) // eslint-disable-line react-hooks/exhaustive-deps
  // The bar's sentence: what you are DOING, not just what you see.
  useEffect(() => {
    if (!view) return
    const ch = view.change
    const chip = changeChip(view)
    setPageContext({
      crumbs: [{ label: `Change ${ch.number} · ${ch.title}` }],
      chip,
    })
  }, [view?.change.number, view?.change.state, view?.change.title, view && changeChip(view).text]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => () => setPageContext({}), [])

  // The work a landing carries: when the latest run is a landing, its
  // receipts are only half the story — the implementation and review are
  // in the working run before it.
  const latestRole = view?.runs?.find((r) => r.id === view.detail?.run.id)?.role
  // Run ids are UUIDv7, so their string order is their creation order.
  const latestId = view?.detail?.run.id ?? ''
  const workRunId = latestRole === 'landing'
    ? view?.runs?.map((r) => r.role === 'working' ? r.id ?? '' : '').filter((id) => id && id < latestId).sort().at(-1)
    : undefined
  const work = useRunDetail(view ?? ({} as ChangeView), workRunId)

  if (!view) return err ? <div className="error">This change could not be read: {err}</div> : <div className="muted">Reading the change…</div>

  const href = (t: ChangeTab, extra: Record<string, string | null | undefined> = {}) => {
    const q = new URLSearchParams()
    if (t !== 'changes') q.set('tab', t)
    for (const [k, v] of Object.entries(extra)) if (v != null) q.set(k, String(v))
    const s = q.toString()
    return `/changes/${view.change.id}${s ? `?${s}` : ''}`
  }
  const workTab = (stepId?: string | null, runId?: string | null) => href('work', { run: runId && runId !== view.detail?.run.id ? runId : undefined, step: stepId })
  const runHref = (runId: string) => href('work', { run: runId !== view.detail?.run.id ? runId : undefined })

  const next = nextStep(view)
  const deciding = next.primary === 'approve' ? next.step?.id ?? undefined : undefined
  const evidenceRuns: RunEvidence[] = []
  if (view.detail) evidenceRuns.push({ run: view.detail.run, role: latestRole, steps: view.detail.steps })
  if (workRunId && work.detail) evidenceRuns.push({ run: work.detail.run, role: 'working', steps: work.detail.steps })
  const firstRun = view.runs?.length ? view.runs[view.runs.length - 1] : undefined
  const stepCount = (view.detail?.steps.length ?? 0)

  const TABS: { key: ChangeTab; label: string }[] = [
    { key: 'changes', label: 'Code' },
    { key: 'work', label: view.detail ? `Work · ${stepCount} step${stepCount === 1 ? '' : 's'}` : 'Work' },
    { key: 'conversation', label: 'Conversation' },
    { key: 'releases', label: 'Previews and releases' },
    { key: 'history', label: 'History' },
    { key: 'about', label: 'About' },
  ]

  return (
    <div className="ch-page">
      <ChangeHeader view={view} workflow={firstRun?.workflowName} aboutTab={href('about')} />

      <StatePanel view={view} onChange={reload} workTab={(s) => workTab(s)}
        reading={read} />

      {view.detail && <Disagreement detail={view.detail} codeTab={href('changes')} />}
      <ChangeWorkerChats changeId={view.change.id!} />

      <Evidence view={view} runs={evidenceRuns} workTab={workTab} extraErr={workRunId && !work.detail ? work.err : undefined} />

      <section className="ch-tabs-sheet">
        <nav className="tabs ch-tabs" aria-label="more about this change">
          {TABS.map((t) => (
            <Link key={t.key} to={href(t.key)} className={'tab' + (t.key === tab ? ' active' : '')} aria-current={t.key === tab ? 'page' : undefined}>{t.label}</Link>
          ))}
        </nav>
        <div className="ch-tab-body">
          {tab === 'changes' && <ChangesTab view={view} run={view.detail?.run} />}
          {tab === 'work' && <WorkTab view={view} reload={reload} runId={runParam} focusStep={stepParam} deciding={deciding} runHref={runHref} />}
          {tab === 'conversation' && (
            <div>
              <div className="row" style={{ marginBottom: 10 }}>
                <Link to={`/projects/${view.projectSlug}/console?change=${view.change.id}`}>Open chat →</Link>
              </div>
              <ConversationLog changeId={view.change.id!} />
            </div>
          )}
          {tab === 'releases' && <ReleasesTab view={view} />}
          {tab === 'history' && <HistoryTab view={view} runHref={runHref} />}
          {tab === 'about' && <ChangeBrief view={view} current={current} reload={reload} />}
        </div>
      </section>
    </div>
  )
}
