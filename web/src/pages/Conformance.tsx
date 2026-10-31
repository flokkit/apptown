import { Tabs } from '../Tabs'
import { useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { post } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import type { ConformanceReviewDetail, ConformanceView, Review } from '../wire.gen'
import { type LiveReview, isUnfinished, LensBanner, ReviewFeed, ReviewProgress, tabClass, useStartRemediation } from './conformance/parts'
import { ProjectsList } from './conformance/Projects'
import { ProjectPage } from './conformance/ProjectPage'
import { ReviewsList, isFleetReview } from './conformance/Reviews'
import { ChecksList } from './conformance/Checks'
import { RemediationsList } from './conformance/Remediations'
import { RemediationPage } from './conformance/RemediationPage'
import { FleetVersionsTab } from './conformance/FleetVersions'
import { WhatIfsList } from './conformance/WhatIfs'
import { PageHead } from '../PageHead'

// Conformance (design/subsystems/conformance-surface.md): projects first, reviews as
// records, checks third. Every view is read through a LENS — the current
// picture, or exactly what one past review recorded — rendered by the same
// components. A project's standing is one plain sentence plus a lane bar;
// there is deliberately no percentage. Styling rides the app's tokens and the
// shared .chip/.card so it reads as one product.
//
// This file is the page: the header, the tabs and the review being
// followed; each view lives in pages/conformance/.

// --- the page ---------------------------------------------------------------------------------------

export default function Conformance({ tab }: { tab: 'projects' | 'project' | 'reviews' | 'checks' | 'remediations' | 'remediation' | 'ways' | 'what-ifs' }) {
  const { reviewId, slug, remId } = useParams()
  const rid = reviewId ?? ''
  const base = rid ? `/conformance/reviews/${rid}` : '/conformance'
  const { run: act, busy: starting, err: actErr } = useAction()
  const remediation = useStartRemediation()
  // The review being followed to its end: its activity polls quickly and the
  // project rows refresh every few seconds, so the list itself shows the
  // review walking. A review someone else started is followed too.
  const [following, setFollowing] = useState('')
  const viewQ = useGet<ConformanceView>(rid ? `/conformance?review=${rid}` : '/conformance', { poll: following ? 2800 : 0 })
  // An AppTown with nothing adopted answers projects: null; the lists below
  // treat that as none (it once crashed the page on a fresh AppTown).
  const view = useMemo(() => viewQ.data ? { ...viewQ.data, projects: viewQ.data.projects ?? [] } : undefined, [viewQ.data])
  const liveQ = useGet<ConformanceReviewDetail>(following ? `/conformance/reviews/${following}` : null, { poll: 700 })
  // A review detail names its review; one without it (gone) has nothing to follow.
  const liveData = liveQ.data
  const live = useMemo<LiveReview | null>(() => following !== '' && liveData?.review ? { review: liveData.review, verdicts: liveData.verdicts ?? [] } : null, [following, liveData])
  const busy = starting || remediation.busy || following !== ''
  const reloadView = viewQ.reload
  // Reviews this page has seen finish. Until the refreshed list arrives, the
  // cached one still says such a review is running; following it again from
  // that stale row, and ending again on its cached finished detail, looped
  // until React stopped it (maximum update depth).
  const [finished, setFinished] = useState<ReadonlySet<string>>(() => new Set())
  useEffect(() => {
    if (following && live && live.review.id === following && !isUnfinished(live.review.status)) {
      setFinished((f) => new Set(f).add(following))
      setFollowing('')
      void reloadView()
    }
  }, [following, live, reloadView])
  // A review the view says is queued or running is followed from the same
  // render that first shows it, not from an effect after it: that render
  // would otherwise commit the queued review beside an enabled "Run a review
  // now", offering a second review (seen as a flaky test, 11 October).
  const isRunning = (r?: { id: string | null; status: string } | null) => !!r?.id && isUnfinished(r.status) && !finished.has(r.id)
  const running = isRunning(view?.lens.review) ? view?.lens.review : view?.reviews?.find(isRunning)
  if (running?.id && !following) setFollowing(running.id)

  const run = async (projects?: string[]) => {
    const rev = await act(() => post<Review>('/conformance/review', projects ? { projects, build: true } : { build: true }))
    if (rev?.id) setFollowing(rev.id)
  }
  // The page's own acts' failures; a read with an answer stays shown under a
  // stale note, one without says why it is missing.
  const shownErr = actErr || remediation.err

  const s = view?.summary
  const readOnly = rid !== ''
  const active = tab === 'project' ? 'projects' : tab === 'remediation' ? 'remediations' : tab

  return (
    <div>
      <PageHead page="conformance" />

      {view && <LensBanner view={view} />}

      {!readOnly && (active === tab || following !== '') && (
        <div className="card" style={{ marginBottom: 'var(--space-4)', padding: '10px 14px' }}>
          <div className="row" style={{ justifyContent: 'space-between', gap: 'var(--space-3)', alignItems: 'center' }}>
            <div className="small">
              {tab === 'projects' && (s ? <>{s.findings} confirmed problem{s.findings === 1 ? '' : 's'} to fix now. {s.flagged > 0 && `${s.flagged} possible problem${s.flagged === 1 ? '' : 's'} to review. `}{s.incompleteProjects} app{s.incompleteProjects === 1 ? '' : 's'} not fully checked.</> : 'No checks yet.')}
            </div>
            {live && isUnfinished(live.review.status)
              ? <ReviewProgress d={live} />
              : <span className="row" style={{ gap: 'var(--space-2)' }}>
                  {s && s.findings > 0 && <button disabled={busy} title="Start a remediation over every finding: one change per project at a time, each waiting for your approval" onClick={() => remediation.start('all', '')}>Fix {s.findings} problems</button>}
                  <button className="primary" disabled={busy} onClick={() => run()}>{busy ? 'Starting…' : 'Check now'}</button>
                </span>}
          </div>
        </div>
      )}
      {readOnly && live && live.review.id === rid && <ReviewFeed d={live} />}
      {shownErr && <div className="error small mb-3">{shownErr}</div>}
      <StaleNote q={[viewQ, liveQ]} />
      {following !== '' && <ReadStatus q={liveQ} what="the running review" className="small" />}

      <Tabs className="tabs">
        <Link to={base} className={tabClass(active === 'projects')}>Projects</Link>
        <Link to="/conformance/reviews" className={tabClass(active === 'reviews')}>Fleet reviews{view?.reviews?.filter(isFleetReview).length ? ` (${view.reviews.filter(isFleetReview).length})` : ''}</Link>
        <Link to={`${base}/checks`} className={tabClass(active === 'checks')}>Checks</Link>
        {!readOnly && <Link to="/conformance/remediations" className={tabClass(active === 'remediations')}>Remediations</Link>}
        {!readOnly && <Link to="/conformance/ways" className={tabClass(active === 'ways')}>Ways of working</Link>}
        {!readOnly && <Link to="/conformance/what-ifs" className={tabClass(active === 'what-ifs')}>What-ifs</Link>}
      </Tabs>

      {!view && <ReadStatus q={viewQ} what="the conformance record" className="small" />}
      {/* Keyed by what they show: a refresh keeps the person's open folds,
          moving to another review or project starts its own. */}
      {view && tab === 'projects' && <ProjectsList key={base} view={view} base={base} onCheck={readOnly || busy ? undefined : sl => run([sl])} />}
      {view && tab === 'project' && slug && <ProjectPage key={`${base}/projects/${slug}`} view={view} slug={slug} readOnly={readOnly} onRunOne={(sl) => run([sl])} />}
      {tab === 'reviews' && <ReviewsList />}
      {view && tab === 'checks' && <ChecksList key={base} view={view} base={base} />}
      {tab === 'remediations' && <RemediationsList />}
      {tab === 'ways' && <FleetVersionsTab />}
      {tab === 'what-ifs' && <WhatIfsList />}
      {tab === 'remediation' && remId && <RemediationPage id={remId} />}
    </div>
  )
}
