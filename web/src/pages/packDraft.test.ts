import { describe, expect, it } from 'vitest'
import { draftFrom, keptAsWritten, shownPolicy } from './packDraft'

// Saving a pack unchanged sends back everything its author wrote, including
// what the form does not edit: an explicit clear is not lost (review PI-03).
describe('a pack draft', () => {
  const declaration = {
    resources: { memory: '160M' }, includes: 'go-small', replaces: { 'deps/minimal-deps': 'team/deps' }, drops: [{ item: 'x', reason: 'not ours' }],
    clears: ['toolchain', 'services'], extensions: ['lsp'], guidance: ['team/review'], workflows: [], policies: [],
  }
  it('keeps every declared field through an unchanged save', () => {
    const draft = draftFrom('team-go', 'ours', declaration)
    const sent = JSON.parse(JSON.stringify({ ...draft, description: 'ours, reworded' }))
    for (const [k, v] of Object.entries(declaration)) expect(sent[k]).toEqual(v)
    expect(sent.description).toBe('ours, reworded')
  })
  it('says which fields it keeps without editing them', () => {
    expect(keptAsWritten(draftFrom('t', '', declaration))).toEqual(['default memory 160M', 'no toolchain (cleared)', 'no services (cleared)', 'extensions lsp'])
  })
})

describe('shownPolicy', () => {
  const library = [{ name: 'deploy-requires-scan', description: 'now', rule: 'requireStepBefore', with: { uses: 'deploy', requires: 'tests' }, origin: 'stock' }]
  it('shows the definition a version pinned, whatever the library says now', () => {
    const pinned = { name: 'deploy-requires-scan', description: 'then', rule: 'requireStepBefore', with: { uses: 'deploy', requires: 'scan' } }
    expect(shownPolicy(pinned, library)).toBe(pinned)
  })
  it('reads the library for a version that named the policy only', () => {
    expect(shownPolicy({ name: 'deploy-requires-scan', description: '', rule: '', with: {} }, library).with).toEqual({ uses: 'deploy', requires: 'tests' })
  })
})
