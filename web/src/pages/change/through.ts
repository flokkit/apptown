import type { ApprovalEvidence } from '../../api'
import { janitorWords } from '../../janitorAttribution'

// How an act came to AppTown, in words, from the way of acting a vote
// records (access.Actor.Via: "token-cli:laptop", "session", …). Acting
// directly says nothing more; a token is named, since a person's token may
// be held by a coding agent and the name is the record's only trace of it.
export function throughWords(via?: string): string {
  if (!via) return ''
  const i = via.indexOf(':')
  const way = i < 0 ? via : via.slice(0, i)
  const subject = i < 0 ? '' : via.slice(i + 1)
  switch (way) {
    case 'session': case 'header': case 'open': return ''
    case 'token-cli': return subject ? `through CLI token ${subject}` : 'through a CLI token'
    case 'token-agent': return subject ? `through agent token ${subject}` : 'through an agent token'
    default: return `through ${via}`
  }
}

// who is a voter's name with the way they voted, when it was not direct.
export const voterWords = (approver: string, via?: string) => {
  const t = throughWords(via)
  return t ? `${approver} ${t}` : approver
}

// closingVia is the way the step's deciding vote was cast: the last vote
// by the approver the step records.
export const closingVote = <V extends { approver: string }>(ev: { approver?: string; decisions?: V[] }) =>
  [...(ev.decisions ?? [])].reverse().find(d => d.approver === ev.approver)

export const closingVia = (ev: { approver?: string; decisions?: { approver: string; via?: string }[] }) => closingVote(ev)?.via

// A typed automatic vote names the grant it actually used; the Change heading
// separately names the firing's original grant.
export const voteWords = (vote: Pick<NonNullable<ApprovalEvidence['decisions']>[number], 'approver' | 'via' | 'standing'>) => vote.standing
  ? janitorWords(vote.standing.rule!, vote.standing.revision, vote.standing.issuer.name)
  : voterWords(vote.approver, vote.via)
