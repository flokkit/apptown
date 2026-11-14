import { describe, expect, it } from 'vitest'
import { changedFiles } from './files'

describe('recorded update observations', () => {
  it('keeps historical package moves when files are absent', () => {
    expect(changedFiles([{ ecosystem: 'npm', package: 'example', from: 'v2026.6', to: 'v2026.7' }])).toBe('example v2026.6 → v2026.7')
  })
  it('shows dependency paths first and counts other files', () => {
    expect(changedFiles([], [{ path: 'src/use.js', class: 'other' }, { path: 'packages/a/package.json', class: 'dependency' }, { path: 'package-lock.json', class: 'dependency' }, { path: 'src/report.js', class: 'other' }])).toBe('packages/a/package.json, package-lock.json · 2 other files')
  })
})
