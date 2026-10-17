import { Component, type ErrorInfo, type ReactNode } from 'react'
import { reportRenderFault } from './errors'

// A render fault on one page must not blank the whole app. The boundary
// shows what happened in plain words and offers the two honest exits:
// reload, or file a complaint if it keeps happening.
export class ErrorBoundary extends Component<{ children: ReactNode }, { error?: Error }> {
  state: { error?: Error } = {}
  static getDerivedStateFromError(error: Error) { return { error } }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('page render failed', error)
    reportRenderFault(error, info.componentStack ?? undefined)
  }
  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="card" style={{ margin: '24px auto', maxWidth: 640, padding: 18 }}>
        <div style={{ fontWeight: 600, marginBottom: 6 }}>This page could not be displayed.</div>
        <div className="muted small" style={{ marginBottom: 10 }}>{this.state.error.message}</div>
        <div className="row" style={{ gap: 10 }}>
          <button className="primary" onClick={() => window.location.reload()}>Reload the page</button>
          <a className="subtle" href="/complaints">If it keeps happening, file a complaint</a>
        </div>
      </div>
    )
  }
}
