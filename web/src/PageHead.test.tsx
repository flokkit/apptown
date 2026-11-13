// @vitest-environment jsdom
// @ts-expect-error -- this package has no Node types; only this read is used.
import { readdirSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { renderToStaticMarkup } from 'react-dom/server'
import { PAGE_HELP, PageHead, SUB_HELP } from './PageHead'
import { INTEGRATION_MENU } from './pages/integrations/kinds'

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

  // Help describes the page it is on: the marks the rows carry, the actions
  // the page has.
  it('names the pack mark the rows carry, and gives an account its own help', () => {
    const packs = render(<PageHead page="waysOfWorking" sub="Packs" />)
    act(() => q(packs).click())
    expect(pop(packs)?.textContent).toContain('marked yours')
    expect(pop(packs)?.textContent).not.toContain('marked AppTown')
    document.body.innerHTML = ''
    const account = render(<PageHead page="people" sub="Account" title="sam" />)
    act(() => q(account).click())
    expect(pop(account)?.textContent).not.toContain('Invite someone')
    expect(pop(account)?.textContent).toContain('resets their password')
  })

  it('shows the title with introductions behind help', () => {
    const el = render(<PageHead page="deployments" />)
    expect(el.querySelector('h1')?.textContent).toBe('All deployments')
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

// A conformance page's help is about that page (its remediations, its
// check), not the area's summary of reviews.
it.each([['Remediations', 'A remediation fixes findings'], ['this check', "This check's result in each project"]])('shows the conformance %s page its own help', (sub, words) => {
  const el = document.createElement('div'); document.body.append(el)
  const root = createRoot(el)
  act(() => root.render(<MemoryRouter><PageHead page="conformance" title="Remediations" sub={sub} /></MemoryRouter>))
  act(() => el.querySelector<HTMLButtonElement>('.page-help-q')!.click())
  const text = el.querySelector('.page-help-pop')!.textContent
  expect(text).toContain(words)
  expect(text).not.toContain(PAGE_HELP.conformance.line)
  act(() => root.unmount()); el.remove()
})

it('explains Memory as project context and leaves decisions out of Settings help', () => {
  const memory = render(<PageHead page="projectMemory" />)
  act(() => q(memory).click())
  expect(pop(memory)?.textContent).toContain('what helpers carry into their work on this project')
  expect(pop(memory)?.querySelector('a')?.getAttribute('href')).toBe('/docs/guides/project-notes')
  const settings = render(<PageHead page="projectSettings" />)
  act(() => q(settings).click())
  expect(pop(settings)?.textContent).not.toContain('decisions')
  expect(SUB_HELP.projectSettings).not.toHaveProperty('Decisions')
})

// The help text a person reads, as plain text, for one page or one area page.
function helpText(node: React.ReactNode): string {
  return renderToStaticMarkup(<MemoryRouter>{node}</MemoryRouter>).replace(/<[^>]+>/g, '').replace(/&#x27;/g, "'").replace(/&amp;/g, '&')
}

// Help speaks in the reader's words: no internal terms or syntax a person
// has nowhere to type, and every fact the old wording carried stays.
describe('help wording', () => {
  const all = [...Object.values(PAGE_HELP).map((h) => h.help), ...Object.values(SUB_HELP).flatMap((p) => Object.values(p ?? {}).map((h) => h.body))]
  it.each(['Previews run on the', 'group:name', 'fingerprint', 'the gate', 'prices pi', 'claims, never evidence', '$0'])('no help says "%s"', (old) => {
    for (const h of all) expect(helpText(h)).not.toContain(old)
  })

  it('says what previews are and that they run on the executor', () => {
    const t = helpText(PAGE_HELP.infrastructure.help)
    expect(t).toContain('Previews, the trial copies of an app made while a change is worked on, run on the executor instead')
  })

  it('says a group can approve without the syntax', () => {
    for (const h of [PAGE_HELP.people.help, SUB_HELP.people!.Groups.body]) {
      expect(helpText(h)).toContain("A workflow's approval step can name a group instead of a person. Then any member's approval counts. The record keeps the name of whoever approved.")
    }
  })

  // Costs: what is priced and how, and that unknown prices are not counted as nothing.
  it('says costs are estimates and how a price is known', () => {
    const t = helpText(PAGE_HELP.usage.help)
    expect(t).toContain("Costs are estimates, not your provider's bill.")
    expect(t).toContain("the provider's own, or one worked out from the model's list price")
    expect(t).toContain('Use with no known price shows as not priced.')
  })

  it('says how a suppression matches and what its export is', () => {
    const t = helpText(SUB_HELP.projectSecurity!.Suppressions.body)
    expect(t).toContain("the scanner's own ID for it, or by the vulnerability's ID (such as a CVE number) and the package")
    expect(t).toContain('fails the security check again')
    expect(t).toContain('OpenVEX export downloads the records in a standard format other security tools read.')
  })

  it('says notes are edited through a change and are not proof', () => {
    const t = helpText(SUB_HELP.projectMemory!.Notes.body)
    expect(t).toContain('every edit has an author and a change number')
    expect(t).toContain("An AI check can't count a note alone as proof of how the app behaves.")
  })

  it('names the secrets file in plain type and says how to add a secret', () => {
    const html = renderToStaticMarkup(<MemoryRouter>{SUB_HELP.projectSettings!.Secrets.body}</MemoryRouter>)
    expect(html).not.toContain('<code>')
    expect(helpText(SUB_HELP.projectSettings!.Secrets.body)).toContain('To add a new one, ask for a change to the app, then enter its value here.')
  })

  // Completeness: a page added to the Integrations menu must be named in the
  // area's help, or the help silently stops describing the menu.
  it('names every page of the Integrations menu, and sends app services to Services', () => {
    const t = helpText(PAGE_HELP.integrations.help)
    const items = INTEGRATION_MENU.find((g) => g.key === 'integrations')!.items
    for (const i of items) expect(t, i.title).toContain(`${i.title}:`)
    expect(t).toContain('Services an app itself connects to, such as a database, are on the Services page.')
  })
})
