import type { STATUS_WORD } from './Status'
import { lastSeen } from './format'
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
    const reading = infra.data?.targets.find(t => t.target === name)
    const error = reading?.error
    const word: keyof typeof STATUS_WORD = connection === 'failed' || connection === 'stopped' ? connection
      : issue ? (attention.refreshFailed || issue.unknown || issue.statusLabel === 'unknown' ? 'unknown' : issue.severity === 'advisory' ? 'not checked yet' : 'failed') : reading?.unknown ? 'unknown' : connection
    const summary = connection === 'failed' ? 'Check connection' : issue
      ? issue.title.slice(issue.title.indexOf(': ') + 2).replace('resources not known', 'capacity not known')
      : reading?.unknown ? 'Capacity not known' : reading?.asOf && !reading.live ? lastSeen(reading.asOf) : ''
    return { word, summary, help: issue?.detail || error || `Host connection: ${connection}.` }
  }
}
