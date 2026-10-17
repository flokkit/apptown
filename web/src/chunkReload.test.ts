import { describe, expect, it } from 'vitest'
import { reloadOnce, withChunkReload, type ReloadEnv } from './chunkReload'

function fakeEnv(start = 1_000_000): ReloadEnv & { reloads: number; clock: number; store: Map<string, string> } {
  const store = new Map<string, string>()
  const env = {
    store, reloads: 0, clock: start,
    storage: { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => { store.set(k, v) } },
    reload: () => { env.reloads++ },
    now: () => env.clock,
  }
  return env
}

describe('a page chunk from an older build', () => {
  it('reloads the page once, then lets a repeated failure through', async () => {
    const env = fakeEnv()
    const failing = withChunkReload(() => Promise.reject(new TypeError('Failed to fetch dynamically imported module')), env)
    const first = failing()
    let settled = false
    first.then(() => { settled = true }, () => { settled = true })
    await new Promise((r) => setTimeout(r, 0))
    expect(env.reloads).toBe(1)
    expect(settled).toBe(false) // the page is going away; nothing renders an error meanwhile

    await expect(failing()).rejects.toThrow('Failed to fetch dynamically imported module')
    expect(env.reloads).toBe(1)
  })

  it('reloads again for a later deploy', () => {
    const env = fakeEnv()
    expect(reloadOnce(env)).toBe(true)
    env.clock += 5 * 60_000
    expect(reloadOnce(env)).toBe(true)
    expect(env.reloads).toBe(2)
  })

  it('does not reload when storage is unavailable, since it could not stop a loop', () => {
    const env = { ...fakeEnv(), storage: null }
    expect(reloadOnce(env)).toBe(false)
    const throwing = fakeEnv()
    throwing.storage = { getItem: () => { throw new Error('blocked') }, setItem: () => {} }
    expect(reloadOnce(throwing)).toBe(false)
    expect(throwing.reloads).toBe(0)
  })

  it('passes a loaded chunk through untouched', async () => {
    const env = fakeEnv()
    const page = { default: 'Docs' }
    await expect(withChunkReload(() => Promise.resolve(page), env)()).resolves.toBe(page)
    expect(env.reloads).toBe(0)
  })
})
