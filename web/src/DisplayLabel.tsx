import { useState } from 'react'
import { put } from './api'
import { useAction } from './data'
import type { Done } from './wire.gen'

type DisplayLabelProps = {
  value: string; machineKey: string; path: string; onChanged?: () => void
}

export function DisplayLabelEdit(props: DisplayLabelProps) {
  return <DisplayLabelForm key={props.path} {...props} />
}

function DisplayLabelForm({ value, machineKey, path, onChanged }: DisplayLabelProps) {
  const [draft, setDraft] = useState<{ expected: string; label: string } | null>(null)
  const { run, busy, err } = useAction(onChanged)
  return <div className="small" style={{ marginTop: 6 }}>
    <span className="muted">Key: <code>{machineKey}</code></span>{' '}
    {!draft ? <button className="link" onClick={() => setDraft({ expected: value, label: value })}>Edit display name</button> :
      <form className="row" onSubmit={async (event) => {
        event.preventDefault()
        const result = await run(() => put<Done>(path, { expectedLabel: draft.expected, label: draft.label }))
        if (result) setDraft(null)
      }}>
        <label>Display name <input value={draft.label} maxLength={128} onChange={(event) => setDraft({ ...draft, label: event.target.value })} /></label>
        <button disabled={busy || !draft.label.trim()} type="submit">Save name</button>
        <button disabled={busy} type="button" onClick={() => { setDraft(null) }}>Cancel</button>
      </form>}
    {err && <div className="error" role="alert">{err}</div>}
  </div>
}
