# Work evidence snapshots

`workflowQueries.snapshot` is the canonical detail/trace read. It returns a typed,
detached `workflowRunSnapshot` from one repeatable-read, read-only transaction:
run and pinned definition, steps, current activity when needed, events and artifacts.
The App no longer has a detail assembly method or duplicate event/artifact scanners.
Project, Change and Catalog enrichment remain composition concerns with their
existing authorization guards.

Detail preserves the newest 200 events in descending ID order and bounded artifact
previews. Trace preserves all events in chronological ID order and full artifact
text. Both preserve exact JSON numbers and return empty arrays for absent lists.
Ordinary detail activity reuses its loaded steps; running child calls use the same
owned-child graph within the same snapshot. A read never gives a Step the authority
to publish an attempt result. Request cancellation releases the read transaction.

Artifacts use one sqlc query and one checked record mapper for previews, trace text
and full retrieval. Projection is a named choice, not independent flags that can
accidentally include full binary bodies in a poll response. Preview text is at most
16 KiB and ends on a UTF-8 boundary. The SQL read bounds the fetched character prefix,
then the owner applies the byte bound. `bodyBytes` reports the original stored byte
count for both text and binary bodies; it is not a character count.

Binary artifacts retain their identity and size in previews without fetching their
body. Full artifact retrieval retains the existing `binary: true` and `bodyBase64`
wire form with exact stored bytes. An artifact containing both a nonempty text body
and a binary body violates the existing storage contract and is rejected; the reader
does not silently choose one encoding. This adds no migration or compatibility reader.

Missing records are distinct from unavailable storage. Failed queries, interrupted
row streams and malformed records return no partial snapshot. Related reads can no
longer combine old step state with new completion receipts or artifacts. The wider
Work domain remains in Forge while its command ownership is canonicalized; this is
not a completed package extraction. The MCP catalog still lacks a request context
on its call value, so that adapter's broader context-lifetime work remains separate.
