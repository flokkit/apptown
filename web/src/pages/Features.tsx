import { Link } from 'react-router-dom'
import { put } from '../api'
import { useAction } from '../data'
import { useFeatures } from '../features'
import { useHolds } from '../instancePages'
import { ReadGate, StaleNote } from '../ReadGate'
import { ListTable } from '../ListTable'
import type { Feature } from '../wire.gen'
import './Features.css'

export default function Features() {
  const q = useFeatures()
  const may = useHolds('identity:manage-auth')
  const action = useAction(q.reload)
  return <>
    <h1>Features</h1>
    <p>Choose which areas are available to everyone. Adopted workflows keep every step. Beta areas start turned off.</p>
    <StaleNote q={q} />
    {action.err && <p className="error">{action.err}</p>}
    <ReadGate q={q} what="Features">{rows => <ListTable<Feature> label="Features" className="features-table" rows={rows} rowKey={f => f.area} columns={[
      { key: 'area', label: 'Area', phone: 'name', render: f => <strong title={f.name}>{f.name}</strong> },
      { key: 'tier', label: 'Tier', phone: 'state', render: f => f.tier === 'always' ? 'Always on' : f.tier === 'beta' ? <span className="badge">beta</span> : 'On by default' },
      { key: 'enabled', label: 'On / off', phone: 'action', render: f => <div className="feature-control"><span>{f.enabled ? 'On' : 'Off'}</span>{f.tier !== 'always' && <button aria-label={`${f.name}: turn ${f.enabled ? 'off' : 'on'}`} disabled={!may || !q.current || action.busy} onClick={() => void action.run(() => put(`/features/${f.area}`, { expected: f.enabled, enabled: !f.enabled }))}>{f.enabled ? 'Turn off' : 'Turn on'}</button>}</div> },
      { key: 'covers', label: 'What it covers', phone: 'detail', render: f => <span title={f.covers}>{f.covers}</span> },
      { key: 'docs', label: 'Docs', phone: 'extra', render: f => <Link to={f.docs}>Guide</Link> },
    ]} />}</ReadGate>
  </>
}
