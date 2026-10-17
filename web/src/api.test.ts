import { describe, expect, it } from 'vitest'
import { findingFixPayload } from './api'

describe('findingFixPayload', () => {
  const vuln = {
    identifiers: ['CVE-2026-0001', 'GHSA-xxxx'], title: 'Something bad in foo', detail: 'foo lets anyone in',
    source: 'osv', fingerprint: 'fp123',
    package: { name: 'foo', version: '1.2.3', fixedIn: '1.2.4' },
  }

  it('names the finding, the package and the fix version', () => {
    const p = findingFixPayload(vuln)
    expect(p.title).toBe('Fix CVE-2026-0001 in foo@1.2.3')
    expect(p.body).toContain('Fix the security finding CVE-2026-0001 reported by osv.')
    expect(p.body).toContain('Location: foo@1.2.3')
    expect(p.body).toContain('Finding: foo lets anyone in')
    expect(p.body).toContain('Known fix version: 1.2.4.')
    expect(p.body).toContain('fingerprint fp123')
    expect(p.intent).toContain('osv finding CVE-2026-0001')
    expect(p.intent).toContain('fp123')
  })

  it('prefers a file location over the package', () => {
    const p = findingFixPayload({ ...vuln, location: { file: 'src/x.go', line: 42 } })
    expect(p.title).toBe('Fix CVE-2026-0001 in src/x.go:42')
    expect(p.body).toContain('Location: src/x.go:42')
  })

  it('falls back to the title when there is no identifier or detail', () => {
    const p = findingFixPayload({ title: 'Hard-coded secret', source: 'gitleaks', fingerprint: 'fp9' })
    expect(p.title).toBe('Fix Hard-coded secret')
    expect(p.body).toContain('Finding: Hard-coded secret')
    expect(p.body).not.toContain('Location:')
    expect(p.body).not.toContain('Known fix version')
  })

  it('keeps the title within 120 characters', () => {
    const p = findingFixPayload({ title: 'x'.repeat(300), source: 's', fingerprint: 'f' })
    expect(p.title.length).toBeLessThanOrEqual(120)
  })
})
