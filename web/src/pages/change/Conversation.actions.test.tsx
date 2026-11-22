// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { getKey, newQueryClient } from '../../data'
import { ChatMessages } from './Conversation'
import { Bubbles } from '../console/Bubbles'
import { AgentActionsSwitch, useAgentActions } from '../../AgentActions'
import type { ChatHistoryEntry, Message } from '../../wire.gen'
import { chatStreamOf } from '../console/chatStream.fixture'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const messages = [
  { i: 0, role: 'you', text: 'Make it blue.' },
  { i: 1, role: 'helper', text: 'I will read the page.', tools: [{ name: 'read', detail: 'index.html' }] },
  { i: 2, role: 'helper', tools: [{ name: 'bash', detail: 'git diff' }] },
  { i: 3, role: 'helper', tools: [{ name: 'read', detail: 'screen.png' }] },
  { i: 4, role: 'helper', text: 'The page is blue. Tests passed.' },
  { i: 5, role: 'you', text: 'Thanks.' },
  { i: 6, role: 'helper', tools: [{ name: 'write', detail: 'notes.md' }] },
  { i: 7, role: 'helper', text: 'Saved the notes.' },
] as ChatHistoryEntry['messages']
const cleanups: (() => void)[] = []
afterEach(() => { cleanups.splice(0).forEach(f => f()); localStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
function LiveSwitch() {
  const [shown, setShown] = useAgentActions()
  return <AgentActionsSwitch shown={shown} onChange={setShown} />
}
async function mount(live = false, viewer = 'sam') {
  Element.prototype.scrollIntoView = () => {}
  vi.stubGlobal('fetch', vi.fn(async (url: string) => String(url).includes('/stream')
    ? chatStreamOf(messages as Message[], 'handed-back')
    : new Response(JSON.stringify(String(url).endsWith('/auth/me') ? { mode: 'internal', user: viewer } : {}))))
  const client = newQueryClient()
  client.setQueryData(getKey('/auth/me'), { mode: 'internal', user: viewer })
  const el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}>{live
    // The live chat's switch is in its page's menu; it shares the viewer's choice.
    ? <><LiveSwitch /><Bubbles sessionId="test" /></>
    : <ChatMessages chat={{ messages } as ChatHistoryEntry} pictures="/pictures/" />}</QueryClientProvider>))
  cleanups.push(() => { act(() => root.unmount()); client.clear() })
  if (live) await vi.waitFor(() => expect(el.textContent).toContain('Tests passed.'))
  return el
}
const inline = (el: HTMLElement) => [...el.querySelectorAll('.steplist.inline li')].map(li => `${li.querySelector('.verb')!.textContent} ${li.querySelector('code')!.textContent}`)
for (const live of [false, true]) {
  it(`${live ? 'live chat' : 'Conversation'} shows each run of steps as one pill by default, keeps replies and obeys the action switch`, async () => {
    const el = await mount(live, `fold-${live}`)
    const toggle = el.querySelector<HTMLButtonElement>('[role="switch"]')!
    expect(toggle).not.toBeNull()
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    expect(el.textContent).toContain('Make it blue.')
    expect(el.textContent).toContain('I will read the page.')
    expect(el.textContent).toContain('The page is blue. Tests passed.')
    expect(el.textContent).toContain('Saved the notes.')
    const pills = el.querySelectorAll('.steps-pill')
    expect(pills).toHaveLength(2)
    expect(pills[0].querySelector('.what')?.textContent).toBe('read 2 files, ran 1 command')
    expect(pills[0].querySelector('.n')?.textContent).toBe('3 steps')
    expect(el.querySelector('details')).toBeNull()
    expect(inline(el)).toHaveLength(0)
    await act(async () => toggle.click())
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    // Every step, in record order, with its command or file.
    expect(inline(el)).toEqual(['read index.html', 'ran git diff', 'read screen.png', 'wrote notes.md'])
    await act(async () => toggle.click())
    expect(inline(el)).toHaveLength(0)
  })
}
it('remembers the switch across chat views for each viewer', async () => {
  const sam = await mount(false, 'remember-sam')
  await act(async () => sam.querySelector<HTMLButtonElement>('[role="switch"]')!.click())
  const sameViewer = await mount(true, 'remember-sam')
  expect(sameViewer.querySelector<HTMLButtonElement>('[role="switch"]')!.getAttribute('aria-checked')).toBe('true')
  const otherViewer = await mount(false, 'remember-priya')
  expect(otherViewer.querySelector<HTMLButtonElement>('[role="switch"]')!.getAttribute('aria-checked')).toBe('false')
  expect(Object.keys(localStorage).some(k => k.includes('remember-sam'))).toBe(true)
})
it('works when localStorage refuses reads and writes', async () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
  const el = await mount(false, 'no-storage')
  const toggle = el.querySelector<HTMLButtonElement>('[role="switch"]')!
  expect(toggle.getAttribute('aria-checked')).toBe('false')
  await act(async () => toggle.click())
  expect(toggle.getAttribute('aria-checked')).toBe('true')
  expect(inline(el)).toHaveLength(4)
})

it('reads a viewer preference already saved in localStorage', async () => {
  localStorage.setItem('apptown.show-agent-actions.saved-viewer', '1')
  const el = await mount(false, 'saved-viewer')
  expect(el.querySelector<HTMLButtonElement>('[role="switch"]')!.getAttribute('aria-checked')).toBe('true')
  expect(inline(el)).toHaveLength(4)
})

it('keeps a long agent session reachable in order behind one pill', async () => {
  const original = [...messages]
  const actions = Array.from({ length: 120 }, (_, i) => ({ i: i + 1, role: 'helper', tools: [{ name: i % 2 ? 'read' : 'bash', detail: `action-${i}` }] }))
  messages.splice(0, messages.length, { i: 0, role: 'you', text: 'Make it blue.' }, ...actions, { i: 121, role: 'helper', text: 'Final report: done.' })
  try {
    const el = await mount(false, 'long-session')
    expect(el.querySelectorAll('.steps-pill')).toHaveLength(1)
    expect(el.querySelector('.steps-pill .what')?.textContent).toBe('ran 60 commands, read 60 files')
    expect(el.querySelector('.steps-pill .n')?.textContent).toBe('120 steps')
    expect(el.textContent).toContain('Final report: done.')
    expect(inline(el)).toHaveLength(0)
    await act(async () => el.querySelector<HTMLButtonElement>('[role="switch"]')!.click())
    expect(inline(el)).toEqual(actions.map((_, i) => `${i % 2 ? 'read' : 'ran'} action-${i}`))
  } finally { messages.splice(0, messages.length, ...original) }
})
