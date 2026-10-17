import { describe, expect, it } from 'vitest'
import { rankProjects } from './findRank'

const projects = [
  { name: 'Open source tracker', slug: 'ost', blurb: 'keeps tabs on upstreams' },
  { name: 'Bakery', slug: 'bakery', blurb: 'the source of sourdough recipes' },
  { name: 'Sourdough timer', slug: 'sourdough-timer', blurb: 'bread, timed' },
  { name: 'Unrelated', slug: 'nothing', blurb: '' },
  { name: 'Cat pictures', slug: 'cats', namespace: 'sour-grapes', blurb: '' },
]

describe('rankProjects', () => {
  it("'sour' finds the sourdough app before a name that merely contains it, before a blurb that says source", () => {
    expect(rankProjects(projects, 'sour').map((p) => p.slug))
      .toEqual(['sourdough-timer', 'ost', 'bakery', 'cats'])
  })

  it('leaves out projects that match nowhere', () => {
    expect(rankProjects(projects, 'sour').some((p) => p.slug === 'nothing')).toBe(false)
  })

  it('returns everything, untouched, for an empty query', () => {
    expect(rankProjects(projects, '   ')).toBe(projects)
  })

  it('matches the slug and ignores case', () => {
    expect(rankProjects(projects, 'OST')[0].slug).toBe('ost')
  })

  it('breaks ties by name', () => {
    const tie = [{ name: 'b-app', slug: 'b' }, { name: 'a-app', slug: 'a' }]
    expect(rankProjects(tie, 'app').map((p) => p.slug)).toEqual(['a', 'b'])
  })
})
