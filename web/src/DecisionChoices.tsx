import { createContext, useContext, useRef, useState } from 'react'

export const DecisionContextOnly = createContext(false)

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
    <div className="ch-decision-choices">
      {accept && <button className="primary" disabled={busy || accept.disabled} onClick={accept.run}>{accept.label}</button>}
      {chat && <button disabled={busy} onClick={() => stopLanding ? setConfirm('chat') : chat.run()}>{chat.label}</button>}
      {rework && <button disabled={busy} onClick={() => {
        if (!note.trim()) { setRequired(true); field.current?.focus(); return }
        rework.run()
      }}>{rework.label}</button>}
      {abandon && <button className="danger" disabled={busy} onClick={() => setConfirm('abandon')}>{abandon.label}</button>}
      {recovery.map(action => <button key={action.label} disabled={busy || action.disabled} onClick={action.run}>{action.label}</button>)}
    </div>
    {confirm && <div className="row ch-inline-confirm">
      <span>{confirm === 'abandon' ? 'Abandon this change?' : 'Stop the landing and go back to the chat?'}</span>
      <button disabled={busy} onClick={() => { (confirm === 'abandon' ? abandon : chat)?.run(); setConfirm(null) }}>{confirm === 'abandon' ? 'Abandon' : 'Yes, stop it'}</button>
      <button disabled={busy} onClick={() => setConfirm(null)}>{confirm === 'abandon' ? 'Keep it' : 'Stay'}</button>
    </div>}
  </>
}
