import { PRODUCT } from '../../brand'
import { Link } from 'react-router-dom'
import type { Artifact, RunDetail, Step } from '../../api'
import { renderMd } from '../../md'
import { useGet } from '../../data'

// The disagreement, shown so a person can see what the problem is and
// adjudicate it: every failed machine verdict in the
// current stage, in full, beside the implementer's claim. The symmetry is
// deliberate: a person is judging a dispute between two parties, not
// reading an error log. The same reading as `apptown show` (cmd/apptown
// disputeText): each output once, a long one as its first line and tail.

const WHO: Record<string, string> = { 'agent/review': 'reviewer', preview: 'boot check', scan: 'scanner' }

export type Verdict = { step: Step; who: string; text?: string; findings: string[]; log?: string; repeatOf?: string; reviewerPassed?: string }

export function disputeOf(d: RunDetail): Verdict[] {
  const shown = new Map<string, string>()
  const once = (step: string, text?: string): [string | undefined, string | undefined] => {
    const key = (text ?? '').trim()
    if (!key) return [undefined, undefined]
    const first = shown.get(key)
    if (first) return [undefined, first]
    shown.set(key, step)
    return [excerpt(key), undefined]
  }
  return d.steps
    .filter((s) => s.stage === d.run.currentStage && s.type !== 'approval' && (s.status === 'fail' || s.status === 'error') && !s.overrideBy)
    .map((s) => {
      const ev = s.evidence as { summary?: string; reason?: string; findings?: unknown; logTail?: string; reviewerSummary?: string }
      const [text, repeatText] = once(s.name, ev.summary || ev.reason)
      const [log, repeatLog] = once(s.name, ev.logTail)
      // A review the model passed and AppTown's file check failed is the
      // AppTown's failure; the reviewer's pass is shown apart.
      const reviewerPassed = ev.reviewerSummary || undefined
      return {
        step: s, who: reviewerPassed ? `${PRODUCT}'s file check` : WHO[s.type] ?? 'machine check', text, log, reviewerPassed,
        findings: Array.isArray(ev.findings) ? ev.findings.map(String) : [],
        repeatOf: repeatText ?? repeatLog,
      }
    })
}

// excerpt keeps a long output's first line and its last 40 lines: an
// error's sentence leads, and a log's end says what broke.
export function excerpt(s: string, tail = 40): string {
  const lines = s.trim().split('\n')
  if (lines.length <= tail + 1) return s.trim()
  return [lines[0], `… (${lines.length - 1 - tail} lines omitted)`, ...lines.slice(lines.length - tail)].join('\n')
}

// claimOf: the implementer's report — the newest artifact of a kind one of
// the run's review steps declares it audits ("of"). The declaration
// decides, never a kind's spelling.
export function claimOf(d: RunDetail): Artifact | undefined {
  const audited = new Set(d.steps.filter((s) => s.type === 'agent/review').map((s) => (s.config as { of?: string }).of).filter(Boolean))
  return [...d.artifacts].reverse().find((a) => audited.has(a.kind))
}

export function Disagreement({ detail, codeTab }: { detail: RunDetail; codeTab: string }) {
  const verdicts = disputeOf(detail)
  if (verdicts.length === 0) return null
  const claim = claimOf(detail)
  return (
    <section className="ch-dispute" aria-label="What failed">
      <div className="ch-section-head">
        <h2>What failed</h2>
      </div>
      <div className={claim ? 'ch-dispute-grid' : undefined}>
        <div className="ch-dispute-side">
          <div className="ch-label">What the checks found</div>
          {verdicts.map((v) => (
            <div key={v.step.id} className="ch-verdict">
              <div className="row"><span className="chip fail">{v.step.status === 'error' ? 'error' : 'failed'}</span><strong>{v.step.name}</strong><span className="muted">{v.who}</span></div>
              {v.text && <pre className="ch-verdict-text">{v.text}</pre>}
              {v.reviewerPassed && <div className="muted small">The reviewer itself passed it: “{v.reviewerPassed}”</div>}
              {v.findings.length > 0 && <ol className="ch-findings">{v.findings.map((f, i) => <li key={i}>{f}</li>)}</ol>}
              {v.log && <pre className="log">{v.log}</pre>}
              {v.repeatOf && <div className="muted small">(the same output as {v.repeatOf}, above)</div>}
            </div>
          ))}
        </div>
        {claim && <Claim artifact={claim} />}
      </div>
      <div className="muted small" style={{ marginTop: 8 }}>The code itself: <Link to={codeTab}>the Code tab</Link>.</div>
    </section>
  )
}

function Claim({ artifact }: { artifact: Artifact }) {
  const truncated = (artifact.bodyBytes ?? 0) > artifact.body.length
  const full = useGet<Artifact>(truncated ? `/artifacts/${artifact.id}` : null, { kind: 'immutable' })
  const body = full.data?.body ?? artifact.body
  return (
    <div className="ch-dispute-side">
      <div className="ch-label">What the agent says ({artifact.kind})</div>
      {full.err && <div className="error small">Showing the first part only; the rest could not be read: {full.err}</div>}
      {body ? <div className="md ch-claim" dangerouslySetInnerHTML={{ __html: renderMd(body) }} /> : <div className="muted small">The report is empty.</div>}
    </div>
  )
}
