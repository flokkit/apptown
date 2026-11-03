// @vitest-environment jsdom
import { act } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'
import { newQueryClient } from '../../data'
import { TechnicalDetailSwitch, TechnicalWords, useTechnicalDetail } from './TechnicalDetail'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
const settle = () => act(async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) })
function Line() { const [shown] = useTechnicalDetail(); return <p>{shown ? 'quick-change v17 · run 3' : 'What was made'}</p> }
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); localStorage.clear() })
it.each([false, true])('shares and remembers the viewer switch even when storage is unavailable (%s)', async unavailable => {
  const user = `technical-${unavailable}`
  if (unavailable) {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('Storage blocked') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Storage blocked') })
  }
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ user }))))
  const client = newQueryClient(), rootElement = document.createElement('div'), root = createRoot(rootElement)
  await act(async () => root.render(<QueryClientProvider client={client}><TechnicalDetailSwitch /><Line /></QueryClientProvider>))
  await settle()
  const control = rootElement.querySelector<HTMLButtonElement>('button.ch-goggles')!
  expect(control.getAttribute('aria-pressed')).toBe('false')
  expect(control.title).toBe('Techno Goggles')
  await act(async () => control.click())
  expect(rootElement.textContent).toContain('quick-change v17 · run 3')
  if (!unavailable) expect(localStorage.getItem(`apptown.technical-detail.${user}`)).toBe('1')
  await act(async () => root.unmount()); client.clear()
})
it('reads the saved preference for this viewer', async () => {
  localStorage.setItem('apptown.technical-detail.saved-tech-viewer', '1')
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ user: 'saved-tech-viewer' }))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  await act(async () => root.render(<QueryClientProvider client={client}><TechnicalDetailSwitch /></QueryClientProvider>))
  await settle()
  expect(el.querySelector('button.ch-goggles')?.getAttribute('aria-pressed')).toBe('true')
  await act(async () => root.unmount()); client.clear()
})

it('reserves the same inline words when hidden so toggling cannot reflow the page', () => {
  const element = (shown: boolean) => { const el = document.createElement('div'); el.innerHTML = renderToStaticMarkup(<TechnicalWords shown={shown}>quick-change v17 · run 3</TechnicalWords>); return el.firstElementChild as HTMLElement }
  const off = element(false), on = element(true)
  expect(off.tagName).toBe(on.tagName)
  expect(off.className).toBe(on.className)
  expect(off.textContent).toBe(on.textContent)
  expect(off.style.visibility).toBe('hidden')
  expect(off.getAttribute('aria-hidden')).toBe('true')
  expect(on.style.visibility).toBe('visible')
})
