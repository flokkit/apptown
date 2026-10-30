import { describe, expect, it } from 'vitest'
import { draftFrom, keptAsWritten } from './packDraft'

// Saving a pack unchanged sends back everything its author wrote, including
// what the form does not edit (review PI-03).
describe('a pack draft', () => {
  const declaration = {
    toolchain: 'go', services: [{ name: 'postgres', image: 'postgres:16', env: {} }], extensions: ['lsp'], guidance: ['team/review'], workflows: [], policies: [],
  }
  it('keeps every declared field through an unchanged save', () => {
    const draft = draftFrom('team-go', 'ours', declaration)
    const sent = JSON.parse(JSON.stringify({ ...draft, description: 'ours, reworded' }))
    for (const [k, v] of Object.entries(declaration)) expect(sent[k]).toEqual(v)
    expect(sent.description).toBe('ours, reworded')
  })
  it('says which fields it keeps without editing them', () => {
    expect(keptAsWritten(draftFrom('t', '', declaration))).toEqual(['toolchain go', 'services postgres', 'extensions lsp'])
  })
})
