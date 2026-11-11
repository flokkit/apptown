import { FeatureOnly } from '../features'
import './DataProtection.css'
import { TechnicalWords, useTechnicalDetail } from './change/TechnicalDetail'
import { DeploymentLine } from './project/settings/DeploymentLine'
import { Section } from '../Section'
import { BackgroundReading } from '../BackgroundReading'
import { ListTable } from '../ListTable'
import { Status } from '../Status'
import { PRODUCT } from '../brand'
import { useEffect, useState, type ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtBytes, fmtWhen, readingAge } from '../format'
import type { BackupStoreState, Coverage, DeploymentProtection, ItemProtection, Operation, OperationAdmitted, OperationView, RehearsalRef, RestoredCopy } from '../wire.gen'
import { useNow } from '../hooks'
import { useHolds } from '../instancePages'
import { setProjectScope } from '../projectScope'

// Protecting deployment data off-host (design/subsystems/deployment.md):
// for each data-holding item of a cloud deployment, how
// recent AppTown's own copies are, the provider's own backups, and when
// a copy was last seen to come back; the long operations that copy, check
// and take down, as they run; and the backup store they keep copies in.
// Unknown is said, never shown as none.

// How an operation stands, in words. Success and verification are separate
// facts: a copy can be done and not yet shown to come back.
export const operationWords: Record<Operation['state'], string> = {
  queued: 'waiting to start',
  running: 'running',
  succeeded: 'done',
  failed: 'failed',
  unknown: `unknown: ${PRODUCT} stopped while it ran`,
  cancelled: 'cancelled',
}

export const live = (op: Operation) => op.state === 'queued' || op.state === 'running'

// stateWords is how one operation stands: a rehearsal or restored copy that
// ended verified passed, which says the app answered on the copy, not only
// that it came back.
export function stateWords(op: Operation): string {
  if (op.state !== 'succeeded') return operationWords[op.state]
  if (op.verification === 'verified') return op.kind === 'deployment.rehearsal' || op.kind === 'deployment.restored-copy' ? 'passed: the app answered on the copy' : 'done, and the copy came back'
  if (op.verification === 'not-verified') return 'done; nothing has checked the copy comes back'
  return 'done'
}

// statusDetail is what an operation's status adds to its word. Waiting,
// running and a plain done say nothing more (the page says which step), so
// their chip opens nothing.
export function statusDetail(op: Operation): string {
  const words = stateWords(op)
  return live(op) || words === 'done' ? '' : words
}

// writesStopped says a live take-down has deleted the app: nothing writes.
export const writesStopped = (op: Operation) => live(op) && op.result.writesStopped === true

// deploymentOf is the deployment an operation was asked on.
export const deploymentOf = (op: Operation) => (typeof op.input.deployment === 'string' ? op.input.deployment : op.subject ?? '')

const kindWords: Record<string, string> = {
  'deployment.copy': 'Copy', 'deployment.drill': 'Check', 'deployment.take-down': 'Take-down', 'deployment.rehearsal': 'Restore rehearsal',
  'deployment.restored-copy': 'Restored copy', 'deployment.restored-copy-end': 'Restored copy take-down', 'deployment.restore': 'Restore',
}

// elapsed is how long since a time, in words a person reads at a glance.
export function elapsed(from: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(from).getTime()) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

// currentStep is the running operation's step in plain words, with how long
// it has run; between steps it says so.
export function currentStep(op: Operation, now = Date.now()): string {
  if (op.state === 'queued') return `${kindWords[op.kind] ?? op.kind} waiting to start (asked ${elapsed(op.createdAt, now)} ago)`
  const cur = op.steps.length > 0 && !op.steps[op.steps.length - 1].ended ? op.steps[op.steps.length - 1] : null
  const head = `${kindWords[op.kind] ?? op.kind} running for ${elapsed(op.startedAt ?? op.createdAt, now)}`
  return cur ? `${head}: ${cur.name}` : `${head}, between steps`
}

// copyLine is one item's copy state in words: fresh, stale, unknown and none
// each read differently, with the newest copy's time when there is one.
// itemLabel names a protected item; two volumes of one need are told apart
// by the volume each row is for.
export function itemLabel(it: ItemProtection, all: ItemProtection[]): string {
  return it.volume && all.filter((o) => o.need === it.need).length > 1 ? `${it.need} · volume ${it.volume}` : it.need
}

export function copyLine(it: ItemProtection): string {
  const last = it.lastCopy ? `newest copy ${fmtWhen(it.lastCopy.takenAt)}, ${fmtBytes(it.lastCopy.bytes)}` : ''
  switch (it.copy) {
    case 'fresh': return `${PRODUCT} copies: ${last}`
    case 'stale': return `${PRODUCT} copies: ${[it.copyNote, last].filter(Boolean).join('; ')}`
    case 'unknown': return `${PRODUCT} copies: ${[it.copyNote, last].filter(Boolean).join('; ')}`
    case 'disposable': return 'disposable: starts empty'
    default: return `no copy${it.copyNote ? ` · ${it.copyNote}` : ''}`
  }
}

// nativeLine is what the provider says of its own backups, or why it could
// not be asked; nothing when the provider declares none.
export function nativeLine(it: ItemProtection): string {
  const n = it.native
  if (!n) return ''
  const who = n.mechanism === 'do-database-backups' ? 'DigitalOcean backups' : n.mechanism
  if (n.unknown) return `${who}: unknown · ${n.unknown}`
  const values = n.count === 0 ? `${who}: none yet` : `${who}: oldest ${fmtWhen(n.oldest ?? '')}, newest ${fmtWhen(n.newest ?? '')}`
  const age = n.readAt ? ` · ${readingAge(n.readAt, n.stale)}` : ''
  return `${values}${age}${n.checkFailed ? ` · Provider backup check failed: ${n.failure}` : ''}`
}

// drillLine is when a copy last came back, or that it never has. A backup
// set has no number people read, so these lines do not name it by its id.
// Where it came back is the place it was filled, not the deployment's
// target; a bucket's new store is filled as no app user.
export function drillLine(it: ItemProtection): string {
  const d = it.drill
  if (!d) return 'never checked to come back'
  if (!d.passed) return `did not come back on ${fmtWhen(d.at)} from a backup set: ${d.detail ?? ''}`
  return `came back on ${fmtWhen(d.at)} from a backup set into ${d.place}${d.user ? ` as ${d.user}` : ''}`
}

// rehearsalLine is the last restore rehearsal: whether the app answered on
// a copy, from which set and release, where; or why there was none.
export function rehearsalLine(r: RehearsalRef | null | undefined, note?: string): string {
  if (!r) return `Backup never tested${note && !note.startsWith('never rehearsed') ? `: ${note}` : ''}`
  const from = [r.backup ? 'from a backup set' : '', r.release ? `release ${r.release}` : '', r.host ? `on ${r.host}${r.facts?.sharedHost ? ' (shared, not set aside for rehearsals)' : ''}` : ''].filter(Boolean).join(', ')
  if (r.outcome.startsWith('unknown')) return `last restore rehearsal ${fmtWhen(r.at)}: ${r.outcome}${from ? ` (${from})` : ''}`
  if (!r.passed) return `last restore rehearsal failed ${fmtWhen(r.at)} ${from}${r.failedStep ? `, while ${r.failedStep}` : ''}`
  return `last restore rehearsal ${r.outcome} ${fmtWhen(r.at)} ${from}`
}

// heldBack is what a rehearsal ran without, said every time: the outbound
// network is not blocked.
export function heldBack(r: RehearsalRef): string {
  return [r.facts.egress, r.facts.withheld?.length ? `withheld: ${r.facts.withheld.join(', ')}` : '',
    r.facts.notStarted?.length ? `not started: ${r.facts.notStarted.join(', ')}` : ''].filter(Boolean).join('; ')
}

// restoreTestLine is the last restore test on the deployment's own
// platform, said apart from the rehearsal: a rehearsal never stands in for it.
export function restoreTestLine(c: RestoredCopy | null | undefined, note?: string): string {
  if (!c) return `last restore test: ${note || 'never tested on its platform'}`
  const from = ['from a backup set', c.release ? `release ${c.release}` : '', c.target ? `on ${c.target}` : ''].filter(Boolean).join(', ')
  const at = fmtWhen(c.checkedAt || c.startedAt)
  if (!c.outcome) return `last restore test ${at}: ${c.endDetail || 'ended before it was checked'} (${from})`
  const gate = gateLine(c)
  if (c.outcome === 'failed') return `last restore test failed ${at} ${from}${c.failedStep ? `, while ${c.failedStep}` : ''}${gate ? ` · ${gate}` : ''}`
  return `last restore test ${c.outcome} ${at} ${from}${gate ? ` · ${gate}` : ''}`
}

// gateLine is AppTown's own check of a copy's gate: how many checks
// passed, and each that failed with what it answered.
export function gateLine(c: RestoredCopy): string {
  const checks = c.facts.gate ?? []
  if (!checks.length) return ''
  const failed = checks.filter((k) => !k.passed)
  const head = `gate: ${checks.length - failed.length} of ${checks.length} checks passed`
  return failed.length ? `${head}; failed: ${failed.map((k) => `${k.name} (${k.path} ${k.status})`).join(', ')}` : head
}

// copyHead is a standing restored copy in one line: what it is a copy of,
// and how far its restore test has got.
export function copyHead(c: RestoredCopy, source: string): string {
  return `restored copy of ${source} from a backup set · ${c.checkedAt ? 'checked, to be taken down' : 'being tested'}`
}

// CoverageLines is what the answer speaks for and what it does not, beside
// the evidence: a fresh copy or a passed rehearsal covers the items named,
// never data the app keeps outside its deployment, so no card reads as a
// whole application's protection.
export function CoverageLines({ c, unknown }: { c: Coverage; unknown?: boolean }) {
  const [technical] = useTechnicalDetail()
  return (
    <div className="data-protection-coverage-lines-div">
      <TechnicalWords shown={technical}>covers: {c.assessed.length ? [...new Set(c.assessed.map(n => /postgres|maria|database|volume/.test(n) ? 'database' : n))].join(', ') : unknown ? 'nothing assessed' : `no data ${PRODUCT} knows of`}</TechnicalWords>
      {c.notAssessed.length > 0 && <><div className="muted">Not covered: anything the app stores outside its declared needs</div><TechnicalWords shown={technical}>{c.notAssessed.map(n => <div key={n}><Status observation word={c.statusLabel} /> {n}</div>)}</TechnicalWords></>}
    </div>
  )
}

const rehearseTitle = "boot the release it serves on its newest backup, on the host kept for rehearsals or, with none kept, on the app's own host beside it; ask the app its readiness, front page and data checks, then take it all down"

// ProtectionLines is a cloud deployment's protection on its card: each
// item, the running operation, and the doors to copy and check now.
function EnabledProtectionLines({ slug, name }: { slug: string; name: string }) {
  const [technical] = useTechnicalDetail()
  const q = useGet<DeploymentProtection>(`/projects/${slug}/deployments/${name}/protection`, { poll: 5000 })
  const { run, busy, err } = useAction()
  useNow(1000, !!q.data?.running)
  // The way to the target's settings is offered only to those who can edit
  // them; AppTown refuses the edit to anyone else.
  const canEditTargets = useHolds('platform:manage-deploy-targets')
  const line = (children: ReactNode) => <DeploymentLine label="Backups" trailer={<Link to="/docs/guides/deployment-backups">how backups work →</Link>}>{children}</DeploymentLine>
  if (q.denied) return null
  if (!q.data) return line(<ReadStatus q={q} what="how its data is protected" />)
  const p = q.data
  // A failed refresh keeps the last answer, said as such: no state is
  // claimed as it is now.
  const old = q.refreshFailed || p.reading?.unknown
  const asRead = old ? ` as read ${elapsed(new Date(q.answeredAt).toISOString())} ago` : ''
  const start = (what: 'copies' | 'drills' | 'rehearsals') => run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${name}/${what}`, {}))
  const last = p.recent.find((o) => !live(o))
  // Whether each door can start is AppTown's answer, from that door's
  // own check: shown greyed with its sentence when it cannot.
  // A door that does not apply on the deployment's platform (a check or a
  // restore test of a Docker host's deployment, a copy it takes as its
  // backups) is not shown at all; one that applies but cannot start now is
  // greyed with its reason.
  const applies = (o: { reason?: string }) => o.reason !== 'not-on-this-platform' && o.reason !== 'backups-page'
  const ops = p.operations
  // Each greyed button's reason, said once under the buttons: several share
  // one (no copy runner), each from its own door's answer.
  const shown = (ops ? [ops.copy, ops.check, ...(p.rehearsalNote !== 'never rehearsed: no rehearsal place' ? [ops.rehearsal] : []), ...(p.dataClass === 'production' ? [ops.restore] : [])] : []).filter(applies)
  const missingBackup = p.items.some(it => it.copy === 'none')
  const whyNot = Array.from(new Set(shown.filter((o) => !o.available && o.why && (!missingBackup || o.reason !== 'no-copy')).map((o) => o.why!)))
  const runnerMissing = shown.some((o) => o.reason === 'no-copy-runner')
  if (p.unknown && !p.reading?.waiting) return line(<div className="small"><Status word={"unknown"} help={p.unknown} /> couldn't read what this deployment holds. {p.reading?.failure && <span className="muted">{p.reading.failure}</span>}<CoverageLines c={p.coverage} unknown /></div>)
  // A deployment that holds no data AppTown knows of says only what is
  // not assessed; one whose take-down would still act on data, or with an
  // operation or copy standing, shows the whole card.
  if (p.items.length === 0 && !p.takeDown && !p.running && !p.restoredCopies.some((c) => !c.endedAt)) return line(<div className="small"><StaleNote q={q} />{p.reading?.failure && <span className="muted">{p.reading.failure}</span>}<CoverageLines c={p.coverage} /></div>)
  // A Docker host's backups on AppTown's own disk are a choice, said
  // plainly; App Platform data with no copy at all is a warning.
  const onHost = ops?.copy.reason === 'backups-page'
  return line(
    <div>
      <StaleNote q={q} />{p.reading?.failure && <span className="muted">{p.reading.failure}</span>}
      {p.startFrom && !p.startFrom.filled && <div className="muted">starts from a backup set: its first release fills its data before the app starts</div>}
      {p.items.map((it) => (
        <div key={`${it.need}:${it.volume ?? ''}`}>
          {it.copy === 'none' ? <div id={`backup-${name}-${it.need}-${it.volume || 'data'}`} className={old ? 'muted' : 'error'}>✗ No backup yet: {itemLabel(it, p.items)}{/postgres|maria/.test(it.need) ? ' database' : ''}{asRead}</div>
            : <div><Status observation word={old ? 'unknown' : it.copyLabel} help={old ? `Last answer shown${asRead}.` : undefined} /> {itemLabel(it, p.items)}: {copyLine(it)}</div>}
          <TechnicalWords shown={technical}>{it.where}</TechnicalWords>
          {it.copy === 'none' && it.copyNote && !onHost && <div className="muted">{it.copyNote}</div>}
          {nativeLine(it) && <div className="muted">{nativeLine(it)}</div>}
          {(!ops || applies(ops.check)) && <div className={!old && it.drill && !it.drill.passed ? 'error' : 'muted'}>{drillLine(it)}</div>}
        </div>
      ))}
      {onHost && p.keptOn === 'host' ? <div className="muted">Kept on this host only; it does not outlive its disk</div>
        : onHost && p.keptOn === 'store' ? <div className="muted">Backups kept in the backup store</div>
        : p.note && <div className={!old && p.dataClass === 'production' && !p.offHost ? 'error' : 'muted'}>{p.note}</div>}
      <CoverageLines c={p.coverage} />
      {(p.rehearsal || !ops || ops.rehearsal.available || p.rehearsalNote === 'never rehearsed: no rehearsal place') && <div className={!old && p.rehearsal && !p.rehearsal.passed ? 'error' : 'muted'}>{rehearsalLine(p.rehearsal, p.rehearsalNote)}{p.rehearsal && asRead}</div>}
      {p.rehearsal && <div className="muted">{heldBack(p.rehearsal)}</div>}
      {p.rehearsal?.previous && <div className="muted">before that: {rehearsalLine(p.rehearsal.previous).replace('last restore rehearsal ', '')}</div>}
      {(p.restoreTest || !ops || applies(ops.restore)) && <div className={!old && p.restoreTest && !(p.restoreTest.outcome ?? '').startsWith('passed') ? 'error' : 'muted'}>{restoreTestLine(p.restoreTest, p.restoreTestNote)}{p.restoreTest && asRead}</div>}
      {p.restoredCopies.filter((c) => !c.endedAt).map((c) => <RestoredCopyLines key={c.id} slug={slug} source={name} copy={c} asRead={asRead} />)}
      {p.running && (
        <div className="mt-1">
          <Status word={old ? 'unknown' : p.running.statusLabel} help={old ? stateWords(p.running) + asRead : statusDetail(p.running)} /> <Link to={`/operations/${p.running.id}`}>{currentStep(p.running)}</Link>
          {writesStopped(p.running) && <span className="muted data-protection-protection-lines-span">writes stopped{asRead}</span>}
        </div>
      )}
      {!p.running && last && (
        <div className="muted mt-1" >
          last: <Link to={`/operations/${last.id}`}>{kindWords[last.kind] ?? last.kind} {stateWords(last)}</Link>{last.finishedAt ? ` ${fmtWhen(last.finishedAt)}` : ''}
        </div>
      )}
      {ops && !p.running && (
        <div className="row mt-1 data-protection-protection-lines-div">
          {applies(ops.copy) && <button className="subtle" disabled={busy || !ops.copy.available} onClick={() => start('copies')} title={ops.copy.why || 'copy its data to the backup store now'}>Copy now</button>}
          {applies(ops.check) && <button className="subtle" disabled={busy || !ops.check.available} onClick={() => start('drills')} title={ops.check.why || 'fill the newest copy into a new database, check it, and remove it'}>Check it comes back</button>}
          {(p.rehearsalNote !== 'never rehearsed: no rehearsal place' || missingBackup) && (
            <FeatureOnly area="restore-rehearsals"><button className="subtle" disabled={busy || !ops.rehearsal.available} aria-describedby={missingBackup ? p.items.filter(it => it.copy === 'none').map(it => `backup-${name}-${it.need}-${it.volume || 'data'}`).join(' ') : undefined} onClick={() => start('rehearsals')} title={ops.rehearsal.why || rehearseTitle}>Test a restore</button></FeatureOnly>
          )}
          {p.dataClass === 'production' && applies(ops.restore) && (
            <FeatureOnly area="restore-rehearsals"><button className="subtle" disabled={busy || !ops.restore.available} onClick={() => run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${name}/restored-copies`, {}))} title={ops.restore.why || restoreTestTitle}>Test a restore here</button></FeatureOnly>
          )}
          {err && <span className="error small">{err}</span>}
        </div>
      )}
      {!p.running && whyNot.length > 0 && (
        <div className="muted mt-1" role="note" >
          {whyNot.map((w) => <div key={w}>{w}</div>)}
          {runnerMissing && canEditTargets && <Link to={`/targets/${p.target}`}>Open {p.target}'s settings</Link>}
        </div>
      )}
      {p.takeDown && !ops?.finalCopy.available && <div className="error mt-1" >{p.takeDown}</div>}
    </div>
  )
}

const restoreTestTitle = `start its release from its newest copy on its own platform, behind ${PRODUCT}'s gate, check it through the gate, record the answer and take it down: about 10 to 20 minutes and a few cents on DigitalOcean`

// RestoredCopyLines is one standing restored copy on its source's card:
// what it is, what it can still do outside and who else can read it, and
// the door to take it down.
function RestoredCopyLines({ slug, source, copy: c, asRead }: { slug: string; source: string; copy: RestoredCopy; asRead: string }) {
  const { run, busy, err } = useAction()
  return (
    <div className="mt-1 data-protection-restored-copy-lines-div">
      <div><Status word="working" help={`Testing a restored copy${asRead}. Open the operation to follow its checks.`} /> {copyHead(c, source)}</div>
      {c.outcome && <div className={c.outcome === 'failed' ? 'error' : 'muted'}>checked through the gate: {c.outcome}{c.facts.checks.length ? ` (${c.facts.checks.map((k) => `${k.path} ${k.status}`).join(', ')})` : ''}{gateLine(c) ? ` · ${gateLine(c)}` : ''}</div>}
      {c.protection && <div className="muted">protected by {c.protection}</div>}
      <div className="muted">{c.warning}</div>
      <div className="muted">{c.boundary}</div>
      {c.endDetail && <div className="error">{c.endDetail}</div>}
      <div className="row mt-1 data-protection-protection-lines-div">
        {c.checkedAt && <FeatureOnly area="restore-rehearsals"><button className="subtle" disabled={busy} onClick={() => run(() => del(`/projects/${slug}/restored-copies/${c.id}`))} title="take it down now; no final copy is taken">Take down</button></FeatureOnly>}
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

// OperationPage is one operation: what it is, who asked, each step as it
// happened, and how it ended.
export function OperationPage() {
  const { id = '' } = useParams()
  const q = useGet<OperationView>(`/operations/${id}`, { poll: 3000 })
  const { run, busy, err } = useAction()
  const [technical] = useTechnicalDetail()
  useNow(1000, q.data ? live(q.data) : false)
  // The operation's project is the page's scope, so the menu and the back
  // link lead to it; a project the reader cannot see sets none.
  const scope = q.data ? q.data.project?.slug ?? null : undefined
  useEffect(() => { if (scope !== undefined) setProjectScope(scope) }, [scope])
  if (!q.data) return <ReadStatus q={q} what="the operation" />
  const op = q.data
  const old = q.refreshFailed
  const asRead = old ? ` as read ${elapsed(new Date(q.answeredAt).toISOString())} ago` : ''
  return (
    <div className="data-protection-operation-page-div">
      <StaleNote q={q} />
      <div className="muted small">operation {op.number}</div>
      <h1 className="mb-1">{kindWords[op.kind] ?? op.kind} of {op.project && typeof op.input.deployment === 'string'
        ? <Link to={`/projects/${op.project.slug}/deployments/${encodeURIComponent(op.input.deployment)}`}>{op.input.deployment}</Link>
        : deploymentOf(op)}</h1>
      {op.project && <div className="mb-1">in <Link to={`/projects/${op.project.slug}`}>{op.project.name}</Link></div>}
      {/* The subject is the hold path in ids, for an administrator matching
          holds: diagnosis only, so it is behind the technical switch. */}
      {technical && op.subject && <code className="tech-line muted mb-1">{op.subject}</code>}
      <div className="row status-strip data-protection-operation-page-status-strip">
        <Status word={old ? 'unknown' : op.statusLabel} help={old ? stateWords(op) + asRead : statusDetail(op)} />
        <span className="muted small">asked by {op.askedBy} {fmtWhen(op.createdAt)}{op.finishedAt ? `, ended ${fmtWhen(op.finishedAt)}` : old || !op.startedAt ? '' : ` · running for ${elapsed(op.startedAt)}`}</span>
        {writesStopped(op) && <span className="muted">writes stopped{asRead}</span>}
      </div>
      {op.detail && <p className="data-protection-operation-page-p">{op.detail}</p>}
      {op.steps.length === 0 ? <p className="muted small data-protection-operation-page-p">no step has started yet</p>
        : <ListTable label="Steps" className="small data-protection-operation-page-p" rows={op.steps} rowKey={st => `${st.name}-${st.started}`} columns={[
          { key: 'step', label: 'Step', phone: 'name', render: st => <>{st.name}{st.detail && <div className={st.failed ? 'error' : 'muted'}>{st.detail}</div>}</> },
          { key: 'started', label: 'Started', kind: 'time', render: st => <span className="muted">{fmtWhen(st.started)}</span> },
          { key: 'took', label: 'Took', render: st => <span className="muted">{st.ended ? elapsed(st.started, new Date(st.ended).getTime()) : elapsed(st.started)}{st.bytes ? ` · ${fmtBytes(st.bytes)}` : ''}</span> },
          { key: 'state', label: 'State', phone: 'state', render: st => !st.ended ? <Status word="working" staleAt={old ? st.started : undefined} /> : st.unknown ? <Status word="unknown" help="The operation did not record an outcome. Open its details before starting another restore." /> : st.failed ? <Status word="failed" /> : <Status word="finished" /> },
        ]} />}
      {op.cancellable && !op.cancelRequested && (
        <div className="row data-protection-operation-page-p">
          <button className="subtle" disabled={busy} onClick={() => run(() => post(`/operations/${op.id}/cancel`, {}))} title="stop before the next step; a step under way finishes">{op.state === 'queued' ? 'Cancel' : 'Stop before the next step'}</button>
          {err && <span className="error small">{err}</span>}
        </div>
      )}
      {op.cancelRequested && op.state === 'running' && <div className="muted small data-protection-operation-page-p">stopping after the item under way</div>}
    </div>
  )
}

// StoreSection is the backup store on the Backups page: where AppTown
// keeps copies off a deployment's host, or that it has none, and the form
// an administrator attaches one with.
export function StoreSection({ canAttach }: { canAttach: boolean }) {
  const q = useGet<BackupStoreState>('/backup-store')
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState({ endpoint: '', region: '', bucket: '', accessKeyFrom: '', secretKeyFrom: '' })
  const { run, busy, err } = useAction(undefined, { affects: ['/backup-store'] })
  if (q.denied) return null
  if (!q.data) return <ReadStatus q={q} what="whether a backup store is attached" className="small" />
  const st = q.data.store
  const attach = async () => {
    const r = await run(() => put<BackupStoreState>('/backup-store', form))
    if (r) { setOpen(false); q.reload() }
  }
  const field = (k: keyof typeof form, label: string, hint: string) => (
    <>
      <label>{label}</label>
      <input aria-label={label} value={form[k]} onChange={(e) => setForm({ ...form, [k]: e.target.value.trim() })} placeholder={hint} className="data-protection-field-input" />
    </>
  )
  const action = canAttach && !open ? <button onClick={() => setOpen(true)}>{st ? 'Attach another' : 'Attach backup store'}</button> : undefined
  return (
    <Section title="Backup store" action={action}>
      <StaleNote q={q} />
      {st
        ? <dl className="facts small">
            <dt>Bucket</dt><dd>Backups go to <b>{st.bucket}</b></dd>
            <dt>Endpoint</dt><dd>{st.endpoint}</dd>
            <dt>Attached</dt><dd>by {st.attachedBy} {fmtWhen(st.attachedAt)}</dd>
            <dt>Checked</dt><dd><BackgroundReading reading={st.reading} /> <span className="muted">by writing, reading back and deleting a test file</span></dd>
            <dt>Keys</dt><dd>{st.accessKeyFrom} and {st.secretKeyFrom}</dd>
          </dl>
        : !open && <p className="muted small">No backup store attached. Copies stay on each app's host.</p>}
      {open && (
        <div className="data-protection-store-section-div">
          <div className="form-grid">
            {field('endpoint', 'Endpoint', 'https://syd1.digitaloceanspaces.com')}
            {field('region', 'Region', 'syd1')}
            {field('bucket', 'Bucket', 'an existing private bucket')}
            {field('accessKeyFrom', 'Access key secret', 'the secret holding the access key')}
            {field('secretKeyFrom', 'Secret key secret', 'the secret holding the secret key')}
          </div>
          <div className="row mt-2" >
            <button className="primary" disabled={busy} onClick={attach}>{busy ? 'checking…' : 'Check and attach'}</button>
            <button className="subtle" disabled={busy} onClick={() => setOpen(false)}>Cancel</button>
            {err && <span className="error small">{err}</span>}
          </div>
        </div>
      )}
    </Section>
  )
}

// endedWords is an ended operation in the waiting-on-you list.
export function endedWords(op: Operation): string {
  return `${kindWords[op.kind] ?? op.kind} of ${deploymentOf(op)}: ${stateWords(op)}`
}

export function ProtectionLines(props: Parameters<typeof EnabledProtectionLines>[0]) {
 return <FeatureOnly area="backups"><EnabledProtectionLines {...props} /></FeatureOnly>
}
