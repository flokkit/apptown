# roomie — meeting-room booking with actual rules

Our office fights over rooms. I want a tiny booking service that enforces
the rules so humans don't have to. JSON API on $PORT, SQLite in the data
dir, single tenant, no auth (names are just strings we trust).

## Stage 1 — rooms and bookings

    POST /api/rooms {name, capacity}            → 201
    GET  /api/rooms
    POST /api/bookings {room_id, who, start, end, attendees}
    GET  /api/bookings?room={id}&date=YYYY-MM-DD
    DELETE /api/bookings/{id}                   → cancel, 204

Times are RFC3339 UTC. The rules — this is the whole point:

- No overlaps in a room. Touching is fine: a booking ending 10:00 and one
  starting 10:00 do not conflict. Overlap → 409 {"error":"conflict",
  "conflicts_with": <booking id>}.
- Bookings must be 15 minutes to 4 hours long, start on a 5-minute
  boundary, and lie entirely within 07:00–20:00 UTC of a single day.
- attendees must fit capacity (attendees <= capacity) → else 422.
- No bookings in the past (start < now → 422). For testability, if the
  env var CLOCK_URL is set, GET it and use the returned
  {"now":"RFC3339"} as the current time; else use the real clock.

## Stage 2 — recurring bookings and the buffer rule

Weekly recurring: same slot every week.

    POST /api/recurring {room_id, who, weekday, start_time, end_time, attendees, weeks}
      → creates up to `weeks` instances starting from the next occurrence
        of weekday (relative to now); SKIPS instances that would conflict
        and reports them: 201 {created:[ids], skipped:[{date, conflicts_with}]}

Buffer rule (new, applies to everything from now on): rooms with
capacity >= 10 need a 10-minute gap between bookings. "Touching is fine"
no longer applies to those big rooms. Pseudocode, to be unambiguous:

    conflict(a, b) where a.room == b.room:
        gap = room.capacity >= 10 ? 10min : 0
        return a.start < b.end + gap && b.start < a.end + gap

Existing stage-1 tests for small rooms still hold.

## Stage 3 — utilization and waitlist

    GET /api/reports/utilization?date=YYYY-MM-DD
      → per room: {room_id, booked_minutes, utilization} where
        utilization = booked_minutes / (13*60), 3 decimal places

    POST /api/waitlist {room_id, who, start, end, attendees}
      → 201 always (validation as stage 1 minus the conflict check)

When a booking is cancelled, the FIRST waitlist entry (FIFO) that now
fits with all rules is promoted automatically into a real booking; the
GET /api/waitlist list shows remaining entries. A promoted entry's
booking appears in GET /api/bookings with who from the waitlist entry.
