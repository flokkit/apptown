import { useState, type Dispatch } from 'react'
import { nameReady, slugOf, type Draft, type DraftAct } from './draft'

// Start with the software; the working name and address remain editable.
export function NameStep({ d, act }: { d: Draft; act: Dispatch<DraftAct> }) {
  const [editingSlug, setEditingSlug] = useState(false)
  const slug = slugOf(d)
  const next = () => act({ type: 'step', step: 2 })
  return (
    <div className="np-panel">
      <label htmlFor="project-description" className="np-label"><strong>What does the app do?</strong></label>
      <textarea id="project-description" className="prose" rows={4} autoFocus
        value={d.description} onChange={(e) => act({ type: 'describe', description: e.target.value })}
        placeholder="A lunch ordering app for our office"
        style={{ width: '100%', boxSizing: 'border-box' }} />
      <label className="np-field mt-3">
        <strong>Name</strong>
        <input value={d.name} onChange={(e) => act({ type: 'name', name: e.target.value })}
          onPaste={e => { if (!d.nameEdited) { e.preventDefault(); act({ type: 'name', name: e.clipboardData.getData('text') }) } }}
          placeholder="Suggested from your description" onKeyDown={(e) => e.key === 'Enter' && nameReady(d) && next()} />
      </label>
      {(slug || d.address !== null || editingSlug) && (
        <div className="muted small mt-2">
          Address: <code>/{slug}</code>
          {!editingSlug && <> · <button className="subtle" onClick={() => setEditingSlug(true)}>Edit</button></>}
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
        <button className="primary" disabled={!nameReady(d)} onClick={next}>Next →</button>
      </div>
    </div>
  )
}
