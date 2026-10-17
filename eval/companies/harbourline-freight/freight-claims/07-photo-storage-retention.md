---
from: Ravi Menon, IT and platform lead
size: medium
kind: ops
exercises: storage retention, backup of object storage, and keeping deletion impossible within the retention period
---

Two things on the claims photos, now we're at about 30 GB:

1. Back them up somewhere separate from where they live, nightly. If the
   storage provider loses them we lose the evidence for every open claim.
2. Finance policy is 7 years from when the claim is settled. After that they
   should be deleted, along with the backups of them. Before that nobody should
   be able to delete them, including me — Graham wants that in writing.

Start the 7-year clock at settlement, not lodgement. Open claims are kept
regardless.
