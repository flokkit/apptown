// Size belongs to this browser. A URL hint changes only the current load.
export type Size = 'small' | 'medium' | 'large'
const KEY = 'apptown-size'

function isSize(value: string | null): value is Size {
  return value === 'small' || value === 'medium' || value === 'large'
}

export function currentSize(): Size {
  const size = document.documentElement.getAttribute('data-size')
  return isSize(size) ? size : 'medium'
}

export function applySize(size: Size) {
  document.documentElement.setAttribute('data-size', size)
  try { localStorage.setItem(KEY, size) } catch { /* private window */ }
}

export function bootSize() {
  const hint = new URLSearchParams(location.search).get('size')
  let stored: string | null = null
  try { stored = localStorage.getItem(KEY) } catch { /* private window */ }
  document.documentElement.setAttribute('data-size', isSize(hint) ? hint : isSize(stored) ? stored : 'medium')
}
