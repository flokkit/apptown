import { useEffect, useState, type ReactNode } from 'react'
import { builtWords, usePreview, type PreviewState } from '../../TryIt'

export function canEmbedPreview(st?: PreviewState): boolean {
  if (st?.state !== 'live' || st.hostOnly || !st.url) return false
  try { return new URL(st.url).protocol === 'https:' } catch { return false }
}

export function PreviewBesideChat({ changeId, children }: { changeId: string; children: (toggle: ReactNode) => ReactNode }) {
  if (!changeId) return children(null)
  return <ChangePreview changeId={changeId}>{children}</ChangePreview>
}

function ChangePreview({ changeId, children }: { changeId: string; children: (toggle: ReactNode) => ReactNode }) {
  const preview = usePreview(changeId)
  const [shown, setShown] = useState(() => {
    try { return localStorage.getItem('preview-beside-chat') === '1' } catch { return false }
  })
  const [desktop, setDesktop] = useState(() => typeof window !== 'undefined' && !!window.matchMedia?.('(width > 1100px)').matches)
  useEffect(() => {
    if (!window.matchMedia) return
    const query = window.matchMedia('(width > 1100px)')
    const update = () => setDesktop(query.matches)
    update()
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [])
  const eligible = desktop && preview.current && canEmbedPreview(preview.data ?? undefined)
  return <div className={`conversation-split${eligible && shown ? ' with-preview' : ''}`}>
    <div className="conversation-chat">
      {children(eligible ? <label className="preview-toggle small"><input type="checkbox" checked={shown} onChange={e => {
        setShown(e.target.checked)
        try { localStorage.setItem('preview-beside-chat', e.target.checked ? '1' : '0') } catch { /* Storage may be disabled. */ }
      }} /> Preview beside chat</label> : null)}
    </div>
    {eligible && shown && <section className="conversation-preview" aria-label="Preview">
      <div className="row small"><a href={preview.data!.url} target="_blank" rel="noreferrer">Open in new tab</a><span>{builtWords(preview.data ?? undefined)}</span></div>
      <iframe title="Change preview" src={preview.data!.url} sandbox="allow-scripts allow-forms allow-same-origin allow-popups" />
    </section>}
  </div>
}
