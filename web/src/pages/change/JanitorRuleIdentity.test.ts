import { expect, it } from 'vitest'
import { voteWords } from './through'
import type { ApprovalEvidence } from '../../api'

it('distinguishes approvals issued by the same person under different rules', () => {
  const vote = (ruleId: string) => ({
    approver: 'the janitor', via: 'standing',
    standing: { rule: { kind: ruleId === 'dependency-rule' ? 'Dependency updates' : 'Keep packs current', scope: 'all projects' }, ruleId, revision: 1, issuer: { name: 'Sam', principal: 'sam', via: 'session' } },
  }) as NonNullable<ApprovalEvidence['decisions']>[number]
  expect(voteWords(vote('dependency-rule'))).not.toEqual(voteWords(vote('pack-rule')))
})
