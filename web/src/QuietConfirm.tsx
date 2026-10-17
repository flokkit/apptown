import { ReactNode, useState } from 'react'

// QuietConfirm: a consequential act without a browser popup — the button
// turns into the question, answered in place. No popups: a first-time user may not know what to
// do with one, and neither does a phone.
//
// `prepare` runs before the question is shown, for acts whose question
// depends on a fetch (what a pack update would change); what it returns
// is shown under the question.
export function QuietConfirm({ label, ask, onYes, prepare, className = 'subtle danger', yes = 'Yes', no = 'No', disabled, title }: {
  label: ReactNode; ask: ReactNode
  onYes: () => unknown
  prepare?: () => Promise<ReactNode | void>
  className?: string; yes?: string; no?: string; disabled?: boolean; title?: string
}) {
  const [asking, setAsking] = useState(false)
  const [detail, setDetail] = useState<ReactNode>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const open = async () => {
    setErr('')
    if (prepare) {
      setBusy(true)
      try { setDetail((await prepare()) ?? null) } catch (e) { setErr((e as Error).message); setBusy(false); return }
      setBusy(false)
    }
    setAsking(true)
  }
  if (!asking) {
    return (
      <>
        <button className={className} onClick={open} disabled={disabled || busy} title={title}>{busy ? '…' : label}</button>
        {err && <span className="error small">{err}</span>}
      </>
    )
  }
  return (
    <span className="small row" style={{ gap: 6 }}>
      <span>{ask}{detail ? <div className="muted small" style={{ whiteSpace: 'pre-wrap', marginTop: 2 }}>{detail}</div> : null}</span>
      <button className="danger" disabled={busy} onClick={async () => {
        setBusy(true); setErr('')
        try { await onYes() } catch (e) { setErr((e as Error).message) } finally { setBusy(false); setAsking(false) }
      }}>{busy ? '…' : yes}</button>
      <button className="subtle" onClick={() => setAsking(false)}>{no}</button>
      {err && <span className="error small">{err}</span>}
    </span>
  )
}

// QuietPrompt: the same treatment for an act that needs words recorded
// with it (a justification, a workflow name, a password) — the button turns
// into a small form, in place. Never window.prompt: that shows a password
// in clear text on a shared screen and has no keyboard path.
export type PromptField = {
  key: string; placeholder?: string; label?: string
  type?: 'text' | 'password'; initial?: string
  required?: boolean; minLength?: number; width?: number
}

export function QuietPrompt({ label, ask, fields, onYes, className = 'subtle', yes = 'OK', no = 'Cancel', disabled, title }: {
  label: ReactNode; ask?: ReactNode
  fields: PromptField[]
  onYes: (values: Record<string, string>) => unknown
  className?: string; yes?: string; no?: string; disabled?: boolean; title?: string
}) {
  const [asking, setAsking] = useState(false)
  const [values, setValues] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const start = () => {
    setValues(Object.fromEntries(fields.map((f) => [f.key, f.initial ?? ''])))
    setErr(''); setAsking(true)
  }
  const ok = fields.every((f) => {
    const v = (values[f.key] ?? '').trim()
    if (f.required && !v) return false
    if (f.minLength && v.length < f.minLength) return false
    return true
  })
  const submit = async () => {
    if (!ok || busy) return
    setBusy(true); setErr('')
    try { await onYes(values); setAsking(false) } catch (e) { setErr((e as Error).message) } finally { setBusy(false) }
  }
  if (!asking) return <button className={className} onClick={start} disabled={disabled} title={title}>{label}</button>
  return (
    <span className="small row" style={{ gap: 6 }}>
      {ask && <span>{ask}</span>}
      {fields.map((f, i) => (
        <input key={f.key} type={f.type ?? 'text'} autoFocus={i === 0}
          value={values[f.key] ?? ''} placeholder={f.placeholder} aria-label={f.label ?? f.placeholder}
          autoComplete={f.type === 'password' ? 'new-password' : 'off'}
          style={{ width: f.width ?? 220 }}
          onChange={(e) => setValues({ ...values, [f.key]: e.target.value })}
          onKeyDown={(e) => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') setAsking(false) }} />
      ))}
      <button className="primary" disabled={!ok || busy} onClick={submit}>{busy ? '…' : yes}</button>
      <button className="subtle" onClick={() => setAsking(false)}>{no}</button>
      {err && <span className="error small">{err}</span>}
    </span>
  )
}
