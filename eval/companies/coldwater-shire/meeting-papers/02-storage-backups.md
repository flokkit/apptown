---
from: Marcus Teo, IT Lead
size: medium
kind: ops
exercises: backups for object storage and database together, with a restore test and a retention that respects disposal
---

The meeting papers are now the only copy of some records, so I want
backups sorted before the audit:

- Nightly backup of the database and the stored files, kept for 35 days,
  plus one from the first of each month kept for 7 years.
- The backup goes somewhere other than where the app runs. We have the
  storage account with the other provider for this already, details in
  the vault under "Offsite backup".
- A restore test every quarter into a separate copy, and a note of
  whether it worked. I want to be able to show the auditor the last one.
- When Rosalind disposes of a record, it will still be in old backups
  until they age out. That's acceptable and the records team agrees, but
  write it down in the app's documentation so nobody's surprised.

Monthly cost needs to stay under $40. Tell me if it can't.
