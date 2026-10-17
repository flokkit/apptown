---
from: Tomasz Wierzbicki, IT and Systems
size: medium
kind: ops
exercises: a scheduled retention job with an exception and a record of what it did
---

Referrals that were closed more than 7 years ago should be removed: the
practice records policy says 7 years for this. Please set up something that
runs once a month and deletes them, including their notes and chase history.

Two exceptions: anything linked to a re-referral that's still open stays until
that one is also past 7 years, and anything Ravi or Adaeze has marked "keep"
(complaints, legal) stays until they unmark it.

Each run should leave a record of how many it removed and when, so I can
show the auditors it's happening. Not the details of the referrals, just
the count and the date range.
