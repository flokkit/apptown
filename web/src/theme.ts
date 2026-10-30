// Theme: follows the system by default; a person can pin light or dark.
// Stored per browser, applied before first paint (main.tsx) so nothing
// flashes. Precision instrument tokens live in styles.css — this only flips the switch.
import { useEffect, useState } from 'react'

export type Theme = 'system' | 'light' | 'dark'
const KEY = 'apptown-theme'

export function currentTheme(): Theme {
  try {
    const t = localStorage.getItem(KEY)
    return t === 'light' || t === 'dark' ? t : 'system'
  } catch { return 'system' }
}

export function applyTheme(t: Theme) {
  const root = document.documentElement
  if (t === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', t)
  try { if (t === 'system') localStorage.removeItem(KEY); else localStorage.setItem(KEY, t) } catch { /* private window */ }
}

// A URL hint (?theme=light) wins for that load — handy for screenshots and
// for showing someone a page in the other mode without touching settings.
export function bootTheme() {
  const hint = new URLSearchParams(location.search).get('theme')
  if (hint === 'light' || hint === 'dark') { document.documentElement.setAttribute('data-theme', hint); return }
  applyTheme(currentTheme())
}

// Whether the page is showing dark right now: a pinned theme, else the
// system's. For drawings that pick their own colours (docs diagrams).
export function isDarkNow(): boolean {
  const pinned = document.documentElement.getAttribute('data-theme')
  if (pinned === 'dark' || pinned === 'light') return pinned === 'dark'
  return !!window.matchMedia?.('(prefers-color-scheme: dark)').matches
}

// isDarkNow, kept current as the person switches theme or the system does.
export function useDarkTheme(): boolean {
  const [dark, setDark] = useState(isDarkNow)
  useEffect(() => {
    const update = () => setDark(isDarkNow())
    const mo = new MutationObserver(update)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    const mq = window.matchMedia?.('(prefers-color-scheme: dark)')
    mq?.addEventListener?.('change', update)
    return () => { mo.disconnect(); mq?.removeEventListener?.('change', update) }
  }, [])
  return dark
}
