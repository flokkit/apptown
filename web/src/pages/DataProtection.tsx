import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadStatus, StaleNote } from '../ReadGate'
import { fmtBytes, fmtWhen } from '../format'
import type { BackupStoreState, Coverage, DeploymentProtection, ItemProtection, Operation, OperationAdmitted, RehearsalRef, RestoredCopy } from '../wire.gen'
import { useNow } from '../hooks'
import { useHolds } from '../instancePages'

// Protecting deployment data off-host (design/subsystems/deployment.md):
// for each data-holding item of a cloud deployment, how
// recent the forge's own copies are, the provider's own backups, and when
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
  unknown: 'unknown: the forge stopped while it ran',
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

export const operationTone = (op: Operation) =>
  op.state === 'succeeded' ? (op.verification === 'not-verified' ? 'pending' : 'pass') : live(op) ? 'running' : 'fail'

// writesStopped says a live take-down has deleted the app: nothing writes.
export const writesStopped = (op: Operation) => live(op) && op.result.writesStopped === true

// deploymentOf is the deployment an operation was asked on.
export const deploymentOf = (op: Operation) => (typeof op.input.deployment === 'string' ? op.input.deployment : op.subject ?? '')

const kindWords: Record<string, string> = {
  'deployment.copy': 'Copy', 'deployment.drill': 'Check', 'deployment.take-down': 'Take-down', 'deployment.rehearsal': 'Restore rehearsal',
  'deployment.restored-copy': 'Restored copy', 'deployment.restored-copy-end': 'Restored copy take-down',
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
    case 'fresh': return `forge copies: ${last}`
    case 'stale': return `forge copies: ${[it.copyNote, last].filter(Boolean).join('; ')}`
    case 'unknown': return `forge copies: ${[it.copyNote, last].filter(Boolean).join('; ')}`
    case 'disposable': return 'disposable: starts empty'
    default: return `no copy${it.copyNote ? ` — ${it.copyNote}` : ''}`
  }
}

// nativeLine is what the provider says of its own backups, or why it could
// not be asked; nothing when the provider declares none.
export function nativeLine(it: ItemProtection): string {
  const n = it.native
  if (!n) return ''
  const who = n.mechanism === 'do-database-backups' ? 'DigitalOcean backups' : n.mechanism
  if (n.unknown) return `${who}: unknown — ${n.unknown}`
  if (n.count === 0) return `${who}: none yet`
  return `${who}: oldest ${fmtWhen(n.oldest ?? '')}, newest ${fmtWhen(n.newest ?? '')}`
}

// drillLine is when a copy last came back, or that it never has. A backup
// set has no number people read, so these lines do not name it by its id.
export function drillLine(it: ItemProtection): string {
  const d = it.drill
  if (!d) return 'never checked to come back'
  if (!d.passed) return `did not come back on ${fmtWhen(d.at)} from a backup set: ${d.detail ?? ''}`
  return `came back on ${fmtWhen(d.at)} from a backup set on ${d.target} as ${d.user}`
}

// rehearsalLine is the last restore rehearsal: whether the app answered on
// a copy, from which set and release, where; or why there was none.
export function rehearsalLine(r: RehearsalRef | null | undefined, note?: string): string {
  if (!r) return `last restore rehearsal: ${note || 'never rehearsed'}`
  const from = [r.backup ? 'from a backup set' : '', r.release ? `release ${r.release}` : '', r.host ? `on ${r.host}` : ''].filter(Boolean).join(', ')
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

// gateLine is the forge's own check of a copy's gate: how many checks
// passed, and each that failed with what it answered.
export function gateLine(c: RestoredCopy): string {
  const checks = c.facts.gate ?? []
  if (!checks.length) return ''
  const failed = checks.filter((k) => !k.passed)
  const head = `gate: ${checks.length - failed.length} of ${checks.length} checks passed`
  return failed.length ? `${head}; failed: ${failed.map((k) => `${k.name} (${k.path} ${k.status})`).join(', ')}` : head
}

// copyHead is a standing restored copy in one line: what it is a copy of,
// how long it stays, and who has been let in.
export function copyHead(c: RestoredCopy, source: string): string {
  const parts = [`restored copy of ${source} from a backup set`]
  if (c.test) parts.push('a restore test, taken down once checked')
  else if (!c.checkedAt) parts.push('starting')
  else if (c.expiresAt) parts.push(`${c.open ? 'open' : 'up'} until ${fmtWhen(c.expiresAt)}`)
  const people = Array.from(new Set(c.entries.map((e) => e.person)))
  parts.push(people.length ? `opened by ${people.join(', ')}` : 'not opened yet')
  return parts.join(' · ')
}

// CoverageLines is what the answer speaks for and what it does not, beside
// the evidence: a fresh copy or a passed rehearsal covers the items named,
// never data the app keeps outside its deployment, so no card reads as a
// whole application's protection.
export function CoverageLines({ c, unknown }: { c: Coverage; unknown?: boolean }) {
  return (
    <div style={{ overflowWrap: 'anywhere' }}>
      <div className="muted">covers: {c.assessed.length ? c.assessed.join(', ') : unknown ? 'nothing assessed' : 'no data the forge knows of'}</div>
      {c.notAssessed.map((n) => <div key={n}><span className="chip pending">not assessed</span> {n}</div>)}
    </div>
  )
}

const rehearseTitle = "boot the release it serves on its newest copy, on the host kept for rehearsals, ask the app its readiness, front page and data checks, then take it all down"

// ProtectionLines is a cloud deployment's protection on its card: each
// item, the running operation, and the doors to copy and check now.
export function ProtectionLines({ slug, name }: { slug: string; name: string }) {
  const q = useGet<DeploymentProtection>(`/projects/${slug}/deployments/${name}/protection`, { poll: 5000 })
  const { run, busy, err } = useAction()
  useNow(1000, !!q.data?.running)
  // The way to the target's settings is offered only to those who can edit
  // them; the forge refuses the edit to anyone else.
  const canEditTargets = useHolds('platform:manage-deploy-targets')
  if (q.denied) return null
  if (!q.data) return <ReadStatus q={q} what="how its data is protected" className="small" />
  const p = q.data
  // A failed refresh keeps the last answer, said as such: no state is
  // claimed as it is now.
  const old = q.refreshFailed
  const asRead = old ? ` as read ${elapsed(new Date(q.answeredAt).toISOString())} ago` : ''
  const start = (what: 'copies' | 'drills' | 'rehearsals') => run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${name}/${what}`, {}))
  const last = p.recent.find((o) => !live(o))
  // Whether each door can start is the forge's answer, from that door's
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
  const whyNot = Array.from(new Set(shown.filter((o) => !o.available && o.why).map((o) => o.why!)))
  const runnerMissing = shown.some((o) => o.reason === 'no-copy-runner')
  if (p.unknown) return <div className="small"><span className="chip pending" title={p.unknown}>unknown</span> couldn't read what this deployment holds<CoverageLines c={p.coverage} unknown /></div>
  // A deployment that holds no data the forge knows of says only what is
  // not assessed; one whose take-down would still act on data, or with an
  // operation or copy standing, shows the whole card.
  if (p.items.length === 0 && !p.takeDown && !p.running && !p.restoredCopies.some((c) => !c.endedAt)) return <div className="small"><StaleNote q={q} /><CoverageLines c={p.coverage} /></div>
  // A Docker host's backups on the forge's own disk are a choice, said
  // plainly; App Platform data with no copy at all is a warning.
  const onHost = ops?.copy.reason === 'backups-page'
  return (
    <div className="small" style={{ marginTop: 2 }}>
      <StaleNote q={q} />
      {onHost
        ? <div className="muted">{p.keptOn === 'store' ? 'Backups kept in the backup store' : p.keptOn === 'host' ? <>Backups kept on <span title="lost with this host's disk">this host ⚠</span></> : 'No backup yet'} · <Link to="/docs/guides/running-a-forge">How backups work</Link></div>
        : p.note && <div className={p.dataClass === 'production' && !p.offHost ? 'error' : 'muted'}>{p.note}</div>}
      {p.startFrom && !p.startFrom.filled && <div className="muted">starts from a backup set: its first release fills its data before the app starts</div>}
      {p.items.map((it) => (
        <div key={`${it.need}:${it.volume ?? ''}`}>
          <span className="muted">{itemLabel(it, p.items)}</span> <b>{it.where}</b>
          <div style={{ marginLeft: 12 }}>
            <div><span className={`chip ${old ? 'pending' : it.copy === 'fresh' ? 'pass' : it.copy === 'none' || it.copy === 'stale' ? 'fail' : 'pending'}`}>{it.copy}{asRead}</span> {copyLine(it)}</div>
            {nativeLine(it) && <div className="muted">{nativeLine(it)}</div>}
            {(!ops || applies(ops.check)) && <div className={it.drill && !it.drill.passed ? 'error' : 'muted'}>{drillLine(it)}</div>}
          </div>
        </div>
      ))}
      <CoverageLines c={p.coverage} />
      {(p.rehearsal || !ops || ops.rehearsal.available || p.rehearsalNote === 'never rehearsed: no rehearsal place') && <div className={p.rehearsal && !p.rehearsal.passed ? 'error' : 'muted'}>{rehearsalLine(p.rehearsal, p.rehearsalNote)}{p.rehearsal && asRead}</div>}
      {p.rehearsal && <div className="muted" style={{ marginLeft: 12 }}>{heldBack(p.rehearsal)}</div>}
      {p.rehearsal?.previous && <div className="muted" style={{ marginLeft: 12 }}>before that: {rehearsalLine(p.rehearsal.previous).replace('last restore rehearsal ', '')}</div>}
      {(p.restoreTest || !ops || applies(ops.restore)) && <div className={p.restoreTest && !(p.restoreTest.outcome ?? '').startsWith('passed') ? 'error' : 'muted'}>{restoreTestLine(p.restoreTest, p.restoreTestNote)}{p.restoreTest && asRead}</div>}
      {p.restoredCopies.filter((c) => !c.endedAt).map((c) => <RestoredCopyLines key={c.id} slug={slug} source={name} copy={c} asRead={asRead} />)}
      {p.running && (
        <div style={{ marginTop: 4 }}>
          <span className="chip running">{p.running.state}{asRead}</span> <Link to={`/operations/${p.running.id}`}>{currentStep(p.running)}</Link>
          {writesStopped(p.running) && <span className="chip fail" style={{ marginLeft: 6 }}>writes stopped{asRead}</span>}
        </div>
      )}
      {!p.running && last && (
        <div className="muted" style={{ marginTop: 4 }}>
          last: <Link to={`/operations/${last.id}`}>{kindWords[last.kind] ?? last.kind} {stateWords(last)}</Link>{last.finishedAt ? ` ${fmtWhen(last.finishedAt)}` : ''}
        </div>
      )}
      {ops && !p.running && (
        <div className="row" style={{ gap: 6, marginTop: 4 }}>
          {applies(ops.copy) && <button className="subtle" disabled={busy || !ops.copy.available} onClick={() => start('copies')} title={ops.copy.why || 'copy its data to the backup store now'}>Copy now</button>}
          {applies(ops.check) && <button className="subtle" disabled={busy || !ops.check.available} onClick={() => start('drills')} title={ops.check.why || 'fill the newest copy into a new database, check it, and remove it'}>Check it comes back</button>}
          {p.rehearsalNote !== 'never rehearsed: no rehearsal place' && (
            <button className="subtle" disabled={busy || !ops.rehearsal.available} onClick={() => start('rehearsals')} title={ops.rehearsal.why || rehearseTitle}>Rehearse a restore</button>
          )}
          {p.dataClass === 'production' && applies(ops.restore) && (
            <>
              <button className="subtle" disabled={busy || !ops.restore.available} onClick={() => run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${name}/restored-copies`, { test: true }))} title={ops.restore.why || restoreTestTitle}>Test a restore here</button>
              <button className="subtle" disabled={busy || !ops.restore.available} onClick={() => run(() => post<OperationAdmitted>(`/projects/${slug}/deployments/${name}/restored-copies`, { minutes: 240 }))} title={ops.restore.why || openCopyTitle}>Restore a copy to look at</button>
            </>
          )}
          {err && <span className="error small">{err}</span>}
        </div>
      )}
      {!p.running && whyNot.length > 0 && (
        <div className="muted" role="note" style={{ marginTop: 4 }}>
          {whyNot.map((w) => <div key={w}>{w}</div>)}
          {runnerMissing && canEditTargets && <Link to={`/infrastructure/${p.target}`}>Open {p.target}'s settings</Link>}
        </div>
      )}
      {p.takeDown && !ops?.finalCopy.available && <div className="error" style={{ marginTop: 4 }}>{p.takeDown}</div>}
    </div>
  )
}

const restoreTestTitle = 'start its release from its newest copy on its own platform, behind the forge gate, check it through the gate, record the answer and take it down: about 10 to 20 minutes and a few cents on DigitalOcean'
const openCopyTitle = 'start its release from its newest copy on its own platform, behind the forge gate, for 4 hours: people who may see this production data can open it from here and sign in to the app; it costs well under a dollar on DigitalOcean'

// RestoredCopyLines is one standing restored copy on its source's card:
// what it is and until when, what it can still do outside and who else
// can read it, and the doors to open, extend and take it down. Open is a
// form the browser submits, so the forge's mutation protection covers it
// and the answer takes the browser to the copy's gate with a one-use ticket.
function RestoredCopyLines({ slug, source, copy: c, asRead }: { slug: string; source: string; copy: RestoredCopy; asRead: string }) {
  const { run, busy, err } = useAction()
  return (
    <div style={{ marginTop: 4, marginLeft: 12 }}>
      <div><span className={`chip ${c.open ? 'running' : 'pending'}`}>{c.open ? 'open' : c.test ? 'testing' : 'restored copy'}{asRead}</span> {copyHead(c, source)}</div>
      {c.outcome && <div className={c.outcome === 'failed' ? 'error' : 'muted'}>checked through the gate: {c.outcome}{c.facts.checks.length ? ` (${c.facts.checks.map((k) => `${k.path} ${k.status}`).join(', ')})` : ''}{gateLine(c) ? ` · ${gateLine(c)}` : ''}</div>}
      {c.protection && <div className="muted">protected by {c.protection}</div>}
      <div className="muted">{c.warning}</div>
      <div className="muted">{c.boundary}</div>
      {c.endDetail && <div className="error">{c.endDetail}</div>}
      <div className="row" style={{ gap: 6, marginTop: 4 }}>
        {c.open && (
          <form method="post" action={`/api/v1/projects/${slug}/restored-copies/${c.id}/open`} target="_blank" style={{ display: 'inline' }}>
            <button className="subtle" type="submit" title="open it in a new tab through the forge gate; the app's own sign-in follows">Open</button>
          </form>
        )}
        {!c.test && c.checkedAt && <button className="subtle" disabled={busy} onClick={() => run(() => post(`/projects/${slug}/restored-copies/${c.id}/extend`, { minutes: 120 }))} title="keep it open for 2 hours from now">Extend 2 hours</button>}
        {c.checkedAt && <button className="subtle" disabled={busy} onClick={() => run(() => del(`/projects/${slug}/restored-copies/${c.id}`))} title="take it down now; no final copy is taken">Take down</button>}
        {err && <span className="error small">{err}</span>}
      </div>
    </div>
  )
}

// OperationPage is one operation: what it is, who asked, each step as it
// happened, and how it ended.
export function OperationPage() {
  const { id = '' } = useParams()
  const q = useGet<Operation>(`/operations/${id}`, { poll: 3000 })
  const { run, busy, err } = useAction()
  useNow(1000, q.data ? live(q.data) : false)
  if (!q.data) return <ReadStatus q={q} what="the operation" />
  const op = q.data
  const old = q.refreshFailed
  const asRead = old ? ` as read ${elapsed(new Date(q.answeredAt).toISOString())} ago` : ''
  return (
    <div style={{ maxWidth: 820 }}>
      <StaleNote q={q} />
      <div className="muted small">operation {op.number}{op.subject ? ` · ${op.subject}` : ''}</div>
      <h1 style={{ marginBottom: 4 }}>{kindWords[op.kind] ?? op.kind} of {deploymentOf(op)}</h1>
      <div className="row" style={{ gap: 8, alignItems: 'center' }}>
        <span className={`chip ${old ? 'pending' : operationTone(op)}`}>{stateWords(op)}{asRead}</span>
        <span className="muted small">asked by {op.askedBy} {fmtWhen(op.createdAt)}{op.finishedAt ? `, ended ${fmtWhen(op.finishedAt)}` : old || !op.startedAt ? '' : ` — running for ${elapsed(op.startedAt)}`}</span>
        {writesStopped(op) && <span className="chip fail">writes stopped{asRead}</span>}
      </div>
      {op.detail && <p style={{ marginTop: 10 }}>{op.detail}</p>}
      <table className="small" style={{ marginTop: 10 }}>
        <thead><tr><th>Step</th><th>Started</th><th>Took</th><th></th></tr></thead>
        <tbody>
          {op.steps.map((st, i) => (
            <tr key={i}>
              <td>{st.name}{st.detail && <div className={st.failed ? 'error' : 'muted'}>{st.detail}</div>}</td>
              <td className="muted" style={{ whiteSpace: 'nowrap' }}>{fmtWhen(st.started)}</td>
              <td className="muted">{st.ended ? elapsed(st.started, new Date(st.ended).getTime()) : elapsed(st.started)}{st.bytes ? ` · ${fmtBytes(st.bytes)}` : ''}</td>
              <td>{!st.ended ? <span className="chip running">{old ? 'last read' : 'now'}</span> : st.unknown ? <span className="chip pending">not known</span> : st.failed ? <span className="chip fail">failed</span> : <span className="chip pass">done</span>}</td>
            </tr>
          ))}
          {op.steps.length === 0 && <tr><td colSpan={4} className="muted">no step has started yet</td></tr>}
        </tbody>
      </table>
      {op.cancellable && !op.cancelRequested && (
        <div className="row" style={{ marginTop: 10 }}>
          <button className="subtle" disabled={busy} onClick={() => run(() => post(`/operations/${op.id}/cancel`, {}))} title="stop before the next step; a step under way finishes">{op.state === 'queued' ? 'Cancel' : 'Stop before the next step'}</button>
          {err && <span className="error small">{err}</span>}
        </div>
      )}
      {op.cancelRequested && op.state === 'running' && <div className="muted small" style={{ marginTop: 10 }}>stopping after the item under way</div>}
    </div>
  )
}

// StoreSection is the backup store on the Backups page: where the forge
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
      <input value={form[k]} onChange={(e) => setForm({ ...form, [k]: e.target.value.trim() })} placeholder={hint} style={{ width: 320 }} />
    </>
  )
  return (
    <div style={{ marginBottom: 10 }}>
      <StaleNote q={q} />
      <div className="row small" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        {st
          ? <span title={`keys in ${st.accessKeyFrom} and ${st.secretKeyFrom}; checked by a write, read back and delete`}>Backup store: <b>{st.bucket}</b> at {st.endpoint} · attached by {st.attachedBy} {fmtWhen(st.attachedAt)}</span>
          : <span>No backup store attached</span>}
        {canAttach && !open && <button className="subtle" onClick={() => setOpen(true)}>{st ? 'Attach another' : 'Attach a bucket'}</button>}
      </div>
      {open && (
        <div style={{ marginTop: 8 }}>
          <div className="form-grid">
            {field('endpoint', 'Endpoint', 'https://syd1.digitaloceanspaces.com')}
            {field('region', 'Region', 'syd1')}
            {field('bucket', 'Bucket', 'an existing private bucket')}
            {field('accessKeyFrom', 'Access key secret', 'the secret holding the access key')}
            {field('secretKeyFrom', 'Secret key secret', 'the secret holding the secret key')}
          </div>
          <div className="row" style={{ marginTop: 8 }}>
            <button className="primary" disabled={busy} onClick={attach}>{busy ? 'checking…' : 'Check and attach'}</button>
            <button className="subtle" disabled={busy} onClick={() => setOpen(false)}>cancel</button>
            {err && <span className="error small">{err}</span>}
          </div>
        </div>
      )}
    </div>
  )
}

// endedWords is an ended operation in the waiting-on-you list.
export function endedWords(op: Operation): string {
  return `${kindWords[op.kind] ?? op.kind} of ${deploymentOf(op)}: ${stateWords(op)}`
}
