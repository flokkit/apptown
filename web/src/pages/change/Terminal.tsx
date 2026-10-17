import { useEffect, useRef, useState } from 'react'
import { useGet } from '../../data'
import type { StepProgress } from '../../wire.gen'

// The terminal: while a step runs, what is actually
// happening, as it happens — the step's stream in a monospace pane under a
// one-line sentence anyone can read. When the step is done the pane folds
// to one line and opens on demand from the transcript the step kept.
// Read-only; the console is where you talk to the agent.


function fmtElapsed(ms: number) {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m ${String(s % 60).padStart(2, '0')}s`
}

function elapsedBetween(startedAt?: string, finishedAt?: string) {
  if (!startedAt || !finishedAt) return 0
  return Math.max(0, new Date(finishedAt).getTime() - new Date(startedAt).getTime())
}

// collapseProgress: a run of lines that differ only in a time or percent
// reading ("deploying… 8s elapsed", "deploying… 16s elapsed", …) is one
// progress line updating in place, so only its latest reading is shown.
// Lines that differ in any other number ("test 1 passed", "test 2 passed")
// are separate facts and are all kept.
const READING = /\d+(\.\d+)?\s?(ms|s|m|min|%)(?![\p{L}\d])/gu
export function collapseProgress(lines: string[]): string[] {
  const shape = (l: string) => l.replace(READING, '#')
  const out: string[] = []
  for (const l of lines) {
    const prev = out[out.length - 1]
    if (prev !== undefined && prev !== l && shape(l) !== l && shape(prev) === shape(l)) out[out.length - 1] = l
    else out.push(l)
  }
  return out
}

export function Terminal({ stepId, name, running, startedAt, finishedAt, defaultOpen }: {
  stepId: string; name: string; running: boolean; startedAt?: string; finishedAt?: string; defaultOpen?: boolean
}) {
  const [open, setOpen] = useState(!!defaultOpen)
  const [follow, setFollow] = useState(true)
  const pane = useRef<HTMLPreElement>(null)
  // Running: poll the ring. Finished: read the transcript once.
  const { data: live } = useGet<StepProgress>(running ? `/steps/${stepId}/live?from=0` : null, { poll: 2000 })
  const { data: kept, err: keptErr } = useGet<StepProgress>(running ? null : `/steps/${stepId}/live?from=0`)
  const feed = running ? live : kept
  const lines = collapseProgress((feed?.lines ?? []).slice(-400))
  useEffect(() => {
    if (open && follow && pane.current) pane.current.scrollTop = pane.current.scrollHeight
  }, [lines.length, open, follow])

  const last = lines.length ? lines[lines.length - 1] : ''
  const elapsed = running ? (live?.elapsedMs ?? 0) : elapsedBetween(startedAt, finishedAt)
  const total = feed?.total ?? 0

  return (
    <div className="terminal-block">
      <div className="terminal-head row" onClick={() => setOpen((o) => !o)} role="button">
        <span className="mono muted small" style={{ width: 12 }}>{open ? '▾' : '▸'}</span>
        <strong className="small">{name}</strong>
        {running
          ? <span className="small" style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {last || 'starting…'}
            </span>
          : <span className="muted small" style={{ flex: 1 }}>
              {elapsed ? `${fmtElapsed(elapsed)} · ` : ''}{keptErr ? 'terminal output could not be read' : !feed ? '…' : total > 0 ? `${total} line${total === 1 ? '' : 's'}` : 'no terminal output kept'}{feed?.status ? ` · ${feed.status}` : ''}
            </span>}
        <span className="muted small">{running ? `${fmtElapsed(elapsed)} · ${total} line${total === 1 ? '' : 's'}` : (open ? 'close' : 'open the terminal')}</span>
      </div>
      {open && (
        <div>
          <pre className="terminal" ref={pane}
            onScroll={(e) => {
              const el = e.currentTarget
              const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
              if (atBottom !== follow) setFollow(atBottom)
            }}>
            {lines.length ? lines.join('\n') : (running || !feed ? '…' : 'nothing was recorded for this step')}
          </pre>
          {!running && keptErr && <div className="error small">The terminal output could not be read: {keptErr}</div>}
          {running && !follow && (
            <div className="row" style={{ justifyContent: 'flex-end', marginTop: 4 }}>
              <button className="small" onClick={() => { setFollow(true); if (pane.current) pane.current.scrollTop = pane.current.scrollHeight }}>follow ↓</button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
