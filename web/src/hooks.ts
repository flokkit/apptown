import { useEffect, useState, useSyncExternalStore } from 'react'

// Small shared hooks, kept once so pages do not grow their own copies.

// useNow is the time, re-rendered every `ms` while `active`, so "2m ago" and
// "running for 1m 4s" keep telling the truth without each page building its
// own ticker.
export function useNow(ms: number, active = true): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const t = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(t)
  }, [ms, active])
  return now
}

// A store is one value the page shares outside React (the top bar's crumb,
// the project a page is inside). Readers subscribe through
// useSyncExternalStore, so a value set between a reader's render and its
// effect is not missed, as it was with hand-built subscriber lists.
export type Store<T> = { get: () => T; set: (v: T) => void; use: () => T }

export function createStore<T>(initial: T): Store<T> {
  let value = initial
  const subs = new Set<() => void>()
  const subscribe = (notify: () => void) => { subs.add(notify); return () => { subs.delete(notify) } }
  const get = () => value
  return {
    get,
    set: (v: T) => {
      if (Object.is(v, value)) return
      value = v
      subs.forEach((f) => f())
    },
    use: () => useSyncExternalStore(subscribe, get, get),
  }
}
