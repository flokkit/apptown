import { describe, expect, it } from 'vitest'
import type { DependencyKind, PackView } from '../../wire.gen'
import { draftReducer, newDraft, originReady, servicesFor, slugOf, type Draft, type DraftAct } from './draft'

const play = (d: Draft, ...acts: DraftAct[]) => acts.reduce(draftReducer, d)

describe('the new-project draft', () => {
  // The address is derived from the name, not kept in step with it, so it
  // follows every edit until someone types their own, and then keeps it.
  it('derives the address from the name until someone types one', () => {
    let d = play(newDraft(), { type: 'name', name: 'Egg Catcher!' })
    expect(slugOf(d)).toBe('egg-catcher')
    d = play(d, { type: 'address', address: 'eggs' }, { type: 'name', name: 'Egg Catcher 2' })
    expect(slugOf(d)).toBe('eggs')
  })

  // A step is left only once its one decision is made: an address, then a
  // way of working (bare counts).
  it('moves on only once each step is decided', () => {
    let d = play(newDraft(), { type: 'step', step: 2 })
    expect(d.step).toBe(1)
    d = play(d, { type: 'describe', description: 'shop' }, { type: 'step', step: 3 })
    expect(d.step).toBe(1)
    d = play(d, { type: 'step', step: 2 }, { type: 'step', step: 3 })
    expect(d.step).toBe(2)
    d = play(d, { type: 'packs', packs: [], why: '', services: [] }, { type: 'step', step: 3 })
    expect(d.step).toBe(3)
  })

  // Picking packs starts from the services they propose together that the
  // catalog offers, at the version its image names, each once; the person
  // tunes them.
  it('starts from the services the chosen packs propose', () => {
    const packs = [
      { name: 'server', services: [{ name: 'postgres', image: 'postgres:16-alpine' }, { name: 'kafka', image: 'kafka:3' }] },
      { name: 'data', services: [{ name: 'postgres', image: 'postgres:16-alpine' }] },
      { name: 'other', services: [{ name: 'redis', image: 'redis:7' }] },
    ] as unknown as PackView[]
    const catalog = [{ capability: 'postgres', title: 'Postgres', local: { image: 'postgres' } }, { capability: 'redis', title: 'Redis', defaults: { version: '7' }, local: { image: 'redis' } }] as DependencyKind[]
    let d = play(newDraft(), { type: 'packs', packs: ['server', 'data'], why: '', services: servicesFor(['server', 'data'], packs, catalog) })
    expect(d.services).toEqual([{ capability: 'postgres', params: { version: '16' } }])
    d = play(d, { type: 'service', kind: catalog[1] }, { type: 'service', kind: catalog[0] })
    expect(d.services).toEqual([{ capability: 'redis', params: { version: '7' } }])
  })

  it('is ready to create from existing code only once the code is named', () => {
    const d = newDraft()
    expect(originReady(d)).toBe(true)
    expect(originReady(play(d, { type: 'repo', set: { origin: 'bundle' } }))).toBe(false)
    expect(originReady(play(d, { type: 'repo', set: { origin: 'url', sourceURL: 'http://plain' } }))).toBe(false)
    expect(originReady(play(d, { type: 'repo', set: { origin: 'url', sourceURL: 'https://x/y.git' } }))).toBe(true)
  })

  // A newer proposal (the catalog arriving late) replaces the pack's
  // services until someone edits them; after that their choice stands.
  it('follows the pack proposal until someone edits the services', () => {
    const pg = { capability: 'postgres', params: { version: '16' } }
    const redis = { capability: 'redis', title: 'Redis', local: { image: 'redis:7' } } as unknown as DependencyKind
    let d = play(newDraft(), { type: 'packs', packs: ['web'], why: '', services: [] }, { type: 'proposed', services: [pg] })
    expect(d.services).toEqual([pg])
    d = play(d, { type: 'service', kind: redis }, { type: 'proposed', services: [pg] })
    expect(d.services.map((n) => n.capability)).toEqual(['postgres', 'redis'])
    d = play(d, { type: 'proposed', services: [] })
    expect(d.services.map((n) => n.capability)).toEqual(['postgres', 'redis'])
  })
})


describe('description-first project identity', () => {
  it('proposes a usable name without a model and preserves explicit choices', () => {
    let d = play(newDraft(), { type: 'describe', description: 'Lunch orders for the office' })
    expect(d.name).toBe('Lunch orders for the office')
    expect(slugOf(d)).toBe('lunch-orders-for-the-office')
    d = play(d, { type: 'name', name: 'Our kitchen' }, { type: 'address', address: 'kitchen' },
      { type: 'describe', description: 'Dinner orders instead' })
    expect(d.name).toBe('Our kitchen')
    expect(slugOf(d)).toBe('kitchen')
  })
  it('keeps separate stable local and upstream operation identities', () => {
    const d = newDraft()
    expect(d.creationRequestId).toMatch(/^[0-9a-f-]{36}$/)
    expect(d.creationRequestId).not.toBe(d.attachmentRequestId)
    const edited = play(d, { type: 'describe', description: 'A library' }, { type: 'name', name: 'Books' })
    expect(edited.creationRequestId).toBe(d.creationRequestId)
    expect(edited.attachmentRequestId).toBe(d.attachmentRequestId)
  })
})


describe('working name and interview boundaries', () => {
  it('keeps a usable automatic address for non-ASCII descriptions', () => {
    const d = play(newDraft(), { type: 'describe', description: '図書館' })
    expect(d.name).toBe('図書館')
    expect(slugOf(d)).toBe('new-project')
    expect(play(d, { type: 'step', step: 2 }).step).toBe(2)
  })
  it('suggests the selected repository name without replacing an override', () => {
    let d = play(newDraft(), { type: 'describe', description: 'Improve our library' },
      { type: 'repo', set: { origin: 'url', sourceURL: 'https://example.test/team/books.git' } })
    expect(d.name).toBe('books')
    d = play(d, { type: 'name', name: 'Our library' },
      { type: 'repo', set: { sourceURL: 'https://example.test/team/other.git' } })
    expect(d.name).toBe('Our library')
  })
  it('refuses a late interview choice after newer input or a manual choice', () => {
    const original = play(newDraft(), { type: 'describe', description: 'Lunch orders' })
    const reply: DraftAct = { type: 'packs', packs: ['late'], why: '', services: [], revision: original.interviewRevision }
    const changed = play(original, { type: 'describe', description: 'Dinner orders' }, reply)
    expect(changed.packs).toBeNull()
    const chosen = play(original, { type: 'packs', packs: [], why: '', services: [] }, reply)
    expect(chosen.packs).toEqual([])
    const backed = play(original, { type: 'step', step: 1 }, reply)
    expect(backed.packs).toBeNull()
  })
})
