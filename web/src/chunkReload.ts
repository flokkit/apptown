// After a deploy, a tab opened on the previous build still names the previous
// build's chunks. apptownd answers those with 404, so the tab's next lazy import
// fails ("Failed to fetch dynamically imported module"). Reloading fetches the
// new index.html, which names chunks that exist. A marker in sessionStorage
// allows one reload per window of time, so a chunk that is really broken shows
// its error instead of reloading forever.

const KEY = 'forge.chunk-reload'
const WINDOW_MS = 60_000

export type ReloadEnv = {
  storage: Pick<Storage, 'getItem' | 'setItem'> | null
  reload: () => void
  now: () => number
}

function browserEnv(): ReloadEnv {
  let storage: ReloadEnv['storage'] = null
  try { storage = window.sessionStorage } catch { /* storage blocked */ }
  return { storage, reload: () => window.location.reload(), now: () => Date.now() }
}

// reloadOnce reloads the page unless it already did so recently, and says
// whether it did. Without working storage it cannot guard a loop, so it does
// not reload.
export function reloadOnce(env: ReloadEnv = browserEnv()): boolean {
  try {
    if (!env.storage) return false
    const last = Number(env.storage.getItem(KEY) ?? 0)
    if (env.now() - last < WINDOW_MS) return false
    env.storage.setItem(KEY, String(env.now()))
  } catch {
    return false
  }
  env.reload()
  return true
}

// withChunkReload wraps a lazy page import: if the chunk cannot be loaded, the
// page reloads once (the promise stays pending while it does); otherwise the
// failure reaches the error boundary as usual.
export function withChunkReload<T>(load: () => Promise<T>, env?: ReloadEnv): () => Promise<T> {
  return () => load().catch((err: unknown) => {
    if (reloadOnce(env)) return new Promise<T>(() => {})
    throw err
  })
}
