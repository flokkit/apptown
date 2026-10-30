import { useState } from 'react'
import { del, post } from '../../api'
import { QuietConfirm } from '../../QuietConfirm'
import { useAction, useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { ServiceKindList } from '../../wire.gen'

// --- Runtime-defined service kinds ---
// A service kind declares a capability whose contributions are data: env
// templates, steering, guidance, resource fields. New org services without
// touching the binary; provisioning stays adapter territory.


const KIND_EXAMPLE = `capability: sso
title: SSO (OIDC)
description: Organisational single sign-on; products wire OIDC middleware.
connector:
  fields:
    - key: url
      label: Issuer base URL
      placeholder: https://auth.example.com
resource:
  fields:
    - key: client_id
      label: OIDC client ID
    - key: client_secret_from
      label: Client secret (secret name)
      type: secretRef
env:
  - key: OIDC_ISSUER
    value: "{{connector.url}}"
  - key: OIDC_CLIENT_ID
    value: "{{resource.client_id}}"
  - key: OIDC_CLIENT_SECRET
    value: "{{secret:resource.client_secret_from}}"
steering: |
  This product authenticates via organisational SSO (OIDC). Issuer, client
  id and secret are injected as OIDC_ISSUER / OIDC_CLIENT_ID /
  OIDC_CLIENT_SECRET — wire the middleware from env, never hardcode.
`

export function ServiceKindsPanel() {
  const kindsQ = useGet<ServiceKindList>('/services', { kind: 'config' })
  const kinds = kindsQ.data?.services ?? []
  const [editing, setEditing] = useState<string | null>(null)
  const { run, busy, err } = useAction()

  const save = () => run(async () => { await post('/services', { yaml: editing }); setEditing(null) })
  const remove = (capability: string) => run(() => del(`/services/${capability}`))

  return (
    <div style={{ maxWidth: 720 }}>
      <div className="muted small" style={{ marginBottom: 12 }}>
        Define service settings and agent guidance in YAML. Projects attach services under Settings › Services.
      </div>
      <ReadStatus q={kindsQ} what="the service kinds" />
      <StaleNote q={kindsQ} />
      {kinds.map((k) => (
        <div key={k.capability} className="card">
          <div className="row">
            <strong>{k.title}</strong>
            <span className="chip">{k.capability}</span>
            {k.error && <span className="chip fail" title={k.error}>invalid</span>}
            <span style={{ flex: 1 }} />
            <button className="subtle" onClick={() => setEditing(k.yaml)}>Edit</button>
            <QuietConfirm label="Delete" ask={`Delete service kind "${k.capability}"?`} onYes={() => void remove(k.capability)} />
          </div>
          {k.description && <div className="muted small" style={{ marginTop: 4 }}>{k.description}</div>}
          <div className="muted small" style={{ marginTop: 4 }}>
            {(k.env ?? []).map((e) => e.key).join(', ') || 'no environment variables'}
          </div>
        </div>
      ))}
      {editing === null ? (
        <button onClick={() => setEditing(KIND_EXAMPLE)}>+ Define service kind</button>
      ) : (
        <div className="card">
          <textarea value={editing} onChange={(e) => setEditing(e.target.value)} rows={20}
            style={{ width: '100%', fontFamily: 'var(--font-mono)', fontSize: 'var(--fs-xs)' }} />
          <div className="row" style={{ marginTop: 8 }}>
            <span style={{ flex: 1 }} />
            <button onClick={() => setEditing(null)}>Cancel</button>
            <button className="primary" disabled={busy} onClick={() => void save()}>Save</button>
          </div>
        </div>
      )}
      {err && <div className="error">{err}</div>}
    </div>
  )
}
