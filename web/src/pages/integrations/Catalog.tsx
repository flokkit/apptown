import { Tabs } from '../../Tabs'
import { PRODUCT } from '../../brand'
import { useState } from 'react'
import { renderMd } from '../../md'
import { ConnIcon } from '../../icons'
import { Modal } from '../../Modal'
import { useGet } from '../../data'
import type { ConnectorEntry, ConnectorPreset, IntegrationDocs } from '../../wire.gen'
import { capabilityMeta } from './kinds'

// Per-integration documentation (the fifth contribution): admin = org-level
// setup, developer = day-to-day use. Rendered from embedded markdown.
export function DocsModal({ spec, onClose }: { spec: ConnectorEntry; onClose: () => void }) {
  const { data: docs, err } = useGet<IntegrationDocs>(`/integrations/docs/${spec.capability}/${spec.provider}`)
  const [tab, setTab] = useState<'admin' | 'developer'>('developer')
  return (
    <Modal onClose={onClose}
      title={<><ConnIcon name={spec.icon ?? spec.capability} size={20} /><h2 style={{ margin: 0 }}>{spec.title} docs</h2></>}
      head={
        <Tabs className="tabs" style={{ margin: 0 }}>
          <button className={'tab' + (tab === 'developer' ? ' active' : '')} onClick={() => setTab('developer')}>Developer</button>
          <button className={'tab' + (tab === 'admin' ? ' active' : '')} onClick={() => setTab('admin')}>Admin</button>
        </Tabs>
      }>
      {err && <div className="error">{err}</div>}
      {docs && <div className="md-doc" dangerouslySetInnerHTML={{ __html: renderMd(tab === 'admin' ? docs.admin : docs.developer) }} />}
    </Modal>
  )
}

// The Harness-style catalog: a modal of preset tiles grouped by capability,
// searchable. A connector AppTown sets up from its own settings cannot be
// added again; its tile opens the one that exists, which says what it needs.
export function CatalogPicker({ catalog, onPick, onOpen, onClose }: {
  catalog: ConnectorEntry[]
  onPick: (spec: ConnectorEntry, preset?: ConnectorPreset) => void
  onOpen: (spec: ConnectorEntry) => void
  onClose: () => void
}) {
  const [q, setQ] = useState('')
  const match = (t: string) => t.toLowerCase().includes(q.toLowerCase())
  const creatable = catalog.filter((c) => c.multi && !c.managed)
  const managed = catalog.filter((c) => c.managed)

  return (
    <Modal onClose={onClose} title="New integration"
      head={<input placeholder="Search…" value={q} onChange={(e) => setQ(e.target.value)} autoFocus aria-label="search" className="catalog-search" />}>
      <div>
        {/* Every kind page offers Connect; a kind with nothing new to add says so. */}
        {creatable.length === 0 && !q && <p className="muted">Nothing new of this kind can be connected yet.{managed.length > 0 ? ` ${PRODUCT} sets up the one below itself.` : ''}</p>}
        {creatable.map((spec) => {
          const meta = capabilityMeta(spec.capability, spec.title)
          // A connector with no presets is offered as itself.
          const all = spec.presets?.length ? spec.presets : [{ name: spec.provider, title: spec.title, description: spec.description, icon: spec.icon ?? spec.capability, config: {} }]
          const presets = all.filter((p) => !q || match(p.title) || match(p.description))
          if (presets.length === 0) return null
          return (
            <div key={spec.capability + spec.provider} style={{ marginBottom: 18 }}>
              <h3 className="catalog-section">{meta.title}
                <span className="muted small"> — {meta.blurb}</span></h3>
              <div className="cat-grid">
                {presets.map((p) => (
                  <div key={p.name} className="cat-tile" onClick={() => onPick(spec, spec.presets?.length ? p : undefined)}>
                    <ConnIcon name={p.icon} size={24} />
                    <div style={{ minWidth: 0 }}>
                      <strong>{p.title}</strong>
                      <div className="muted small clamp2">{p.description}</div>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )
        })}
        {/* Only when there is one to show: an empty heading reads as broken. */}
        {managed.some((m) => !q || match(m.title)) && (
          <div>
            <h3 className="catalog-section">Set up by {PRODUCT}</h3>
            <div className="cat-grid">
              {managed.filter((m) => !q || match(m.title)).map((m) => (
                <div key={m.title} className="cat-tile" onClick={() => onOpen(m)}>
                  <ConnIcon name={m.icon ?? m.capability} size={24} />
                  <div style={{ minWidth: 0 }}>
                    <strong>{m.title}</strong>
                    <div className="muted small clamp2">{m.description}</div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </Modal>
  )
}
