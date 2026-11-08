import { Switch } from '../Switch'
import { HelpButton, PageHead } from '../PageHead'
import { put } from '../api'
import { useAction } from '../data'
import { blocking, useFeatures } from '../features'
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
    <PageHead page="features" />
    <StaleNote q={q} />
    {action.err && <p className="error">{action.err}</p>}
    <ReadGate q={q} what="Features">{rows => <>{(['standard', 'beta'] as const).map(tier => {
      const title = tier === 'standard' ? 'On by default' : 'Beta'
      return <Section key={tier} title={title}>
        <ListTable<Feature> label={title} className="features-table" rows={rows.filter(f => f.tier === tier)} rowKey={f => f.area} columns={[
          { key: 'area', label: 'Area', phone: 'name', render: f => <strong>{f.name}</strong> },
          { key: 'enabled', label: 'On / off', phone: 'action', render: f => <span className="features-switch">
            <Switch label={f.name} checked={f.enabled && !blocking(rows, f.area)} disabled={!may || !q.current || action.busy || !!blocking(rows, f.area)} onChange={enabled => void action.run(() => put(`/features/${f.area}`, { expected: f.enabled, enabled }))} />
            {/* What turning it off means, before anyone does. */}
            {f.off && <HelpButton about={`turning ${f.name} off`} guide={f.docs.split('/').pop() || 'features'}><p><strong>Turning it off:</strong> {f.off}</p></HelpButton>}
          </span> },
          { key: 'covers', label: 'What it covers', phone: 'detail', render: f => { const b = blocking(rows, f.area); return <span>{f.covers}{b && <span className="muted small"> · Needs {b.name} on</span>}</span> } },
        ]} />
      </Section>
    })}</>}</ReadGate>
  </>
}
