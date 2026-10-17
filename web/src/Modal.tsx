import { ReactNode, RefObject, useEffect, useRef } from 'react'

// One dialog for the whole app (debt review, finding 8): a backdrop and a
// panel with a real dialog role, Escape and click-outside close it, focus
// is trapped inside while it is open and handed back when it closes. The
// five hand-rolled modals used to have none of that keyboard path.

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

export function Modal({ title, head, onClose, children, maxWidth }: {
  title: ReactNode
  // Extra controls in the title row (tabs, a search box), before the ✕.
  head?: ReactNode
  onClose: () => void
  children: ReactNode
  maxWidth?: number
}) {
  const panel = useRef<HTMLDivElement>(null)
  useDismiss(panel, onClose)

  // Focus goes in on open — the first control in the body (not the ✕), or
  // the panel itself — and back to whatever had it when the dialog closes.
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null
    const first = panel.current?.querySelector<HTMLElement>(`.modal-body ${FOCUSABLE.split(', ').join(', .modal-body ')}`)
    ;(first ?? panel.current)?.focus()
    return () => before?.focus?.()
  }, [])

  // Tab cycles inside the panel.
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== 'Tab' || !panel.current) return
    const items = Array.from(panel.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (items.length === 0) { e.preventDefault(); return }
    const first = items[0], last = items[items.length - 1]
    const active = document.activeElement
    if (e.shiftKey && (active === first || active === panel.current)) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus() }
  }

  return (
    <div className="modal-backdrop">
      <div ref={panel} className="modal-panel" role="dialog" aria-modal="true" tabIndex={-1}
        aria-label={typeof title === 'string' ? title : undefined}
        style={maxWidth ? { maxWidth } : undefined} onKeyDown={onKeyDown}>
        <div className="row" style={{ marginBottom: 10 }}>
          {typeof title === 'string' ? <h2 style={{ margin: 0 }}>{title}</h2> : title}
          <span style={{ flex: 1 }} />
          {head}
          <button className="subtle" onClick={onClose} aria-label="close" title="close">✕</button>
        </div>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  )
}

// Escape and a click outside `ref` call onClose. Shared with the split
// button's menu, which is a popover rather than a dialog but wants the
// same two exits.
export function useDismiss(ref: RefObject<HTMLElement>, onClose: () => void, active = true) {
  useEffect(() => {
    if (!active) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); onClose() } }
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('mousedown', onDown)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('mousedown', onDown)
    }
  }, [ref, onClose, active])
}
