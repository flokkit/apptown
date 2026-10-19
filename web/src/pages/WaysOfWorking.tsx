import { PRODUCT } from '../brand'
import { NavLink } from 'react-router-dom'
import Packs from './Packs'
import WorkflowLibrary from './WorkflowLibrary'
import GuidanceLibrary from './GuidanceLibrary'
import PoliciesPage from './PoliciesPage'

// One page for the instance's ways of working (design/subsystems/ui-coherence.md):
// packs, workflows, guidance and policies were four pages of one shape,
// each re-explaining stock-vs-yours in its own words. Now one heading, one
// explanation, four tabs. The old routes still resolve — they land on
// their tab.

export type WowTab = 'packs' | 'workflows' | 'guidance' | 'policies'

const TABS: { key: WowTab; to: string; label: string }[] = [
  { key: 'packs', to: '/packs', label: 'Packs' },
  { key: 'workflows', to: '/workflow-library', label: 'Workflows' },
  { key: 'guidance', to: '/guidance', label: 'Guidance' },
  { key: 'policies', to: '/policies', label: 'Policies' },
]

export default function WaysOfWorking({ tab }: { tab: WowTab }) {
  return (
    <div>
      <h1 style={{ marginBottom: 4 }}>Ways of working</h1>
      <p className="muted small" style={{ maxWidth: 680, marginTop: 0 }}>
        How projects get built: <strong>workflows</strong> are the steps (Core for everyone, Extras for
        particular situations, System for {PRODUCT}'s own jobs), <strong>guidance</strong> is
        what the agents are told, <strong>policies</strong> are the rules every workflow must keep,
        and a <strong>pack</strong> bundles all three so a new project starts with a way of working.
        Ones marked <span className="chip">{PRODUCT}</span> ship with {PRODUCT}. A new {PRODUCT} release adds new
        versions; a project moves to them when it adopts the update under Settings › Way of working.
        To change one, make a pack of your own that includes it.
      </p>
      <div className="tabs">
        {TABS.map((t) => (
          <NavLink key={t.key} to={t.to} className={() => 'tab' + (tab === t.key ? ' active' : '')}>{t.label}</NavLink>
        ))}
      </div>
      {tab === 'packs' && <Packs />}
      {tab === 'workflows' && <WorkflowLibrary />}
      {tab === 'guidance' && <GuidanceLibrary />}
      {tab === 'policies' && <PoliciesPage />}
    </div>
  )
}
