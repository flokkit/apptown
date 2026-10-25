import { useState, type Dispatch } from 'react'
import { nameReady, slugOf, type Draft, type DraftAct } from './draft'

// Start with the software; the working name and address remain editable.
export function NameStep({ d, act }: { d: Draft; act: Dispatch<DraftAct> }) {
  const [editingSlug, setEditingSlug] = useState(false)
  const slug = slugOf(d)
  const next = () => act({ type: 'step', step: 2 })
  return (
    <div className="card" style={{ padding: 'var(--space-5)' }}>
      <label htmlFor="project-description"><strong>What would you like to build?</strong></label>
      <textarea id="project-description" className="prose mt-2" rows={4} autoFocus
        value={d.description} onChange={(e) => act({ type: 'describe', description: e.target.value })}
        placeholder="A lunch ordering app for our office"
        style={{ width: '100%', boxSizing: 'border-box' }} />
      <div className="muted small mt-2">Describe the software, or what you want to do with existing code.</div>
      <label className="row mt-3" style={{ gap: 'var(--space-2)' }}>
        <span>Working name</span>
        <input value={d.name} onChange={(e) => act({ type: 'name', name: e.target.value })}
          placeholder="Suggested from your description" onKeyDown={(e) => e.key === 'Enter' && next()}
          style={{ flex: 1, minWidth: 0 }} />
      </label>
      {(slug || d.address !== null || editingSlug) && (
        <div className="muted small mt-2">
          Proposed address: <code>/{slug}</code>
          {!editingSlug && <> · <button className="subtle" onClick={() => setEditingSlug(true)}>change that</button></>}
          {editingSlug && (
            <input aria-label="Project address" value={slug} autoFocus style={{ marginLeft: 'var(--space-2)', width: 180 }}
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
