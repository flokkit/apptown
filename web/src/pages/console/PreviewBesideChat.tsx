import { useState, type ReactNode } from 'react'
import { useFolded } from '../../chrome'
import { builtWords, usePreview, type PreviewState } from '../../TryIt'
import { Switch } from '../../Switch'

export function canEmbedPreview(st?: PreviewState): boolean {
  if (st?.state !== 'live' || st.hostOnly || !st.url) return false
  try { return new URL(st.url).protocol === 'https:' } catch { return false }
}

export function PreviewBesideChat({ changeId, children }: { changeId: string; children: (toggle: ReactNode, shown: boolean) => ReactNode }) {
  if (!changeId) return children(null, false)
  return <ChangePreview changeId={changeId}>{children}</ChangePreview>
}

function ChangePreview({ changeId, children }: { changeId: string; children: (toggle: ReactNode, shown: boolean) => ReactNode }) {
  const preview = usePreview(changeId)
  const [shown, setShown] = useState(() => {
    try { return localStorage.getItem('preview-beside-chat') === '1' } catch { return false }
  })
  // Beside the chat only where the nav is shown: the same width decides both.
  const desktop = !useFolded()
  const eligible = desktop && preview.current && canEmbedPreview(preview.data ?? undefined)
  return <div className={`conversation-split${eligible && shown ? ' with-preview' : ''}`}>
    <div className="conversation-chat">
      {children(eligible ? <Switch label="Preview beside chat" checked={shown} onChange={on => {
        setShown(on)
        try { localStorage.setItem('preview-beside-chat', on ? '1' : '0') } catch { /* Storage may be disabled. */ }
      }} /> : null, !!(eligible && shown))}
    </div>
    {eligible && shown && <section className="conversation-preview" aria-label="Preview">
      <div className="row small"><a href={preview.data!.url} target="_blank" rel="noreferrer">Open in new tab</a><span>{builtWords(preview.data ?? undefined)}</span></div>
      <iframe title="Change preview" src={preview.data!.url} sandbox="allow-scripts allow-forms allow-same-origin allow-popups" />
    </section>}
  </div>
}
