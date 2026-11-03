import { expect, it } from 'vitest'
import { shortName } from './draft'

// A name is the thing, not the sentence describing it.
it.each([
  ['Feet catalog with hundreds of picturs of different feet; big feet, small feet', 'Feet catalog'],
  ['A todo list for my kids', 'Todo list'],
  ['lunch orders, collected by 11am', 'Lunch orders'],
  ['Recipe book', 'Recipe book'],
  ['A very long name for something that has many words', 'Very long name'],
  ['', ''],
])('offers %j as %j', (description, name) => {
  expect(shortName(description)).toBe(name)
})
