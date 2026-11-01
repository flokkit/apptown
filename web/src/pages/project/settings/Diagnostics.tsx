import { BackgroundReading } from '../../../BackgroundReading'
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
// per-part facts, limit events and log are one fold down. A read that did
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
  const q = useGet<Diag>(`/projects/${slug}/deployments/${name}/diagnostics`, { poll: 15000 })
  const anchor = `diagnostics-${name}`
  const linked = useLocation().hash === `#${anchor}`
  const [open, setOpen] = useState(linked)
  // The log lives in this component's state alone: not in the read cache,
  // and not through useAction, whose mutation cache would keep its text.
  const [log, setLog] = useState<PartLog | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => {
    if (linked) document.getElementById(anchor)?.scrollIntoView({ block: 'center' })
  }, [linked, anchor, q.data != null])
  if (q.denied) return null
  if (!q.data) return <div id={anchor}><ReadStatus q={q} what="its diagnostics" className="small" /></div>
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
  const failing = d.parts.filter((p) => trouble(p) !== '')
  return (
    <div id={anchor} className="small">
      <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
        <span className="muted">diagnostics</span>
        {d.unsupported
          ? <span className="muted">{d.unsupported}</span>
          : failing.length === 0
            ? <Status word="running" help={`All ${d.parts.length} parts are running.`} />
            : failing.map((p) => (
              <span key={p.part}>{partName(p)}: <Status word={p.presence === 'unknown' ? 'unknown' : 'failed'} help={`${p.reason || trouble(p)}. Next: ${nextStep(p)}.`} /></span>
            ))}
        {d.hold && <Status word="failed" help={`${d.hold.confirmed ? 'Stopped after a crash loop.' : 'Crash loop: stop not confirmed.'} ${d.hold.detail || 'Read the app log and fix the crash, then deploy again.'}`} />}
        {d.unsettled && <span className="badge" title="only a part whose own container names its generation is shown with one">{d.unsettled}</span>}
        {(d.parts.length > 0 || d.events.length > 0) && <button className="link small" onClick={() => setOpen(!open)}>{open ? 'hide details' : 'details'}</button>}
        <StaleNote q={q} inline /><BackgroundReading reading={d.reading} />
      </div>
      {failing.length > 0 && !d.unsupported && (
        <div>
          {failing.map((p) => (
            <div key={p.part}>
              <b>{partName(p)}</b>: {nextStep(p)}
              {d.containers && p.presence !== 'absent' && <button className="link small" disabled={busy} onClick={() => readLog(p.part)}>read log</button>}
            </div>
          ))}
        </div>
      )}
      {d.hold && (
        <div>
          {d.hold.confirmed
            ? <>{PRODUCT} stopped it {at(d.hold.at)} after {d.hold.restarts} restarts in {d.hold.window}; the next deploy replaces it.</>
            : <>{PRODUCT} tried to stop it {at(d.hold.at)} after {d.hold.restarts} restarts in {d.hold.window}, but the stop was not confirmed{d.hold.detail ? `: ${d.hold.detail}` : ''}.</>}
          {' '}<span className="muted">receipt {d.hold.receipt}</span>
          {d.releaseNumber ? <> · <Link to={`/projects/${slug}/releases/${d.releaseNumber}`}>release #{d.releaseNumber}</Link></> : null}
        </div>
      )}
      {open && <DiagnosticsDetail d={d} busy={busy} onLog={readLog} />}
      {err && <div className="error">{err}</div>}
      {log && <LogView log={log} onClose={() => setLog(null)} />}
    </div>
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
                const old = p.stale && f
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
                      {p.reason && <div className="muted">{p.reason}</div>}
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
