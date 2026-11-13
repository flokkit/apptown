import { Status } from '../../Status'
import { Section } from '../../Section'
import { PRODUCT } from '../../brand'
import { Link } from 'react-router-dom'
import type { ChangeView, Run, Step, TestsEvidence } from '../../api'
import { fmtStamp } from '../../format'
import { checkedWords } from '../../runtime'
import { approversOf, decisionStatus } from './next'
import { closingVia, closingVote, voterWords, voteWords } from './through'

// The evidence as receipts: one ruled line per recorded fact — what was
// checked, its result, who or what did it, when — each linking to its full
// record in the Work tab. These lines come from records; the summary at the
// top of the page is an AI's narrative, and the two never mix.

export type Receipt = {
  standing?: boolean
  key: string
  statusLabel: Step['statusLabel']
  result: string   // the stamp's words
  title: string
  kind?: string
  brief?: string
  name?: string    // the step's own name, when it says more than the title
  sentence: string
  by: string
  when: string
  stepId?: string
}

const TITLE: Record<string, string> = {
  'agent/pi': 'Implementation', 'agent/review': 'Review', tests: 'Tests', scan: 'Scan',
  'guidance/conformance': 'Standards check', approval: 'Approval', preview: 'Preview', build: 'Build',
  reconcile: 'Up to date with main', 'artifact/require': 'Required record', 'adr/require': 'Decision record',
  'scm/merged': 'Merged', 'policy/current': 'Rules still current', 'workflow/call': 'Called workflow', script: 'Script',
}

export function stepTitle(s: Step): string {
  return stepTitleOf(s.type, s.name, s.type === 'artifact/require' ? s.evidence.kind : undefined)
}

// stepTitleOf is a step's plain title from its type and name, for a step
// that has run (with its record's kind) or one only declared in a workflow.
export function stepTitleOf(type: string, name?: string, recordKind?: string): string {
  if (type === 'script' && name) return name.replace(/[-_]+/g, ' ').replace(/^./, c => c.toUpperCase())
  if (type === 'scan') {
    if (name?.includes('sbom')) return 'Software inventory'
    if (name?.includes('license')) return 'Licence scan'
    if (name?.includes('osv')) return 'Dependency scan'
    if (name?.includes('gitleaks')) return 'Secret scan'
    return 'Security scan'
  }
  if (type === 'artifact/require') return name === 'release-exists' || recordKind === 'release' ? 'Release created' : 'Required file recorded'
  return TITLE[type] || 'Check'
}

// testsNote is what a tests step's report says, in words. Only counts with
// no skips are a plain pass: skipped tests, and counts not known (no report,
// one that could not be read, a receipt from before reports), are notes a
// reader sees without opening anything.
export function testsNote(ev: TestsEvidence): { note: string; plain: boolean } {
  const c = ev.testCounts
  if (c) {
    if (c.passed === 0 && c.failed === 0 && c.skipped === 0) return { note: 'no tests', plain: true }
    const parts = [`${c.passed} passed`]
    if (c.failed) parts.push(`${c.failed} failed`)
    if (c.skipped) parts.push(`${c.skipped} skipped`)
    return { note: parts.join(', '), plain: c.skipped === 0 }
  }
  if (ev.testReportUnreadable) return { note: `test report unreadable: ${ev.testReportUnreadable}`, plain: false }
  return { note: 'Number of tests unknown', plain: false }
}

// unmetRules: the ways-of-working rules a conformance check found not met.
const unmetRules = (s: Step) => s.type === 'guidance/conformance' ? (s.checkReading?.checks ?? []).filter((c) => c.verdict === 'fail').map((c) => c.check) : []

function result(s: Step, live: boolean): { result: string } {
  if (s.overrideBy) return { result: 'overridden' }
  // A conformance check can pass while some rules are not met (they do not
  // block); it never reads as a plain pass beside "not met".
  if (s.status === 'pass' && unmetRules(s).length) return { result: 'passed with notes' }
  if (s.type === 'tests' && s.status === 'pass' && !testsNote(s.evidence).plain) return { result: 'passed with notes' }
  // Evidence the server could not read is unknown: never a plain pass.
  if (s.status === 'pass' && s.checkReading?.unreadable) return { result: 'evidence unreadable' }
  // A run that ended leaves its unfinished steps unfinished, not waiting.
  if (!live && s.status === 'pending') return { result: s.type === 'approval' ? 'never decided' : 'never ran' }
  if (!live && s.status === 'running') return { result: 'stopped' }
  switch (s.status) {
    case 'pass': return { result: s.type === 'approval' ? 'approved' : s.type === 'agent/pi' ? 'done' : 'passed' }
    case 'fail': return { result: s.type === 'approval' ? 'rejected' : 'failed' }
    case 'error': return { result: 'error' }
    case 'needs_human': return { result: 'needs a person' }
    case 'running': return { result: 'running' }
    case 'skipped': return { result: 'skipped' }
    case 'not_applicable': return { result: 'not applicable' }
    default: return s.type === 'approval' ? { result: 'waiting' } : { result: 'not run yet' }
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
  // Not a pass: what the step looked for that this app does not declare.
  if (s.status === 'not_applicable') return clip((s.type === 'scan' ? s.evidence.notApplicable : '') || reason)
  switch (s.type) {
    case 'agent/pi': {
      const files = (s.evidence.files ?? []).filter((f) => !f.startsWith('.apptown/'))
      if (s.status === 'running') return 'Working now.'
      if (s.status === 'pending') return 'Not started.'
      const changed = files.length ? `Changed ${plural(files.length, 'file')}: ${files.slice(0, 4).join(', ')}${files.length > 4 ? ', …' : ''}.` : 'Changed no files.'
      const needs = s.evidence.needs
      return join(failing && reason ? clip(reason) : changed, needs?.started?.length ? `Needs started: ${needs.started.map(n => `${n.kind} (${n.env.join(', ')})`).join('; ')}` : '', needs?.refused && `Needs refused: ${needs.refused}`)
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
      // Every check that applies to nothing here is named: never counted
      // among the rules, so never read as met.
      const notApplicable = checks.filter((c) => c.verdict === 'not-applicable').map((c) => c.check)
      const rules = checks.length - notApplicable.length
      const failed = checks.filter((c) => c.verdict === 'fail')
      // What main already fails, or what could not be compared with main,
      // is shown apart: it does not block this change.
      const unmet = failed.filter((c) => !c.preexisting && !c.undecided).map((c) => c.check)
      const onMain = failed.filter((c) => c.preexisting).length
      const undecided = failed.filter((c) => c.undecided).length
      const named = `${unmet.slice(0, 3).join(', ')}${unmet.length > 3 ? ', …' : ''}`
      return `${met} of ${plural(rules, 'rule')} met.${unmet.length ? s.status === 'pass' ? ` Not met, and not blocking: ${named}.` : ` Not met: ${named}.` : ''}`
        + (onMain ? ` ${onMain} already failing on main.` : '')
        + (undecided ? ` ${undecided} undecided (not known whether this change introduced ${undecided === 1 ? 'it' : 'them'}).` : '')
        + (notApplicable.length ? ` Not applicable here: ${notApplicable.join(', ')}.` : '')
    }
    case 'approval': {
      const ev = s.evidence
      if (ev.withdrawal) return `Acceptance withdrawn by ${ev.withdrawal.by}: “${ev.withdrawal.reason}”`
      if (s.status === 'pending') return ''
      if (ev.evalAuthorised) return `Eval-authorised, not a person's approval: ${ev.evalAuthorised}.`
      const decided = ev.decision === 'reject' ? 'Rejected' : 'Approved'
      const closing = closingVote(ev)
      if (closing?.standing) return `${decided} by ${voteWords(closing)}.`
      const who = ev.approver ? voterWords(ev.approver, closingVia(ev)) : 'a person'
      return `${decided} by ${who}${ev.comment ? `: “${clip(ev.comment, 160)}”` : '.'}`
    }
    case 'preview':
      return join(s.evidence.url || clip(reason), s.evidence.screenshots?.failedResources?.length ? `${plural(s.evidence.screenshots.failedResources.length + (s.evidence.screenshots.failedResourcesMore ?? 0), 'resource')} failed to load` : '')
    case 'build':
      return s.evidence.runtime === false ? 'Clean-tree build; no runtime declared' : s.evidence.bundle ? 'Runtime bundle built' : clip(reason)
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
      const names = (s.evidence.decisions ?? []).map(voteWords)
      if (s.status === 'pending') return `needed from ${approversOf(s)}`
      if (s.evidence.evalAuthorised) return 'eval-authorised'
      return names.length ? [...new Set(names)].join(', ') : s.evidence.approver ?? ''
    }
    case 'scan':
      return join(s.evidence.binding ?? s.name, where(s), secs(s.evidence.durationMs))
    case 'build':
      return ''
    default:
      return join(where(s), secs(s.evidence?.durationMs))
  }
}

function brief(s: Step): string | undefined {
  if (s.overrideBy) return `Exception approved by ${s.overrideBy}`
  if (s.type === 'approval') {
    if (s.evidence.withdrawal) return `Approval withdrawn by ${s.evidence.withdrawal.by}`
    if (s.evidence.evalAuthorised) return 'Comparison run; no personal approval'
    if (s.status !== 'pass' && s.status !== 'fail') return undefined
    const vote = closingVote(s.evidence)
    if (vote?.standing) return `${s.status === 'fail' || s.evidence.decision === 'reject' ? 'Rejected' : 'Approved'} by ${voteWords(vote)}`
    if (s.evidence.approver) return `${s.status === 'fail' || s.evidence.decision === 'reject' ? 'Rejected' : 'Approved'} by ${s.evidence.approver}`
  }
  if (s.type === 'tests' && (s.status === 'pass' || s.status === 'fail')) {
    return s.status === 'pass' && !s.evidence.command ? clip(s.evidence.reason || '') : testsNote(s.evidence).note
  }
  if (s.type === 'guidance/conformance' && unmetRules(s).length) return `${unmetRules(s).length} rules not met. Open Details to review them.`
  return undefined
}

export function receiptOf(s: Step, live = true, awaitingDecision = false): Receipt {
  const r = result(s, live)
  const title = stepTitle(s)
  const name = s.name && s.name.toLowerCase() !== title.toLowerCase() ? s.name : undefined
  return {
    standing: s.type === 'approval' && !!closingVote(s.evidence)?.standing,
    key: `step-${s.id}`, statusLabel: decisionStatus(s, live && awaitingDecision), ...r, title, name, kind: TITLE[s.type] || s.type, brief: brief(s), sentence: sentence(s, live), by: by(s),
    when: fmtStamp(s.finishedAt || s.startedAt), stepId: s.id ?? undefined,
  }
}

export type RunEvidence = { run: Run; role?: string; steps: Step[] }

export function EvidenceContext({ view }: { view: ChangeView }) {
  const proof = view.evidence
  return <Section title="Requirements and delivery">
    {proof?.inAdvance && <p><strong>Accepted in advance</strong> by {proof.inAdvance.approver}, {fmtStamp(proof.inAdvance.at)}: {proof.inAdvance.shown.join(' ')}</p>}
    {proof?.targets.map(t => <div key={`${t.block}:${t.check}`}><strong>{t.title || t.expects || t.check}</strong><p>{t.expects}</p><p className="muted small">{t.block}{t.version ? `@${t.version}` : ''} · {t.check}</p></div>)}
    <p>{proof?.merged ? 'The merge into main is recorded.' : 'No merge is recorded for this change.'}</p>
    {proof && <Status word={proof.mergeLabel} />}
  </Section>
}

export function Evidence({ view, runs, workTab, extraErr }: {
  view: ChangeView
  // runs: the latest run first, then the work it lands, when read.
  runs: RunEvidence[]
  workTab: (stepId?: string | null, runId?: string | null) => string
  extraErr?: string
}) {
  const proof = view.evidence
  return (
    <Section title="Evidence" label="Recorded evidence" note={view.checks.text}>
      {proof?.inAdvance && <p className="small" data-testid="in-advance">
        <strong>Accepted in advance</strong> by {proof.inAdvance.approver}, {fmtStamp(proof.inAdvance.at)}: {proof.inAdvance.shown.join(' ')}
      </p>}
      {!!proof?.targets.length && <section className="ch-targets">
        <h3>{proof.targets.length === 1 ? 'The requirement this change answers to' : `${proof.targets.length} requirements this change answers to`}</h3>
        {proof.targets.map(t => <div key={`${t.block}:${t.check}`} className="mt-2">
          <strong>{t.title || t.expects || t.check}</strong>
          <div className="muted small"><code>{t.block}{t.version ? `@${t.version}` : ''} · {t.check}</code></div>
          {t.title && t.expects && <p className="small">{t.expects}</p>}
        </div>)}
      </section>}
      <div className="ch-ledger">
        {runs.map(({ run, role, steps }) => {
          const live = run.status === 'running'
          const receipts = steps.map((s) => receiptOf(s, live, view.approval?.viewer === 'yes' && view.approval.stepId === s.id))
          const quietScans = new Set(steps.filter(s => s.type === 'scan' && s.status === 'pass' && !s.checkAttention).map(s => s.id))
          // A finished run whose every receipt is a pass reads as one line
          // until asked; anything else shows every receipt.
          const clean = !live && receipts.length > 0 && !steps.some(s => s.checkAttention) && receipts.every((r) => r.statusLabel === 'passed' && r.result !== 'passed with notes')
          return (
            <div key={run.id} className="ch-ledger-group">
              <div className="ch-ledger-head">
                <strong>{role === 'landing' ? 'Landing' : role === 'working' || role === 'record' ? 'Work' : 'Run'}</strong>
                {run.status !== 'running' && <Status word={run.statusLabel} />}
              </div>
              {steps.length === 0 && <div className="ch-line"><span className="muted">No steps recorded.</span></div>}
              {clean
                ? <>
                  {/* An approval is a person's decision, not a check: it keeps its own line. */}
                  <CleanRun noun="steps" receipts={receipts.filter((_, i) => steps[i].type !== 'approval')} to={(id) => workTab(id, run.id)} />
                  {receipts.filter((_, i) => steps[i].type === 'approval').map(r => <Line key={r.key} r={r} to={workTab(r.stepId, run.id)} />)}
                </>
                : <>
                  {receipts.filter(r => !quietScans.has(r.stepId ?? '')).map(r => <Line key={r.key} r={r} to={workTab(r.stepId, run.id)} />)}
                  {quietScans.size > 0 && <CleanRun noun="checks" receipts={receipts.filter(r => quietScans.has(r.stepId ?? ''))} to={id => workTab(id, run.id)} />}
                </>}
            </div>
          )
        })}
        {extraErr && <div className="ch-line"><Status word="unknown" /><span className="ch-line-what">The work this landing carries could not be read: {extraErr}</span></div>}
        {proof && (proof.merged || proof.releases.length > 0 || view.change.state === 'shipped' || view.change.state === 'abandoned') && (
          <div className="ch-ledger-group">
            <div className="ch-ledger-head"><strong>Delivery</strong></div>
            {(proof.merged || proof.releases.length > 0) && <div className="ch-line">
              <Status word={proof.mergeLabel} />
              <span className="ch-line-what"><span className="ch-line-title">Merge</span>
                <span className="ch-line-sentence">{proof.merged ? 'The merge into main is recorded.' : 'No merge is recorded for this change.'}</span></span>
              <span className="ch-line-by" /><span className="ch-line-when" /><span />
            </div>}
            {proof.releases.map(r => (
              <div className="ch-line" key={r.number}>
                <Status word={r.statusLabel} />
                <span className="ch-line-what"><span className="ch-line-title">Release #{r.number}</span>
                  <span className="ch-line-sentence">to {r.deployment}{r.serving ? '' : r.observed ? ` · ${observedWords(r.observed)}` : ''}{!r.serving && r.reason ? ` (${r.reason})` : ''}</span></span>
                <span className="ch-line-by">{r.serving && <Status word="running" />}</span>
                <span className="ch-line-when">{r.observed && checkedWords(r)}</span>
                <Link className="ch-line-link" to={`/projects/${view.projectSlug}/releases/${r.number}`}>Details</Link>
              </div>
            ))}
            {/* Merged with no release is the merge row alone; nothing at all
                delivered is said, never left blank. */}
            {!proof.merged && proof.releases.length === 0 && (
              <div className="ch-line">
                <span className="badge">none</span>
                <span className="ch-line-what"><span className="ch-line-sentence">Not merged and not released.</span></span>
                <span className="ch-line-by" /><span className="ch-line-when" /><span />
              </div>
            )}
          </div>
        )}
      </div>
    </Section>
  )
}

// CleanRun: every receipt of a finished run passed, so it reads as one line
// naming what passed, with the receipts one click away.
function CleanRun({ receipts, to, noun = 'checks' }: { noun?: string; receipts: Receipt[]; to: (stepId?: string) => string }) {
  if (receipts.length === 0) return null
  // Which checks they were is in Details: the usual answer is just that they passed.
  return (
    <>
      <div className="ch-line">
        <Status word="passed" />
        <span className="ch-line-what">
          <span className="ch-line-title">All {noun} passed</span>
        </span>
        <span className="ch-line-by" />
        <span className="ch-line-when">{receipts[receipts.length - 1]?.when}</span>
        <Link className="ch-line-link" to={to()}>Full record</Link>
      </div>

    </>
  )
}

const observedWords = (o: string) => o === 'stopped' ? 'stopped' : o === 'missing' ? 'its container is missing' : `running: ${o}`

function Line({ r, to }: { r: Receipt; to: string }) {
  return (
    <section className="ch-receipt" id={r.key}>
      <h3 className="ch-line">
        <span className="receipt-state"><Status word={r.statusLabel} /></span>
        <span className="ch-line-what">
          <span className="ch-line-title">{r.title}</span>
          {r.brief && <span className="ch-line-sentence">{r.standing ? <Link to="/janitor">{r.brief}</Link> : r.brief}</span>}
        </span>
        <span className="ch-line-by" />
        <span className="ch-line-when">{r.when}</span>
        <Link to={to}>Full record</Link>
      </h3>

      <div className="ch-receipt-body small"><p>{r.result}</p>{r.result === 'evidence unreadable' && <p>{r.sentence}</p>}
      </div>
    </section>
  )
}

// checkPurpose says in one sentence what a check is for. A workflow step may
// declare it (`description:`); built-in check types have AppTown's own words;
// a script with no description says only that it is the workflow's own check.
export function checkPurpose(s: Step): string | undefined {
  const declared = (s.config as { description?: unknown } | null)?.description
  if (typeof declared === 'string' && declared.trim()) return declared.trim()
  const title = stepTitle(s)
  switch (s.type) {
    case 'tests': return 'Runs the tests each part of the app declares, in that part’s folder.'
    case 'scan':
      if (title === 'Secret scan') return 'Looks for passwords, keys and tokens written into the code.'
      if (title === 'Dependency scan') return 'Checks the libraries the app uses against the public list of known security problems.'
      if (title === 'Licence scan') return 'Checks that the licences of the software the app uses are allowed.'
      return 'Lists the software the app is built from and looks for known security problems in it.'
    case 'guidance/conformance': return 'Checks the change against the project’s written rules.'
    case 'agent/review': return 'A second helper reads the change and judges whether it does what was asked.'
    case 'preview': return 'Starts the app the way it will run, so it can be tried.'
    case 'scm/merged': return 'Confirms the change merged into the main version.'
    case 'script': return undefined
  }
  return undefined
}

// checkMethod says how the check ran, from what it recorded.
export function checkMethod(s: Step): string | undefined {
  const cmd = (s.evidence as { command?: unknown }).command
  if (typeof cmd === 'string' && cmd) return `Ran ${cmd}`
  return undefined
}
