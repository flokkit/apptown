// One chip, one tooltip, everywhere something can be forge-shipped or
// yours (design/subsystems/ui-coherence.md — five copies became this).
export function OriginChip({ origin, modified }: { origin?: string; modified?: boolean }) {
  const stock = origin === 'stock' || (origin ?? '').startsWith('stock/')
  if (stock) {
    return (
      <span className="chip" title={modified
        ? 'Came with the forge; edited here. The edit stays until you take the next update.'
        : 'Came with the forge; read-only here. Updates arrive with each release.'}>
        forge{modified ? '*' : ''}
      </span>
    )
  }
  return <span className="chip pass" title="Yours — written here, editable">yours</span>
}
