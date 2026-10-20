import { useState, type Dispatch } from 'react'
import { nameReady, rollName, slugOf, type Draft, type DraftAct } from './draft'

// Step 1 — Name: the address derives quietly from it (editable, but never
// looks required).
export function NameStep({ d, act }: { d: Draft; act: Dispatch<DraftAct> }) {
  const [editingSlug, setEditingSlug] = useState(false)
  const slug = slugOf(d)
  const next = () => act({ type: 'step', step: 2 })
  return (
    <div className="card" style={{ padding: 'var(--space-5)' }}>
      <div className="mb-2"><strong>What's it called?</strong></div>
      <div className="row" style={{ gap: 'var(--space-2)' }}>
        <input value={d.name} onChange={(e) => act({ type: 'name', name: e.target.value })} placeholder="e.g. Lunch Orders" autoFocus
          onKeyDown={(e) => e.key === 'Enter' && next()}
          style={{ flex: 1, fontSize: 'var(--fs-md)', padding: '10px 12px', boxSizing: 'border-box' }} />
        <button title="pick a name for me — press again for another"
          onClick={() => act({ type: 'roll', next: rollName() })}
          style={{ fontSize: 'var(--fs-lg)', padding: '8px 14px' }}>🎲</button>
      </div>
      {slug && (
        <div className="muted small mt-2">
          it will live at <code>/{slug}</code>
          {!editingSlug && <> · <button className="subtle" onClick={() => setEditingSlug(true)}>change that</button></>}
          {editingSlug && (
            <input value={slug} autoFocus style={{ marginLeft: 'var(--space-2)', width: 180 }}
              onChange={(e) => act({ type: 'address', address: e.target.value })}
              onBlur={() => setEditingSlug(false)}
              onKeyDown={(e) => e.key === 'Enter' && setEditingSlug(false)} />
          )}
        </div>
      )}
      <div className="row mt-4">
        <span className="grow" />
        <button className="primary" disabled={!nameReady(d)} onClick={next}
          style={{ fontSize: 'var(--fs-base)', padding: '10px 26px' }}>Next →</button>
      </div>
    </div>
  )
}
