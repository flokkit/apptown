// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { getKey, newQueryClient, retryDelay } from './data'
import { InstanceGate, useHolds } from './instancePages'
import { SidebarNav } from './App'
import Deployments from './pages/Deployments'
import { Route, Routes } from 'react-router-dom'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// What /authz/me answers for someone who holds only their own projects, and
// for an administrator.
const member = { user: 'member', instance: { 'project:create': true, 'institution:read-structure': false, 'project:read': false, 'deployment:read': false, 'conformance:read': false, 'standards:manage-packs': false } }
const admin = { user: 'bls', instance: { 'project:create': true, 'institution:read-structure': true, 'project:read': true, 'deployment:read': true, 'conformance:read': true, 'standards:manage-packs': true } }

function answer(me: unknown | null) {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (String(url).includes('/authz/me')) {
      return me === null ? new Response(JSON.stringify({ error: 'down' }), { status: 503 }) : new Response(JSON.stringify(me))
    }
    return new Response(JSON.stringify({}))
  }))
}

// render mounts a node with its own client; unmount() ends it early, and
// cleanup() ends whatever is still mounted and restores fetch and the clock.
const mounted: (() => void)[] = []
async function render(node: React.ReactNode, at = '/') {
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  await act(async () => { root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[at]}>{node}</MemoryRouter></QueryClientProvider>) })
  let live = true
  const unmount = () => { if (live) { live = false; act(() => root.unmount()); client.clear() } }
  mounted.push(unmount)
  return { el, client, unmount }
}
function cleanup() {
  for (const unmount of mounted.splice(0)) unmount()
  vi.useRealTimers()
  vi.unstubAllGlobals()
}
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
const authzReads = () => vi.mocked(fetch).mock.calls.filter(([url]) => String(url).includes('/authz/me')).length

describe('pages about the whole AppTown', () => {
  afterEach(cleanup)

  // Someone AppTown says cannot read the instance's people sees a plain
  // statement in place of the page, not the page's errors.
  it('says the page is not available to someone without the read', async () => {
    answer(member)
    const { el } = await render(<InstanceGate page="people"><div>the people page</div></InstanceGate>)
    await vi.waitFor(() => expect(el.textContent).toContain('Not available to you'))
    expect(el.textContent).not.toContain('the people page')
    expect(el.querySelector('h1')?.textContent).toBe('People')
  })

  it('shows the page to someone who holds the read', async () => {
    answer(admin)
    const { el } = await render(<InstanceGate page="people"><div>the people page</div></InstanceGate>)
    await vi.waitFor(() => expect(el.textContent).toContain('the people page'))
  })

  // Not knowing is not a refusal: when the holdings cannot be read, the page
  // shows and speaks for itself.
  it('keeps the page when the holdings cannot be read', async () => {
    vi.useFakeTimers()
    answer(null)
    const { el } = await render(<InstanceGate page="people"><div>the people page</div></InstanceGate>)
    expect(el.textContent).toContain('Checking access')
    expect(el.textContent).not.toContain('Not available')
    // A failed read is retried before it counts as failed: until the last
    // retry on the client's schedule fails, the gate is still reading.
    await advance(retryDelay(0) + retryDelay(1) - 1)
    expect(authzReads()).toBe(2)
    expect(el.textContent).toContain('Checking access')
    await advance(1)
    expect(authzReads()).toBe(3)
    await vi.waitFor(() => expect(el.textContent).toContain('the people page'))
    expect(el.textContent).not.toContain('Not available')
  })

  // The menu leaves out exactly the instance pages the person cannot open
  // and keeps everything that has an answer for their own projects.
  it('leaves the pages a project member cannot open out of the menu', async () => {
    answer(member)
    const { el, unmount } = await render(<SidebarNav />)
    const links = () => [...el.querySelectorAll('a.nav')].map((a) => a.textContent)
    await vi.waitFor(() => expect(links()).not.toContain('People'))
    expect(links()).toEqual(['Home', 'Projects', 'Deployments', 'Ways of working', 'Your account', 'Usage', 'Docs'])
    unmount()
    answer(admin)
    const again = await render(<SidebarNav />)
    await vi.waitFor(() => expect([...again.el.querySelectorAll('a.nav')].map((a) => a.textContent)).toContain('People'))
    expect([...again.el.querySelectorAll('a.nav')].map((a) => a.textContent)).toEqual(['Home', 'Projects', 'Deployments', 'Infrastructure', 'Conformance', 'Ways of working', 'Evals', 'Integrations', 'Your account', 'People', 'Usage', 'Complaints', 'Docs'])
  })

  // Deployments keeps the caller's own departures and deployments; the hosts
  // and every deployment's backups are infrastructure.
  it('leaves infrastructure out of Deployments for a project member', async () => {
    answer(member)
    const { el } = await render(<Routes><Route path="/deployments/:gtab" element={<Deployments />} /></Routes>, '/deployments/backups')
    await vi.waitFor(() => expect(el.textContent).toContain('Not available to you'))
    const tabs = [...el.querySelectorAll('.tabs a')].map((a) => a.textContent)
    expect(tabs).toEqual(['Departures', 'Deployments'])
  })
})

// useHolds is what a page asks before showing a control. It is false until
// AppTown has answered, false when AppTown cannot be read, and true
// only for what AppTown says the person holds, on AppTown or in the
// named project. (People once showed administrator controls to everyone
// while its role read was on its way.)
describe('useHolds', () => {
  afterEach(cleanup)
  function Probe({ cap, project }: { cap: string; project?: string }) {
    return <span>{useHolds(cap, project) ? 'yes' : 'no'}</span>
  }

  it('is false while AppTown has not answered', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))
    const { el } = await render(<Probe cap="identity:manage-auth" />)
    expect(el.textContent).toBe('no')
  })

  it('is false when the holdings cannot be read', async () => {
    vi.useFakeTimers()
    answer(null)
    const { el, client } = await render(<Probe cap="project:create" />)
    await advance(retryDelay(0) + retryDelay(1))
    await vi.waitFor(() => expect(client.getQueryState(getKey('/authz/me'))?.status).toBe('error'))
    expect(el.textContent).toBe('no')
  })

  it('follows AppTown: an instance capability, and one in a project', async () => {
    answer({ ...member, project: { 'project:manage-members': true, 'project:configure': false } })
    const { el } = await render(<><Probe cap="project:create" /><Probe cap="deployment:read" /><Probe cap="project:manage-members" project="shop" /><Probe cap="project:configure" project="shop" /></>)
    await vi.waitFor(() => expect(el.textContent).toBe('yesnoyesno'))
  })
})
