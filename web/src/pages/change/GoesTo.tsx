import { Link } from 'react-router-dom'
import type { ChangeView } from '../../api'

// Current feed settings describe destinations, not a promise of release success.
export function GoesTo({ view, labelled = false }: { view: ChangeView; labelled?: boolean }) {
  if (['shipped', 'abandoned'].includes(view.change.state)) return null
  const { branch, deployments } = view.goesTo
  return <span>{!labelled && 'Goes to: '}{branch}{deployments.length ? <> → {deployments.map((d, i) => <span key={d.name}>{i > 0 && ', '}<Link to={`/projects/${view.projectSlug}/deployments/${d.name}`} title={d.targetLabel}>{d.displayLabel || d.name}</Link>{['public', 'partners', 'organisation'].includes(d.audience) && ` (${d.audience})`}</span>)}</> : ' (no deployment follows main)'}</span>
}
