import { useAttention } from './attention'
import { useGet } from './data'
import type { InfrastructureTargets, TargetLabel } from './wire.gen'

// Connection failures take priority; otherwise every host view uses the same
// current Attention condition, including unknown coverage.
export function useHostStates() {
  const attention = useAttention()
  const infra = useGet<InfrastructureTargets>('/infrastructure', { poll: 30000 })
  return (name: string, connection: TargetLabel) => {
    const issue = attention.data?.items.find(item => decodeURIComponent(item.href) === `/infrastructure/${name}`)
    const error = infra.data?.targets.find(t => t.target === name)?.error
    const word = connection === 'failed' || connection === 'stopped' ? connection
      : issue ? (attention.refreshFailed ? 'unknown' : issue.statusLabel) : error ? 'unknown' : connection
    const summary = connection === 'failed' ? 'Check connection' : issue
      ? issue.title.slice(issue.title.indexOf(': ') + 2).replace('resources not known', 'capacity not known')
      : error ? 'Capacity not known' : ''
    return { word, summary, help: issue?.detail || error || `Host connection: ${connection}.` }
  }
}
