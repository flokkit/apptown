import { useState, type ReactNode } from 'react'

// Keep the offered choices equal in size, with full rows at each width.
export function DecisionGrid({ children, inline = false }: { children: ReactNode; inline?: boolean }) {
  return inline ? <>{children}</> : <div className="ch-decision-choices">{children}</div>
}

// Keep decision state here while a table places prose and buttons in separate cells.
export type DecisionLayout = (details: ReactNode, actions: ReactNode) => ReactNode

export type DecisionChoice = { label: string; run: () => unknown; disabled?: boolean; prompt?: { label?: string; ask: string; run: (reason: string) => unknown } }

// The page words and places only the acts AppTown offers.
export function DecisionChoices({ note, setNote, accept, chat, rework, abandon, conversation, busy, highlight, showConversation = false, children, secondary }: {
  children?: DecisionLayout
  secondary?: DecisionChoice
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
  const layout: DecisionLayout = children ?? ((details, actions) => details ? <div className="ch-forward-reason">{details}{actions}</div> : actions)
  const choices = <>
    {mode === 'comment' ? <div className="ch-send-back">
      <h3>What should change?</h3>
      <textarea className="prose" rows={2} autoFocus aria-label="What should change?" value={note} onChange={e => setNote(e.target.value)} />
      <div className="row">
        <button className="primary" disabled={busy || !note.trim()} onClick={rework?.run}>Send back</button>
        <button disabled={busy} onClick={() => setMode('choices')}>Cancel</button>
      </div>
    </div> : mode === 'abandon' ? <AbandonConfirm busy={busy} onYes={() => { abandon?.run(); setMode('choices') }} onNo={() => setMode('choices')} /> : mode === 'reason' ? layout(<>
      <p>{accept?.prompt?.ask}</p>
      <input autoFocus aria-label="Why (required)" placeholder="Why (required)" value={reason} onChange={e => setReason(e.target.value)} />
    </>, <div className="row">
        <button className="primary" disabled={busy || accept?.disabled || !reason.trim()} onClick={async () => { await accept?.prompt?.run(reason.trim()); setMode('choices') }}>{accept?.prompt?.label ?? accept?.label}</button>
        <button disabled={busy} onClick={() => setMode('choices')}>Cancel</button>
      </div>) : layout(null, <DecisionGrid inline={!!children}>
      {accept && <button className={highlight ? highlight === 'accept' ? 'primary' : undefined : chat ? undefined : 'primary'} disabled={busy || accept.disabled} onClick={() => {
        if (accept.prompt) { setReason(''); setMode('reason') } else accept.run()
      }}>{accept.label}</button>}
      {chat && <button className={accept ? undefined : 'primary'} disabled={busy} onClick={chat.run}>{chat.label}</button>}
      {rework && <button className={highlight === 'rework' ? 'primary' : undefined} disabled={busy} onClick={() => {
        setMode('comment')
      }}>Send back…</button>}
      {secondary && <button disabled={busy || secondary.disabled} onClick={secondary.run}>{secondary.label}</button>}
      {abandon && <button className="danger" disabled={busy} onClick={() => setMode('abandon')}>Abandon change</button>}
    </DecisionGrid>)}
  </>
  return <>
    {children ? choices : <div className="ch-decision-slot">{choices}</div>}
    {showConversation && conversation && !chat && <p>Want to talk it through first? <a href={conversation.href} onClick={e => {
      if (conversation.run) { e.preventDefault(); conversation.run() }
    }}>Open the conversation →</a></p>}
  </>
}

export function AbandonConfirm({ busy, onYes, onNo }: { busy: boolean; onYes: () => unknown; onNo: () => unknown }) {
  return <div className="ch-inline-confirm">
    <p>Abandon this change? The branch is kept; the workspace is removed.</p>
    <div className="row">
      <button className="danger" disabled={busy} onClick={onYes}>Abandon</button>
      <button disabled={busy} onClick={onNo}>Keep it</button>
    </div>
  </div>
}
