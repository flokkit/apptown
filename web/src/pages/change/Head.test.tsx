import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { ChangeView } from '../../api'
import { QueryClientProvider } from '@tanstack/react-query'
import { newQueryClient } from '../../data'
import { MemoryRouter } from 'react-router-dom'
import { ChangeHeader, ChangesTab } from './Head'

// Record ids are UUIDv7 strings.
const CHANGE = '00000000-0001-7000-8000-000000000000'

const view = (state: 'landing' | 'abandoned'): ChangeView => ({
  change: { id: CHANGE, number: 1, title: 'Game', state, body: '', branch: 'change' },
  evidence: { merged: true }, editing: 0, runs: [], commits: [],
} as unknown as ChangeView)

const empty = 'No committed changes since this change started.'
describe('change state and committed evidence', () => {
  it.each([['landing', 'landing'], ['abandoned', 'abandoned']] as const)('keeps %s visible even with a merge receipt', (state, label) => {
    const html = renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><ChangeHeader view={view(state)} aboutTab="/changes/1?tab=about" /></QueryClientProvider></MemoryRouter>)
    expect(html).toContain(`>${label}</span>`)
    expect(html).not.toContain('>merged</span>')
  })
  // Who asked is said only when the record names them; an older change
  // whose opener was never recorded says nothing rather than guess.
  it('says who asked for the change only when the record names them', () => {
    const head = (v: ChangeView) => renderToStaticMarkup(<MemoryRouter><QueryClientProvider client={newQueryClient()}><ChangeHeader view={v} aboutTab="/changes/1?tab=about" /></QueryClientProvider></MemoryRouter>)
    const known = view('landing')
    known.change.openedBy = 'alice via mcp/claude'
    expect(head(known)).toContain('Asked by alice via mcp/claude')
    expect(head(view('landing'))).not.toContain('Asked by')
  })
  it('shows the precise empty message only after a successful comparison', () => {
    const footer = (v: ChangeView) => renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><ChangesTab view={v} /></QueryClientProvider>)
    const emptyView = { ...view('landing'), committedDiff: { status: 'empty' as const, message: empty } }
    expect(footer(emptyView)).toContain(empty)
    const unreadable = { ...view('landing'), committedDiff: { status: 'unavailable' as const, message: 'Branch unavailable' } }
    const html = footer(unreadable)
    expect(html).not.toContain(empty)
    expect(html).toContain('Branch unavailable')
    expect(footer(view('landing'))).not.toContain(empty)
  })
  // A change with no code of its own says so, and reads its release's diff
  // only when the comparison with the release before it was made.
  it('says a change with no code of its own shipped main, without asking for a branch diff', () => {
    const footer = (v: ChangeView) => renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><ChangesTab view={v} /></QueryClientProvider>)
    const said = 'This change has no code of its own: it shipped main as it stood at 5b9ec68, as release #16 to main.'
    const noCode = (since?: number) => ({ ...view('landing'), committedDiff: { status: 'no-code', message: said, shipped: { release: 16, since } } } as unknown as ChangeView)
    const first = footer(noCode())
    expect(first).toContain(said)
    expect(first).not.toContain('the committed diff')
    expect(first).not.toContain(empty)
    expect(footer(noCode(15))).toContain('the committed diff')
  })
})
