import { Disclosure } from './Popover'

export type MoreItem = { label: string; hint?: string; onClick: () => void; disabled?: boolean; danger?: boolean }

// MoreMenu holds a row's less common actions behind "⋯", each with a line
// saying what it does, so the row itself carries only its identity.
export function MoreMenu({ label, items }: { label: string; items: MoreItem[] }) {
  if (items.length === 0) return null
  return (
    <Disclosure className="more-menu" trigger={
      <button className="subtle more-menu-button" aria-label={`More actions for ${label}`}>⋯</button>
      }>
        <div className="more-menu-list">
          {items.map((it) => (
            <button key={it.label} className={`subtle${it.danger ? ' danger' : ''}`} disabled={it.disabled}
              onClick={() => { it.onClick() }}>
              {it.label}
              {it.hint && <span className="muted small">{it.hint}</span>}
            </button>
          ))}
        </div>
    </Disclosure>
  )
}

// Action menus with form controls (account management and chat resets) share
// the same popover boundary as a row's command menu.
export { Disclosure as FormMenu } from './Popover'
