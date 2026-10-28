import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { InfrastructureView as TargetInfra } from '../wire.gen'
import { InfrastructureView, ResourceTables } from './Infrastructure'

const target = (name: string, fields: Partial<TargetInfra> = {}): TargetInfra => ({
  target: name, kind: 'portainer', family: 'containers', origin: 'attached', status: 'ready',
  facts: '', place: name, vendor: '', live: false, refreshing: false, resources: [], ...fields,
})

const show = (targets: TargetInfra[]) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets })
  return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><InfrastructureView /></QueryClientProvider></MemoryRouter>)
}

describe('infrastructure inventory availability', () => {
  it('preserves healthy targets alongside a records-only unknown target without offering unknown resources for removal', () => {
    const html = show([
      target('healthy', { live: true, asOf: '2026-10-29T00:00:00Z', resources: [{ kind: 'stack', name: 'healthy-app', record: 'recorded' }] }),
      target('unbound', { error: 'Inventory placement could not be verified: provider association required', resources: [{ kind: 'stack', name: 'recorded-app', record: 'recorded' }] }),
      target('unknown-empty', { error: 'provider association required' }),
    ])
    expect(html).toContain('healthy-app')
    expect(html).toContain('recorded-app')
    expect(html).toContain('provider association required')
    expect(html).toContain('inventory unavailable')
    expect(html).toContain('records only')
    expect(html).toContain('Inventory is unknown.')
    expect(html).not.toContain('Empty inventory.')
    expect(html).not.toContain('>remove<')
  })

  it('distinguishes a live empty answer from an old empty answer and unknown inventory', () => {
    const render = (t: TargetInfra) => renderToStaticMarkup(<ResourceTables t={t} onChanged={() => {}} />)
    expect(render(target('live', { live: true }))).toContain('Empty inventory.')
    const stale = render(target('stale', { asOf: '2026-10-29T00:00:00Z', error: 'unreachable' }))
    expect(stale).toContain('No resources in the inventory read')
    expect(stale).not.toContain('Empty inventory.')
    const unknown = render(target('unread'))
    expect(unknown).toContain('Inventory is unknown.')
    expect(unknown).not.toContain('Empty inventory.')
  })
})
