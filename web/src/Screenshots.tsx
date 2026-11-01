import { fmtStamp } from './format'
import { useEffect, useState } from 'react'
import { get } from './api'
import type { PreviewScreenshots } from './api'
import { Modal } from './Modal'
import { PRODUCT } from './brand'

// AppTown's pictures of a run's preview, shown where a person approves
// (private/design/plans/preview-screenshots-2026-10-21.md): taken by AppTown
// from the commit the boot step booted, never supplied by the agent. The
// commit is compared with the change's current one: the same, a different
// one, or not compared, and never "current" by default.

export function sameCommitWords(commit?: string, head?: string): { kind: 'same' | 'different' | 'unknown'; text: string } {
  const short = (s: string) => s.slice(0, 7)
  if (!commit || !head) return { kind: 'unknown', text: "could not compare with the change's current commit" }
  if (commit === head) return { kind: 'same', text: "this is the change's current commit" }
  return { kind: 'different', text: `the change is now at ${short(head)}; these show a different commit, ${short(commit)}` }
}

type Loaded = { src?: string; err?: string }

function useShot(id: string): Loaded {
  const [out, setOut] = useState<Loaded>({})
  useEffect(() => {
    const ctl = new AbortController()
    get<{ bodyBase64?: string }>(`/artifacts/${encodeURIComponent(id)}`, { signal: ctl.signal })
      .then((a) => setOut(a.bodyBase64 ? { src: `data:image/png;base64,${a.bodyBase64}` } : { err: 'the picture has no image' }))
      .catch((e) => { if (!ctl.signal.aborted) setOut({ err: (e as Error).message }) })
    return () => ctl.abort()
  }, [id])
  return out
}

function Thumb({ id, label, onOpen }: { id: string; label: string; onOpen: (src: string, label: string) => void }) {
  const { src, err } = useShot(id)
  return (
    <figure className="shot">
      {src
        ? <button className="shot-img" title={`Open ${label} full size`} onClick={() => onOpen(src, label)}><img src={src} alt={`Screenshot of ${label}`} /></button>
        : <div className="shot-img shot-empty">{err ? <span className="error small">could not load: {err}</span> : <span className="muted small">loading…</span>}</div>}
      <figcaption className="muted small">{label}</figcaption>
    </figure>
  )
}

export function Screenshots({ shots, head, at }: { shots?: PreviewScreenshots; head?: string; at?: string }) {
  const [open, setOpen] = useState<{ src: string; label: string } | null>(null)
  if (!shots) return null
  const taken = shots.shots ?? []
  const cmp = sameCommitWords(shots.commit, head)
  return (
    <div className="ch-shots">
      <div className="ch-label">What the preview looked like</div>
      {taken.length > 0 && (
        <>
          <div className="muted small">
            Taken by {PRODUCT} from commit <code>{(shots.commit ?? '').slice(0, 7) || 'unknown'}</code>{at ? ` at ${fmtStamp(at)}` : ''}: {' '}
            <span className={cmp.kind === 'different' ? 'warn' : undefined}>{cmp.text}.</span>
          </div>
          <div className="shot-row">
            {taken.map((s) => <Thumb key={s.artifact} id={s.artifact} label={`${s.path} · ${s.view}`} onOpen={(src, label) => setOpen({ src, label })} />)}
          </div>
        </>
      )}
      {shots.unavailable && <div className="error small">Screenshots could not be taken: {shots.unavailable}</div>}
      {(shots.missing?.length ?? 0) > 0 && <div className="muted small">Not pictured: {shots.missing!.join('; ')}</div>}
      {(shots.ignored?.length ?? 0) > 0 && <div className="muted small">Pages named but not on the preview: {shots.ignored!.join(', ')}</div>}
      {open && (
        <Modal title={open.label} onClose={() => setOpen(null)} maxWidth={1320}>
          <img src={open.src} alt={`Screenshot of ${open.label}`} style={{ maxWidth: '100%', height: 'auto', display: 'block' }} />
        </Modal>
      )}
    </div>
  )
}
