import { PRODUCT } from '../../../brand'
import { useState } from 'react'
import { post } from '../../../api'
import { useAction, useGet } from '../../../data'
import { ReadStatus, StaleNote } from '../../../ReadGate'
import type { RepositoryAttachmentState } from '../../../wire.gen'

// Durable knowledge, not a provider health probe. Resuming keeps the recorded
// upstream choice; it never creates a replacement for an unknown result.
export function RepositoryAttachment({ slug }: { slug: string }) {
  const path = `/projects/${slug}/repo/remote`
  const q = useGet<RepositoryAttachmentState>(path)
  const [providerId, setProviderId] = useState('')
  const { run, busy, err } = useAction(undefined, { affects: [path, `/projects/${slug}/overview`] })
  const state = q.data
  if (!state) return <ReadStatus q={q} what="the upstream repository" />
  const request = state.request
  const uncertainCreate = request?.create && state.state !== 'identified'
  const id = Number(providerId)
  return <div style={{ marginTop: 12 }}>
    <StaleNote q={q} />
    {state.state === 'none' ? <span className="muted small">No upstream repository is recorded.</span>
      : state.state === 'attached' ? <span className="muted small">Verified upstream: {state.remote}</span>
      : state.state === 'unverified' ? <span className="muted small">Existing upstream: {state.remote}. Its provider identity will be verified before use.</span>
      : <>
        <div>Upstream attachment needs attention: {state.repositoryName || state.remote || state.attachmentId}</div>
        {uncertainCreate ? <p className="small">The creation result is unknown. {PRODUCT} will not create another repository. Find the repository created by the original account with description <code>AppTown repository attachment {state.attachmentId}</code>, then enter its GitLab project ID.</p>
          : <p className="small">The original repository choice is retained. Resume to finish verification and the first push.</p>}
        {state.canResume && request ? <div className="row">
          {uncertainCreate && <label>GitLab project ID <input inputMode="numeric" value={providerId} onChange={(e) => setProviderId(e.target.value)} /></label>}
          <button disabled={busy || !!q.err || (uncertainCreate && (!Number.isSafeInteger(id) || id <= 0))}
            onClick={() => { void run(() => post(path, { ...request, recoverProviderId: uncertainCreate ? id : 0 })) }}>Resume original attachment</button>
        </div> : <p className="small muted">The person who requested this attachment can resume it.</p>}
      </>}
    {err && <div className="error">{err}</div>}
  </div>
}
