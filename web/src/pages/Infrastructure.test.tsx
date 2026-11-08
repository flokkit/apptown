import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient } from '../data'
import type { InfrastructureView as TargetInfra } from '../wire.gen'
import { ResourceTables } from './Infrastructure'

const target = (name: string, fields: Partial<TargetInfra> = {}): TargetInfra => ({
  target: name, kind: 'portainer', family: 'containers', origin: 'attached', status: 'ready', statusLabel: 'working',
  facts: '', place: name, vendor: '', tookSeconds: 0, live: false, invalidated: false, resources: [], ...fields,
})

const show = (targets: TargetInfra[]) => {
  const client = newQueryClient()
  client.setQueryData(getKey('/infrastructure'), { targets })
  return renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><>{targets.map(t => <ResourceTables key={t.target} t={t} onChanged={() => {}} />)}</></QueryClientProvider></MemoryRouter>)
}

describe('infrastructure inventory availability', () => {
  it('distinguishes a live empty answer from an old empty answer and unknown inventory', () => {
    const render = (t: TargetInfra) => renderToStaticMarkup(<ResourceTables t={t} onChanged={() => {}} />)
    expect(render(target('live', { live: true }))).toContain('Empty inventory.')
    const stale = render(target('stale', { asOf: '2026-10-29T00:00:00Z', error: 'unreachable' }))
    expect(stale).toContain('No resources in the inventory last seen')
    expect(stale).not.toContain('Empty inventory.')
    const unknown = render(target('unread'))
    expect(unknown).toContain('Inventory is unknown.')
    expect(unknown).not.toContain('Empty inventory.')
  })


})

it('separates confirmed presence from health and ownership for non-running resources', () => {
 const client=newQueryClient()
 const html=renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={client}><ResourceTables t={target('host',{live:true,resources:[{kind:'volume',name:'calendar-data',record:'recorded',statusLabel:'unknown'}]})} onChanged={()=>{}}/></QueryClientProvider></MemoryRouter>)
 // A volume has no health to check: its row says it is present, and its
 // ownership is said under View resource, not as a guess at health.
 expect(html).toContain('<span class="row-status"><span class="muted">present</span></span>')
 expect(html).not.toContain('chip unknown')
 expect(html).not.toContain('unrecorded')
 client.clear()
})

it('does not confirm presence from inventory that predates a provider change', () => {
 const html=show([target('host',{live:false,invalidated:true,unknown:false,asOf:new Date(Date.now()-60000).toISOString(),resources:[{kind:'volume',name:'new-data',record:'recorded',statusLabel:'unknown'}]})])
 expect(html).not.toContain('>present</span>')
 expect(html).toContain('Inventory predates the latest change.')
 // The unknown status is shown, its reason in its details.
 expect(html).toMatch(/class="chip[^"]*\bunknown\b/)
})
