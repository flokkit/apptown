import { TechnicalWords, useTechnicalDetail } from '../../change/TechnicalDetail'
import { DeploymentLine } from './DeploymentLine'
import { Status } from '../../../Status'
import { PRODUCT } from '../../../brand'
import { useEffect, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { get } from '../../../api'
import { useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { fmtBytes, fmtStamp, fmtWhen } from '../../../format'
import type { DeploymentDiagnostics, PartLog } from '../../../wire.gen'

// Diagnostics (design/subsystems/deployment.md, "Runtime diagnostics"):
// what the deployment's settled generation runs, part by part, as its host
// reports it now. The failure and what to do about it come first; the
// per-part facts and limit events stay in technical detail. A read that did
// not answer is unknown, with the last facts read and their age, never
// stopped. A part's log is read only when someone asks, and kept nowhere.

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
function nextStep(p: Part): string {
  const f = p.facts
  if (p.presence === 'unknown') return 'its host did not answer; it is asked again shortly'
  if (f?.oomKilled) return 'raise its memory (Override memory…) or read its log'
  if (p.presence === 'absent') return 'deploy again to start it'
  return 'read its log to see why'
}

export function DiagnosticsLines({ slug, name }: { slug: string; name: string }) {
  const [technical] = useTechnicalDetail()
  const q = useGet<Diag>(`/projects/${slug}/deployments/${name}/diagnostics`, { poll: 15000 })
  const anchor = `diagnostics-${name}`
  const linked = useLocation().hash === `#${anchor}`
  // The log lives in this component's state alone: not in the read cache,
  // and not through useAction, whose mutation cache would keep its text.
  const [log, setLog] = useState<PartLog | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const hasData = !!q.data
  useEffect(() => {
    if (linked) document.getElementById(anchor)?.scrollIntoView({ block: 'center' })
  }, [linked, anchor, hasData])
  if (q.denied) return null
  if (!q.data) return <DeploymentLine label="Parts"><div id={anchor}><ReadStatus q={q} what="its diagnostics" /></div></DeploymentLine>
  const d = q.data
  const readLog = async (part: string) => {
    if (busy) return
    setBusy(true)
    setErr('')
    setLog(null)
    try {
      setLog(await get<PartLog>(`/projects/${slug}/deployments/${name}/logs?part=${encodeURIComponent(part)}`))
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const unknown = d.parts.some(p => p.presence === 'unknown')
  return (
    <DeploymentLine label="Parts" trailer={d.containers ? <a href={`#${anchor}-logs`}>logs →</a> : undefined}><div id={anchor}>
      {d.reading?.waiting && <div className="muted">Last values (old)</div>}
      {d.unsupported && <div className="muted">{d.unsupported}</div>}
      {d.parts.map(p => <div key={p.part}>
        {partName(p)}: <Status observation word={p.presence === 'unknown' || d.reading?.unknown || q.refreshFailed ? 'unknown' : trouble(p) ? 'failed' : 'running'} help={p.presence === 'unknown' ? p.reason : undefined} />
        {p.presence !== 'unknown' && trouble(p) && !d.reading?.waiting && <span> {nextStep(p)}<TechnicalWords shown={technical}> · {trouble(p)}</TechnicalWords></span>}
      </div>)}
      {unknown && !d.reading?.waiting && <div>Its host did not report the state of every part. It is asked again shortly.</div>}
      <TechnicalWords shown={technical}>{d.unsettled}</TechnicalWords>
      <StaleNote q={q} inline />{d.reading?.failure && <div className="muted">{d.reading.failure}</div>}
      {d.hold && (
        <div>
          {d.hold.confirmed
            ? <>{PRODUCT} stopped it {at(d.hold.at)} after {d.hold.restarts} restarts in {d.hold.window}; the next deploy replaces it.</>
            : <>{PRODUCT} tried to stop it {at(d.hold.at)} after {d.hold.restarts} restarts in {d.hold.window}, but the stop was not confirmed{d.hold.detail ? `: ${d.hold.detail}` : ''}.</>}
          {' '}<TechnicalWords shown={technical}>receipt {d.hold.receipt}</TechnicalWords>
          {d.releaseNumber ? <> · <Link to={`/projects/${slug}/releases/${d.releaseNumber}`}>release #{d.releaseNumber}</Link></> : null}
        </div>
      )}
      {technical && <DiagnosticsDetail d={d} busy={busy} onLog={readLog} />}
      {d.containers && <div id={`${anchor}-logs`} className="row">
        {d.parts.filter(p => p.presence !== 'absent').map(p => <button key={p.part} className="link" disabled={busy} onClick={() => readLog(p.part)}>Read {partName(p)} log</button>)}
      </div>}
      {err && <div className="error">{err}</div>}
      {log && <LogView log={log} onClose={() => setLog(null)} />}
    </div></DeploymentLine>
  )
}

function DiagnosticsDetail({ d, busy, onLog }: { d: Diag; busy: boolean; onLog: (part: string) => void }) {
  return (
    <div style={{ marginTop: 4 }}>
      <div className="muted">
        generation {d.generation}{d.commit ? <> · commit <code>{d.commit.slice(0, 12)}</code></> : null}
        {' · '}observed {d.runtime.state}{d.runtime.observedAt ? ` ${at(d.runtime.observedAt)}` : ''}{d.runtime.reason ? ` (${d.runtime.reason})` : ''}
      </div>
      {d.parts.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="small" style={{ marginTop: 2 }}>
            <thead><tr><th>part</th><th>state</th><th title="Docker's count for this container; a deploy starts a new one">restarts</th><th>health check</th><th></th></tr></thead>
            <tbody>
              {d.parts.map((p) => {
                const f = p.facts
                const old = (p.stale || d.reading?.waiting) && f
                return (
                  <tr key={p.part || 'app'} title={p.observedAt ? `read ${p.observedAt}${p.attemptedAt && p.attemptedAt !== p.observedAt ? `, last asked ${p.attemptedAt}` : ''}` : 'never read'}>
                    <td>
                      {partName(p)}{p.public && p.part ? <span className="muted"> (front door)</span> : null}{p.need ? <span className="muted"> (service)</span> : null}
                      {p.generation == null
                        ? f && <div className="muted">generation not known</div>
                        : p.generation !== d.generation && <div><span className="badge">generation {p.generation}</span></div>}
                    </td>
                    <td>
                      {p.presence === 'unknown' ? <Status word={"unknown"} /> : p.presence}
                      {!old && f?.exitCode != null ? ` · exit ${f.exitCode}` : ''}
                      {!old && f?.oomKilled ? <Status word="failed" help="The app ran out of memory. Raise its memory limit or fix its memory use, then deploy again." /> : null}
                      {!old && f && <span className="muted"> · {f.running ? `started ${at(f.startedAt)}` : `finished ${at(f.finishedAt)}`}</span>}
                      {p.reason && !d.reading?.waiting && <div className="muted">{p.reason}</div>}
                      {old && <div className="muted">last read {at(p.observedAt)}: {f.status}{f.exitCode != null ? `, exit ${f.exitCode}` : ''}{f.oomKilled ? ', out of memory' : ''}</div>}
                    </td>
                    <td style={old ? { opacity: 0.6 } : undefined}>{f ? f.restarts : ''}</td>
                    <td style={old ? { opacity: 0.6 } : undefined}>{f?.health ?? ''}</td>
                    <td>{d.containers && p.presence !== 'absent' && <button className="link small" disabled={busy} onClick={() => onLog(p.part)}>log</button>}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
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
function LogView({ log, onClose }: { log: PartLog; onClose: () => void }) {
  return (
    <div style={{ marginTop: 4 }}>
      <div className="row" style={{ gap: 6 }}>
        <b>{partName(log)}</b>
        <span className="muted">{log.generation == null ? `log of container ${log.container.slice(0, 12)} (generation not known)` : `log of generation ${log.generation}`}, read {fmtStamp(log.readAt)} · newest {log.maxLines} lines, at most {fmtBytes(log.maxBytes)}{log.truncated ? '; older output not shown' : ''}</span>
        <button className="link small" onClick={onClose}>close</button>
      </div>
      <pre style={{ maxHeight: 320, overflow: 'auto', background: 'var(--code-bg)', padding: '8px 10px', borderRadius: 'var(--radius-sm)', fontSize: 'var(--fs-xs)', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{log.text || '(empty)'}</pre>
    </div>
  )
}
