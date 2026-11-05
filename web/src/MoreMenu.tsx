import { useCallback, useRef, useState } from 'react'
import { useDismiss } from './Modal'

export type MoreItem = { label: string; hint?: string; onClick: () => void; disabled?: boolean; danger?: boolean }

// MoreMenu holds a row's less common actions behind "⋯", each with a line
// saying what it does, so the row itself carries only its identity.
export function MoreMenu({ label, items }: { label: string; items: MoreItem[] }) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const close = useCallback(() => setOpen(false), [])
  useDismiss(box, close, open)
  if (items.length === 0) return null
  return (
    <div ref={box} className="more-menu" onClick={(e) => e.stopPropagation()}>
      <button className="subtle more-menu-button" aria-label={`More actions for ${label}`} aria-haspopup="menu" aria-expanded={open}
        onClick={() => setOpen((o) => !o)}>⋯</button>
      {open && (
        <div className="more-menu-list" role="menu">
          {items.map((it) => (
            <button key={it.label} className={`subtle${it.danger ? ' danger' : ''}`} role="menuitem" disabled={it.disabled}
              onClick={() => { setOpen(false); it.onClick() }}>
              {it.label}
              {it.hint && <span className="muted small">{it.hint}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
