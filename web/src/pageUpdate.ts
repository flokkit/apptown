// A tab keeps the code it was loaded with: its data refreshes by polling, its
// own code never does. Every response names the entry script of the page the
// server serves now (X-AppTown-Web, web/entry.go); when that differs from the
// script this tab was loaded from, AppTown has been updated under it. The tab
// then says so, and reloads by itself the next time the person goes to
// another page: leaving a page already lets go of whatever it held, so the
// reload loses nothing more. It reloads once per new build; when the reload
// still lands on the old page (a cache serving it), the note stays instead.

import { createStore } from './hooks'

export const ENTRY_HEADER = 'X-AppTown-Web'
const KEY = 'apptown.reloaded-for'

// pageEntry is the entry script this tab was loaded from. A development
// server serves source (/src/...), which no build names: it never compares.
export function pageEntry(doc: Pick<Document, 'querySelector'> = document): string {
  const src = doc.querySelector<HTMLScriptElement>('script[type="module"][src]:not([src="/@vite/client"])')?.getAttribute('src') ?? ''
  return src.startsWith('/src/') ? '' : src
}

// served is the entry the latest response named ('' until one has).
export const served = createStore('')

// noteServed records the entry a response named.
export function noteServed(entry: string | null | undefined) {
  if (entry) served.set(entry)
}

// updated says whether the server now serves a different page than this tab
// runs. Unknown (nothing heard yet, or a development page) is not updated.
export function updated(page: string = pageEntry(), now: string = served.get()): boolean {
  return page !== '' && now !== '' && page !== now
}

export type ReloadEnv = {
  storage: Pick<Storage, 'getItem' | 'setItem'> | null
  reload: () => void
}

function browserEnv(): ReloadEnv {
  let storage: ReloadEnv['storage'] = null
  try { storage = window.sessionStorage } catch { /* storage blocked */ }
  return { storage, reload: () => window.location.reload() }
}

// reloadForUpdate reloads the tab when it runs an older page than the server
// serves, at most once for each build the server serves, and says whether it
// did. Without working storage it cannot tell a second try from a first, so
// it leaves the reload to the person.
export function reloadForUpdate(page: string = pageEntry(), now: string = served.get(), env: ReloadEnv = browserEnv()): boolean {
  if (!updated(page, now)) return false
  try {
    if (!env.storage || env.storage.getItem(KEY) === now) return false
    env.storage.setItem(KEY, now)
  } catch {
    return false
  }
  env.reload()
  return true
}
