import { Disclosure } from './Popover'

export function SplitButton({ icon, label, sub, busy, primary, green, compact = false, onMain, items }: {
  icon: string; label: string; sub?: string; compact?: boolean; busy?: boolean; primary?: boolean; green?: boolean
  onMain: () => void
  items: { label: string; hint?: string; onClick?: () => void; disabled?: boolean }[]
}) {
  const face = primary ? 'primary' : ''
  const toneStyle = green ? { background: 'var(--ok-bg)', border: '1px solid var(--ok)' } : {}
  return (
    <div className="split-button" style={{ position: 'relative', display: 'flex', flex: compact ? undefined : '1 1 300px', alignItems: 'stretch' }}>
      <button className={`${face} grow`} onClick={onMain} disabled={busy}
        style={{ borderTopRightRadius: items.length > 0 ? 0 : undefined, borderBottomRightRadius: items.length > 0 ? 0 : undefined, ...toneStyle }}>
        <span className="action-label">{busy ? '…' : `${icon ? icon + ' ' : ''}${label}`}</span>
        {sub && <div className="btn-sub">{sub}</div>}
      </button>
      {items.length > 0 && (
        <Disclosure trigger={<button className={face} aria-label={`${label}: more options`}
          title="more options"
          style={{ borderTopLeftRadius: 0, borderBottomLeftRadius: 0, padding: compact ? '0 8px' : '0 14px', fontSize: 'var(--fs-base)',
                   borderLeft: '1px solid var(--overlay)', ...toneStyle }}>▾</button>}>
        <div className="split-menu">
          {items.map((it) => (
            <button key={it.label} className="subtle" disabled={it.disabled}
              style={{ opacity: it.disabled ? 0.45 : 1, cursor: it.disabled ? 'default' : undefined }}
              onClick={() => { if (it.disabled) return; it.onClick?.() }}>
              {it.label}
              {it.hint && <div className="muted small" style={{ marginTop: 2 }}>{it.hint}</div>}
            </button>
          ))}
        </div>
        </Disclosure>
      )}
    </div>
  )
}
