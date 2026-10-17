---
from: Marcus Teo, IT Lead
size: medium
kind: ops
exercises: setting up and changing a scheduled job with explicit dates, a quiet period and a record of what was sent
---

For the renewal and overdue emails, here's how I want them to run so we
don't get surprised:

- Renewal notices: once, on the first Tuesday of July at 9 am.
- Overdue reminders: on the 1st and 15th of each month from September to
  December, 9 am, only for dogs still unpaid.
- Nothing goes out between 20 December and 10 January (Christmas shutdown,
  nobody at the counter to answer calls).
- Nothing goes out on a weekend. If a date falls on one, send the next
  Monday.
- Each run should keep a record of who was sent what and whether the mail
  server accepted it, and Tash should be able to see that list.
- If a run fails part-way, it must not send twice to the people who
  already got it when it's retried.

Also, the email has to come from the council's own sending service, not
some new one. The settings for that are already in the password vault
under "Mail relay – apps".
