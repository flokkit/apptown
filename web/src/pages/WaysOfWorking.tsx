import { PageHead } from '../PageHead'
import { NavLink } from 'react-router-dom'
import Packs from './Packs'
import WorkflowLibrary from './WorkflowLibrary'
import GuidanceLibrary from './GuidanceLibrary'
import PoliciesPage from './PoliciesPage'

// One page for the instance's ways of working (design/subsystems/ui-coherence.md):
// packs, workflows, guidance and policies were four pages of one shape,
// each re-explaining stock-vs-yours in its own words. Now one heading, one
// explanation behind its "?", four tabs. The old routes still resolve — they land on
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
      <PageHead page="waysOfWorking" />
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
