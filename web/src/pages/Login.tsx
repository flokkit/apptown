import { useState } from 'react'
import { post } from '../api'
import { PRODUCT } from '../brand'
import { LocalTrialNote, takeSetupToken } from '../LocalTrialNote'

// The front door for internal-auth instances. First run has no accounts,
// so the same screen creates the first one (as admin), with the setup token
// the forge wrote on its host — bootstrap is refused by the server without
// it, and the moment any account exists.
export default function Login({ needsBootstrap, onSignedIn }: {
  needsBootstrap: boolean
  onSignedIn: () => void
}) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [setupToken, setSetupToken] = useState(takeSetupToken)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try {
      await post(needsBootstrap ? '/auth/bootstrap' : '/auth/login',
        needsBootstrap ? { username, password, setupToken } : { username, password })
      onSignedIn()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center' }}>
      <form onSubmit={submit} className="card" style={{ width: 340, display: 'grid', gap: 12, padding: 24 }}>
        <div style={{ fontSize: 'var(--fs-lg)' }}>⚒ <b>{PRODUCT}</b></div>
        {needsBootstrap ? (
          <div className="muted small">
            No accounts exist yet. Create the first one — it will be the
            administrator. It needs the setup token in the file
            {' '}<code>setup-token</code> in AppTown's data directory on its
            host (AppTown's log says where; the desktop trial's ready link fills it in).
          </div>
        ) : (
          <div className="muted small">Sign in to continue.</div>
        )}
        {needsBootstrap && <LocalTrialNote />}
        <label className="small muted">Username
          <input value={username} onChange={e => setUsername(e.target.value)}
            autoFocus autoComplete="username" style={{ width: '100%' }} />
        </label>
        <label className="small muted">Password
          <input type="password" value={password} onChange={e => setPassword(e.target.value)}
            autoComplete={needsBootstrap ? 'new-password' : 'current-password'} style={{ width: '100%' }} />
        </label>
        {needsBootstrap && (
          <label className="small muted">Setup token
            <input value={setupToken} onChange={e => setSetupToken(e.target.value)}
              autoComplete="off" spellCheck={false} style={{ width: '100%' }} />
          </label>
        )}
        {err && <div className="small" style={{ color: 'var(--bad)' }}>{err}</div>}
        <button className="primary" disabled={busy || !username || !password || (needsBootstrap && !setupToken)}>
          {needsBootstrap ? 'Create account' : 'Sign in'}
        </button>
      </form>
    </div>
  )
}
