---
from: Fiona MacLeod, Risk and Compliance Officer
size: medium
kind: rule
exercises: a way-of-working change that must apply to every app's changes, prompted by a privacy incident; checks rather than promises
---

Hyun-woo forwarded me an error email from the Complaints Register last week.
It contained the full text of a complaint, including the member's name,
address and the name of her ex-partner. That email went to the IT shared
mailbox, which eleven people can read. I've logged it as a privacy incident.

House rule 2 already says member details never go in logs or error
reports. It clearly isn't being checked. From now on, for every app, not
just this one:

- No change goes live unless something has actually checked that the app
  doesn't write member details to logs or error reports. Someone saying "I
  was careful" doesn't count.
- Test data must be invented. No copies of real records, even with names
  changed, because the free-text fields are where the details are.
- Error reports may include the record's reference number and nothing from
  its free-text fields.

Please fix the Complaints Register first and tell me what it would take to
check the other apps the same way. I'd like the IT shared mailbox cleaned of
the old error emails too; Hyun-woo will do that part.
