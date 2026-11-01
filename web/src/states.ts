// closedNothingToChange: the change's recorded closedAs says AppTown
// closed it because there was nothing to change. Read only from that fact,
// never from the reason's words, which a person can also type.
export function closedNothingToChange(closedAs: string | undefined): boolean {
  return closedAs === 'nothing-to-change'
}
