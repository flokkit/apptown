import { expect, it } from 'vitest'
import ts from 'typescript'

// Parse text nodes and prose literals, leaving machine names, schema values,
// CSS classes and step types alone. People and token attribution name accounts.
it('calls the AI helper and uses plain release and data words', () => {
  const sources = import.meta.glob('./**/*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
  const violations: string[] = []
  for (const [file, text] of Object.entries(sources)) {
    if (/\.(test|fixture)\.|wire.gen|vocabulary.test|\/People.tsx|\/AccountAccess.tsx|\/through.ts/.test(file)) continue
    const source = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, file.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
    function visit(n: ts.Node) {
      if (ts.isJsxText(n) || ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n)) {
        const words = n.text
        const technical = ts.isLiteralTypeNode(n.parent) || /agent\/|agent-|\/agent|agent_|agents-and-tokens|\buses:|through agent token|through an agent token/.test(words)
        const machineLabel = words === 'Agents and tokens'
        const machineHelp = file === './PageHead.tsx' && /Agents and tokens|connect.*agent/.test(words)
        const prose = ts.isJsxText(n) || /\s/.test(words) || /^(Agent|Agents)$/i.test(words)
        if (prose && !technical && !machineHelp && !machineLabel && /\bagents?\b|\brelease grant\b|\bsynthetic\b/i.test(words)) {
          violations.push(`${file}:${source.getLineAndCharacterOfPosition(n.getStart(source)).line + 1}: ${words.trim()}`)
        }
      }
      ts.forEachChild(n, visit)
    }
    visit(source)
  }
  expect(violations).toEqual([])
})
