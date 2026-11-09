// @vitest-environment jsdom
// @ts-expect-error -- this package has no Node types; only this read is used.
import { readdirSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { PAGE_HELP, PageHead, SUB_HELP } from './PageHead'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function render(node: React.ReactNode): HTMLDivElement {
  const el = document.createElement('div')
  document.body.appendChild(el)
  act(() => { createRoot(el).render(<MemoryRouter>{node}</MemoryRouter>) })
  return el
}

const q = (el: HTMLElement) => el.querySelector<HTMLButtonElement>('button.page-help-q')!
const pop = (el: HTMLElement) => el.querySelector('.page-help-pop')
const down = (target: Element) => act(() => { target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })) })

describe('page help', () => {
  afterEach(() => { document.body.innerHTML = '' })

  it('shows the title with introductions behind help', () => {
    const el = render(<PageHead page="deployments" />)
    expect(el.querySelector('h1')?.textContent).toBe('Deployments')
    expect(el.textContent).not.toContain(PAGE_HELP.deployments.line)
    expect(q(el).getAttribute('aria-expanded')).toBe('false')
    expect(pop(el)).toBeNull()
  })

  it('opens from the "?", which says it is open and names what it opens', () => {
    const el = render(<PageHead page="deployments" />)
    act(() => q(el).click())
    expect(q(el).getAttribute('aria-expanded')).toBe('true')
    expect(q(el).getAttribute('aria-controls')).toBe(pop(el)?.id)
    expect(pop(el)?.textContent).toContain(PAGE_HELP.deployments.line)
    expect(pop(el)?.textContent).toContain('follows main, promotion from another deployment, or by hand')
    expect(pop(el)?.querySelector('a')?.getAttribute('href')).toBe('/docs/guides/container-lifecycle')
  })

  // Focus inside the popover would be lost when it closes; it goes back to
  // the "?" so a keyboard user carries on from where they opened it.
  it('closes on Escape and hands focus back to the "?"', () => {
    const el = render(<PageHead page="deployments" />)
    act(() => q(el).click())
    const link = pop(el)!.querySelector('a')!
    act(() => link.focus())
    act(() => { link.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })) })
    expect(pop(el)).toBeNull()
    expect(document.activeElement).toBe(q(el))
  })

  it('closes on a click outside and stays open on a click inside', () => {
    const el = render(<PageHead page="deployments" />)
    act(() => q(el).click())
    down(pop(el)!)
    expect(pop(el)).not.toBeNull()
    down(el.querySelector('h1')!)
    expect(pop(el)).toBeNull()
  })

  it('keeps the page actions in the title row', () => {
    const el = render(<PageHead page="evals" actions={<button>Start an eval</button>} />)
    expect(el.querySelector('.page-head-row')?.textContent).toContain('Start an eval')
  })

  // On a phone the popover would lie over what the page shows; the "?" opens
  // a dialog with a close button instead, and nothing opens by itself.
  it('opens a dialog with a close button on a phone', () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    try {
      const el = render(<PageHead page="deployments" />)
      expect(document.querySelector('[role="dialog"]')).toBeNull()
      act(() => q(el).click())
      const dialog = document.querySelector('[role="dialog"]')!
      expect(dialog.textContent).toContain(PAGE_HELP.deployments.line)
      expect(dialog.querySelector('a')?.getAttribute('href')).toBe('/docs/guides/container-lifecycle')
      expect(pop(el)).toBeNull()
      act(() => dialog.querySelector<HTMLButtonElement>('button[aria-label="close"]')!.click())
      expect(document.querySelector('[role="dialog"]')).toBeNull()
    } finally { vi.unstubAllGlobals() }
  })
})

// Every page's "Read more" opens a guide the binary ships, and its line
// stays one sentence: the explanation lives behind the "?", not above the
// page. Read from disk as docsLinks.test.ts does; vitest runs from web/.
describe('every page help entry', () => {
  const guides = new Set((readdirSync('../stock/docs/guides') as string[]).filter((f) => f.endsWith('.md')).map((f) => f.replace(/\.md$/, '')))
  for (const [page, h] of Object.entries(PAGE_HELP)) {
    it(`${page} links a shipped guide and has at most a one-sentence line`, () => {
      expect(guides).toContain(h.guide)
      const line: string | undefined = 'line' in h ? h.line : undefined
      if (line !== undefined) expect(line.match(/[.!?](\s|$)/g) ?? []).toHaveLength(1)
    })
  }
})

// A page inside an area says what it is for, not the area's help, and its
// "Read more" opens a shipped guide.
describe('every area page help entry', () => {
  const guides = new Set((readdirSync('../stock/docs/guides') as string[]).filter((f) => f.endsWith('.md')).map((f) => f.replace(/\.md$/, '')))
  for (const [area, pages] of Object.entries(SUB_HELP)) {
    for (const [title, h] of Object.entries(pages ?? {})) {
      it(`${area} › ${title} links a shipped guide`, () => { expect(guides).toContain(h.guide) })
    }
  }
})

it("shows the Backups page's own help", () => {
  const el = document.createElement('div'); document.body.append(el)
  const root = createRoot(el)
  act(() => root.render(<MemoryRouter><PageHead page="backups" /></MemoryRouter>))
  act(() => el.querySelector<HTMLButtonElement>('.page-help-q')!.click())
  const text = el.querySelector('.page-help-pop')!.textContent
  expect(text).toContain("Saved data")
  expect(text).not.toContain(PAGE_HELP.deployments.line)
  expect(el.querySelector('.page-help-pop a')?.getAttribute('href')).toBe('/docs/guides/container-lifecycle')
  act(() => root.unmount()); el.remove()
})

it('explains Memory as project context and leaves decisions out of Settings help', () => {
  const memory = render(<PageHead page="projectMemory" />)
  act(() => q(memory).click())
  expect(pop(memory)?.textContent).toContain('what agents carry into their work on this project')
  expect(pop(memory)?.querySelector('a')?.getAttribute('href')).toBe('/docs/guides/project-notes')
  const settings = render(<PageHead page="projectSettings" />)
  act(() => q(settings).click())
  expect(pop(settings)?.textContent).not.toContain('decisions')
  expect(SUB_HELP.projectSettings).not.toHaveProperty('Decisions')
})
