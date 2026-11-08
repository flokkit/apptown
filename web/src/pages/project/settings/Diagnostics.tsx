import { TechnicalWords, useTechnicalDetail } from '../../change/TechnicalDetail'
import { ListTable } from '../../../ListTable'
import { Status } from '../../../Status'
import { PRODUCT } from '../../../brand'
import { Link } from 'react-router-dom'
import { fmtBytes, fmtStamp, fmtWhen } from '../../../format'
import type { DeploymentDiagnostics, PartLog } from '../../../wire.gen'

// Diagnostics (design/subsystems/deployment.md, "Runtime diagnostics"):
// what the deployment's settled generation runs, part by part, as its host
// reports it now. The failure and what to do about it come first; the
// per-part facts and limit events stay in technical detail. A read that did
// not answer is unknown, with the last facts read and their age, never
// stopped. A part's log is read only when someone asks, and kept nowhere.
// The containers table on the deployment page shows them.

type Diag = DeploymentDiagnostics
type Part = Diag['parts'][number]

// trouble is what is wrong with a part now, in a few words, or '' when
// nothing is.
export function trouble(p: Part): string {
  const f = p.facts
  switch (p.presence) {
    case 'absent':
      return 'no container'
    case 'unknown':
      return 'unknown'
    case 'stopped':
      return ['stopped', f?.exitCode != null ? `exit ${f.exitCode}` : '', f?.oomKilled ? 'killed: out of memory' : ''].filter(Boolean).join(' · ')
  }
  return [f && f.restarts > 0 ? `restarted ${f.restarts}×` : '', f?.health === 'unhealthy' ? 'unhealthy' : ''].filter(Boolean).join(' · ')
}

const partName = (p: { part: string }) => p.part || 'app'
const at = (iso?: string) => (iso ? fmtWhen(iso) : '')

// nextStep is the one thing to do first about a part's trouble.
export function nextStep(p: Part): string {
  const f = p.facts
  if (p.presence === 'unknown') return 'its host did not answer; it is asked again shortly'
  if (f?.oomKilled) return 'raise its memory (Override memory…) or read its log'
  if (p.presence === 'absent') return 'deploy again to start it'
  return 'read its log to see why'
}

// HoldLine says AppTown stopped a part that kept restarting, or tried to.
export function HoldLine({ slug, d }: { slug: string; d: Diag }) {
  const [technical] = useTechnicalDetail()
  if (!d.hold) return null
  return <div>
      {d.hold.confirmed
        ? <>{PRODUCT} stopped it {at(d.hold.at)} after {d.hold.restarts} restarts in {d.hold.window}; the next deploy replaces it.</>
        : <>{PRODUCT} tried to stop it {at(d.hold.at)} after {d.hold.restarts} restarts in {d.hold.window}, but the stop was not confirmed{d.hold.detail ? `: ${d.hold.detail}` : ''}.</>}
      {' '}<TechnicalWords shown={technical}>receipt {d.hold.receipt}</TechnicalWords>
      {d.releaseNumber ? <> · <Link to={`/projects/${slug}/releases/${d.releaseNumber}`}>release #{d.releaseNumber}</Link></> : null}
  </div>
}

export function DiagnosticsDetail({ d, busy, onLog }: { d: Diag; busy: boolean; onLog: (part: string) => void }) {
  return (
    <div className="mt-1">
      <div className="muted">
        generation {d.generation}{d.commit ? <> · commit <code>{d.commit.slice(0, 12)}</code></> : null}
        {' · '}observed {d.runtime.state}{d.runtime.observedAt ? ` ${at(d.runtime.observedAt)}` : ''}{d.runtime.reason ? ` (${d.runtime.reason})` : ''}
      </div>
      {d.parts.length > 0 && (
        <ListTable label="Containers" className="small diagnostics-parts" rows={d.parts} rowKey={p => p.part || 'app'} columns={[
          { key: 'part', label: 'part', phone: 'name', render: p => {
            const f = p.facts
            return <span title={p.observedAt ? `read ${p.observedAt}${p.attemptedAt && p.attemptedAt !== p.observedAt ? `, last asked ${p.attemptedAt}` : ''}` : 'never read'}>
              {partName(p)}{p.public && p.part ? <span className="muted"> (front door)</span> : null}{p.need ? <span className="muted"> (service)</span> : null}
              {p.generation == null
                ? f && <div className="muted">generation not known</div>
                : p.generation !== d.generation && <div><span className="badge">generation {p.generation}</span></div>}
            </span>
          } },
          { key: 'state', label: 'state', phone: 'state', render: p => {
            const f = p.facts
            const old = (p.stale || d.reading?.waiting) && f
            return <>
              {p.presence === 'unknown' ? <Status word={"unknown"} /> : p.presence}
              {!old && f?.exitCode != null ? ` · exit ${f.exitCode}` : ''}
              {!old && f?.oomKilled ? <Status word="failed" help="The app ran out of memory. Raise its memory limit or fix its memory use, then deploy again." /> : null}
              {!old && f && <span className="muted"> · {f.running ? `started ${at(f.startedAt)}` : `finished ${at(f.finishedAt)}`}</span>}
              {p.reason && !d.reading?.waiting && <div className="muted">{p.reason}</div>}
              {old && <div className="muted">last read {at(p.observedAt)}: {f.status}{f.exitCode != null ? `, exit ${f.exitCode}` : ''}{f.oomKilled ? ', out of memory' : ''}</div>}
            </>
          } },
          { key: 'restarts', label: 'restarts', phone: 'extra', kind: 'number', render: p => <span className={(p.stale || d.reading?.waiting) && p.facts ? 'diagnostics-old' : undefined} title="Docker's count for this container; a deploy starts a new one">{p.facts ? p.facts.restarts : ''}</span> },
          { key: 'health', label: 'health check', phone: 'extra', render: p => <span className={(p.stale || d.reading?.waiting) && p.facts ? 'diagnostics-old' : undefined}>{p.facts?.health ?? ''}</span> },
          { key: 'log', label: '', phone: 'action', render: p => d.containers && p.presence !== 'absent' && <button className="link small" disabled={busy} onClick={() => onLog(p.part)}>log</button> },
        ]} />
      )}
      <EventLines d={d} />
    </div>
  )
}

const kindWords: Record<string, string> = { 'out-of-memory': 'killed: out of memory', exited: 'exited' }

// EventLines lists the last day's limit events with the generation each
// happened under: an earlier generation's is marked as such, so it is
// never read as the running one's.
function EventLines({ d }: { d: Diag }) {
  if (!d.eventsKnown) return <div className="muted">limit events: {d.eventsSource}</div>
  if (d.events.length === 0) return <div className="muted" title={d.eventsSource}>no out-of-memory kills or exits recorded in the last day</div>
  return (
    <ul style={{ margin: '2px 0', paddingLeft: 16 }} title={d.eventsSource}>
      {d.events.map((e, i) => (
        <li key={i}>
          <b>{partName(e)}</b>: {kindWords[e.kind] || e.kind}{e.exitCode != null ? ` (exit ${e.exitCode})` : ''} <span className="muted">{at(e.at)}</span>{' '}
          {e.generation == null
            ? <span className="muted">generation not known</span>
            : e.generation === d.generation ? null : <span className="badge">generation {e.generation}, earlier</span>}
        </li>
      ))}
    </ul>
  )
}

// LogView shows one log read as it came: its part, when it was read, and
// its bounds; it is not refreshed and not kept.
export function LogView({ log, onClose }: { log: PartLog; onClose: () => void }) {
  return (
    <div className="mt-1">
      <div className="row" style={{ gap: 6 }}>
        <b>{partName(log)}</b>
        <span className="muted">{log.generation == null ? `log of container ${log.container.slice(0, 12)} (generation not known)` : `log of generation ${log.generation}`}, read {fmtStamp(log.readAt)} · newest {log.maxLines} lines, at most {fmtBytes(log.maxBytes)}{log.truncated ? '; older output not shown' : ''}</span>
        <button className="link small" onClick={onClose}>close</button>
      </div>
      <pre style={{ maxHeight: 320, overflow: 'auto', background: 'var(--code-bg)', padding: '8px 10px', borderRadius: 'var(--radius-sm)', fontSize: 'var(--fs-xs)', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{log.text || '(empty)'}</pre>
    </div>
  )
}
