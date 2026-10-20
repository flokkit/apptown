import { useCallback, useRef, useState, useSyncExternalStore } from 'react'
import { CancelledError, QueryCache, QueryClient, onlineManager, useMutation, useQuery, useQueryClient, type Query, type QueryKey } from '@tanstack/react-query'
import { HttpError, get, getText, writeListeners } from './api'

// The page data layer, on TanStack Query. It keeps the promises the hand-built
// poller made (debt review, findings 24 and 25): two components asking for the
// same path share one request and one answer; nothing polls while the tab is
// hidden and a visible tab refreshes at once; a failing poll backs off,
// doubling up to a minute, and returns to pace on the next success. A failed
// request is an error the page shows, never an empty result.
//
// design/architecture/web-data.md is the current description of these rules.

// The longest a failing poll waits between attempts.
export const MAX_BACKOFF = 60_000

// pollPace is the wait before the next poll: the page's pace, doubled per
// consecutive failure, capped at MAX_BACKOFF. A pace of 0 does not poll.
export function pollPace(ms: number, failures: number): number | false {
  if (ms <= 0) return false
  return Math.min(ms * 2 ** failures, MAX_BACKOFF)
}

// The error count each query had at its last good read, so a failing poll
// knows how many reads in a row have failed.
const errorsAtLastSuccess = new WeakMap<object, number>()

// consecutiveFailures is how many reads of a query have failed since its last
// good one. TanStack's own fetchFailureCount restarts with every fetch, so with
// retries off it never passes one and the poll would never back off further.
export function consecutiveFailures(
  query: object,
  state: { errorUpdateCount: number; errorUpdatedAt: number; dataUpdatedAt: number },
): number {
  if (state.dataUpdatedAt >= state.errorUpdatedAt) {
    errorsAtLastSuccess.set(query, state.errorUpdateCount)
    return 0
  }
  return state.errorUpdateCount - (errorsAtLastSuccess.get(query) ?? 0)
}

// retryRead: a network failure or a server error (5xx) may pass on a second
// try, up to two more; a refusal or a missing record (4xx) will not, so it is
// shown at once instead of after three requests.
export function retryRead(failures: number, err: unknown): boolean {
  if (failures >= 2) return false
  return !(err instanceof HttpError) || err.status >= 500
}

// retryDelay is the wait before a read is asked again: one second, then
// two. Tests advance a fake clock by it rather than wait in real time.
export function retryDelay(attempt: number): number {
  return Math.min(1000 * 2 ** attempt, MAX_BACKOFF)
}

// The key every GET is cached under: the path's segments, then its query
// string. A path names a resource and the paths under it name its parts, so
// refreshing '/projects/shop' also refreshes '/projects/shop/settings', and
// an action can refresh exactly the resources it changed.
export function getKey(path: string): QueryKey {
  const [route, query] = path.split('?')
  const parts = route.split('/').filter(Boolean)
  return query ? ['get', ...parts, '?' + query] : ['get', ...parts]
}
const authKey = getKey('/auth/me')
const isAuth = (key: QueryKey) => key.length === authKey.length && key.every((k, i) => k === authKey[i])

// Who the reads are for. A change of identity (signing in or out, a session
// expiring, another name in open mode) resets every read: held answers are
// dropped, readers on screen stop showing theirs and read again, and any
// read still in flight throws its answer away. One person's views must never
// show what was read for another. (Removing the answers alone left readers on
// screen showing them, the independent reviewer found on 28 September.)
let identityEpoch = 0
export function identityChanged(client: QueryClient) {
  identityEpoch++
  const others = { predicate: (q: Query) => !isAuth(q.queryKey) }
  void client.cancelQueries(others)
  void client.resetQueries(others)
}

async function readFor<T>(read: () => Promise<T>): Promise<T> {
  const epoch = identityEpoch
  const value = await read()
  if (epoch !== identityEpoch) throw new CancelledError()
  return value
}

// How long an answer counts as current, by kind of resource. Every read is
// asked again on mount, focus and reconnect unless its kind says otherwise;
// polls are separate and set by the page.
//   live       operational state (runs, changes, deployments): always asked again.
//   config     settings and catalogues people edit rarely: current for 30 s,
//              so moving between tabs does not re-ask what was just read.
//   immutable  versioned or content-addressed answers (a block version, an
//              artifact, a commit's diff): never asked again while held.
export type ReadKind = 'live' | 'config' | 'immutable'
const staleFor: Record<ReadKind, number> = { live: 0, config: 30_000, immutable: Infinity }

export function newQueryClient(): QueryClient {
  const client: QueryClient = new QueryClient({
    // A read refused for want of a session means the session ended: ask who
    // this is again, which shows the sign-in page and changes the identity.
    queryCache: new QueryCache({
      onError: (err, query) => {
        if (err instanceof HttpError && err.status === 401 && !isAuth(query.queryKey)) {
          void client.invalidateQueries({ queryKey: authKey })
        }
      },
    }),
    defaultOptions: {
      queries: {
        staleTime: 0,
        // Keep an answer five minutes after its last reader leaves, so a
        // revisited page shows it at once while it reads again. A held answer
        // never seeds a form (forms wait for `current`), and a change of
        // identity drops every held answer.
        gcTime: 5 * 60_000,
        // A visible tab refreshes at once; a hidden one does not poll.
        refetchOnWindowFocus: true,
        refetchIntervalInBackground: false,
        // A poll's own backoff handles repeated failure; a one-off read
        // tries at most twice more, and only when asking again could help.
        retry: retryRead,
        retryDelay,
      },
    },
  })
  return client
}

// Read is what a page knows about one read.
export type Read<T> = {
  // data is the last good answer, kept while a refresh runs or fails.
  data: T | null
  // err is why the read failed: the first read, or the latest refresh while
  // an earlier answer is still shown, in which case it says the answer shown
  // is the last one read.
  err: string
  // reason is why the latest read failed, in AppTown's own words, without
  // err's note about the answer shown: what a stale note quotes.
  reason: string
  // loading: no answer yet and one is on its way.
  loading: boolean
  // refreshing: an answer is shown and a newer one is being read.
  refreshing: boolean
  // refreshFailed: an answer is shown but the latest attempt to refresh it
  // failed, so it may be old.
  refreshFailed: boolean
  // paused: the browser is offline and the read waits for the connection.
  paused: boolean
  // current: this reader received its answer itself, for its current path,
  // and no refresh has failed since. Only a current answer may seed a form
  // that saves: an answer another reader held may be old, and saving a form
  // seeded from it would send old values back.
  current: boolean
  // denied: AppTown refused this read to the caller (403). It is neither
  // empty nor broken: the page says the read is not available to them.
  denied: boolean
  // answeredAt is when the answer shown arrived (ms since the epoch), 0 when
  // there is none. For a reading the server takes as it answers (an
  // integration's health), it is when that reading was taken.
  answeredAt: number
  reload: () => Promise<void>
}

// useGet reads one path, and polls it every `poll` ms when given. A null path
// reads nothing (what it depends on is not known yet).
export function useGet<T>(path: string | null, opts: { poll?: number; kind?: ReadKind } = {}): Read<T> {
  const poll = opts.poll ?? 0
  const client = useQueryClient()
  const key = getKey(path ?? '')
  // The good answers the path had when this reader first asked for it. A
  // changed path starts again from what the new path holds. Counting answers
  // rather than comparing clock times judges an answer that arrives in the
  // same millisecond as the mount the same way.
  // A reset starts the count again, so the baseline starts again with it:
  // after an identity change (which resets every read, and may remount this
  // reader first, holding the old count), or whenever the count went back.
  const seen = useRef<{ path: string | null; epoch: number; answers: number } | null>(null)
  const answers = client.getQueryState(key)?.dataUpdateCount ?? 0
  if (seen.current === null || seen.current.path !== path) {
    seen.current = { path, epoch: identityEpoch, answers }
  } else if (seen.current.epoch !== identityEpoch || answers < seen.current.answers) {
    seen.current = { path, epoch: identityEpoch, answers: 0 }
  }
  const q = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => readFor(() => get<T>(path!, { signal })),
    enabled: path !== null,
    staleTime: staleFor[opts.kind ?? 'live'],
    // A poll does not retry; its own backoff paces it. Set only for polls: an
    // explicit undefined would replace the client's retry rule with
    // TanStack's own default of three.
    ...(poll > 0 ? { retry: false } : {}),
    refetchInterval: (query: Query<T, Error, T, QueryKey>) => pollPace(poll, consecutiveFailures(query, query.state)),
  })
  // TanStack re-renders only for the result fields a reader touches. Touching
  // isFetching re-renders at the end of every fetch, so an answer identical to
  // the held one still counts toward `current`.
  void q.isFetching
  const { refetch } = q
  const reload = useCallback(async () => { await refetch() }, [refetch])
  const refreshFailed = q.isRefetchError
  const message = q.error ? q.error.message : ''
  return {
    data: (q.data ?? null) as T | null,
    err: refreshFailed ? `${message} (showing the last answer read)` : message,
    reason: message,
    loading: q.isPending && path !== null,
    refreshing: q.isFetching && q.data !== undefined,
    refreshFailed,
    paused: q.fetchStatus === 'paused',
    current: q.data !== undefined && !refreshFailed && (client.getQueryState(key)?.dataUpdateCount ?? 0) > seen.current.answers,
    denied: q.error instanceof HttpError && q.error.status === 403,
    answeredAt: q.data !== undefined ? q.dataUpdatedAt : 0,
    reload,
  }
}

// family is the resource family a path belongs to: its first segment.
// '/projects/shop/services' and '/projects' are both 'projects'.
export const family = (path: string) => '/' + (path.split('?')[0].split('/').filter(Boolean)[0] ?? '')

// Families whose acts change what another family shows: a step or a run
// belongs to a change, and a console session works on one.
const reaches: Record<string, string[]> = {
  '/steps': ['/changes', '/runs'],
  '/runs': ['/changes', '/steps'],
  '/console': ['/changes'],
}

// refreshFamilies refreshes every read in the families of the given paths,
// and in the families those reach. Only reads on screen ask again; the rest
// are marked old for next time.
export function refreshFamilies(client: QueryClient, paths: string[]) {
  const families = new Set(paths.map(family))
  for (const f of [...families]) for (const r of reaches[f] ?? []) families.add(r)
  for (const f of families) void client.invalidateQueries({ queryKey: getKey(f) })
}

// useAction is the busy/error shape of every button. `run` clears the error,
// awaits the act, refreshes what it changed, calls onDone on success and keeps
// the failure's message for the page to show. What it changed is the family
// of every path the act wrote to (see api.ts writes), plus any paths named in
// `affects` for an act whose effects reach another family. A second run while
// one is under way is ignored, so a double click cannot send an act twice.
export function useAction(onDone?: () => void, opts: { affects?: string[] } = {}) {
  const client = useQueryClient()
  const affects = opts.affects
  // A page may also set its own message (a missing field) through setErr.
  const [note, setNote] = useState('')
  const running = useRef(false)
  const m = useMutation({
    mutationFn: async (fn: () => Promise<unknown>) => {
      // Two acts running at once each hear the other's writes too; that only
      // refreshes a little more.
      const wrote: string[] = []
      const listen = (path: string) => { wrote.push(path) }
      writeListeners.add(listen)
      try {
        return { value: await fn(), wrote }
      } finally {
        writeListeners.delete(listen)
      }
    },
    onSuccess: ({ wrote }) => {
      refreshFamilies(client, [...wrote, ...(affects ?? [])])
      onDone?.()
    },
  })
  const { mutateAsync, reset } = m
  const run = useCallback(async <T,>(fn: () => Promise<T>): Promise<T | undefined> => {
    if (running.current) return undefined
    running.current = true
    setNote('')
    try {
      return (await mutateAsync(fn)).value as T
    } catch {
      return undefined
    } finally {
      running.current = false
    }
  }, [mutateAsync])
  const setErr = useCallback((msg: string) => { reset(); setNote(msg) }, [reset])
  // failure is the act's error itself, for a page that answers a conflict
  // differently from other failures.
  return { run, busy: m.isPending, err: note || (m.error ? m.error.message : ''), failure: m.error, setErr }
}

// useRefresh refreshes the families of the given paths, for code that
// changed something outside useAction.
export function useRefresh(...paths: string[]) {
  const client = useQueryClient()
  const key = paths.join(' ')
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useCallback(async () => { refreshFamilies(client, paths) }, [client, key])
}

// firstErr is the first non-empty error message, for a page that shows one
// error line for several reads.
export const firstErr = (...errs: string[]) => errs.find((e) => e) ?? ''

// useOnline is whether the browser has a connection. Reads wait while it has
// none; the page says so once, rather than every read reporting a failure.
export function useOnline(): boolean {
  return useSyncExternalStore((notify) => onlineManager.subscribe(notify), () => onlineManager.isOnline())
}

// useGetText reads one path as plain text (a diff), under the path's own key
// with a marker, so refreshing the path refreshes its text too. It answers
// in the same terms as useGet, so the same read gate can show it.
export function useGetText(path: string | null, opts: { kind?: ReadKind } = {}) {
  const q = useQuery({
    queryKey: [...getKey(path ?? ''), '#text'],
    queryFn: ({ signal }) => readFor(() => getText(path!, { signal })),
    enabled: path !== null,
    staleTime: staleFor[opts.kind ?? 'live'],
  })
  const refreshFailed = q.isRefetchError
  const message = q.error ? q.error.message : ''
  return {
    data: q.data ?? null,
    err: refreshFailed ? `${message} (showing the last answer read)` : message,
    reason: message,
    loading: q.isPending && path !== null,
    refreshFailed,
    denied: q.error instanceof HttpError && q.error.status === 403,
    answeredAt: q.data !== undefined ? q.dataUpdatedAt : 0,
  }
}
