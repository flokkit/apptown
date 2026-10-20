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

  // The box starts empty under a fixed example; each 🎲 puts a freshly
  // rolled name in it.
  it('starts empty and rolls a name into the box', () => {
    let d = newDraft()
    expect(d.name).toBe('')
    d = play(d, { type: 'roll', next: 'Brave Comet' })
    expect(d.name).toBe('Brave Comet')
    d = play(d, { type: 'roll', next: 'Turbo Pickle' })
    expect(d.name).toBe('Turbo Pickle')
  })

  // A step is left only once its one decision is made: an address, then a
  // way of working (bare counts).
  it('moves on only once each step is decided', () => {
    let d = play(newDraft(), { type: 'step', step: 2 })
    expect(d.step).toBe(1)
    d = play(d, { type: 'name', name: 'shop' }, { type: 'step', step: 3 })
    expect(d.step).toBe(1)
    d = play(d, { type: 'step', step: 2 }, { type: 'step', step: 3 })
    expect(d.step).toBe(2)
    d = play(d, { type: 'pack', pack: '', why: '', services: [] }, { type: 'step', step: 3 })
    expect(d.step).toBe(3)
  })

  // Picking a way of working starts from the services it proposes that the
  // catalog offers, at the version its image names; the person tunes them.
  it('starts from the services a pack proposes', () => {
    const packs = [{ name: 'server', services: [{ name: 'postgres', image: 'postgres:16-alpine' }, { name: 'kafka', image: 'kafka:3' }] }] as unknown as PackView[]
    const catalog = [{ capability: 'postgres', title: 'Postgres', local: { image: 'postgres' } }, { capability: 'redis', title: 'Redis', defaults: { version: '7' }, local: { image: 'redis' } }] as DependencyKind[]
    let d = play(newDraft(), { type: 'pack', pack: 'server', why: '', services: servicesFor('server', packs, catalog) })
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
    let d = play(newDraft(), { type: 'pack', pack: 'web', why: '', services: [] }, { type: 'proposed', services: [pg] })
    expect(d.services).toEqual([pg])
    d = play(d, { type: 'service', kind: redis }, { type: 'proposed', services: [pg] })
    expect(d.services.map((n) => n.capability)).toEqual(['postgres', 'redis'])
    d = play(d, { type: 'proposed', services: [] })
    expect(d.services.map((n) => n.capability)).toEqual(['postgres', 'redis'])
  })
})
