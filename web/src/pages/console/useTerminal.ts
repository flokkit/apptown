import { type RefObject, useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

// The terminal is the chat at forensic zoom: xterm.js speaks to apptownd over a
// websocket; apptownd holds the PTY on the session's container. Its bytes are a
// stream, not a read, so they stay out of the query cache. Sessions survive
// disconnects (the server replays its ring buffer on reconnect).

export type TerminalStatus = 'connecting' | 'attached' | 'reconnecting'

// xterm paints on a canvas and cannot read CSS variables, so the terminal
// takes the theme's --term-* tokens as resolved values when it opens.
function termTheme() {
  const css = getComputedStyle(document.documentElement)
  const v = (name: string) => css.getPropertyValue(name).trim()
  return {
    background: v('--term-bg'),
    foreground: v('--term-fg'),
    cursor: v('--term-cursor'),
    selectionBackground: v('--term-selection'),
  }
}

// useTerminal attaches the terminal in `into` to the session's PTY and
// reconnects when the socket drops. It reconnects for another session, a
// change in whether the reader may act, or taking control, never for a new
// copy of the same session's view. Keystrokes go to the helper only in
// control; the server refuses them otherwise.
export function useTerminal(sessionId: string | null, mayAct: boolean, into: RefObject<HTMLDivElement | null>, visible = false, control = false): TerminalStatus {
  const [connection, setConnection] = useState<{ id: string | null; status: TerminalStatus }>({ id: null, status: 'connecting' })
  const terminalRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    if (!sessionId || !into.current) return
    const setStatus = (status: TerminalStatus) => setConnection({ id: sessionId, status })
    setStatus('connecting')
    // Resolve rem through an element: xterm requires a size in CSS pixels.
    const fontProbe = document.createElement('span')
    fontProbe.hidden = true
    fontProbe.style.fontSize = 'var(--fs-sm)'
    into.current.append(fontProbe)
    const fontSize = () => parseFloat(getComputedStyle(fontProbe).fontSize)
    const term = new Terminal({
      fontSize: fontSize(),
      fontFamily: getComputedStyle(document.documentElement).getPropertyValue('--font-mono').trim(),
      theme: termTheme(),
      scrollback: 5000,
      allowProposedApi: true,
      disableStdin: !control,
    })
    terminalRef.current = term
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(into.current)
    fit.fit()

    const enc = new TextEncoder()
    let closed = false
    let retry = 0
    const pending: Uint8Array[] = [] // keystrokes typed before the socket opens

    const connect = () => {
      if (closed) return
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const ws = new WebSocket(`${proto}://${location.host}/api/v1/console/${sessionId}/pty${control ? '?control=1' : ''}`)
      ws.binaryType = 'arraybuffer'
      wsRef.current = ws
      ws.onopen = () => {
        retry = 0
        setStatus('attached')
        term.reset() // server replays its ring buffer from scratch
        fit.fit()
        if (!mayAct) return // a watcher's terminal only shows
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
        for (const p of pending) ws.send(p)
        pending.length = 0
      }
      ws.onmessage = (ev) => {
        term.write(new Uint8Array(ev.data as ArrayBuffer))
      }
      ws.onclose = () => {
        if (closed) return
        setStatus('reconnecting')
        retry++
        setTimeout(connect, Math.min(1000 * retry, 5000))
      }
    }
    connect()

    const dataSub = term.onData((d) => {
      if (!control) return
      if (wsRef.current?.readyState === WebSocket.OPEN) wsRef.current.send(enc.encode(d))
      else pending.push(enc.encode(d))
    })
    const resizeSub = term.onResize(({ cols, rows }) => {
      if (mayAct && wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(JSON.stringify({ type: 'resize', cols, rows }))
      }
    })
    const refit = () => {
      term.options.fontSize = fontSize()
      fit.fit()
    }
    const sizeObserver = new MutationObserver(refit)
    sizeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-size'] })
    const ro = new ResizeObserver(refit)
    ro.observe(into.current)

    return () => {
      closed = true
      ro.disconnect()
      sizeObserver.disconnect()
      fontProbe.remove()
      dataSub.dispose()
      resizeSub.dispose()
      wsRef.current?.close()
      terminalRef.current = null
      term.dispose()
    }
  }, [sessionId, mayAct, control, into])

  // Opening the hidden terminal must not take focus from a draft. Zooming
  // into the terminal still puts the cursor there, ready to type.
  useEffect(() => { if (visible) terminalRef.current?.focus() }, [visible, sessionId, mayAct])

  return connection.id === sessionId ? connection.status : 'connecting'
}
