import { Switch } from '../Switch'
import { Link } from 'react-router-dom'
import { put } from '../api'
import { useAction } from '../data'
import { useFeatures } from '../features'
import { useHolds } from '../instancePages'
import { ReadGate, StaleNote } from '../ReadGate'
import { ListTable } from '../ListTable'
import { Section } from '../Section'
import type { Feature } from '../wire.gen'
import './Features.css'

export default function Features() {
  const q = useFeatures()
  const may = useHolds('identity:manage-auth')
  const action = useAction(q.reload)
  return <>
    <h1>Features</h1>
    <p>Choose which areas are available to everyone. Adopted workflows keep every step. Beta areas start turned off.</p>
    <p><Link to="/docs/guides/features">Features guide</Link> · Includes the always-on areas.</p>
    <StaleNote q={q} />
    {action.err && <p className="error">{action.err}</p>}
    <ReadGate q={q} what="Features">{rows => <>{(['standard', 'beta'] as const).map(tier => {
      const title = tier === 'standard' ? 'On by default' : 'Beta'
      return <Section key={tier} title={title}>
        <ListTable<Feature> label={title} className="features-table" rows={rows.filter(f => f.tier === tier)} rowKey={f => f.area} columns={[
          { key: 'area', label: 'Area', phone: 'name', render: f => <strong>{f.name}</strong> },
          { key: 'enabled', label: 'On / off', phone: 'action', render: f => <Switch label={f.name} checked={f.enabled} disabled={!may || !q.current || action.busy} onChange={enabled => void action.run(() => put(`/features/${f.area}`, { expected: f.enabled, enabled }))} /> },
          { key: 'covers', label: 'What it covers', phone: 'detail', render: f => <span>{f.covers}</span> },
        ]} />
      </Section>
    })}</>}</ReadGate>
  </>
}
