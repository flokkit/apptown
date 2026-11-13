import { viewerTurn } from './change/Turn'
import './change/deploy-scene.css'
import '../overview-line.css'
import { runStory } from './change/story'
import { InlineHelp } from '../InlineHelp'
import { Tabs, revealActiveTab } from '../Tabs'
import { useEffect } from 'react'
import { Link, useLocation, useParams, useSearchParams } from 'react-router-dom'
import { setProjectScope } from '../projectScope'
import { setPageContext } from '../chrome'
import { ChangeView } from '../api'
import { useGet } from '../data'
import { ChangesTab, changeReport, changeTitle } from './change/Head'
import { ChangeOverview } from './change/Overview'
import { StatePanel } from './change/StatePanel'
import { ConversationLog } from './change/Conversation'
import { useRunDetail } from './change/Tabs'
import { ChangeDetails } from './change/Details'
import './change/change.css'

// Overview contains the change and its decision; Details keeps the complete
// run, records and reference material. A step is addressable with tab=details.
export const CHANGE_TABS = ['overview', 'changes', 'conversation', 'details'] as const
export type ChangeTab = typeof CHANGE_TABS[number]

export default function ChangePage() {
  const { id } = useParams()
  const location = useLocation()
  const [params] = useSearchParams()
  const rawTab = params.get('tab') ?? ''
  const tab: ChangeTab = (CHANGE_TABS as readonly string[]).includes(rawTab) ? rawTab as ChangeTab : 'overview'
  const runParam = params.get('run') || undefined

  // The address names the change by its id or its friendly form (slug#n).
  const read = useGet<ChangeView>(`/changes/${encodeURIComponent(id ?? '')}`, { poll: 3000 })
  const { data: view, err, reload, current } = read

  useEffect(() => { if (view) setProjectScope(view.projectSlug) }, [view?.projectSlug]) // eslint-disable-line react-hooks/exhaustive-deps
  // The bar's sentence: what you are DOING, not just what you see.
  useEffect(() => {
    if (!view) return
    const ch = view.change
    setPageContext({
      names: { [`/changes/${id}`]: `Change ${ch.number} · ${changeTitle(ch)}` },
    })
  }, [view?.change.number, view?.change.state, view?.change.title, view?.change.body]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => () => setPageContext({}), [])

  useEffect(() => {
    const strip = document.querySelector<HTMLElement>('.ch-tabs')
    if (strip) revealActiveTab(strip)
  }, [tab, view?.change.id])

  useEffect(() => {
    if (!location.hash || !view) return
    document.getElementById(location.hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [tab, location.key, location.hash, view?.change.id]) // eslint-disable-line react-hooks/exhaustive-deps

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
    if (t !== 'overview') q.set('tab', t)
    for (const [k, v] of Object.entries(extra)) if (v != null) q.set(k, String(v))
    const s = q.toString()
    return `/changes/${view.change.id}${s ? `?${s}` : ''}`
  }
  const workTab = (stepId?: string | null, runId?: string | null) => href('details', { run: runId && runId !== view.detail?.run.id ? runId : undefined, step: stepId })

  const steps = [...(workRunId && work.detail ? work.detail.steps : []), ...(view.detail?.steps ?? [])]
  const TABS: { key: ChangeTab; label: string }[] = [
    { key: 'overview', label: 'Overview' },
    { key: 'changes', label: 'Code' },
    { key: 'conversation', label: 'Conversation' },
    { key: 'details', label: 'Details' },
  ]

  return (
    <InlineHelp.Provider value><div className="ch-page">
      <StatePanel view={view} reading={read} />
      {tab !== 'overview' && current && viewerTurn(view) && <p>Your decision is waiting · <Link to={href('overview')}>Overview</Link></p>}


      <section className="ch-tabs-sheet">
        <Tabs className="tabs ch-tabs" role="navigation" aria-label="more about this change">
          {TABS.map((t) => (
            <Link key={t.key} to={href(t.key)} className={'tab' + (t.key === tab ? ' active' : '')} aria-current={t.key === tab ? 'page' : undefined}>{t.label}</Link>
          ))}
        </Tabs>
        <div className="ch-tab-body">

          {tab === 'changes' && <ChangesTab view={view} run={view.detail?.run} />}
          <ChangeOverview progressCurrent={!read.refreshFailed} shown={tab === 'overview'} view={view} current={current} steps={steps} extraErr={workRunId && !work.detail ? work.err : undefined} onChange={reload} workTab={workTab} />
          {tab === 'conversation' && (
            <ConversationLog earlierRuns={(view.runs ?? []).map(r => r.id!).filter(id => id !== view.detail?.run.id && id !== work.detail?.run.id)} rounds={[...(workRunId && work.detail ? runStory(work.detail) : []), ...(view.detail ? runStory(view.detail) : [])].filter(e => e.step?.type === 'agent/pi' || e.key.startsWith('work-'))} request={view.change.body || view.change.title} report={changeReport(view)} changeId={view.change.id!} chatHref={view.change.state === 'in_progress' ? `/projects/${view.projectSlug}/console?change=${view.change.id}` : undefined} />
          )}
          {tab === 'details' && <ChangeDetails key={view.change.id} view={view} current={current} reload={reload} runId={runParam} />}

        </div>
      </section>

    </div></InlineHelp.Provider>
  )
}
