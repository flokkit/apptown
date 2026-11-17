import { Link } from 'react-router-dom'
import type { StandingVote } from './wire.gen'

type Rule = NonNullable<StandingVote['rule']>
export const janitorWords = (rule: Rule, revision: number, issuer: string) =>
  `the janitor, under the janitor choices (rev ${revision}), set by ${issuer} · ${rule.kind}`

export function JanitorAttribution({ rule, revision, issuer }: { rule: Rule; revision: number; issuer: string }) {
  return <Link to="/maintenance">{janitorWords(rule, revision, issuer)}</Link>
}
