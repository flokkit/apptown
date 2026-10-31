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
// searchable, with managed system connectors shown (dimmed) for completeness.
export function CatalogPicker({ catalog, onPick, onClose }: {
  catalog: ConnectorEntry[]
  onPick: (spec: ConnectorEntry, preset?: ConnectorPreset) => void
  onClose: () => void
}) {
  const [q, setQ] = useState('')
  const match = (t: string) => t.toLowerCase().includes(q.toLowerCase())
  const creatable = catalog.filter((c) => c.multi && !c.managed)
  const managed = catalog.filter((c) => c.managed)

  return (
    <Modal onClose={onClose} title="New integration"
      head={<input placeholder="Search…" value={q} onChange={(e) => setQ(e.target.value)} autoFocus aria-label="search" style={{ width: 220 }} />}>
      <div>
        {creatable.map((spec) => {
          const meta = capabilityMeta(spec.capability, spec.title)
          const presets = (spec.presets ?? []).filter((p) => !q || match(p.title) || match(p.description))
          if (presets.length === 0) return null
          return (
            <div key={spec.capability + spec.provider} style={{ marginBottom: 18 }}>
              <h3 className="catalog-section">{meta.title}
                <span className="muted small" style={{ fontWeight: 400 }}> — {meta.blurb}</span></h3>
              <div className="cat-grid">
                {presets.map((p) => (
                  <div key={p.name} className="cat-tile" onClick={() => onPick(spec, p)}>
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
        {(!q || managed.some((m) => match(m.title))) && (
          <div>
            <h3 className="catalog-section">Managed by {PRODUCT}</h3>
            <div className="cat-grid">
              {managed.filter((m) => !q || match(m.title)).map((m) => (
                <div key={m.title} className="cat-tile cat-tile-managed" title={`Configured by ${PRODUCT}. Open it in the list to see its status or clone it to change it.`}>
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
