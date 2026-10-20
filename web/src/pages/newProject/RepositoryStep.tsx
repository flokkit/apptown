import { PRODUCT } from '../../brand'
import { type Dispatch, type ReactNode } from 'react'
import { useGet } from '../../data'
import { useHolds } from '../../instancePages'
import type { RemoteNamespaces, SecretNameList } from '../../wire.gen'
import { originReady, slugOf, type Draft, type DraftAct } from './draft'

// Step 3 — Repository: every project gets a repository on AppTown (not a
// question); the choices are a GitLab copy too, and whether the code starts
// empty or from existing code. Create runs the assembly from here.
export function RepositoryStep({ d, act, busy, onCreate, children }: {
  d: Draft
  act: Dispatch<DraftAct>
  busy: boolean
  onCreate: (where: { remote: boolean; namespaceId: string }) => void
  children?: ReactNode // the assembly log, under the Create button
}) {
  // A GitLab copy is offered only when this AppTown has a GitLab connection
  // (the namespaces answer says so), and then it is the default. Until
  // someone picks, the choice follows that answer; the group defaults to
  // the first one listed.
  const namespacesQ = useGet<RemoteNamespaces>('/scm/namespaces', { kind: 'config' })
  const gitlab = namespacesQ.data?.configured === true
  const remote = gitlab && (d.remoteChoice ?? true)
  const namespaces = namespacesQ.data?.namespaces ?? []
  const namespaceId = d.namespaceId || (namespaces.length > 0 ? String(namespaces[0].id) : '')
  const setRepo = (set: Extract<DraftAct, { type: 'repo' }>['set']) => act({ type: 'repo', set })

  // Or the repository starts from existing code: a Git bundle (another
  // forge's Download code) or an https repository. Only its default branch
  // and tags come; the project's changes and history here start fresh.
  // Naming a apptown secret as the git credential is for administrators: the
  // AppTown refuses anyone else, so only they are offered it.
  const { origin } = d
  const admin = useHolds('identity:manage-auth')
  const secretNamesQ = useGet<SecretNameList>(admin && origin === 'url' ? '/secrets/names' : null)
  const slug = slugOf(d)

  return (
    <div className="card" style={{ padding: 'var(--space-5)' }}>
      <div className="mb-2"><strong>Where does the code live?</strong></div>
      {!gitlab ? (
        <div className="muted small mb-3">
          {namespacesQ.loading ? `Checking whether ${PRODUCT} can make a GitLab copy…`
            : <>The code lives in a git repository in {PRODUCT}.{' '}
              {namespacesQ.err
                ? <span className="error">Whether a GitLab copy can be made could not be checked ({namespacesQ.reason}); you can add one later in the project's settings.</span>
                : <>No GitLab connection is set up here, so there is no copy to make; one can be added later.</>}</>}
        </div>
      ) : <>
      <div className="muted small mb-3">
        Every project gets a git repository in {PRODUCT}. Also create a copy on GitLab?
      </div>
      <label className="row" style={{ gap: 'var(--space-2)', cursor: 'pointer', padding: '4px 0' }}>
        <input type="radio" name="remote" checked={remote} onChange={() => setRepo({ remoteChoice: true })} />
        <span><strong>Yes, create a GitLab copy</strong> <span className="muted small">(recommended — backed up and shareable)</span></span>
      </label>
      {remote && (
        <div className="row" style={{ margin: '4px 0 4px 24px' }}>
          {namespaces.length > 0 ? (
            <label className="row" style={{ gap: 'var(--space-2)' }}>
              <span className="muted small">in group</span>
              <select value={namespaceId} onChange={(e) => setRepo({ namespaceId: e.target.value })}>
                {namespaces.map((n) => <option key={n.id} value={n.id}>{n.path}</option>)}
              </select>
            </label>
          ) : (
            <label className="row" style={{ gap: 'var(--space-2)' }}>
              <span className="muted small">GitLab group id</span>
              <input value={namespaceId} onChange={(e) => setRepo({ namespaceId: e.target.value })} placeholder="56" style={{ width: 90 }} />
              <span className="muted small">(the connection can see no groups to list)</span>
            </label>
          )}
        </div>
      )}
      <label className="row" style={{ gap: 'var(--space-2)', cursor: 'pointer', padding: '4px 0' }}>
        <input type="radio" name="remote" checked={!remote} onChange={() => setRepo({ remoteChoice: false })} />
        <span>No — keep it only in {PRODUCT} <span className="muted small">(you can add GitLab later)</span></span>
      </label>
      </>}

      <div style={{ borderTop: '1px solid var(--border)', margin: '16px 0 0', paddingTop: 'var(--space-4)' }}>
        <div className="mb-2"><strong>Start from</strong></div>
        <label className="row" style={{ gap: 'var(--space-2)', cursor: 'pointer', padding: '4px 0' }}>
          <input type="radio" name="origin" checked={origin === 'empty'} onChange={() => setRepo({ origin: 'empty' })} />
          <span>An empty repository</span>
        </label>
        <label className="row" style={{ gap: 'var(--space-2)', cursor: 'pointer', padding: '4px 0' }}>
          <input type="radio" name="origin" checked={origin !== 'empty'} onChange={() => setRepo({ origin: 'bundle' })} />
          <span>Existing code</span>
        </label>
        {origin !== 'empty' && (
          <div style={{ margin: '6px 0 0 24px', display: 'grid', gap: 'var(--space-2)' }}>
            <div className="muted small">Its default branch and tags come in; changes, runs and chats here start fresh.</div>
            <div className="row" style={{ gap: 'var(--space-3)', flexWrap: 'wrap' }}>
              <label className="row small" style={{ gap: 'var(--space-2)', cursor: 'pointer' }}>
                <input type="radio" name="source" checked={origin === 'bundle'} onChange={() => setRepo({ origin: 'bundle' })} /> a Git bundle file
              </label>
              <label className="row small" style={{ gap: 'var(--space-2)', cursor: 'pointer' }}>
                <input type="radio" name="source" checked={origin === 'url'} onChange={() => setRepo({ origin: 'url' })} /> an https Git URL
              </label>
            </div>
            {origin === 'bundle' && (
              <div>
                <input type="file" accept=".bundle,application/x-git-bundle" aria-label="Git bundle file"
                  onChange={(e) => setRepo({ bundleFile: e.target.files?.[0] ?? null })} style={{ maxWidth: '100%' }} />
                <div className="muted small mt-1">
                  From another {PRODUCT}: its project's Repository › Download code. Or make one with <code>git bundle create code.bundle main --tags</code>.
                </div>
              </div>
            )}
            {origin === 'url' && (
              <div style={{ display: 'grid', gap: 'var(--space-2)' }}>
                <input value={d.sourceURL} onChange={(e) => setRepo({ sourceURL: e.target.value })} placeholder="https://gitlab.example.com/team/app.git"
                  aria-label="https Git URL" style={{ width: '100%', boxSizing: 'border-box' }} />
                {admin && (
                  <label className="row small" style={{ gap: 'var(--space-2)', flexWrap: 'wrap' }}>
                    <span className="muted">git credential</span>
                    <select value={d.gitSecret} onChange={(e) => setRepo({ gitSecret: e.target.value })} aria-label="git credential">
                      <option value="">none (a public repository)</option>
                      {(secretNamesQ.data?.names ?? []).map((n) => <option key={n} value={n}>{n}</option>)}
                    </select>
                    {secretNamesQ.err && <span className="error">{PRODUCT}'s secrets could not be read: {secretNamesQ.reason}</span>}
                  </label>
                )}
                <div className="muted small">Fetched by {PRODUCT} from a public address, over https only.</div>
              </div>
            )}
            <label className="row small" style={{ gap: 'var(--space-2)' }}>
              <span className="muted">branch</span>
              <input value={d.sourceBranch} onChange={(e) => setRepo({ sourceBranch: e.target.value })} placeholder="the source's default" style={{ width: 180 }} />
            </label>
          </div>
        )}
      </div>

      <div className="muted small" style={{ margin: '14px 0 4px' }}>
        Creating: <strong>{d.name || slug}</strong> at <code>/{slug}</code>
        {origin !== 'empty' ? ' · from existing code' : ''}
        {d.pack ? <> · {d.pack}</> : ' · bare'}
        {remote ? ' · with a GitLab copy' : ''}
      </div>

      <div className="row mt-3">
        <button className="subtle" onClick={() => act({ type: 'step', step: 2 })} disabled={busy}>← back</button>
        <span className="grow" />
        <button className="primary" onClick={() => onCreate({ remote, namespaceId })} disabled={busy || !slug || !originReady(d)}
          style={{ fontSize: 'var(--fs-base)', padding: '10px 26px' }}>{busy ? 'Creating…' : 'Create project'}</button>
      </div>
      {children}
    </div>
  )
}
