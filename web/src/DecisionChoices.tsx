import { createContext, useContext, useState, type ReactNode } from 'react'

export const DecisionContextOnly = createContext(false)

// Keep the offered choices equal in size, with full rows at each width.
export function DecisionGrid({ children }: { children: ReactNode }) {
  return <div className="ch-decision-choices">{children}</div>
}

export type DecisionChoice = { label: string; run: () => unknown; disabled?: boolean; prompt?: { ask: string; run: (reason: string) => unknown } }

// The page words and places only the acts AppTown offers.
export function DecisionChoices({ note, setNote, accept, chat, rework, abandon, conversation, busy, highlight, showConversation = false }: {
  // showConversation: a held agent change can be taken into a chat from here.
  showConversation?: boolean
  highlight?: 'accept' | 'rework' | 'none'
  note: string; setNote: (note: string) => void
  // chat: a change worked in a chat continues there; it takes the send-back place.
  accept?: DecisionChoice; chat?: DecisionChoice; rework?: DecisionChoice; abandon?: DecisionChoice
  conversation?: { href: string; run?: () => unknown }; busy: boolean
}) {
  const [mode, setMode] = useState<'choices' | 'comment' | 'abandon' | 'reason'>('choices')
  const [reason, setReason] = useState('')
  const contextOnly = useContext(DecisionContextOnly)
  if (contextOnly) return null
  return <>
    <div className="ch-decision-slot">
    {mode === 'comment' ? <div className="ch-send-back">
      <h3>What should change?</h3>
      <textarea className="prose" rows={2} autoFocus aria-label="What should change?" value={note} onChange={e => setNote(e.target.value)} />
      <div className="row">
        <button className="primary" disabled={busy || !note.trim()} onClick={rework?.run}>Send to agent</button>
        <button disabled={busy} onClick={() => setMode('choices')}>Cancel</button>
      </div>
    </div> : mode === 'abandon' ? <div className="ch-inline-confirm">
      <p>Abandon this change? The branch is kept; the workspace is removed.</p>
      <div className="row">
        <button className="danger" disabled={busy} onClick={() => { abandon?.run(); setMode('choices') }}>Abandon</button>
        <button disabled={busy} onClick={() => setMode('choices')}>Keep it</button>
      </div>
    </div> : mode === 'reason' ? <div className="ch-forward-reason">
      <p>{accept?.prompt?.ask}</p>
      <input autoFocus aria-label="Why (required)" placeholder="Why (required)" value={reason} onChange={e => setReason(e.target.value)} />
      <div className="row">
        <button className="primary" disabled={busy || !reason.trim()} onClick={async () => { await accept?.prompt?.run(reason.trim()); setMode('choices') }}>{accept?.label}</button>
        <button disabled={busy} onClick={() => setMode('choices')}>Cancel</button>
      </div>
    </div> : <DecisionGrid>
      {accept && <button className={highlight ? highlight === 'accept' ? 'primary' : undefined : chat ? undefined : 'primary'} disabled={busy || accept.disabled} onClick={() => {
        if (accept.prompt) { setReason(''); setMode('reason') } else accept.run()
      }}>{accept.label}</button>}
      {chat && <button className={accept ? undefined : 'primary'} disabled={busy} onClick={chat.run}>{chat.label}</button>}
      {rework && <button className={highlight === 'rework' ? 'primary' : undefined} disabled={busy} onClick={() => {
        setMode('comment')
      }}>Send back…</button>}
      {abandon && <button className="danger" disabled={busy} onClick={() => setMode('abandon')}>Abandon change</button>}
    </DecisionGrid>}
    </div>
    {showConversation && conversation && !chat && <p>Want to talk it through first? <a href={conversation.href} onClick={e => {
      e.currentTarget.closest('dialog')?.close()
      if (conversation.run) { e.preventDefault(); conversation.run() }
    }}>Open the conversation →</a></p>}
  </>
}
