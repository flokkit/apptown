// @vitest-environment jsdom
// @ts-expect-error -- only source reads need Node.
import { readFileSync, readdirSync } from 'node:fs'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import Console, { ChatPage } from './Console'
import ChangePage, { changeDefaultTab } from './ChangePage'
import { ConversationLog } from './change/Conversation'
import { withFeatures } from '../features.fixture'
import { getKey, newQueryClient } from '../data'
import type { ChangeView } from '../api'
import { PreviewBesideChat, canEmbedPreview } from './console/PreviewBesideChat'

vi.mock('./change/StatePanel', () => ({ StatePanel: () => <header>Change state</header> }))
vi.mock('./change/Overview', () => ({ ChangeOverview: () => null }))
vi.mock('./console/useTerminal', () => ({ useTerminal: () => 'attached' }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const view = (state = 'in_progress', decision = false) => ({ change: { state }, waitingOn: { you: decision ? 'yes' : 'no', kind: 'approval' } }) as ChangeView

it('defaults to Conversation only while work is open and no decision is due; explicit tabs win', () => {
 expect(changeDefaultTab('', view())).toBe('conversation')
 for (const state of ['shipped', 'abandoned', 'landing']) expect(changeDefaultTab('', view(state))).toBe('overview')
 expect(changeDefaultTab('', view('in_progress', true))).toBe('overview')
 expect(changeDefaultTab('overview', view())).toBe('overview')
 expect(changeDefaultTab('conversation', view('shipped'))).toBe('conversation')
})

it('replaces the old change chat address, retaining kickoff and the exact session', async () => {
 const el = document.createElement('div'); const root = createRoot(el)
 function Address() { const l = useLocation(); return <output>{l.pathname}{l.search}</output> }
 await act(async () => root.render(<MemoryRouter initialEntries={['/projects/s/console?change=X&kickoff=1&session=old']}><Routes><Route path="/projects/:slug/console" element={<Console />} /><Route path="/changes/:id" element={<Address />} /></Routes></MemoryRouter>))
 expect(el.textContent).toBe('/changes/X?tab=conversation&kickoff=1&session=old')
 act(() => root.unmount())
})

it('lists ended sessions and reads a selected session with pictures without opening or saying anything', async () => {
 const client = newQueryClient()
 const sessions = [{ id: 'old', ended: true, actor: 'sam', createdAt: '', messages: [{ i: 0, role: 'you', text: 'Earlier words (I pasted a picture — look at /console/uploads/picture-1.png)' }] }, { id: 'current', ended: false, messages: [] }]
 client.setQueryData(getKey('/changes/X/transcript'), { sessions, unreadable: 1 })
 const rows = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><ConversationLog changeId="X" live /></MemoryRouter></QueryClientProvider>)
 expect(rows).toContain('/changes/X?tab=conversation&amp;chat=old')
 expect(rows).not.toContain('chat=current')
 const fetcher = vi.fn(async (_url: unknown, _init?: RequestInit) => new Response(JSON.stringify({ sessions, unreadable: 1 })))
 vi.stubGlobal('fetch', fetcher)
 const el = document.createElement('div'); const root = createRoot(el)
 await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter><ConversationLog changeId="X" selectedChat="old" /></MemoryRouter></QueryClientProvider>))
 expect(el.textContent).toContain('Earlier words')
 expect(el.textContent).toContain('Back to the current chat')
 expect(el.textContent).toContain('some chats may be missing')
 expect(el.querySelector('img')?.getAttribute('src')).toBe('/api/v1/changes/X/transcript/old/pictures/picture-1.png')
 expect(fetcher.mock.calls.every(args => !(args[1] as RequestInit | undefined)?.method)).toBe(true)
 act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
})

it('offers only live https previews that are reachable away from the host, with a sandbox and built line', async () => {
 expect(canEmbedPreview({ state: 'live', url: 'https://app.preview.example.org' })).toBe(true)
 for (const st of [{ state: 'cold', url: 'https://app.preview.example.org' }, { state: 'live', url: 'http://host:8080' }, { state: 'live', url: 'https://host', hostOnly: true }]) expect(canEmbedPreview(st as Parameters<typeof canEmbedPreview>[0])).toBe(false)
 let desktop = true
 let onWidth = () => {}
 vi.stubGlobal('matchMedia', () => ({ get matches() { return desktop }, addEventListener(_event: string, listener: () => void) { onWidth = listener }, removeEventListener() {} }))
 localStorage.setItem('preview-beside-chat', '1')
 const client = newQueryClient(); client.setQueryData(getKey('/changes/X/preview'), { state: 'live', url: 'https://app.preview.example.org', built: '2026-11-13T00:00:00Z', commit: '123456789' })
 // A current read is required before an iframe is mounted; a cached reading alone offers nothing.
 const html = renderToStaticMarkup(<QueryClientProvider client={client}><PreviewBesideChat changeId="X">{toggle => <>{toggle}chat</>}</PreviewBesideChat></QueryClientProvider>)
 expect(html).not.toContain('<iframe')
 const fetcher = vi.fn(async () => new Response(JSON.stringify({ state: 'live', url: 'https://app.preview.example.org', built: '2026-11-13T00:00:00Z', commit: '123456789' })))
 vi.stubGlobal('fetch', fetcher)
 const el = document.createElement('div'); const root = createRoot(el)
 await act(async () => root.render(<QueryClientProvider client={client}><PreviewBesideChat changeId="X">{toggle => <>{toggle}chat</>}</PreviewBesideChat></QueryClientProvider>))
 await vi.waitFor(() => expect(el.querySelector('iframe')?.getAttribute('src')).toBe('https://app.preview.example.org'))
 expect(el.querySelector('iframe')?.getAttribute('sandbox')).toBe('allow-scripts allow-forms allow-same-origin allow-popups')
 expect(el.textContent).toContain('built from commit 1234567')
 expect(el.textContent).toContain('Open in new tab')
 act(() => { desktop = false; onWidth() })
 expect(el.querySelector('iframe')).toBeNull()
 expect(el.textContent).not.toContain('Preview beside chat')
 act(() => root.unmount()); vi.unstubAllGlobals()
 localStorage.clear(); client.clear()
})

it('embeds change chat without repeating the change title, state or Goes to line', () => {
 const client = newQueryClient()
 client.setQueryData(getKey('/changes/X'), { change: { id: 'X', number: 2, title: 'Title already above' }, goesTo: { branch: 'main', deployments: [] } })
 const html = renderToStaticMarkup(<QueryClientProvider client={client}><MemoryRouter><ChatPage slug="s" changeId="X" forcedSession="" kickoff="" /></MemoryRouter></QueryClientProvider>)
 expect(html).toContain('aria-label="your message"')
 expect(html).not.toContain('<h1')
 expect(html).not.toContain('Goes to:')
 client.clear()
})

it('has no in-app links to the retired change chat address', () => {
 const scan = (dir: string): string[] => readdirSync(dir, { withFileTypes: true }).flatMap((e: {name: string; isDirectory(): boolean}) => e.isDirectory() ? scan(`${dir}/${e.name}`) : /\.tsx?$/.test(e.name) && !e.name.includes('.test.') ? [readFileSync(`${dir}/${e.name}`, 'utf8')] : [])
 expect(scan('src').join('\n')).not.toMatch(/console\?change=/)
})

// Keep the page and session admission real: the page alone decides whether to
// mount the live opener or the selected history, and carries the kickoff guard.
it('opens the change Conversation with actions and starts the first build once across remounts; closed and selected chats never open', async () => {
 Element.prototype.scrollIntoView = () => {}
 localStorage.clear(); sessionStorage.clear()
 const session = { id: 'current', changeId: 'X', mayAct: true, state: 'detached', openedBy: 'sam' }
 let state = 'in_progress'
 const writes: string[] = []
 const history = { sessions: [{ id: 'old', ended: true, actor: 'sam', createdAt: '', messages: [{ i: 0, role: 'you', text: 'Recorded words' }] }, { id: 'current', ended: false, actor: 'sam', createdAt: '', messages: [] }] }
 vi.stubGlobal('fetch', vi.fn(withFeatures(async (url: string, init?: RequestInit) => {
  const path = String(url)
  if (init?.method === 'POST') writes.push(path)
  const body = path === '/api/v1/changes/X' ? { ...view(state), change: { id: 'X', state, title: 'Build a calendar', body: 'A shared calendar', number: 1 }, projectSlug: 's', runs: [], goesTo: { branch: 'main', deployments: [] } }
   : path.endsWith('/transcript') ? history
   : path === '/api/v1/projects/s/console' ? { session, sessions: [] }
   : path.endsWith('/messages') ? { messages: [] }
   : path.endsWith('/changes') ? { changes: [] } : {}
  return new Response(JSON.stringify(body))
 })))
 const mount = async (address: string) => {
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[address]}><Routes><Route path="/changes/:id" element={<ChangePage />} /></Routes></MemoryRouter></QueryClientProvider>))
  return { el, done: () => { act(() => root.unmount()); client.clear() } }
 }
 try {
  let page = await mount('/changes/X?tab=conversation&kickoff=1')
  await vi.waitFor(() => expect(writes).toContain('/api/v1/console/current/say'))
  expect(page.el.querySelector('textarea[aria-label="your message"]')).not.toBeNull()
  expect(page.el.textContent).toContain('Try it')
  expect(page.el.textContent).toContain('Check and land')
  expect(page.el.querySelector('a[href="/changes/X?tab=conversation&chat=old"]')).not.toBeNull()
  page.done()
  page = await mount('/changes/X?tab=conversation&kickoff=1')
  await vi.waitFor(() => expect(page.el.textContent).toContain('Check and land'))
  expect(writes.filter(p => p.endsWith('/say'))).toHaveLength(1)
  page.done(); writes.length = 0
  for (const address of ['/changes/X?tab=conversation&chat=old&kickoff=1&session=current', '/changes/X?tab=conversation']) {
   if (!address.includes('chat=old')) state = 'shipped'
   page = await mount(address)
   await vi.waitFor(() => expect(page.el.textContent).toContain('Recorded words'))
   expect(page.el.querySelector('textarea')).toBeNull()
   expect(writes).toEqual([])
   page.done()
  }
 } finally { vi.unstubAllGlobals(); localStorage.clear(); sessionStorage.clear() }
})
