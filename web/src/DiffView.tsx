import { useMemo } from 'react'

// A reviewer's diff viewer — file-by-file cards, hunk structure, line
// numbers, add/del coloring, change stats. Deliberately NOT an editor:
// line-level only, read-only, no comments. (Rebuilding Cursor is the
// failure mode; design/subsystems/console.md §7.)

type DiffLine = { type: 'add' | 'del' | 'ctx' | 'hunk'; old?: number; new?: number; text: string }
type DiffFile = {
  path: string; status: 'modified' | 'added' | 'deleted' | 'renamed' | 'binary'
  oldPath?: string; adds: number; dels: number; lines: DiffLine[]
}

export function parseUnifiedDiff(raw: string): DiffFile[] {
  const files: DiffFile[] = []
  let f: DiffFile | null = null
  let oldN = 0, newN = 0

  for (const line of raw.split('\n')) {
    if (line.startsWith('diff --git ')) {
      f = { path: '', status: 'modified', adds: 0, dels: 0, lines: [] }
      files.push(f)
      continue
    }
    if (!f) continue
    if (line.startsWith('--- ')) {
      const p = line.slice(4).replace(/^a\//, '')
      if (p !== '/dev/null') f.oldPath = p
      continue
    }
    if (line.startsWith('+++ ')) {
      const p = line.slice(4).replace(/^b\//, '')
      if (p === '/dev/null') { f.status = 'deleted'; f.path = f.oldPath ?? '' }
      else {
        f.path = p
        if (f.oldPath === undefined) f.status = 'added'
        else if (f.oldPath !== p) f.status = 'renamed'
      }
      continue
    }
    if (line.startsWith('new file mode')) { f.status = 'added'; continue }
    if (line.startsWith('deleted file mode')) { f.status = 'deleted'; continue }
    if (line.startsWith('rename from ')) { f.oldPath = line.slice(12); continue }
    if (line.startsWith('rename to ')) { f.path = line.slice(10); f.status = 'renamed'; continue }
    if (line.startsWith('Binary files')) {
      f.status = 'binary'
      const m = line.match(/^Binary files (?:a\/)?(.+?) and (?:b\/)?(.+?) differ$/)
      if (m) f.path = m[2] !== '/dev/null' ? m[2] : m[1]
      continue
    }
    if (line.startsWith('index ') || line.startsWith('similarity ') || line.startsWith('old mode') || line.startsWith('new mode')) continue
    const hunk = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/)
    if (hunk) {
      oldN = parseInt(hunk[1], 10)
      newN = parseInt(hunk[2], 10)
      f.lines.push({ type: 'hunk', text: line })
      continue
    }
    if (line.startsWith('+')) { f.lines.push({ type: 'add', new: newN++, text: line.slice(1) }); f.adds++ }
    else if (line.startsWith('-')) { f.lines.push({ type: 'del', old: oldN++, text: line.slice(1) }); f.dels++ }
    else if (line.startsWith(' ')) f.lines.push({ type: 'ctx', old: oldN++, new: newN++, text: line.slice(1) })
    else if (line.startsWith('\\')) f.lines.push({ type: 'ctx', text: line }) // "\ No newline at end of file"
  }
  return files.filter((x) => x.path || x.lines.length > 0)
}

const statusChip: Record<DiffFile['status'], string> = {
  modified: '', added: 'pass', deleted: 'fail', renamed: 'pending', binary: '',
}

export default function DiffView({ raw }: { raw: string }) {
  const files = useMemo(() => parseUnifiedDiff(raw), [raw])
  if (files.length === 0) return <div className="muted small">(empty diff)</div>
  const totalAdds = files.reduce((n, f) => n + f.adds, 0)
  const totalDels = files.reduce((n, f) => n + f.dels, 0)

  return (
    <div style={{ marginTop: 8 }}>
      <div className="row" style={{ marginBottom: 8 }}>
        <span className="small">{files.length} file{files.length === 1 ? '' : 's'} changed</span>
        <span className="small" style={{ color: 'var(--ok)' }}>+{totalAdds}</span>
        <span className="small" style={{ color: 'var(--bad)' }}>−{totalDels}</span>
        {files.length > 1 && files.map((f) => (
          <a key={f.path} href={`#diff-${f.path}`} className="muted small">{f.path.split('/').pop()}</a>
        ))}
      </div>
      {files.map((f) => (
        <details key={f.path} id={`diff-${f.path}`} className="diff-file" open={f.lines.length <= 400}>
          <summary>
            <span className={`chip ${statusChip[f.status]}`}>{f.status}</span>
            <code>{f.status === 'renamed' ? `${f.oldPath} → ${f.path}` : f.path}</code>
            <span className="small" style={{ color: 'var(--ok)' }}>+{f.adds}</span>
            <span className="small" style={{ color: 'var(--bad)' }}>−{f.dels}</span>
          </summary>
          {f.status === 'binary'
            ? <div className="muted small" style={{ padding: 8 }}>binary file</div>
            : (
              <div className="diff-body">
                <table className="diff-table">
                  <tbody>
                    {f.lines.map((l, i) => (
                      <tr key={i} className={`diff-${l.type}`}>
                        <td className="diff-num">{l.old ?? ''}</td>
                        <td className="diff-num">{l.new ?? ''}</td>
                        <td className="diff-sign">{l.type === 'add' ? '+' : l.type === 'del' ? '−' : ''}</td>
                        <td className="diff-text">{l.type === 'hunk' ? l.text : l.text || ' '}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
        </details>
      ))}
    </div>
  )
}
