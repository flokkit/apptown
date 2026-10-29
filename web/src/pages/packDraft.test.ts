import { describe, expect, it } from 'vitest'
import { draftFrom, keptAsWritten } from './packDraft'

// Saving a pack unchanged sends back everything its author wrote, including
// what the form does not edit: an explicit clear is not lost (review PI-03).
describe('a pack draft', () => {
  const declaration = {
    includes: 'go-small', replaces: { 'deps/minimal-deps': 'team/deps' }, drops: [{ item: 'x', reason: 'not ours' }],
    clears: ['toolchain', 'services'], extensions: ['lsp'], guidance: ['team/review'], workflows: [], policies: [],
  }
  it('keeps every declared field through an unchanged save', () => {
    const draft = draftFrom('team-go', 'ours', declaration)
    const sent = JSON.parse(JSON.stringify({ ...draft, description: 'ours, reworded' }))
    for (const [k, v] of Object.entries(declaration)) expect(sent[k]).toEqual(v)
    expect(sent.description).toBe('ours, reworded')
  })
  it('says which fields it keeps without editing them', () => {
    expect(keptAsWritten(draftFrom('t', '', declaration))).toEqual(['no toolchain (cleared)', 'no services (cleared)', 'extensions lsp'])
  })
})
