---
from: Hyun-woo Park, IT Lead
size: medium
kind: ops
exercises: scheduled backups with a stated retention, a restore test, and a second deployment that must never hold real data
---

Two operational things for Cash Orders now it's the only record of who
approved what:

1. Backups. Nightly at 1am, kept for 35 days, plus the month-end backup kept
   for 7 years (house rule 6). I want a restore test once a quarter, with the
   result written down somewhere I can show the auditors. If a backup fails,
   email me and Arjun the next morning, not just a log line.
2. A training copy. New tellers start in January and Leilani wants them to
   practise ordering and approving without frightening Arjun. Please set up a
   separate deployment of the same app, with a big "TRAINING" banner, invented
   branches and staff, and no connection to the real data or the real email.
   It should be reset to the same starting data every Sunday night.

Nothing from the real system should ever be copied into the training copy,
even "just the branch list". Type the made-up one in.
