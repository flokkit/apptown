import { ListTable } from '../ListTable'
import { Switch } from '../Switch'
import { useState } from 'react'
import { PRODUCT } from '../brand'
import { del, post, put } from '../api'
import { useAction, useGet } from '../data'
import { ReadGate } from '../ReadGate'
import { Section } from '../Section'
import { AgentsCard, TokensCard } from './AccountAccess'
import './ExternalSignIn.css'
import type { AuthMe, ExternalProvider, ExternalProviderChange, ExternalBindingList, ExternalBindRequest, ExternalEnableRequest, ExternalRedirect, ExternalStartRequest } from '../wire.gen'

export function ExternalProviderSettings() {
  const q = useGet<ExternalProvider | null>('/auth/external/provider')
  const bindings = useGet<ExternalBindingList>('/auth/external/bindings?all=true')
  const [draft, setDraft] = useState<ExternalProviderChange | null>(null)
  const [subject, setSubject] = useState(''), [principal, setPrincipal] = useState('')
  const { run, busy, err } = useAction()
  const [tested, setTested] = useState(false)
  const stale = !!draft && draft.revision !== (q.data?.revision ?? 0)
  const edit = () => { setTested(false); setDraft({ revision: q.data?.revision ?? 0, issuer: q.data?.issuer ?? '', displayName: q.data?.displayName ?? '', clientId: q.data?.clientId ?? '', secretRef: q.data?.secretRef ?? '', allowSignup: q.data?.allowSignup ?? false, initialGroup: q.data?.initialGroup ?? '' }) }
  return <section className="card"><h2>External sign-in</h2>
    <section><h3>Sign-in limits</h3><p>Keep a local-password administrator for recovery. External sessions last at most one hour. Disabling this provider ends its sessions; local passwords and CLI tokens remain independent.</p></section>
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
        <label className="row">Provider enabled <Switch label={q.data.displayName} checked={q.data.enabled} disabled={busy || !q.current} onChange={enabled => { const input: ExternalEnableRequest = { revision: q.data!.revision, enabled }; void run(() => post('/auth/external/enabled', input)) }} /></label>
        {tested && <p>Discovery succeeded. This does not prove a user can sign in.</p>}
        <h3>Explicit account binding</h3><label>Local principal UUID<input value={principal} onChange={e => setPrincipal(e.target.value)} /></label><label>Exact provider subject<input value={subject} onChange={e => setSubject(e.target.value)} /></label>
        <button disabled={!principal || !subject} onClick={() => { const input: ExternalBindRequest = { providerId: q.data!.id, principalId: principal, subject }; void run(async () => { await post('/auth/external/bindings', input); setSubject('') }) }}>Bind identity</button>
      </>}
    </ReadGate>
    <ReadGate q={bindings} what="external identity bindings">{(bindings.data?.bindings.length ?? 0) > 0 && <ListTable label="External identity bindings" rows={bindings.data?.bindings ?? []} rowKey={b => String(b.id)} columns={[
      { key: 'principal', label: 'Principal', phone: 'name', render: b => b.principalId },
      { key: 'identity', label: 'Issuer / subject', phone: 'detail', render: b => <>{b.issuer} / {b.subject}</> },
      { key: 'revoked', label: 'Revocation', phone: 'state', render: b => b.revoked ? 'revoked' : null },
      { key: 'action', label: '', phone: 'action', render: b => !b.revoked && <button onClick={() => { void run(() => del(`/auth/external/bindings/${b.id}`)) }}>Revoke</button> },
    ]} />}</ReadGate>
  </section>
}

export function MyExternalSignIn({ embedded = false }: { embedded?: boolean }) {
  const me = useGet<AuthMe>('/auth/me')
  const q = useGet<ExternalBindingList>('/auth/external/bindings')
  const [password, setPassword] = useState('')
  const { run, err } = useAction()
  const provider = me.data?.external?.displayName
  const linked = q.data?.bindings.filter(b => !b.revoked) ?? []
  const passwordField = <input type="password" aria-label="Your password" placeholder="Your password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} />
  return <>{!embedded && <><h1>Your account</h1><ReadGate q={me} what="your account"><p><strong>{me.data?.user}</strong>{me.data?.role === 'admin' && ' · Admin'}</p></ReadGate><TokensCard personal /><AgentsCard canEdit={false} /></>}
  <Section title={embedded ? 'Link your sign-in' : 'External sign-in'} count={q.data ? linked.length : undefined}>
    <div className="account-signin">
    {me.data && !provider && <p className="muted">No external sign-in is turned on here.</p>}
    {provider && <p>Link your {provider} sign-in to this account: enter your password to confirm. Accounts are never matched by email.</p>}
    {(provider || linked.length > 0) && <div className="row">
      {passwordField}
      {provider && <button className="subtle" disabled={!password} onClick={() => { const input: ExternalStartRequest = { name: '', returnPath: '/account', password, link: true }; void run(async () => { const out = await post<ExternalRedirect>('/auth/external/start', input); setPassword(''); window.location.assign(out.url) }) }}>Link sign-in</button>}
    </div>}
    {err && <p className="error">{err}</p>}
    </div>
    <ReadGate q={q} what="your external identities">{(q.data?.bindings.length ?? 0) > 0 && <ListTable label="Your external identities" rows={q.data?.bindings ?? []} rowKey={b => String(b.id)} columns={[
      { key: 'issuer', label: 'Issuer', phone: 'name', render: b => b.issuer },
      { key: 'subject', label: 'Subject', phone: 'detail', render: b => b.subject },
      { key: 'state', label: 'Revocation', phone: 'state', render: b => b.revoked ? <span className="muted small">revoked</span> : null },
      { key: 'action', label: '', phone: 'action', render: b => !b.revoked && <button className="subtle" disabled={!password} title={password ? undefined : 'Enter your password above first'} onClick={() => { void run(async () => { await post(`/auth/external/bindings/${b.id}/unlink`, { password }); setPassword('') }) }}>Unlink</button> },
    ]} />}</ReadGate>
  </Section></>
}

export function externalSignInFailure(code: string | null): string {
  if (code === 'unavailable') return 'The external sign-in provider could not be reached. Try again or use your local password.'
  if (code === 'name-unavailable') return 'That account name is already used. Choose another username and start external sign-in again.'
  if (code === 'unbound') return `This external identity is not linked to an available ${PRODUCT} account. Ask an administrator to check its binding.`
  if (code === 'failed') return 'External sign-in or linking could not complete. Start again or use your local password. An existing local session is unchanged.'
  return ''
}
