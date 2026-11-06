import { expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { newQueryClient } from '../../data'
import type { ChangeView } from '../../api'
import { AdrLinks } from './Tabs'

it('links decision creation to Memory and editing to the decision itself', () => {
 const view = { change: { id: 'change' }, projectSlug: 'shop', adrs: [{ id: 'decision', number: 4, title: 'Static pages', statusLabel: 'draft', tags: '' }] } as unknown as ChangeView
 const html = renderToStaticMarkup(<QueryClientProvider client={newQueryClient()}><MemoryRouter><AdrLinks view={view} onChange={() => {}} /></MemoryRouter></QueryClientProvider>)
 expect(html).toContain('href="/projects/shop/memory/decisions"')
 expect(html).toContain('href="/adrs/decision"')
 expect(html).not.toContain('<form')
 expect(html).not.toContain('<select')
 expect(html).not.toContain('Create draft')
})
