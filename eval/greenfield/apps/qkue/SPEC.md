# qkue — a boring little job queue

I need a queue that a few workers can pull from over HTTP, with leases so
a crashed worker's job comes back. Single node, SQLite, $PORT. No auth.

Time is the tricky part, so same trick as roomie: if CLOCK_URL is set,
GET it for {"now":"RFC3339"} whenever you need the current time. All
lease math must go through that clock.

## Stage 1 — push, pull, ack

    POST /api/queues/{q}/jobs {payload: <any json>}   → 201 {id}
    POST /api/queues/{q}/pull {lease_seconds}         → 200 {id, payload, attempt}
                                                        or 204 if empty
    POST /api/jobs/{id}/ack                           → 204
    GET  /api/queues/{q}/stats  → {ready, leased, done, dead}

Semantics:
- pull leases the OLDEST ready job (FIFO by creation). While leased it is
  invisible to other pulls.
- ack while leased marks done. ack after the lease expired → 409 (someone
  else may have it now; late acks must not double-complete).
- attempt is the DELIVERY COUNT: the first pull of a job returns
  attempt = 1; a pull after its lease expired returns attempt = 2; and
  so on. (attempt counts pulls, not expiries.)
- lease expiry returns the job to ready; it keeps its delivery count.
- lease_seconds: 1..600, default 30.

## Stage 2 — retries, backoff, dead letters

- A job that returns to ready via expiry (or via explicit nack:
  POST /api/jobs/{id}/nack {delay_seconds?}) is retried.
- After 5 total attempts, it goes to DEAD, not ready.
- Backoff on expiry-return: the job becomes pullable only after
  min(2^(attempt-1), 60) seconds from when the lease expired, where
  attempt is the delivery number of the lease that just expired. Explicit
  nack uses delay_seconds if given, else the same formula.

    GET  /api/queues/{q}/dead            → list dead jobs {id, payload, attempts}
    POST /api/jobs/{id}/revive           → dead job back to ready, attempts reset to 0

## Stage 3 — priorities and delayed jobs

- push takes optional {priority: 1..9} (default 5). Lower number pulls
  first; within a priority, still FIFO.
- push takes optional {not_before: RFC3339} — not pullable until then.
- New: POST /api/queues/{q}/purge {state: "dead"|"done"} → {purged: N}.

Ordering rule, exactly:

    pullable = ready jobs with not_before <= now and backoff elapsed
    next = min by (priority, created_at, id)
