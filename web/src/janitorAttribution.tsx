import { Link } from 'react-router-dom'
import type { StandingVote } from './wire.gen'

type Rule = NonNullable<StandingVote['rule']>
export const janitorWords = (rule: Rule, revision: number, issuer: string) =>
  `the janitor, under the ${rule.kind} approval for ${rule.scope} (revision ${revision}), granted by ${issuer}`

export function JanitorAttribution({ rule, revision, issuer }: { rule: Rule; revision: number; issuer: string }) {
  return <Link to="/janitor">{janitorWords(rule, revision, issuer)}</Link>
}
