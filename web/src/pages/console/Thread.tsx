import { memo, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { renderMd } from '../../md'
import { pastedPictures } from '../../pictures'
import { toolSummary } from '../../AgentActions'
import { useGet } from '../../data'
import type { AuthMe } from '../../wire.gen'
import { BUTTON_MARK, CHECK_MARK, askLine, morphHtml, sinceAsk, stepCount, stepVerb, type Ask, type Part, type RunPart, type Step, type Turn } from './threadModel'
import './thread.css'

// ---- drawing a thread ------------------------------------------------------
// Your words right in accent bubbles, the helper's left: its working-out as
// small words, each run of steps as one pill, its answer in a green-edged
// bubble. A pill opens its steps: a sheet on a phone, the steps column wider.

// pictures turns a pasted picture's name into its address: a live chat's
// uploads and a recorded chat's pictures are read from different places.
export type ThreadProps = {
  turns: Turn[]
  pictures: (name: string) => string
  showActions: boolean
  selectedRun?: string
  onPill?: (run: RunPart, turn: Turn) => void
}

export function Thread({ turns, pictures, showActions, selectedRun, onPill }: ThreadProps) {
  return <>{turns.map((t) => <TurnView key={t.key} turn={t} pictures={pictures} showActions={showActions}
    selectedRun={t.parts.some((p) => p.key === selectedRun) ? selectedRun : undefined} onPill={onPill} />)}</>
}

// A turn is drawn again only when what it shows changed: while words stream
// into the newest turn, earlier turns are left alone.
const TurnView = memo(function TurnView({ turn, pictures, showActions, selectedRun, onPill }: { turn: Turn; pictures: (n: string) => string; showActions: boolean; selectedRun?: string; onPill?: (run: RunPart, turn: Turn) => void }) {
  return <section className={'turn' + (turn.ask?.kind === 'check' ? ' turn-check' : '')}>
    {turn.ask && <AskBubble ask={turn.ask} pictures={pictures} />}
    {turn.parts.length > 0 && <div className="reply">
      {turn.parts.map((p) => <PartView key={p.key} part={p} turn={turn} showActions={showActions} selected={p.key === selectedRun} onPill={onPill} />)}
    </div>}
  </section>
}, (a, b) => a.showActions === b.showActions && a.selectedRun === b.selectedRun && a.pictures === b.pictures && a.onPill === b.onPill && sameTurn(a.turn, b.turn))

function sameTurn(a: Turn, b: Turn): boolean {
  if (a.key !== b.key || a.ask?.message.text !== b.ask?.message.text || a.parts.length !== b.parts.length) return false
  return a.parts.every((p, i) => {
    const q = b.parts[i]
    if (p.key !== q.key || p.kind !== q.kind) return false
    if (p.kind === 'run' && q.kind === 'run') return p.live === q.live && p.steps.length === q.steps.length && p.steps.every((s, j) => s.done === q.steps[j].done)
    return 'text' in p && 'text' in q && p.text === q.text
  })
}

function AskBubble({ ask, pictures }: { ask: Ask; pictures: (n: string) => string }) {
  const text = ask.message.text ?? ''
  if (ask.kind === 'check') return <div className="ask ask-check">
    <div className="lab">Boot check: the app did not start</div>
    <div className="ask-t">{text.replace(CHECK_MARK, '').trim()}</div>
  </div>
  const button = text.match(BUTTON_MARK)
  if (button) return <div className="ask">
    <div className="ask-lab">{button[1]}</div>
    <div className="ask-t ask-more">{text.slice(button[0].length).trim()}</div>
  </div>
  // A pasted picture travels as a sentence naming its upload; show the
  // picture, not the plumbing. Its box is reserved before it loads.
  const { names, files, display } = pastedPictures(text)
  return <div className="ask">
    {display && <div className="ask-t">{display}</div>}
    {files.map((f) => <div key={f.name}><a href={pictures(f.name)} download>{f.name}</a> ({f.size} bytes)</div>)}
    {names.map((n) => <a key={n} className="pic-link" href={pictures(n)} target="_blank" rel="noreferrer" title="open the picture full size">
      <img className="pic" src={pictures(n)} alt="pasted picture" />
    </a>)}
  </div>
}

function PartView({ part, turn, showActions, selected, onPill }: { part: Part; turn: Turn; showActions: boolean; selected: boolean; onPill?: (run: RunPart, turn: Turn) => void }) {
  switch (part.kind) {
    case 'note': return <Md className="note" text={part.text} live={part.draft} />
    case 'final': return <Md className="final" text={part.text} />
    case 'error': return <div className="err"><div className="lab">The helper stopped</div><div className="ask-t">{part.text}</div></div>
    case 'stopped': return <div className="stopped"><div className="lab">Stopped by you</div><div className="ask-t">{part.text}</div></div>
    case 'run': return <>
      <StepsPill run={part} selected={selected} onClick={onPill ? () => onPill(part, turn) : undefined} />
      {showActions && <StepList steps={part.steps} ask={turn.ask} live={part.live} inline />}
    </>
  }
}

// Md is the helper's words as markdown. Each new rendering is morphed into
// the shown one, so streaming words change only the growing text node.
// Words already recorded are drawn once, as markup.
const Md = memo(function Md({ text, className, live = false }: { text: string; className: string; live?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => { if (live && ref.current) morphHtml(ref.current, renderMd(text)) }, [text, live])
  if (live) return <div ref={ref} className={`${className} md streaming`} />
  return <div className={`${className} md`} dangerouslySetInnerHTML={{ __html: renderMd(text) }} />
})

// StepsPill is a run of steps on one line: what the newest step is doing
// while it works, what the run amounted to once done, and how many steps.
export function StepsPill({ run, selected = false, onClick }: { run: RunPart; selected?: boolean; onClick?: () => void }) {
  const newest = run.steps.at(-1)!
  return <button type="button" className={'steps-pill' + (run.live ? ' live' : '') + (selected ? ' selected' : '')} onClick={onClick}
    aria-label={`${stepCount(run.steps.length)}: ${toolSummary(run.steps)}. Show the steps`}>
    <span className="pill-dot" aria-hidden="true" />
    <span className="what">{run.live ? <>{stepVerb(newest)} {newest.detail && <code>{newest.detail}</code>}</> : toolSummary(run.steps)}</span>
    <span className="n">{stepCount(run.steps.length)}</span>
  </button>
}

export function StepList({ steps, ask, inline = false, live = false }: { steps: Step[]; ask: Ask | null; inline?: boolean; live?: boolean }) {
  return <ol className={'steplist' + (inline ? ' inline' : '')}>
    {steps.map((s, i) => <li key={i} className={live && !s.done ? 'now' : undefined}>
      <span className="verb">{stepVerb(s)}</span>
      <code title={s.detail}>{s.detail ?? ''}</code>
      <span className="at">{sinceAsk(ask, s)}</span>
    </li>)}
  </ol>
}

// StepsSheet: a run's steps from the bottom of a phone's screen, with a
// Close button; the scrim and Escape close it too.
export function StepsSheet({ run, turn, onClose }: { run: RunPart; turn: Turn; onClose: () => void }) {
  const close = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null
    close.current?.focus()
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.preventDefault(); onClose() } }
    document.addEventListener('keydown', key)
    return () => { document.removeEventListener('keydown', key); before?.focus?.({ preventScroll: true }) }
  }, [onClose])
  return createPortal(<>
    <div className="sheet-scrim" onClick={onClose} />
    <div className="steps-sheet" role="dialog" aria-modal="true" aria-label={`${stepCount(run.steps.length)} of ${askLine(turn.ask)}`}>
      <div className="steps-sheet-head">
        <span className="steps-sheet-title">{toolSummary(run.steps)}</span>
        <button ref={close} type="button" onClick={onClose}>Close</button>
      </div>
      <StepList steps={run.steps} ask={turn.ask} live={run.live} />
    </div>
  </>, document.body)
}

// StepsColumn: every turn's steps beside the chat, live. A pill tap scrolls
// it to that run and marks it; it follows the newest step while it is at
// its end.
export function StepsColumn({ turns, selected, facts, onClose }: { turns: Turn[]; selected?: string; facts?: ReactNode; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null)
  const atEnd = useRef(true)
  const total = turns.reduce((n, t) => n + t.parts.reduce((m, p) => m + (p.kind === 'run' ? p.steps.length : 0), 0), 0)
  useLayoutEffect(() => {
    const el = box.current
    if (el && atEnd.current && !selected) el.scrollTop = el.scrollHeight
  }, [total, selected])
  useLayoutEffect(() => {
    const el = box.current
    const run = selected ? el?.querySelector<HTMLElement>(`[data-run="${selected}"]`) : null
    if (!el || !run) return
    // Only the column moves: scrolling an element into view also pans the page on Safari.
    const top = run.offsetTop - el.offsetTop - 8
    if (top < el.scrollTop || top + run.offsetHeight > el.scrollTop + el.clientHeight) el.scrollTop = top
  }, [selected])
  const withSteps = turns.filter((t) => t.parts.some((p) => p.kind === 'run'))
  return <aside className="steps-column" aria-label="Steps">
    <div className="steps-column-head">
      <h2>Steps</h2>
      {facts && <span className="steps-column-facts">{facts}</span>}
      <button type="button" className="subtle steps-column-close" aria-label="Close the steps" title="Close the steps" onClick={onClose}>✕</button>
    </div>
    <div ref={box} className="steps-column-body" onScroll={(e) => { const el = e.currentTarget; atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40 }}>
      {withSteps.length === 0 && <p className="muted small steps-column-empty">No steps yet.</p>}
      {withSteps.map((t) => <section key={t.key}>
        <h3 title={askLine(t.ask)}>{askLine(t.ask)}</h3>
        {t.parts.map((p) => p.kind === 'run' && <div key={p.key} data-run={p.key} className={'steps-group' + (p.key === selected ? ' selected' : '')}>
          <StepList steps={p.steps} ask={t.ask} live={p.live} />
        </div>)}
      </section>)}
    </div>
  </aside>
}

// ---- where the steps show ------------------------------------------------
// The column is open unless you closed it; on smaller screens it opens only
// after you tap a pill. The choice is yours, kept per person on this browser
// (in memory too, when storage is refused). Under the phone width there is
// no column: a pill opens a sheet.
export const STEPS_WIDE = '(min-width: 1280px)'
export const STEPS_ROOM = '(min-width: 801px)'
type ColumnChoice = 'open' | 'closed' | ''
const choices = new Map<string, ColumnChoice>()
const listeners = new Set<() => void>()
const subscribe = (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn) } }

export function useMedia(query: string): boolean {
  const get = () => typeof window !== 'undefined' && !!window.matchMedia && window.matchMedia(query).matches
  const [on, setOn] = useState(get)
  useEffect(() => {
    if (!window.matchMedia) return
    const mq = window.matchMedia(query)
    const change = () => setOn(mq.matches)
    change()
    mq.addEventListener('change', change)
    return () => mq.removeEventListener('change', change)
  }, [query])
  return on
}

export function useStepsColumn(): { shown: boolean; room: boolean; open: () => void; close: () => void } {
  const me = useGet<AuthMe>('/auth/me').data
  const key = me?.user ? `apptown.steps-column.${encodeURIComponent(me.user)}` : ''
  const choice = useSyncExternalStore(subscribe, () => {
    if (!choices.has(key)) {
      let v: ColumnChoice = ''
      try { const s = key ? localStorage.getItem(key) : null; v = s === 'open' || s === 'closed' ? s : '' } catch { /* Storage is optional. */ }
      choices.set(key, v)
    }
    return choices.get(key)!
  }, () => '' as ColumnChoice)
  const wide = useMedia(STEPS_WIDE)
  const room = useMedia(STEPS_ROOM)
  const set = (v: ColumnChoice) => {
    choices.set(key, v)
    try { if (key) localStorage.setItem(key, v) } catch { /* Keep the in-memory choice. */ }
    listeners.forEach((fn) => fn())
  }
  return { shown: room && (choice === 'open' || (choice === '' && wide)), room, open: () => set('open'), close: () => set('closed') }
}
