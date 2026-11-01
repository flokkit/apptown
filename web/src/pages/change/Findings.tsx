import { Status } from '../../Status'
import { useCallback } from 'react'
import { FindingsTable } from '../../FindingsTable'
import { useGet } from '../../data'
import { ReadStatus, StaleNote } from '../../ReadGate'
import type { RunFindingsView } from '../../wire.gen'

// A run's findings: the normalized view over its scan reports, with
// per-finding acceptance — see FindingsTable. A report AppTown could not
// read is listed with its reason; with any such report, an empty list is
// not "no findings", since what it holds is unknown.
export function RunFindings({ runId, onChange }: { runId: string; onChange: () => void }) {
  const q = useGet<RunFindingsView>(`/runs/${runId}/findings`)
  const { data, reload } = q
  const changed = useCallback(() => { reload(); onChange() }, [reload, onChange])
  if (!data) return <ReadStatus q={q} what="the findings" className="small" />
  const unread = data.unread ?? []
  return (
    <>
      <StaleNote q={q} />
      {unread.map((u) => (
        <div key={`${u.kind}:${u.name}`} className="small" style={{ marginBottom: 6 }}>
          <Status word="unknown" help="This report could not be read. Re-run its scan in a change, or ask a project member with permission to do so." /> <code>{u.name}</code>: {u.reason}
        </div>
      ))}
      {unread.length > 0 && data.findings.length === 0
        ? <div className="muted small">No findings in the reports that could be read; the unread ones may have some.</div>
        : <FindingsTable findings={data.findings} projectSlug={data.projectSlug} onChanged={changed} />}
    </>
  )
}
