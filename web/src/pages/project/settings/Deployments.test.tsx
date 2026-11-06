import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../../../data'
import type { DeploymentView } from '../../../wire.gen'
import { EnvCard, RetainedRow, allowanceWords, tearDownWords } from './Deployments'
import type { Retained } from '../../../targets'

// Record ids are UUIDv7 strings.
const RETAINED1 = '00000000-0001-7000-8000-000000000000'
const RETAINED2 = '00000000-0002-7000-8000-000000000000'
const PROJECT = '00000000-0001-7000-8000-000000000000'
const TARGET1 = '00000000-0001-7000-8000-000000000000'
const TARGET2 = '00000000-0002-7000-8000-000000000000'

const card = (readiness: string) => renderToStaticMarkup(
  <QueryClientProvider client={newQueryClient()}>
    <MemoryRouter>
      <EnvCard slug="game" env={{ name: 'main', kind: 'standing', url: '', feed: 'main', audience: 'team', data: 'synthetic', protected: false, readiness } as unknown as DeploymentView} onChanged={() => {}} />
    </MemoryRouter>
  </QueryClientProvider>)

describe('a deployment that needs attention offers Check again', () => {
  it('offers Check again, and says what it does, when AppTown does not know whether it stands', () => {
    const html = card('uncertain')
    expect(html).toContain('Check again')
    expect(html).toContain('deploys nothing')
  })
  it('offers nothing of the kind for a settled deployment', () => {
    expect(card('reconciled')).not.toContain('Check again')
  })
})

describe('a retained resource says what it is', () => {
  const row = (r: Record<string, unknown>) => renderToStaticMarkup(
    <QueryClientProvider client={newQueryClient()}>
      <MemoryRouter>
        <table><tbody><RetainedRow slug="demo-smoke" r={{ id: RETAINED1, projectId: PROJECT, deployment: 'cloud', targetId: TARGET2, target: 'demo-do', retainedBy: 'bls', retainedAt: '2026-10-03T05:58:08Z', disposition: 'retained', ...r } as unknown as Retained} onChanged={() => {}} /></tbody></table>
      </MemoryRouter>
    </QueryClientProvider>)
  it('shows an image repository with its size', () => {
    const html = row({ need: 'image repository', handle: { kind: 'repository', name: 'flokkit/demo-smoke', size: '23.95 MB (latest image, compressed; 1 tag)' } })
    expect(html).toContain('flokkit/demo-smoke')
    expect(html).toContain('23.95 MB (latest image, compressed; 1 tag)')
  })
  it('does not read a database plan as a size', () => {
    expect(row({ need: 'postgres', handle: { id: 'db-1', name: 'forge-x', size: 'db-s-1vcpu-1gb' } })).not.toContain('db-s-1vcpu-1gb')
  })
})

describe('a take-down says what it will do before anyone confirms', () => {
  it('backs a Docker host deployment up, then deletes it', () => {
    expect(tearDownWords('docker-host')).toContain('backed up first')
    expect(tearDownWords(undefined)).toContain('backed up first')
  })
  it('deletes an App Platform database only after its final copy is checked, and leaves Portainer volumes where they are', () => {
    expect(tearDownWords('do-app')).toContain('only then is the database cluster deleted')
    expect(tearDownWords('do-app')).toContain('the take-down is refused unless you choose to go without one, and the cluster stays')
    expect(tearDownWords('portainer')).toContain('backed up first, through Portainer')
  })
  it('deletes a static site\'s bucket, which holds no data', () => {
    expect(tearDownWords('s3-bucket')).toContain('bucket and every file in it are deleted')
  })
})

describe('what a take-down left says why, and closes only as deleted', () => {
  const html = renderToStaticMarkup(
    <QueryClientProvider client={newQueryClient()}>
      <MemoryRouter>
        <table><tbody><RetainedRow slug="shop" r={{ id: RETAINED2, projectId: PROJECT, deployment: 'staging', targetId: TARGET1, need: 'volume shop-lan_data', handle: { kind: 'volume', name: 'shop-lan_data', why: 'kept: AppTown takes no backups on Portainer; delete it there, then mark it deleted' }, retainedBy: 'bls', retainedAt: '2026-10-04T05:58:08Z', disposition: 'retained', holdsData: true } as unknown as Retained} onChanged={() => {}} /></tbody></table>
      </MemoryRouter>
    </QueryClientProvider>)
  it('shows why it was left', () => {
    expect(html).toContain('AppTown takes no backups on Portainer')
  })
  it('offers I deleted it, and no longer Keep it', () => {
    expect(html).toContain('I deleted it')
    expect(html).not.toContain('Keep it')
  })
})

describe('an AI models key says what it may still spend', () => {
  it('shows the allowance over its own period, lifetime spend apart', () => {
    const words = allowanceWords({ reading: { waiting: false, stale: false, unknown: false }, name: 'k', allowance: { limit: 5, remaining: 4, reset: 'monthly', periodSpend: 1, lifetimeSpend: 12 } })
    expect(words).toContain('$4.00 of $5.00 left this month')
    expect(words).toContain('$12.00 spent in all')
    expect(words).not.toContain('$12.00 of')
  })
  it('says when the allowance never renews', () => {
    expect(allowanceWords({ reading: { waiting: false, stale: false, unknown: false }, name: 'k', allowance: { limit: 3, remaining: 3, periodSpend: 0, lifetimeSpend: 0 } })).toContain('$3.00 of $3.00 left, never renewed')
  })
  it('says unknown, never zero, when OpenRouter cannot be asked', () => {
    const words = allowanceWords({ reading: { waiting: false, stale: false, unknown: false }, name: 'k', unknown: 'OpenRouter read key: HTTP 503' })
    expect(words).toContain('allowance unknown · OpenRouter read key: HTTP 503')
    expect(words).not.toContain('$0')
  })
  it('reads the allowance on a card whose deployment owns a key', () => {
    const html = renderToStaticMarkup(
      <QueryClientProvider client={newQueryClient()}>
        <MemoryRouter>
          <EnvCard slug="game" env={{ name: 'prod', kind: 'standing', url: '', feed: 'promotion', audience: 'team', data: 'synthetic', protected: false, readiness: 'reconciled', aiModels: ['forge-game-prod-3'] } as unknown as DeploymentView} onChanged={() => {}} />
        </MemoryRouter>
      </QueryClientProvider>)
    expect(html).toContain('AI models')
    expect(html).toContain('reading the allowance')
    expect(card('reconciled')).not.toContain('AI models')
  })
})

// All page readings use the same waiting classification. Values and age remain
// visible, with one release check line and one host overcommit line per card.
it('says checking once and keeps old readings and the address on the card', () => {
 const client = newQueryClient()
 const base='/projects/game/deployments/main'
 const reading={identity:'old',waiting:true,unknown:true,stale:true,observedAt:'2026-11-03T08:00:00Z',attemptedAt:'2026-11-03T08:00:00Z'}
 client.setQueryData(getKey(`${base}/resources`),{reading,recorded:true,profile:'default',how:'default',parts:[{part:'web',memory:'512Mi',cpu:'no limit',observed:{known:true,usedBytes:123456789,limitBytes:536870912}}],limits:[],limitsKnown:true,share:{target:'local',hostMemory:{known:true,value:8*2**30},requested:{known:true,value:10*2**30},overcommitted:true,readAt:reading.observedAt}})
 client.setQueryData(getKey(`${base}/diagnostics`),{reading,deployment:'main',generation:1,desired:1,containers:true,parts:[{part:'web',presence:'running',facts:{status:'running',restarts:0,running:true},observedAt:reading.observedAt}],events:[],eventsKnown:true})
 client.setQueryData(getKey(`${base}/protection`),{reading,deployment:'main',items:[],recent:[],restoredCopies:[],coverage:{assessed:[],notAssessed:[]}})
 client.setQueryData(getKey(`${base}/ai-models`),{keys:[{reading,name:'models',allowance:{limit:5,remaining:4,lifetimeSpend:1}}]})
 const html=renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><EnvCard slug="game" env={{name:'main',kind:'standing',feed:'main',audience:'team',data:'synthetic',generation:2,readiness:'reconciled',url:'https://game.example',aiModels:['models'],target:{kind:'docker-host',name:'local'},lastApplied:{at:'2026-11-03T08:01:00Z',generation:2}} as DeploymentView} onChanged={()=>{}} /></MemoryRouter></QueryClientProvider>)
 expect(html.match(/Checking after the release/g)).toHaveLength(1)
 expect(html.match(/10.00 GB requested; host total 8.00 GB/g)).toHaveLength(1)
 expect(html).toContain('game.example')
 expect(html).toContain('512Mi')
 expect(html).toContain('$4.00 of $5.00')
 expect(html).toContain('(old)')
 expect(html).not.toContain('The deployment changed')
 expect(html).not.toContain('class="error')
 expect(html).not.toContain('host did not answer')
})

it('shows the deployment host and path without the scheme and keeps the full link', () => {
  const client = newQueryClient()
  const url = 'https://game.apps.example.test/path'
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><EnvCard slug="game" env={{ name: 'main', kind: 'standing', url, feed: 'main', readiness: 'reconciled' } as DeploymentView} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>)
  expect(html).toContain(`href="${url}" target="_blank" rel="noopener noreferrer" class="app-address"`)
  expect(html.replace(/<[^>]*>/g, '')).toContain('game.apps.example.test/path')
  expect(html.replace(/<[^>]*>/g, '')).not.toContain('https://')
  expect(html).toContain('<wbr/>')
  client.clear()
})

it.each(['main', 'staging'])('offers follows-main confirmation for %s by feed', name => {
  const client = newQueryClient()
  const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><EnvCard slug="game" env={{ name, kind: 'standing', feed: 'main', feedConfirmed: false, feedRevision: 1, readiness: 'reconciled' } as DeploymentView} onChanged={() => {}} /></MemoryRouter></QueryClientProvider>)
  expect(html).toContain('Confirm follows main')
  expect(html).not.toContain('Deploy latest')
  client.clear()
})
