// The list's rules, kept apart from the page so they can be tested
// without a browser.

export type List = readonly string[]

// add returns the list with text appended, trimmed. Blank text and an item
// already on the list (ignoring case) leave the list as it was.
export function add(list: List, text: string): List {
  const item = text.trim()
  if (item === '' || list.some((it) => it.toLowerCase() === item.toLowerCase())) {
    return list
  }
  return [...list, item]
}

// remove returns the list without the item at index.
export function remove(list: List, index: number): List {
  return list.filter((_, i) => i !== index)
}

// parse reads a list saved by save. Anything else reads as an empty list,
// so a damaged or old saved value never stops the app from opening.
export function parse(saved: string | null): List {
  if (saved === null) return []
  try {
    const value: unknown = JSON.parse(saved)
    return Array.isArray(value) ? value.filter((it): it is string => typeof it === 'string') : []
  } catch {
    return []
  }
}

export function save(list: List): string {
  return JSON.stringify(list)
}
