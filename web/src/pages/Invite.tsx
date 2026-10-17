import { useEffect, useState } from 'react'
import { post } from '../api'
import type { InviteOffer, SignedInAs } from '../wire.gen'

// The page an invite link or a password reset link opens
// (design/subsystems/auth.md, Invites). Its visitor has no session: the
// token in the link is what lets them see whose account it is and set its
// password, once. Nothing here works without the link, and a used,
// withdrawn or expired one says so.
//
// The token is a live credential, so the page takes it out of the address
// bar as soon as it has it: what an error report, a referrer, a screenshot
// or the browser's history then sees is /invite. The token stays only in
// this page's memory; reloading shows the "open the link again" message.
export default function InvitePage({ token: fromLink }: { token: string }) {
  // Held from the first render: once the address bar says /invite, the
  // router may render this page again with no token.
  const [token] = useState(fromLink)
  useEffect(() => {
    if (token) window.history.replaceState(window.history.state, '', '/invite')
  }, [token])
  const [offer, setOffer] = useState<InviteOffer | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!token) return
    let live = true
    post<InviteOffer>('/auth/invite', { token })
      .then((o) => { if (live) setOffer(o) })
      .catch((e) => { if (live) setLoadErr((e as Error).message) })
    return () => { live = false }
  }, [token])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (password !== again) { setErr('The two passwords are different.'); return }
    setBusy(true); setErr('')
    try {
      await post<SignedInAs>('/auth/invite/accept', { token, password })
      // A fresh load starts every page as the new account.
      window.location.replace('/')
    } catch (e) {
      setErr((e as Error).message)
      setBusy(false)
    }
  }

  const reset = offer?.purpose === 'password'
  const dead: Record<string, string> = reset ? {
    accepted: 'This password reset link has already been used. If it was you, sign in with the password you chose.',
    revoked: 'This password reset link was withdrawn. Ask an administrator for a new one.',
    expired: 'This password reset link has expired. Ask an administrator for a new one.',
  } : {
    accepted: 'This invite has already been used. If it was you, sign in with the password you chose.',
    revoked: 'This invite was withdrawn. Ask whoever invited you for a new link.',
    expired: 'This invite has expired. Ask whoever invited you for a new link.',
  }

  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 16, boxSizing: 'border-box' }}>
      <div className="card" style={{ width: 'min(380px, 100%)', boxSizing: 'border-box', display: 'grid', gap: 12, padding: 24 }}>
        <div style={{ fontSize: 'var(--fs-lg)' }}>⚒ <b>forge</b></div>
        {!token && <>
          <div>Open the link you were given again. For safety this page does not keep it after it is loaded.</div>
          <a href="/" className="small">Go to sign in</a>
        </>}
        {token && !offer && !loadErr && <div className="muted small">Reading the link…</div>}
        {loadErr && <>
          <div>{loadErr}</div>
          <a href="/" className="small">Go to sign in</a>
        </>}
        {offer && offer.state !== 'pending' && <>
          <div>{dead[offer.state] ?? `This invite cannot be used (${offer.state}).`}</div>
          <a href="/" className="small">Go to sign in</a>
        </>}
        {offer && offer.state === 'pending' && (
          <form onSubmit={submit} style={{ display: 'grid', gap: 12 }}>
            {reset ? <>
              <div>
                <b>{offer.invitedBy}</b> made a password reset link for <b>{offer.username}</b>.
              </div>
              <div className="muted small">
                Choose a new password. Your old password stops working and you are signed out
                everywhere else; you will sign in here as <b>{offer.username}</b>.
                The link works once, until {new Date(offer.expiresAt).toLocaleDateString()}.
              </div>
            </> : <>
              <div>
                <b>{offer.invitedBy}</b> invited you to this forge as <b>{offer.username}</b>
                {offer.role === 'admin' ? ', an administrator' : ''}.
              </div>
              <div className="muted small">
                Choose a password to finish. You will sign in as <b>{offer.username}</b>.
                The link works once, until {new Date(offer.expiresAt).toLocaleDateString()}.
              </div>
            </>}
            {/* Lets a password manager save the new password under the right name. */}
            <input type="text" autoComplete="username" value={offer.username} readOnly hidden />
            <label className="small muted">Password (at least 8 characters)
              <input type="password" value={password} onChange={(e) => setPassword(e.target.value)}
                autoFocus autoComplete="new-password" style={{ width: '100%', boxSizing: 'border-box' }} />
            </label>
            <label className="small muted">The same password again
              <input type="password" value={again} onChange={(e) => setAgain(e.target.value)}
                autoComplete="new-password" style={{ width: '100%', boxSizing: 'border-box' }} />
            </label>
            {err && <div className="small" style={{ color: 'var(--bad)' }}>{err}</div>}
            <button className="primary" disabled={busy || password.length < 8 || !again}>
              {busy ? 'Signing in…' : reset ? 'Set new password and sign in' : 'Set password and sign in'}
            </button>
          </form>
        )}
      </div>
    </div>
  )
}
