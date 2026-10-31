import { useState } from 'react'
import { PRODUCT } from '../brand'
import { del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadGate } from '../ReadGate'
import type { AuthMe, ExternalProvider, ExternalProviderChange, ExternalBindingList, ExternalBindRequest, ExternalEnableRequest, ExternalRedirect, ExternalStartRequest } from '../wire.gen'

export function ExternalProviderSettings() {
  const q = useGet<ExternalProvider | null>('/auth/external/provider')
  const bindings = useGet<ExternalBindingList>('/auth/external/bindings?all=true')
  const [draft, setDraft] = useState<ExternalProviderChange | null>(null)
  const [subject, setSubject] = useState(''), [principal, setPrincipal] = useState('')
  const { run, err } = useAction()
  const [tested, setTested] = useState(false)
  const stale = !!draft && draft.revision !== (q.data?.revision ?? 0)
  const edit = () => { setTested(false); setDraft({ revision: q.data?.revision ?? 0, issuer: q.data?.issuer ?? '', displayName: q.data?.displayName ?? '', clientId: q.data?.clientId ?? '', secretRef: q.data?.secretRef ?? '', allowSignup: q.data?.allowSignup ?? false, initialGroup: q.data?.initialGroup ?? '' }) }
  return <section className="card"><h2>External sign-in</h2>
    <details><summary>Sign-in limits</summary><p>Keep a local-password administrator for recovery. External sessions last at most one hour. Disabling this provider ends its sessions; local passwords and CLI tokens remain independent.</p></details>
    {err && <p className="error">{err}</p>}
    <ReadGate q={q} what="external sign-in settings">
      <p>{q.data ? `${q.data.displayName}: ${q.data.enabled ? 'enabled' : 'disabled'}` : 'No provider configured.'}</p>
      {q.data && <p>Register callback: <code>{q.data.callbackOrigin}/api/v1/auth/external/callback</code></p>}
      {!draft && <button onClick={edit}>Configure provider</button>}
      {draft && <form onSubmit={e => { e.preventDefault(); void run(async () => { await put('/auth/external/provider', draft); setDraft(null); setTested(false) }) }}>
        {(['issuer', 'displayName', 'clientId', 'secretRef', 'initialGroup'] as const).map(key => <label key={key} style={{ display: 'block' }}>{({ issuer: 'Issuer URL', displayName: 'Button label', clientId: 'Client ID', secretRef: 'Instance secret name', initialGroup: 'Initial local group (optional)' })[key]}<input value={draft[key]} onChange={e => setDraft({ ...draft, [key]: e.target.value })} /></label>)}
        <p className="small">Set the client secret with the existing instance-secret editor or <code>apptown secret set NAME</code>. Enter only its name here.</p>
        <label><input type="checkbox" checked={draft.allowSignup} onChange={e => setDraft({ ...draft, allowSignup: e.target.checked })} />Allow verified subjects to create new accounts</label>
        <p className="small">Otherwise an administrator must bind an exact provider subject, or an existing account must link explicitly. Email never links accounts.</p>
        {stale && <p className="error">Settings changed while you were editing. Cancel and reload before saving.</p>}
        <button disabled={stale}>Save disabled configuration</button><button type="button" onClick={() => setDraft(null)}>Cancel</button>
      </form>}
      {q.data && !draft && <>
        <button onClick={() => { void run(async () => { await post('/auth/external/test', {}); setTested(true) }) }}>Test discovery</button>
        <button onClick={() => { const input: ExternalEnableRequest = { revision: q.data!.revision, enabled: !q.data!.enabled }; void run(() => post('/auth/external/enabled', input)) }}>{q.data.enabled ? 'Disable provider' : 'Enable provider'}</button>
        {tested && <p>Discovery succeeded. This does not prove a user can sign in.</p>}
        <h3>Explicit account binding</h3><label>Local principal UUID<input value={principal} onChange={e => setPrincipal(e.target.value)} /></label><label>Exact provider subject<input value={subject} onChange={e => setSubject(e.target.value)} /></label>
        <button disabled={!principal || !subject} onClick={() => { const input: ExternalBindRequest = { providerId: q.data!.id, principalId: principal, subject }; void run(async () => { await post('/auth/external/bindings', input); setSubject('') }) }}>Bind identity</button>
      </>}
    </ReadGate>
    <ReadGate q={bindings} what="external identity bindings">{bindings.data?.bindings.map(b => <p key={b.id}>{b.principalId}: {b.issuer} / {b.subject} — {b.revoked ? 'revoked' : <button onClick={() => { void run(() => del(`/auth/external/bindings/${b.id}`)) }}>Revoke</button>}</p>)}</ReadGate>
  </section>
}

function YourTokens() {
 const q = useGet<AuthMe>('/auth/me')
 return <section className="card"><h2>Your access tokens</h2>
   <ReadGate q={q} what="your access tokens">
     {!q.data?.ownTokens?.length && <p className="muted">No access tokens.</p>}
     {q.data?.ownTokens?.map(t => <p key={t.Name}><strong>{t.Name}</strong> · {t.Purpose === 'agent' ? 'Coding agent' : 'Command line'} · last used {t.LastUsed ? t.LastUsed.slice(0, 10) : 'never'}</p>)}
   </ReadGate>
 </section>
}

export function MyExternalSignIn({ embedded = false }: { embedded?: boolean }) {
  const me = useGet<AuthMe>(embedded ? null : '/auth/me')
  const q = useGet<ExternalBindingList>('/auth/external/bindings')
  const [password, setPassword] = useState('')
  const { run, err } = useAction()
  return <>{!embedded && <><h1>Your account</h1><ReadGate q={me} what="your account"><p><strong>{me.data?.user}</strong>{me.data?.role === 'admin' && ' · Admin'}</p></ReadGate><YourTokens /></>}<section className="card"><h2>{embedded ? 'Link your sign-in' : 'External sign-in'}</h2>
    <details className="small"><summary>About linking accounts</summary><p>Linking requires a local-password session and your password, then recent authentication at the provider. It never matches by email.</p></details>
    <label style={{ display: 'block', margin: '12px 0' }}>Local password <input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} /></label>
    <button disabled={!password} onClick={() => { const input: ExternalStartRequest = { name: '', returnPath: '/account', password, link: true }; void run(async () => { const out = await post<ExternalRedirect>('/auth/external/start', input); setPassword(''); window.location.assign(out.url) }) }}>Link identity</button>
    {err && <p className="error">{err}</p>}
    <ReadGate q={q} what="your external identities">{q.data?.bindings.map(b => <p key={b.id}>{b.issuer} / {b.subject} — {b.revoked ? 'revoked' : <button disabled={!password} onClick={() => { void run(async () => { await post(`/auth/external/bindings/${b.id}/unlink`, { password }); setPassword('') }) }}>Unlink using local password</button>}</p>)}</ReadGate>
  </section></>
}

export function externalSignInFailure(code: string | null): string {
  if (code === 'unavailable') return 'The external sign-in provider could not be reached. Try again or use your local password.'
  if (code === 'name-unavailable') return 'That account name is already used. Choose another username and start external sign-in again.'
  if (code === 'unbound') return `This external identity is not linked to an available ${PRODUCT} account. Ask an administrator to check its binding.`
  if (code === 'failed') return 'External sign-in or linking could not complete. Start again or use your local password. An existing local session is unchanged.'
  return ''
}
