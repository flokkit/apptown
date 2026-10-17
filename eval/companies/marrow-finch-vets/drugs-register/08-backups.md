---
from: Tomasz Wierzbicki, IT and Systems
size: medium
kind: ops
exercises: backups with a retention period and a proven restore, for a legal record
---

The register needs proper backups. We're required to keep it for at least two
years after the last entry on a page, and Ravi wants us to keep everything
for seven to be safe.

- A backup every night at 2am, kept for 35 days.
- The last backup of each month kept for 7 years.
- Stored somewhere other than the machine the app runs on.
- Once a month, restore the latest backup somewhere separate and check that the
  balances match the live register. Tell me and Ravi if they don't, or if the
  restore fails. I'd rather know it fails than assume it works.
