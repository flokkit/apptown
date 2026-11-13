// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes, useNavigate, type NavigateFunction } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { getKey, newQueryClient } from '../data'
import Project from './Project'
import TopBar from '../TopBar'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const project = (id: number, slug: string, name: string) =>
  ({ id: `00000000-${String(id).padStart(4, '0')}-7000-8000-000000000000`, slug, name, namespace: '', icon: '', blurb: '', createdAt: '', requireIndependentApproval: false, outOfBandEdits: [] })

// The project page stays mounted when the sidebar moves to another project.
// Save sends every field, so a draft started on one project must never be
// what the next project's form shows or saves. Today the page shows loading
// while it reads the next project, which unmounts the settings tabs and drops
// the draft; this holds that behaviour in place.
it('moving to another project shows that project, not the last one\'s draft', async () => {
  const projects: Record<string, ReturnType<typeof project>> = { a: project(1, 'a', 'Alpha'), b: project(2, 'b', 'Bravo') }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const m = url.match(/\/projects\/([a-z]+)$/)
    if (m) return new Response(JSON.stringify(projects[m[1]]))
    return new Response(url.endsWith('/projects') || url.endsWith('/deployments') ? '[]' : '{}')
  }))
  let navigate: NavigateFunction = () => {}
  function Nav() { navigate = useNavigate(); return null }
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const nameInput = () => el.querySelector('fieldset input') as HTMLInputElement
  try {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={['/projects/a/settings/general']}>
            <Nav />
            <Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes>
          </MemoryRouter>
        </QueryClientProvider>)
    })
    await vi.waitFor(() => expect(nameInput()?.value).toBe('Alpha'))
    await vi.waitFor(() => expect(el.querySelector('fieldset')!.disabled).toBe(false))
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(nameInput(), 'Alpha renamed')
      nameInput().dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => { navigate('/projects/b/settings/general') })
    await vi.waitFor(() => expect(nameInput()?.value).toBe('Bravo'))
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

// A failed refresh of the project keeps the page mounted under a stale note,
// so the settings form and what someone typed into it survive a network
// blip (web review B14, finding 1: the error used to replace the page).
it('a draft in the settings form survives a failed refresh of the project', async () => {
  const alpha = project(1, 'a', 'Alpha')
  let down = false
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (down) return new Response(JSON.stringify({ error: 'AppTown is restarting' }), { status: 502 })
    if (/\/projects\/a$/.test(url)) return new Response(JSON.stringify(alpha))
    return new Response(url.endsWith('/projects') || url.endsWith('/deployments') ? '[]' : '{}')
  }))
  const client = newQueryClient()
  const el = document.createElement('div')
  const root = createRoot(el)
  const nameInput = () => el.querySelector('fieldset input') as HTMLInputElement
  try {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={['/projects/a/settings/general']}>
            <Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes>
          </MemoryRouter>
        </QueryClientProvider>)
    })
    await vi.waitFor(() => expect(nameInput()?.value).toBe('Alpha'))
    await vi.waitFor(() => expect(el.querySelector('fieldset')!.disabled).toBe(false))
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(nameInput(), 'Alpha renamed')
      nameInput().dispatchEvent(new Event('input', { bubbles: true }))
    })
    down = true
    await act(async () => { await client.refetchQueries({ queryKey: getKey('/projects/a') }).catch(() => {}) })
    await vi.waitFor(() => expect(el.textContent).toContain('unknown'))
    expect(nameInput()?.value).toBe('Alpha renamed')
  } finally {
    act(() => root.unmount())
    client.clear()
    vi.unstubAllGlobals()
  }
})

// Open chats have one continuation. Completed chats do not become a second
// history section underneath the project's activity record.
it.each([true, false])('shows open chats without a Past chats section (chat=%s)', async chat => {
  const alpha = project(1, 'a', 'Alpha')
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const body = url.endsWith('/projects/a') ? alpha
      : url.endsWith('/overview') ? { active: [], history: [], open: chat ? [{ changeId: 'chat', changeNumber: 3, waitingOn: { kind: 'chat', says: 'chat open', tone: 'neutral', you: 'no' }, title: 'Add lunch orders', state: 'in_progress', statusLabel: 'waiting' }] : [] }
      : url.endsWith('/schedules') ? { schedules: [] }
      : url.endsWith('/transcript') ? { sessions: [{ id: 'past', title: 'A completed chat' }] }
      : url.endsWith('/release/plan') ? { configured: false }
      : []
    return new Response(JSON.stringify(body))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/a/changes']}><Routes><Route path="/projects/:slug/:tab" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain(chat ? 'Add lunch orders' : 'No open changes.'))
    if (chat) {
      expect(el.querySelector('.row-status .chip')?.textContent).toContain('chat open')
      expect(el.querySelector('.row-action a')?.textContent).toBe('Continue chat')
      expect(el.querySelector('.row-action a')?.getAttribute('href')).toBe('/changes/chat?tab=conversation')
    }
    expect(el.textContent).not.toContain('Past chats')
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith('/transcript'))).toBe(false)
  } finally {
    act(() => root.unmount()); client.clear(); vi.unstubAllGlobals()
  }
})

// Public app addresses omit the scheme in text and keep it in the safe new-tab link.
it('shows the public app address on Overview without notes or a Changes link', async () => {
  const url = 'https://alpha.apps.example.test/public/path'
  const label = 'alpha.apps.example.test/public/path'
  vi.stubGlobal('fetch', vi.fn(async (path: string) => new Response(JSON.stringify(
    path.endsWith('/projects/a') ? { ...project(1, 'a', 'Alpha'), blurb: 'Our app' }
      : path.endsWith('/overview') ? { active: [], history: [], open: [], notes: { present: true, commit: 'abc', notes: [{ text: 'Credit photos.', author: 'Sam', change: 4, line: '- Credit photos. (Sam · change 4)', changeId: 'source-change', acceptedBy: ['Priya'] }] } }
      : path.endsWith('/deployments') ? [{ id: 'main', name: 'main', url, running: true, status: { word: 'working', reason: '', stale: false }, observation: 'running' }]
      : path.endsWith('/scans') ? { queues: [], scans: { main: "", source: [], running: [], historical: [], known: false, stale: false, lastScan: "" }, recent: [] } : path.endsWith('/schedules') ? { schedules: [] } : []
  ))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/a']}><Routes><Route path="/projects/:slug" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('.application-heading .app-address')?.textContent).toBe(label))
    const address = el.querySelector('.application-heading .app-address') as HTMLAnchorElement
    expect(el.querySelector('a[href="/projects/a/changes"]')).toBeNull()
    expect(el.textContent).not.toContain('Credit photos.')
    expect(el.textContent).not.toContain('notes')
    expect(el.textContent).not.toContain('No open changes.')
    expect(address.tagName).toBe('A')
    expect(address.getAttribute('href')).toBe(url)
    expect(address.target).toBe('_blank')
    expect(address.rel.split(' ')).toContain('noopener')
    expect(address.previousElementSibling?.textContent).toBe('Our app')
    expect([...el.querySelectorAll('.app-address')].map(a => a.textContent)).toEqual([label])
    // The deployment row opens the same address; it is the link's title.
    expect(el.querySelector('a.fleet-open')?.getAttribute('href')).toBe(url)
    expect(el.querySelector('a.fleet-open')?.getAttribute('title')).toBe(url)
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// Every page reachable from the project menu explains itself beside its
// title, including when its content cannot be read.
it.each([
  ['', 'Overview', null, 'changes-and-main'],
  ['security', 'Security findings', 'Security', 'monitoring'],
  ['security/suppressions', 'Suppressions', 'Security', 'glossary'],
  ['settings/general', 'General', 'Settings', 'project-settings'],
  ['settings', 'General', 'Settings', 'project-settings'],
  ['settings/models', 'Models', 'Settings', 'architecture'],
  ['settings/working', 'Way of working', 'Settings', 'packs-and-what-if'],
  ['settings/services', 'Services', 'Settings', 'services-and-data'],
  ['settings/secrets', 'Secrets', 'Settings', 'secrets'],
  ['settings/members', 'Members', 'Settings', 'people-and-roles'],
  ['memory/notes', 'Notes', 'Memory', 'project-notes'],
  ['memory/decisions', 'Decisions', 'Memory', 'glossary'],
  ['settings/usage', 'Usage', 'Settings', 'monitoring'],
])('has page help at /projects/a/%s', async (path, title, area, guide) => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith('/projects/a')
    ? new Response(JSON.stringify(project(1, 'a', 'Alpha')))
    : new Response(JSON.stringify({ error: 'Unavailable' }), { status: 503 })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  document.body.append(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/projects/a/${path}`]}><Routes><Route path="/projects/:slug/:tab?/:stab?" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    const button = await vi.waitFor(() => {
      const b = el.querySelector<HTMLButtonElement>(`button[aria-label="About ${title}"]`)
      expect(b).not.toBeNull()
      return b!
    })
    expect([...el.querySelectorAll('h1')].map(h => h.textContent)).toEqual([area ? title : 'Alpha'])
    expect(el.querySelector('.page-area')).toBeNull()
    void area
    expect(el.querySelector('.page-help-pop')).toBeNull()
    act(() => button.click())
    const popover = el.querySelector('.page-help-pop')!
    expect(popover.querySelector('a')?.getAttribute('href')).toBe(`/docs/guides/${guide}`)
    if (title === 'Overview') {
      expect(popover.textContent).toContain('recent activity and deployments')
      expect(popover.textContent).toContain('Open Changes')
      expect(popover.textContent).not.toContain('notes')
    }
    if (title === 'Notes') {
      expect(popover.textContent).toContain('PROJECT-NOTES.md')
      expect(popover.textContent).toContain('every session')
      expect(popover.textContent).toContain('an author and a change number')
      expect(popover.textContent).toContain("can't count a note alone as proof")
    }
    if (title === 'Decisions') {
      expect(popover.textContent).toContain('what this project chose and why')
      expect(popover.textContent).toContain('status')
    }
    if (title === 'Secrets') {
      expect(popover.textContent).toContain('Secrets are values your app needs but should not be part of the code: passwords, API keys, signing keys. AppTown keeps them and gives them to the app when it runs.')
      expect(popover.textContent).toContain('The app\'s code lists the secrets it needs, in its secretspec.toml file.')
      expect(popover.textContent).toContain('Entered values will not be shown again.')
    }
    const link = popover.querySelector('a')!
    act(() => link.focus())
    act(() => link.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(el.querySelector('.page-help-pop')).toBeNull()
    expect(document.activeElement).toBe(button)
  } finally { act(() => root.unmount()); el.remove(); client.clear(); vi.unstubAllGlobals() }
})

it('shows secret bindings and warnings without the introduction or secret count', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const data = url.endsWith('/projects/a') ? project(1, 'a', 'Alpha')
      : url.endsWith('/secrets') ? {
        declared: true, spec: 'secretspec.toml', project: 'a', stores: [], profiles: ['default'],
        ignored: ['providers'], secrets: [{ name: 'API_KEY', allParts: true }],
        deployments: [{ name: 'main', profile: 'default', missing: ['API_KEY'], secrets: [{ name: 'API_KEY', required: true, bound: false }] }],
      } : {}
    return new Response(JSON.stringify(data))
  }))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/a/settings/secrets']}><Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('API_KEY'))
    expect(el.textContent).not.toContain('declares 1 secret')
    expect(el.textContent).not.toContain('A value you put here')
    expect(el.querySelector('.section-count')).toBeNull()
    expect(el.textContent).toContain('does not use these parts of the declaration: providers')
    expect(el.textContent).toContain('Nothing provides API_KEY here.')
    expect(el.querySelector('select[aria-label="Secrets profile for main"]')).toBeNull()
    expect(el.querySelector('a[href="/projects/a/deployments/main"]')).not.toBeNull()
    act(() => [...el.querySelectorAll('button')].find(b => b.textContent === 'Set…')!.click())
    expect(el.querySelector('textarea')).not.toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// These sub-pages share the project context but do not use PageHead.
it.each([
  ['changes', 'Changes'], ['deployments', 'Deployments'],
  ['releases/2', 'Release #2'],
])('has one page heading at /projects/a/%s', async (path, title) => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url.endsWith('/projects/a')
    ? new Response(JSON.stringify(project(1, 'a', 'Alpha')))
    : new Response(JSON.stringify({ error: 'Unavailable' }), { status: 503 })))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/projects/a/${path}`]}><Routes><Route path="/projects/:slug/:tab/:stab?" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect([...el.querySelectorAll('h1')].map(h => h.textContent)).toEqual([title]))
    expect(el.querySelector('.project-heading')).toBeNull()
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it.each([false, true])('Memory Notes keeps attributed notes and governed actions (present=%s)', async present => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/projects/a') ? project(1, 'a', 'Alpha')
      : { notes: { present, commit: 'abc', notes: present ? [{ text: 'Credit photos.', author: 'Sam', change: 4, line: '- Credit photos. (Sam · change 4)', changeId: 'source-change', acceptedBy: ['Priya'] }] : [] } }
  ))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/a/memory/notes']}><Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.textContent).toContain('What agents know about this project'))
    if (present) {
      expect(el.textContent).toContain('Credit photos.')
      expect(el.textContent).toContain('Sam · change 4')
      expect(el.textContent).toContain('Accepted by Priya')
      expect(el.querySelector('section a')?.getAttribute('href')).toBe('/changes/source-change')
      expect(el.textContent).toContain('Add a note')
    } else {
      expect(el.textContent).toContain('No notes yet')
      expect(el.textContent).toContain("Write this project's notes")
    }
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

// The loaded Memory page must keep its project context without introducing
// a second page title; the camera requires exactly one level-one heading.
it.each([['notes', 'Notes'], ['decisions', 'Decisions']])('loaded Memory %s has a labelled breadcrumb and one page title', async (tab, title) => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(
    url.endsWith('/projects/a') ? project(1, 'a', 'Alpha')
      : url.endsWith('/overview') ? { notes: { present: false, notes: [] } } : []
  ))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[`/projects/a/memory/${tab}`]}>
      <TopBar me={null} /><main><Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes></main>
    </MemoryRouter></QueryClientProvider>))
    await vi.waitFor(() => expect(el.querySelector('main h1')?.textContent).toBe(title))
    expect(el.querySelector('.topbar-crumb:last-child')?.textContent).toBe('›Memory')
    expect([...el.querySelectorAll('main h1')].map(h => h.textContent)).toEqual([title])
    expect(el.querySelector('main .project-heading')).toBeNull()
    // The page's own list loads after its heading; wait for it rather than
    // reading it on the heading's frame.
    await vi.waitFor(() => expect(el.textContent).toContain(tab === 'notes' ? 'No notes yet' : 'No decisions recorded yet.'))
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})

it('does not keep the removed Settings Deployments page as a second door', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(project(1, 'a', 'Alpha')))))
  const client = newQueryClient(), el = document.createElement('div'), root = createRoot(el)
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/projects/a/settings/deployments']}><Routes><Route path="/projects/:slug/:tab/:stab" element={<Project />} /></Routes></MemoryRouter></QueryClientProvider>))
    expect(el.querySelector('h1')?.textContent).toBe('Page not found')
    expect(el.textContent).not.toContain('New deployment')
  } finally { act(() => root.unmount()); client.clear(); vi.unstubAllGlobals() }
})
