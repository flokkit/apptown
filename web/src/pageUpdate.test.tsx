// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, useNavigate } from 'react-router-dom'
import { get } from './api'
import { ENTRY_HEADER, pageEntry, reloadForUpdate, served, updated, type ReloadEnv } from './pageUpdate'
import { UpdateNote } from './UpdateNote'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const OLD = '/assets/index-Old111.js'
const NEW = '/assets/index-New222.js'

function loadedFrom(src: string) {
  document.head.innerHTML = `<script type="module" crossorigin src="${src}"></script>`
}

function answering(entry: string) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 200, headers: { [ENTRY_HEADER]: entry } })))
}

function fakeEnv(): ReloadEnv & { reloads: number; store: Map<string, string> } {
  const store = new Map<string, string>()
  const env = {
    store, reloads: 0,
    storage: { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, v) } },
    reload: () => { env.reloads++ },
  }
  return env
}

afterEach(() => { vi.unstubAllGlobals(); sessionStorage.clear(); served.set(''); document.head.innerHTML = '' })

describe('a tab opened before AppTown was updated', () => {
  it('learns from any response that the server now serves another page', async () => {
    loadedFrom(OLD)
    answering(NEW)
    await get('/me')
    expect(updated()).toBe(true)
  })

  it('stays quiet while the server serves the page it runs', async () => {
    loadedFrom(OLD)
    answering(OLD)
    await get('/me')
    expect(updated()).toBe(false)
  })

  it('never compares a development page, which runs source no build names', () => {
    loadedFrom('/src/main.tsx')
    expect(pageEntry()).toBe('')
    expect(updated('', NEW)).toBe(false)
  })

  it('reloads once for a new build, and leaves it to the person when the reload still lands on the old page', () => {
    const env = fakeEnv()
    expect(reloadForUpdate(OLD, NEW, env)).toBe(true)
    expect(reloadForUpdate(OLD, NEW, env)).toBe(false)
    expect(env.reloads).toBe(1)
    expect(reloadForUpdate(OLD, '/assets/index-Later333.js', env)).toBe(true)
    expect(env.reloads).toBe(2)
  })

  it('does not reload without storage, since it could not stop a loop', () => {
    expect(reloadForUpdate(OLD, NEW, { storage: null, reload: () => { throw new Error('reloaded') } })).toBe(false)
  })
})

describe('the note', () => {
  function mount() {
    let go: (to: string) => void = () => {}
    function Nav() { go = useNavigate(); return null }
    const el = document.createElement('div')
    const root = createRoot(el)
    act(() => root.render(<MemoryRouter initialEntries={['/changes/1']}><UpdateNote /><Nav /></MemoryRouter>))
    return { el, root, go: (to: string) => act(() => go(to)) }
  }

  it('says AppTown was updated, and reloads only when the person goes to another page', () => {
    loadedFrom(OLD)
    const reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })
    const { el, root, go } = mount()
    expect(el.textContent).toBe('')
    act(() => served.set(NEW))
    expect(el.textContent).toContain('was updated since this page opened')
    expect(reload).not.toHaveBeenCalled() // the page in hand, and any form on it, stays
    go('/projects')
    expect(reload).toHaveBeenCalledTimes(1)
    act(() => root.unmount())
  })

  it('shows nothing when the server serves the page this tab runs', () => {
    loadedFrom(OLD)
    const { el, root } = mount()
    act(() => served.set(OLD))
    expect(el.textContent).toBe('')
    act(() => root.unmount())
  })
})
