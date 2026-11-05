import type { ChangeView } from '../../api'

export function NotesDiff({ diff }: { diff: ChangeView['notesDiff'] }) {
  if (!diff) return null
  const notes = (lines: string[]) => lines.filter(line => line.startsWith('- ')).length
  return <div className="ch-overview-line">
    <div className="ch-row-label">Project notes</div>
    <div className="ch-value">
      <p>Project notes: adds {notes(diff.added)}, removes {notes(diff.removed)}</p>
      {diff.removed.map((line, i) => <p key={`remove-${i}`}><del>{line}</del></p>)}
      {diff.added.map((line, i) => <p key={`add-${i}`}><ins>{line}</ins></p>)}
    </div>
  </div>
}
