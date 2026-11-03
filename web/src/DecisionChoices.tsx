import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'

export const DecisionContextOnly = createContext(false)

// Use the widest equal grid that fills every row. Five choices stay in one
// column on a phone; six choices can use two columns without an orphan.
export function DecisionGrid({ children }: { children: ReactNode }) {
  const grid = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = grid.current!
    const measure = () => {
      const count = el.childElementCount
      let columns = Math.min(count, Math.max(1, Math.floor(el.getBoundingClientRect().width / 150)))
      while (columns > 1 && count % columns !== 0) columns--
      el.style.gridTemplateColumns = `repeat(${columns || 1}, minmax(0, 1fr))`
    }
    measure()
    const resize = new ResizeObserver(measure), mutation = new MutationObserver(measure)
    resize.observe(el); mutation.observe(el, { childList: true })
    return () => { resize.disconnect(); mutation.disconnect() }
  }, [])
  return <div ref={grid} className="ch-decision-choices">{children}</div>
}

export type DecisionChoice = { label: string; run: () => unknown; disabled?: boolean }

// Both acceptance and held recovery use the same note and action order.
export function DecisionChoices({ note, setNote, accept, chat, rework, abandon, recovery = [], busy, stopLanding = false }: {
  note: string; setNote: (note: string) => void
  accept?: DecisionChoice; chat?: DecisionChoice; rework?: DecisionChoice; abandon?: DecisionChoice
  recovery?: DecisionChoice[]; busy: boolean; stopLanding?: boolean
}) {
  const field = useRef<HTMLInputElement>(null)
  const [required, setRequired] = useState(false)
  const [confirm, setConfirm] = useState<'abandon' | 'chat' | null>(null)
  const contextOnly = useContext(DecisionContextOnly)
  if (contextOnly) return null
  return <>
    <input ref={field} aria-label="comment" placeholder="Note (optional)" value={note} onChange={e => { setNote(e.target.value); setRequired(false) }} aria-invalid={required || undefined} />
    {required && <p className="error" role="alert">Say what should change</p>}
    <DecisionGrid>
      {accept && <button className="primary" disabled={busy || accept.disabled} onClick={accept.run}>{accept.label}</button>}
      {chat && <button disabled={busy} onClick={() => stopLanding ? setConfirm('chat') : chat.run()}>{chat.label}</button>}
      {rework && <button disabled={busy} onClick={() => {
        if (!note.trim()) { setRequired(true); field.current?.focus(); return }
        rework.run()
      }}>{rework.label}</button>}
      {recovery.map(action => <button key={action.label} disabled={busy || action.disabled} onClick={action.run}>{action.label}</button>)}
      {abandon && <button className="danger" disabled={busy} onClick={() => setConfirm('abandon')}>{abandon.label}</button>}
    </DecisionGrid>
    {confirm && <div className="row ch-inline-confirm">
      <span>{confirm === 'abandon' ? 'Abandon this change?' : 'Stop the landing and go back to the chat?'}</span>
      <button disabled={busy} onClick={() => { (confirm === 'abandon' ? abandon : chat)?.run(); setConfirm(null) }}>{confirm === 'abandon' ? 'Abandon' : 'Yes, stop it'}</button>
      <button disabled={busy} onClick={() => setConfirm(null)}>{confirm === 'abandon' ? 'Keep it' : 'Stay'}</button>
    </div>}
  </>
}
