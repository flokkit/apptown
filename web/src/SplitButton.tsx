import { useCallback, useRef, useState } from 'react'
import { useDismiss } from './Modal'

export function SplitButton({ icon, label, sub, busy, primary, green, onMain, items }: {
  icon: string; label: string; sub: string; busy?: boolean; primary?: boolean; green?: boolean
  onMain: () => void
  items: { label: string; hint?: string; onClick?: () => void; disabled?: boolean }[]
}) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const close = useCallback(() => setOpen(false), [])
  useDismiss(box, close, open)
  const face = primary ? 'primary' : ''
  const toneStyle = green ? { background: 'var(--ok-bg)', border: '1px solid var(--ok)' } : {}
  return (
    <div ref={box} style={{ position: 'relative', display: 'flex', flex: '1 1 300px', alignItems: 'stretch' }}>
      <button className={`${face} btn-door`} onClick={onMain} disabled={busy}
        style={{ flex: 1, borderTopRightRadius: items.length > 0 ? 0 : undefined, borderBottomRightRadius: items.length > 0 ? 0 : undefined, ...toneStyle }}>
        {busy ? '…' : `${icon} ${label}`}
        <div className="btn-sub">{sub}</div>
      </button>
      {items.length > 0 && (
        <button className={face} onClick={() => setOpen((o) => !o)} aria-label={`${label}: more options`}
          aria-expanded={open} aria-haspopup="menu" title="more options"
          style={{ borderTopLeftRadius: 0, borderBottomLeftRadius: 0, padding: '0 14px', fontSize: 'var(--fs-base)',
                   borderLeft: '1px solid var(--overlay)', ...toneStyle }}>▾</button>
      )}
      {open && (
        <div className="card" role="menu" style={{ position: 'absolute', top: '100%', right: 0, zIndex: 10, marginTop: 6,
             minWidth: 280, padding: 6, display: 'flex', flexDirection: 'column', gap: 2 }}>
          {items.map((it) => (
            <button key={it.label} className="subtle" role="menuitem" disabled={it.disabled}
              style={{ textAlign: 'left', padding: '10px 12px', fontSize: 'var(--fs-base)',
                       opacity: it.disabled ? 0.45 : 1, cursor: it.disabled ? 'default' : undefined }}
              onClick={() => { if (it.disabled) return; setOpen(false); it.onClick?.() }}>
              {it.label}
              {it.hint && <div className="muted small" style={{ marginTop: 2 }}>{it.hint}</div>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
