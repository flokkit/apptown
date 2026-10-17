---
from: Graham Achterberg, Finance controller
size: medium
kind: ops
exercises: moving from manual upload to a scheduled job, with a clear failure signal
---

Ravi has set it up so the statement and the TMS export land in the shared
finance folder automatically on the 1st. So we can drop the manual upload:

- Run at 06:00 on the second business day of each month, for the previous
  month. Business day = Monday to Friday, not a public holiday.
- If either file isn't there, don't run on half the data. Email me and my team
  that it didn't run and why.
- Keep the upload button for re-runs.
