import { describe, expect, it } from 'vitest'
import apiSource from './api.ts?raw'
import wireSource from './wire.gen.ts?raw'

// Response shapes have one source: the Go types, generated into wire.gen.ts.
// A type in api.ts is one of those, or derived from one (it names a generated
// type), or one of the few shapes the server leaves open and the page reads
// more precisely. A hand-written copy of a response drifts from the server
// silently; this keeps it from coming back.
const uiOnly = new Set([
  // Words the page switches on, not responses.
  'Doing',
  // A step's config and evidence are open JSON on the server; the page reads
  // them by the step's type.
  'ApprovalWithdrawal', 'ApprovalEvidence', 'AgentEvidence', 'ReviewEvidence', 'ScriptEvidence', 'TestsEvidence', 'ScanEvidence',
  'ServeEvidence', 'ConformanceEvidence', 'CallEvidence', 'RequireEvidence', 'PolicyEvidence',
  'StepConfig', 'ApprovalConfig', 'StepType', 'StepEvidence', 'StepOf',
  // A guidance check's declaration is JSON inside a block's front matter.
  'GuidanceCheck',
])

describe('api.ts response types', () => {
  it('come from the generated types, or say why not', () => {
    const api = apiSource
    const generated = new Set([...wireSource.matchAll(/^export type (\w+)/gm)].map((m) => m[1]))
    const imported = new Set<string>()
    for (const m of api.matchAll(/import type \{([^}]*)\} from '\.\/wire\.gen'/g)) {
      for (const part of m[1].split(',')) {
        const local = part.trim().split(/\s+as\s+/).pop()
        if (local) imported.add(local)
      }
    }
    const problems: string[] = []
    // Every type declaration, exported or not, with its body.
    const decls = [...api.matchAll(/^(?:export )?type (\w+)(?:<[^=]*>)?\s*=([\s\S]*?)(?=\n(?:export |\/\/|type |const |function |async )|(?![\s\S]))/gm)]
    const bodies = new Map(decls.map(([, name, body]) => [name, body]))
    const words = (body: string) => [...body.matchAll(/\b(\w+)\b/g)].map(([, w]) => w)
    // derived: the body names a generated type, or a local type that is derived.
    const derived = (name: string, seen = new Set<string>()): boolean => {
      if (seen.has(name)) return false
      seen.add(name)
      return words(bodies.get(name) ?? '').some((w) => imported.has(w) || (w !== name && bodies.has(w) && derived(w, seen)))
    }
    const exported = [...api.matchAll(/^export type (\w+)/gm)].map(([, n]) => n)
    expect(exported.length).toBeGreaterThan(20)
    for (const name of exported) {
      if (uiOnly.has(name)) continue
      if (!derived(name)) {
        problems.push(generated.has(name)
          ? `${name} redeclares a generated type by hand`
          : `${name} is a hand-written shape: use or derive the generated type, or list it with its reason`)
      }
    }
    expect(problems).toEqual([])
  })
})
