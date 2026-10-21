import { useEffect, useRef } from 'react'
import { useLocation } from 'react-router-dom'
import { PRODUCT } from './brand'
import { pageEntry, reloadForUpdate, served, updated } from './pageUpdate'

// UpdateNote tells the person when the AppTown they are looking at has been
// updated since this tab loaded, and reloads the tab the next time they go
// to another page. The page they are on stays as it is until then, so a
// half-written form is never thrown away by a reload.
export function UpdateNote() {
  const isUpdated = updated(pageEntry(), served.use())
  const { pathname } = useLocation()
  const first = useRef(true)
  useEffect(() => {
    if (first.current) { first.current = false; return }
    reloadForUpdate()
  }, [pathname])
  if (!isUpdated) return null
  return (
    <div className="card tone-warn small update-note" role="status" style={{ marginBottom: 10 }}>
      {PRODUCT} was updated since this page opened.{' '}
      <button className="subtle" onClick={() => window.location.reload()}>Reload to get the new version</button>
      <span className="muted"> (it reloads by itself when you go to another page)</span>
    </div>
  )
}
