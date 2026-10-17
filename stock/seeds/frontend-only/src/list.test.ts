import { describe, expect, it } from 'vitest'
import { add, parse, remove, save } from './list'

describe('the list', () => {
  it('adds trimmed items and skips blanks and repeats', () => {
    let list = add([], '  milk ')
    list = add(list, '')
    list = add(list, 'Milk')
    list = add(list, 'bread')
    expect(list).toEqual(['milk', 'bread'])
  })

  it('removes the item at an index', () => {
    expect(remove(['a', 'b', 'c'], 1)).toEqual(['a', 'c'])
  })

  it('reads back what it saved, and reads anything else as empty', () => {
    expect(parse(save(['a', 'b']))).toEqual(['a', 'b'])
    expect(parse(null)).toEqual([])
    expect(parse('not json')).toEqual([])
    expect(parse('{"a":1}')).toEqual([])
  })
})
