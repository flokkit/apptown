// The starter app for this way of working: a list kept in this browser.
// It is not your app yet; say what yours should be and it will be
// reshaped or replaced. The page works with no server and no network.
import { add, parse, remove, save, type List } from './list'

const KEY = 'list'

function load(): List {
  try {
    return parse(localStorage.getItem(KEY))
  } catch {
    return [] // storage can be switched off; the app still opens
  }
}

function store(list: List): void {
  try {
    localStorage.setItem(KEY, save(list))
  } catch {
    // Without storage the list lasts until the page is closed.
  }
}

function element<T extends HTMLElement>(id: string): T {
  const el = document.getElementById(id)
  if (!el) throw new Error(`the page has no #${id}`)
  return el as T
}

const form = element<HTMLFormElement>('add')
const input = element<HTMLInputElement>('item')
const items = element<HTMLUListElement>('items')
const empty = element<HTMLParagraphElement>('empty')

let list = load()

function render(): void {
  items.replaceChildren(
    ...list.map((text, index) => {
      const li = document.createElement('li')
      li.textContent = text
      const button = document.createElement('button')
      button.type = 'button'
      button.textContent = 'Remove'
      button.setAttribute('aria-label', `Remove ${text}`)
      button.addEventListener('click', () => {
        list = remove(list, index)
        store(list)
        render()
      })
      li.append(button)
      return li
    }),
  )
  empty.hidden = list.length > 0
}

form.addEventListener('submit', (event) => {
  event.preventDefault()
  list = add(list, input.value)
  store(list)
  input.value = ''
  render()
})

render()
