import { useLayoutEffect, useRef, type ReactNode } from 'react'

// ---- the input -----------------------------------------------------------
// One line that grows to five, then scrolls. Attach, and send. Stop joins
// only while the helper works: in Send's place while the box is empty, as a
// small square beside Send once something is typed, so the box keeps its
// width on a phone. With a keyboard Enter sends; on a touch screen Enter
// adds a line and the button sends.
export type DraftFile = { file: File; name: string; path: string; thumb: string }

export const COARSE = '(pointer: coarse)'
export const enterSends = () => !(typeof window !== 'undefined' && window.matchMedia?.(COARSE).matches)

export function Composer({ text, onText, files, onRemoveFile, onAddFiles, onSend, onStop, busy, top }: {
  text: string; onText: (t: string) => void
  files: DraftFile[]; onRemoveFile: (i: number) => void; onAddFiles: (f: File[]) => void
  onSend: () => void; onStop?: () => void; busy: boolean
  top?: ReactNode
}) {
  const box = useRef<HTMLTextAreaElement>(null)
  const picker = useRef<HTMLInputElement>(null)
  // Fit the box to what is typed, up to five lines (the stylesheet's cap);
  // browsers with field-sizing do this themselves.
  useLayoutEffect(() => {
    const el = box.current
    if (!el || (typeof CSS !== 'undefined' && CSS.supports?.('field-sizing', 'content'))) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight + 2}px`
  }, [text])
  const empty = !text.trim() && files.length === 0
  const keepKeyboard = (e: React.PointerEvent) => e.preventDefault()
  return <div className="composer-dock">
    {top}
    {files.length > 0 && <div className="drafts">
      {files.map((f, i) => <span key={`${f.name}-${i}`} className="draft">
        {f.thumb ? <img src={f.thumb} alt="picture to send" /> : <span>{f.name} ({f.file.size} bytes)</span>}
        <button type="button" onClick={() => onRemoveFile(i)} title="don't send this file" aria-label="don't send this file">×</button>
      </span>)}
      <span className="muted small">going with your next message</span>
    </div>}
    <div className={'composer' + (busy ? ' working' : '') + (empty ? ' empty' : '')}
      onDragOver={(e) => { if (e.dataTransfer.types.includes('Files')) e.preventDefault() }}
      onDrop={(e) => { e.preventDefault(); onAddFiles(Array.from(e.dataTransfer.files)) }}>
      <input ref={picker} type="file" multiple hidden onChange={(e) => { onAddFiles(Array.from(e.target.files ?? [])); e.target.value = '' }} />
      <button type="button" className="composer-attach" aria-label="Attach files" title="Attach files (or drop or paste them)"
        onPointerDown={keepKeyboard} onClick={() => picker.current?.click()}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11.5l-8 8a5 5 0 01-7-7l8.5-8.5a3.5 3.5 0 015 5L10 17.5a2 2 0 01-3-3l7.5-7.5" /></svg>
      </button>
      <textarea ref={box} rows={1} value={text} aria-label="your message"
        placeholder="Say what to change"
        onChange={(e) => onText(e.target.value)}
        onPaste={(e) => { const f = Array.from(e.clipboardData.files); if (f.length) { e.preventDefault(); onAddFiles(f) } }}
        onKeyDown={(e) => {
          if (e.key !== 'Enter' || e.shiftKey || e.nativeEvent.isComposing) return
          if (!enterSends()) return
          e.preventDefault(); onSend()
        }} />
      {busy && onStop && <button type="button" className={'composer-stop' + (empty ? '' : ' small')} onPointerDown={keepKeyboard} onClick={onStop}
        aria-label="Stop" title="Stop what it's doing right now · then tell it what you'd rather have">
        <i aria-hidden="true" />{empty && <span>Stop</span>}
      </button>}
      {!(busy && onStop && empty) && <button type="button" className="composer-send primary" aria-label="Send" title="Send"
        disabled={empty} onPointerDown={keepKeyboard} onClick={onSend}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V5M6 11l6-6 6 6" /></svg>
      </button>}
    </div>
  </div>
}
