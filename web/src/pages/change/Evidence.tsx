import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ChangeView, Run, Step, TestsEvidence } from '../../api'
import { fmtStamp } from '../../format'
import { approversOf } from './next'
import { closingVia, voterWords } from './through'

// The evidence as receipts: one ruled line per recorded fact — what was
// checked, its result, who or what did it, when — each linking to its full
// record in the Work tab. These lines come from records; the summary at the
// top of the page is an AI's narrative, and the two never mix.

const checkTypes = new Set(['tests', 'scan', 'script', 'guidance/conformance', 'agent/review', 'scm/merged'])

export function verificationSummary(steps: Step[]): string {
  const checks = steps.filter(s => checkTypes.has(s.type))
  if (!checks.length) return 'No verification checks recorded in this run.'
  // A pass whose check evidence could not be read is not counted as a pass.
  const unread = (s: Step) => !s.overrideBy && s.status === 'pass' && !!s.checkReading?.unreadable
  const count = (status: string) => checks.filter(s => !s.overrideBy && s.status === status && !unread(s)).length
  const parts = [
    [count('pass'), 'passed'],
    [checks.filter(unread).length, 'passed with unreadable evidence'],
    [checks.filter(s => !!s.overrideBy).length, 'overridden'],
    [count('fail') + count('error') + count('needs_human'), 'need attention'],
    [count('pending') + count('running'), 'unfinished'],
    [count('skipped'), 'skipped'],
  ] as const
  return parts.filter(([n]) => n > 0).map(([n, label]) => `${n} ${label}`).join(' · ')
}

export type Receipt = {
  key: string
  result: string   // the stamp's words
  cls: string      // the stamp's chip class
  title: string
  name?: string    // the step's own name, when it says more than the title
  sentence: string
  by: string
  when: string
  stepId?: string
}

const TITLE: Record<string, string> = {
  'agent/pi': 'Implementation', 'agent/review': 'Review', tests: 'Tests', scan: 'Scan',
  'guidance/conformance': 'Ways of working', approval: 'Approval', preview: 'Preview', deploy: 'Deploy',
  reconcile: 'Up to date with main', 'artifact/require': 'Required record', 'adr/require': 'Decision record',
  'scm/merged': 'Merged', 'policy/current': 'Rules still current', 'workflow/call': 'Called workflow', script: 'Script',
}

// testsNote is what a tests step's report says, in words. Only counts with
// no skips are a plain pass: skipped tests, and counts not known (no report,
// one that could not be read, a receipt from before reports), are notes a
// reader sees without opening anything.
export function testsNote(ev: TestsEvidence): { note: string; plain: boolean } {
  const c = ev.testCounts
  if (c) {
    const parts = [`${c.passed} passed`]
    if (c.failed) parts.push(`${c.failed} failed`)
    if (c.skipped) parts.push(`${c.skipped} skipped`)
    return { note: parts.join(', '), plain: c.skipped === 0 }
  }
  if (ev.testReportUnreadable) return { note: `test report unreadable: ${ev.testReportUnreadable}`, plain: false }
  return { note: 'test counts not reported', plain: false }
}

// unmetRules: the ways-of-working rules a conformance check found not met.
const unmetRules = (s: Step) => s.type === 'guidance/conformance' ? (s.checkReading?.checks ?? []).filter((c) => c.verdict === 'fail').map((c) => c.check) : []

function result(s: Step, live: boolean): { result: string; cls: string } {
  if (s.overrideBy) return { result: 'overridden', cls: 'overridden' }
  // A conformance check can pass while some rules are not met (they do not
  // block); it never reads as a plain pass beside "not met".
  if (s.status === 'pass' && unmetRules(s).length) return { result: 'passed with notes', cls: 'pass' }
  if (s.type === 'tests' && s.status === 'pass' && !testsNote(s.evidence).plain) return { result: 'passed with notes', cls: 'pass' }
  // Evidence the server could not read is unknown: never a plain pass.
  if (s.status === 'pass' && s.checkReading?.unreadable) return { result: 'evidence unreadable', cls: 'stale' }
  // A run that ended leaves its unfinished steps unfinished, not waiting.
  if (!live && s.status === 'pending') return { result: s.type === 'approval' ? 'never decided' : 'never ran', cls: '' }
  if (!live && s.status === 'running') return { result: 'stopped', cls: '' }
  switch (s.status) {
    case 'pass': return { result: s.type === 'approval' ? 'approved' : s.type === 'agent/pi' ? 'done' : 'passed', cls: 'pass' }
    case 'fail': return { result: s.type === 'approval' ? 'rejected' : 'failed', cls: 'fail' }
    case 'error': return { result: 'error', cls: 'fail' }
    case 'needs_human': return { result: 'needs a person', cls: 'pending' }
    case 'running': return { result: 'running', cls: 'running' }
    case 'skipped': return { result: 'skipped', cls: 'skipped' }
    default: return s.type === 'approval' ? { result: 'waiting', cls: 'pending' } : { result: 'not run yet', cls: '' }
  }
}

const clip = (s: string, n = 200) => {
  const t = s.replace(/\s+/g, ' ').trim()
  return t.length > n ? t.slice(0, n - 1) + '…' : t
}
const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`
const secs = (ms?: number) => ms == null ? '' : ms < 60000 ? `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)} s` : `${Math.floor(ms / 60000)} min ${Math.round((ms % 60000) / 1000)} s`
// Where a check ran, in words: the worker's internal address is plumbing
// (it stays in the step's full record), not something a reader acts on.
const where = (s: Step) => {
  const p = s.checkReading?.placement
  return p?.worker ? 'on the worker' : p?.local ? `on ${PRODUCT}'s host` : ''
}
const join = (...parts: (string | undefined | false)[]) => parts.filter(Boolean).join(' · ')

function sentence(s: Step, live: boolean): string {
  if (!live && (s.status === 'pending' || s.status === 'running')) return 'The run ended before this step finished.'
  const reason = s.evidence?.reason ?? ''
  const failing = s.status !== 'pass' && s.status !== 'pending' && s.status !== 'running'
  if (s.overrideBy) return `Overridden by ${s.overrideBy}: “${s.overrideReason}”`
  switch (s.type) {
    case 'agent/pi': {
      if (failing && reason) return clip(reason)
      const files = (s.evidence.files ?? []).filter((f) => !f.startsWith('.apptown/'))
      if (s.status === 'running') return 'Working now.'
      if (s.status === 'pending') return 'Not started.'
      return files.length ? `Changed ${plural(files.length, 'file')}: ${files.slice(0, 4).join(', ')}${files.length > 4 ? ', …' : ''}.` : 'Changed no files.'
    }
    case 'agent/review':
      if (s.evidence.reviewerSummary) return `${clip(reason || s.evidence.summary || '')}; the reviewer itself passed it.`
      return s.evidence.summary ? `“${clip(s.evidence.summary, 240)}”` : reason ? clip(reason) : s.status === 'pending' ? 'Not run yet.' : ''
    case 'tests': {
      if (s.status === 'pass' && !s.evidence.command) return clip(reason) // no tests found, as recorded
      // An error gave no answer; only a pass or a failure has counts to read.
      const done = s.status === 'pass' || s.status === 'fail'
      return join(s.evidence.command, failing && reason && clip(reason, 160), done && clip(testsNote(s.evidence).note, 160))
    }
    case 'script':
      return join(s.evidence.command && `${s.evidence.command}`, failing && reason ? clip(reason, 160) : s.status === 'pass' ? 'passed' : '')
    case 'scan': {
      const f = s.evidence.findings
      if (failing && reason && !f) return clip(reason)
      if (!f) return s.status === 'pass' ? 'Report recorded.' : ''
      if (f.total === 0) return 'Nothing found.'
      return `${plural(f.actionable, 'finding')} to act on, of ${f.total}${f.dispositioned ? `; ${f.dispositioned} suppressed` : ''}.`
    }
    case 'guidance/conformance': {
      if (s.checkReading?.unreadable) return 'Its check evidence could not be read.'
      const checks = s.checkReading?.checks ?? []
      if (!checks.length) return reason ? clip(reason) : ''
      const met = checks.filter((c) => c.verdict === 'pass').length
      const failed = checks.filter((c) => c.verdict === 'fail')
      // What main already fails, or what could not be compared with main,
      // is shown apart: it does not block this change.
      const unmet = failed.filter((c) => !c.preexisting && !c.undecided).map((c) => c.check)
      const onMain = failed.filter((c) => c.preexisting).length
      const undecided = failed.filter((c) => c.undecided).length
      const named = `${unmet.slice(0, 3).join(', ')}${unmet.length > 3 ? ', …' : ''}`
      return `${met} of ${plural(checks.length, 'rule')} met.${unmet.length ? s.status === 'pass' ? ` Not met, and not blocking: ${named}.` : ` Not met: ${named}.` : ''}`
        + (onMain ? ` ${onMain} already failing on main.` : '')
        + (undecided ? ` ${undecided} undecided (not known whether this change introduced ${undecided === 1 ? 'it' : 'them'}).` : '')
    }
    case 'approval': {
      const ev = s.evidence
      if (ev.withdrawal) return `Acceptance withdrawn by ${ev.withdrawal.by}: “${ev.withdrawal.reason}”`
      if (s.status === 'pending') return ev.means ? `Approving means: “${ev.means}”` : s.config.means ? `Approving means: “${s.config.means}”` : 'Waiting for a decision.'
      if (ev.evalAuthorised) return `Eval-authorised, not a person's approval: ${ev.evalAuthorised}.`
      const decided = ev.decision === 'reject' ? 'Rejected' : 'Approved'
      const who = ev.approver ? voterWords(ev.approver, closingVia(ev)) : 'a person'
      return `${decided} by ${who}${ev.comment ? `: “${clip(ev.comment, 160)}”` : '.'}`
    }
    case 'preview':
    case 'deploy':
      return s.evidence.url ? s.evidence.url : clip(reason)
    case 'artifact/require':
    case 'adr/require':
    case 'scm/merged':
      if (s.evidence.adrs?.length) return `Satisfied by ${s.evidence.adrs.map((a) => `ADR-${String(a.number).padStart(3, '0')} ${a.title}`).join(', ')}.`
      if (s.evidence.artifactId != null) return `Satisfied by the ${s.evidence.kind ?? 'recorded'} record “${s.evidence.name || s.evidence.kind}”.`
      return clip(reason)
    case 'workflow/call':
      return s.evidence.childRunId ? `${s.evidence.workflow} v${s.evidence.workflowVersion ?? '?'}, ${s.evidence.childRunNumber ? `run ${s.evidence.childRunNumber}` : 'a called run'}${reason ? `: ${clip(reason, 120)}` : ''}` : clip(reason)
    default:
      return clip(reason)
  }
}

function by(s: Step): string {
  switch (s.type) {
    case 'agent/pi':
    case 'agent/review':
      return join(s.evidence.model && `${s.evidence.model}`, secs(s.evidence.durationMs))
    case 'approval': {
      const names = (s.evidence.decisions ?? []).map((d) => voterWords(d.approver, d.via))
      if (s.status === 'pending') return `needed from ${approversOf(s)}`
      if (s.evidence.evalAuthorised) return 'eval-authorised'
      return names.length ? [...new Set(names)].join(', ') : s.evidence.approver ?? ''
    }
    case 'scan':
      return join(s.evidence.binding ?? s.name, where(s), secs(s.evidence.durationMs))
    case 'deploy':
      return join((s.evidence as { deployment?: string }).deployment)
    default:
      return join(where(s), secs(s.evidence?.durationMs))
  }
}

export function receiptOf(s: Step, live = true): Receipt {
  const r = result(s, live)
  const title = TITLE[s.type] ?? s.type
  const name = s.name && s.name.toLowerCase() !== title.toLowerCase() ? s.name : undefined
  return {
    key: `step-${s.id}`, ...r, title, name, sentence: sentence(s, live), by: by(s),
    when: fmtStamp(s.finishedAt || s.startedAt), stepId: s.id ?? undefined,
  }
}

// The commit the checks judged, when they all name the same one.
function judged(steps: Step[]): string {
  const c = new Set(steps.map((s) => s.evidence?.candidate).filter(Boolean))
  return c.size === 1 ? [...c][0]!.slice(0, 7) : ''
}

export type RunEvidence = { run: Run; role?: string; steps: Step[] }

export function Evidence({ view, runs, workTab, extraErr }: {
  view: ChangeView
  // runs: the latest run first, then the work it lands, when read.
  runs: RunEvidence[]
  workTab: (stepId?: string | null, runId?: string | null) => string
  extraErr?: string
}) {
  const proof = view.evidence
  const latest = view.detail
  return (
    <section className="ch-evidence" aria-label="Recorded evidence">
      <div className="ch-section-head">
        <h2>Evidence</h2>
        <span className="muted">{latest ? `Verification in the latest run: ${verificationSummary(latest.steps)}` : 'No run recorded yet.'}</span>
      </div>
      {proof?.inAdvance && <p className="small" data-testid="in-advance">
        <strong>Accepted in advance</strong> by {proof.inAdvance.approver}, {fmtStamp(proof.inAdvance.at)}: {proof.inAdvance.shown.join(' ')}
      </p>}
      {!!proof?.targets.length && <details className="ch-targets">
        <summary>{proof.targets.length === 1 ? 'The requirement this change answers to' : `${proof.targets.length} requirements this change answers to`}</summary>
        {proof.targets.map(t => <div key={`${t.block}:${t.check}`} style={{ marginTop: 8 }}>
          <strong>{t.title || t.expects || t.check}</strong>
          <div className="muted small"><code>{t.block}{t.version ? `@${t.version}` : ''} · {t.check}</code></div>
          {t.title && t.expects && <p className="small">{t.expects}</p>}
        </div>)}
      </details>}
      <div className="ch-ledger">
        {runs.map(({ run, role, steps }) => {
          const commit = judged(steps)
          const live = run.status === 'running'
          const receipts = steps.map((s) => receiptOf(s, live))
          // A finished run whose every receipt is a pass reads as one line
          // until asked; anything else shows every receipt.
          const clean = !live && receipts.length > 0 && receipts.every((r) => r.cls === 'pass' && r.result !== 'passed with notes')
          return (
            <div key={run.id} className="ch-ledger-group">
              <div className="ch-ledger-head">
                <strong>{role === 'landing' ? 'Landing' : role === 'working' || role === 'record' ? 'Work' : 'Run'}</strong>
                <span>{run.workflowName} v{run.workflowVersion} · run {run.number}</span>
                {run.status !== 'running' && <span className={`chip ${run.status === 'done' ? 'pass' : ''}`}>{run.status === 'done' ? 'finished' : run.status}</span>}
                {commit && <span>· checked commit <code>{commit}</code></span>}
              </div>
              {steps.length === 0 && <div className="ch-line"><span className="muted">No steps recorded.</span></div>}
              {clean
                ? <CleanRun receipts={receipts} to={(id) => workTab(id, run.id)} />
                : receipts.map((r) => <Line key={r.key} r={r} to={workTab(r.stepId, run.id)} />)}
            </div>
          )
        })}
        {extraErr && <div className="ch-line"><span className="chip stale">not read</span><span className="ch-line-what">The work this landing carries could not be read: {extraErr}</span></div>}
        {proof && (
          <div className="ch-ledger-group">
            <div className="ch-ledger-head"><strong>Delivery</strong></div>
            {(proof.merged || proof.releases.length > 0) && <div className="ch-line">
              <span className={`chip ${proof.merged ? 'pass' : ''}`}>{proof.merged ? 'merged' : 'not merged'}</span>
              <span className="ch-line-what"><span className="ch-line-title">Merge</span>
                <span className="ch-line-sentence">{proof.merged ? 'The merge into main is recorded.' : 'No merge is recorded for this change.'}</span></span>
              <span className="ch-line-by" /><span className="ch-line-when" /><span />
            </div>}
            {proof.releases.map(r => (
              <div className="ch-line" key={r.number}>
                <span className={`chip ${r.state}`}>{r.state}</span>
                <span className="ch-line-what"><span className="ch-line-title">Release #{r.number}</span>
                  <span className="ch-line-sentence">to {r.deployment}{r.serving ? '' : r.observed ? ` · ${observedWords(r.observed)}` : ''}{!r.serving && r.reason ? ` (${r.reason})` : ''}</span></span>
                <span className="ch-line-by">{r.serving && <span className="chip pass">running now</span>}</span>
                <span className="ch-line-when" />
                <Link className="ch-line-link" to={`/projects/${view.projectSlug}/releases/${r.number}`}>record</Link>
              </div>
            ))}
            {proof.releases.length === 0 && (
              <div className="ch-line">
                <span className="chip">none</span>
                <span className="ch-line-what"><span className="ch-line-sentence">{proof.merged ? 'No release is recorded for this change.' : 'Not merged and not released.'}</span></span>
                <span className="ch-line-by" /><span className="ch-line-when" /><span />
              </div>
            )}
          </div>
        )}
      </div>
    </section>
  )
}

// CleanRun: every receipt of a finished run passed, so it reads as one line
// naming what passed, with the receipts one click away.
function CleanRun({ receipts, to }: { receipts: Receipt[]; to: (stepId?: string) => string }) {
  const [open, setOpen] = useState(false)
  const kinds = [...new Set(receipts.map((r) => r.title))]
  return (
    <>
      <div className="ch-line">
        <span className="chip pass">all passed</span>
        <span className="ch-line-what">
          <span className="ch-line-title">{receipts.length} receipt{receipts.length === 1 ? '' : 's'}</span>
          <span className="ch-line-sentence">{kinds.join(', ')}.</span>
        </span>
        <span className="ch-line-by" />
        <span className="ch-line-when">{receipts[receipts.length - 1]?.when}</span>
        <button className="subtle ch-line-link" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? 'hide' : 'show'}</button>
      </div>
      {open && receipts.map((r) => <Line key={r.key} r={r} to={to(r.stepId)} />)}
    </>
  )
}

const observedWords = (o: string) => o === 'stopped' ? 'stopped' : o === 'missing' ? 'its container is missing' : `running: ${o}`

function Line({ r, to }: { r: Receipt; to: string }) {
  return (
    <div className="ch-line" id={r.key}>
      <span className={`chip ${r.cls}`}>{r.result}</span>
      <span className="ch-line-what">
        <span className="ch-line-title">{r.title}{r.name && <span className="ch-line-name"> · {r.name}</span>}</span>
        {r.sentence && <span className="ch-line-sentence">{r.sentence}</span>}
      </span>
      <span className="ch-line-by">{r.by}</span>
      <span className="ch-line-when">{r.when}</span>
      <Link className="ch-line-link" to={to}>record</Link>
    </div>
  )
}
