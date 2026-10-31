import { NewChangeButton } from '../NewChange'
import { PRODUCT } from '../../../brand'
import { useState } from 'react'
import { del, get, put } from '../../../api'
import type { ProjectSecrets, SecretDeployment, SecretRevealed, SecretStatus, SecretStoreInfo as SecretStore } from '../../../wire.gen'
import { Modal } from '../../../Modal'
import { QuietConfirm } from '../../../QuietConfirm'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import { relTime } from '../../../format'
import { useHolds } from '../../../instancePages'

// Settings › Secrets: the app says which secrets it
// needs (secretspec.toml in the repo); this page says, per deployment,
// where each one comes from. Values go in and never come back out.

export function SecretsTab({ slug }: { slug: string }) {
  const [adding, setAdding] = useState(false)
  const q = useGet<ProjectSecrets>(`/projects/${slug}/secrets`)
  const { data, reload } = q
  const { run, err } = useAction(reload)
  const [editing, setEditing] = useState<{ env: string; name: string } | null>(null)
  const canReveal = useHolds('secret:reveal')
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const reveal = async (env: string, name: string) => {
    try {
      const r = await get<SecretRevealed>(`/projects/${slug}/secrets/${env}/${name}/reveal`)
      setRevealed((m) => ({ ...m, [`${env}/${name}`]: r.value }))
    } catch (e) { alert((e as Error).message) }
  }

  if (!data) return <ReadStatus q={q} what="the project's secrets" />
  const internal = data.stores.find((s) => s.name === 'internal')
  const infisical = data.stores.find((s) => s.name === 'infisical')

  if (data.problem) {
    return (
      <div>
        <StaleNote q={q} />
        <div className="error">{data.problem}</div>
        <p className="muted small">Declared in <code>{data.spec}</code> on main. Edit through a Change.</p>
      </div>
    )
  }
  if (!data.declared) {
    return (
      <div>
        <StaleNote q={q} />
        <p>No secrets yet.</p>
        <button onClick={() => setAdding(true)}>Add secret</button>
        {adding && <NewChangeButton slug={slug} request={{ title: 'Add a secret', body: 'Declare the secret this app needs and explain where the app uses it. Ask me for its name and purpose before editing. Keep its value out of the code and this change; I will set the value in Settings after the change lands.' }} external={{ open: true, onClose: () => setAdding(false) }} />}
        <details className="small"><summary>About secrets</summary><p className="muted small" style={{ maxWidth: 640, marginTop: 0 }}>
          This app declares no secrets. When it needs one · a signing key, an API key · it
          commits a <code>secretspec.toml</code> naming the secret (never the value), and this
          page is where each deployment gets its value from. {PRODUCT} generates keys it can
          make itself.
        </p></details>
        <StoresNote internal={internal} infisical={infisical} />
      </div>
    )
  }

  return (
    <div>
      <p className="muted small" style={{ maxWidth: 680, marginTop: 0 }}>
        <code>{data.spec}</code> declares {data.secrets?.length ?? 0} secrets for <strong>{data.project}</strong>.
        Each deployment says where its values come from. A value you put here goes into the
        built-in store and is never shown again; a value in Infisical is named, not copied.
        {(data.ignored?.length ?? 0) > 0 && (
          <> {PRODUCT} does not use these parts of the declaration: {data.ignored!.join('; ')}.</>
        )}
      </p>
      <StaleNote q={q} />
      {err && <div className="error">{err}</div>}

      {data.deployments.map((env) => (
        <DeploymentCard key={env.name} env={env} profiles={data.choosable ?? data.profiles ?? []} declared={data}
          onProfile={(p) => run(() => put(`/projects/${slug}/secrets/${env.name}`, { profile: p }))}
          onEdit={(name) => setEditing({ env: env.name, name })}
          onUnbind={(name) => run(() => del(`/projects/${slug}/secrets/${env.name}/${name}`))}
          canReveal={canReveal} revealed={revealed}
          onReveal={(name) => reveal(env.name, name)}
          onHide={(name) => setRevealed((m) => { const c = { ...m }; delete c[`${env.name}/${name}`]; return c })} />
      ))}

      <StoresNote internal={internal} infisical={infisical} />

      {editing && (
        <BindModal slug={slug} env={editing.env} name={editing.name} stores={data.stores}
          onClose={() => setEditing(null)} onDone={() => { setEditing(null); reload() }} />
      )}
    </div>
  )
}

function DeploymentCard({ env, profiles, declared, onProfile, onEdit, onUnbind, canReveal, revealed, onReveal, onHide }: {
  env: SecretDeployment; profiles: string[]; declared: ProjectSecrets
  onProfile: (p: string) => void; onEdit: (name: string) => void; onUnbind: (name: string) => void
  canReveal: boolean; revealed: Record<string, string>; onReveal: (name: string) => void; onHide: (name: string) => void
}) {
  const consumersOf = (name: string) => {
    const d = declared.secrets?.find((s) => s.name === name)
    if (!d) return ''
    return d.allParts ? 'every part' : d.consumers.join(', ')
  }
  return (
    <div className="card" style={{ marginBottom: 12 }}>
      <div className="row" style={{ marginBottom: 6 }}>
        <strong>{env.name}</strong>
        {env.missing.length > 0
          ? <span className="chip fail">{env.missing.length} missing</span>
          : <span className="chip pass">ready</span>}
        <span style={{ flex: 1 }} />
        <span className="muted small">profile</span>
        <select value={env.chosen || env.profile} onChange={(e) => onProfile(e.target.value)}>
          {profiles.map((p) => <option key={p} value={p}>{p}</option>)}
        </select>
        {env.chosen && env.chosen !== env.profile && <span className="muted small" title="secretspec.toml does not declare this profile, so its secrets come from the one named">secrets: {env.profile}</span>}
      </div>
      {env.missing.length > 0 && (
        <div className="muted small" style={{ marginBottom: 6 }}>
          Nothing provides {env.missing.join(', ')} here. The {env.name === 'preview' ? 'preview' : 'deploy'} stops until it is set.
        </div>
      )}
      <table style={{ width: '100%' }}>
        <thead><tr><th>Secret</th><th>Goes to</th><th>Comes from</th><th></th></tr></thead>
        <tbody>
          {env.secrets.map((s) => (
            <tr key={s.name}>
              <td><code>{s.name}</code>{!s.required && <span className="muted small"> optional</span>}</td>
              <td className="muted small">{consumersOf(s.name)}</td>
              <td><Source s={s} /></td>
              <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                {canReveal && s.bound && s.store === 'internal' && (
                  revealed[`${env.name}/${s.name}`] !== undefined
                    ? <span className="small" style={{ marginRight: 8 }}><code>{revealed[`${env.name}/${s.name}`]}</code> <button className="subtle" onClick={() => onHide(s.name)}>Hide</button></span>
                    : <QuietConfirm label="Reveal…" className="subtle" ask={`Show the value of ${s.name} for ${env.name}? This read is recorded in the audit log under your name.`} onYes={() => onReveal(s.name)} />
                )}
                <button className="subtle" onClick={() => onEdit(s.name)}>{s.bound && !s.generated ? 'Change…' : 'Set…'}</button>
                {s.bound && (
                  <QuietConfirm label="Unbind" ask={`Forget ${s.name} for ${env.name}? ${s.store === 'internal' ? 'The stored value is deleted.' : 'The Infisical key stays where it is.'}`}
                    onYes={() => onUnbind(s.name)} />
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Source({ s }: { s: SecretStatus }) {
  if (s.bound && s.generated) {
    return <span className="small">made by {PRODUCT} <span className="muted">· built-in store v{s.version}</span></span>
  }
  if (s.bound && s.store === 'internal') {
    return <span className="small">built-in store v{s.version} <span className="muted">· set by {s.updatedBy}{s.updatedAt ? `, ${relTime(s.updatedAt)}` : ''}</span></span>
  }
  if (s.bound && s.store === 'infisical') {
    return <span className="small">Infisical key <code>{s.ref}</code> <span className="muted">· bound by {s.updatedBy}</span></span>
  }
  if (s.generated) return <span className="small muted">{PRODUCT} will make one on the first boot</span>
  if (s.default) return <span className="small muted">the declaration's default</span>
  if (!s.required) return <span className="small muted">not set (optional)</span>
  return <span className="chip fail">not set</span>
}

function BindModal({ slug, env, name, stores, onClose, onDone }: {
  slug: string; env: string; name: string; stores: SecretStore[]; onClose: () => void; onDone: () => void
}) {
  const infisical = stores.find((s) => s.name === 'infisical')
  const [store, setStore] = useState<'internal' | 'infisical'>('internal')
  const [value, setValue] = useState('')
  const [ref, setRef] = useState(name)
  const { run, busy, err } = useAction()
  const save = async () => {
    const body = store === 'internal' ? { store, value } : { store, ref }
    const r = await run(() => put(`/projects/${slug}/secrets/${env}/${name}`, body))
    if (r) onDone()
  }
  return (
    <Modal onClose={onClose} title={<h2 style={{ margin: 0 }}>{name} <span className="muted small">in {env}</span></h2>}>
      <div className="row" style={{ gap: 6, marginBottom: 10 }}>
        <button className={store === 'internal' ? 'primary' : 'subtle'} onClick={() => setStore('internal')}>Type a value</button>
        <button className={store === 'infisical' ? 'primary' : 'subtle'} disabled={!infisical?.available}
          title={infisical?.available ? '' : infisical?.note} onClick={() => setStore('infisical')}>Name an Infisical key</button>
      </div>
      {store === 'internal' ? (
        <>
          <textarea rows={3} value={value} onChange={(e) => setValue(e.target.value)} placeholder="the value · pasted once, never shown again" autoFocus style={{ width: '100%' }} />
          <p className="muted small">Database backups include the encryption key and can reveal all secret values. Use Infisical for team secrets.</p>
        </>
      ) : (
        <>
          <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="KEY_NAME in Infisical" autoFocus style={{ width: '100%' }} />
          <p className="muted small">The key must exist in the configured Infisical project and environment.</p>
        </>
      )}
      {err && <div className="error">{err}</div>}
      <div className="row" style={{ marginTop: 10 }}>
        <button className="primary" onClick={save} disabled={busy || (store === 'internal' ? !value.trim() : !ref.trim())}>Save</button>
        <button onClick={onClose}>Cancel</button>
      </div>
    </Modal>
  )
}

function StoresNote({ internal, infisical }: { internal?: SecretStore; infisical?: SecretStore }) {
  return (
    <details style={{ marginTop: 8 }}>
      <summary className="muted small" style={{ cursor: 'pointer' }}>Secret stores</summary>
      <ul className="small" style={{ marginTop: 6 }}>
        {internal && <li><strong>{internal.title}</strong> — {internal.note}</li>}
        {infisical && <li><strong>{infisical.title}</strong>{infisical.available ? '' : ' (not set up)'} — {infisical.note}</li>}
      </ul>
    </details>
  )
}
