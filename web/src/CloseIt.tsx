import { post } from './api'
import { useAction } from './data'
import { QuietPrompt } from './QuietConfirm'

// CloseIt: close a change that cannot go on, with the reason recorded. Offered
// only where the server's next acts include `abandon` for this reader.
export function CloseIt({ changeId }: { changeId: string }) {
  const { run, err } = useAction()
  return (
  <>
  <QuietPrompt label="Close it" className="subtle danger"
      ask="Close this change? Its record is kept. Why?"
      fields={[{ key: 'reason', placeholder: 'why (required)', required: true, width: 260 }]}
      yes="Close it" onYes={(v) => run(() => post(`/changes/${changeId}/abandon`, { reason: v.reason.trim() }))} />
  {err && <span className="error small">{err}</span>}
  </>
  )
}
