---
from: Arjun Ramaswamy, Treasury and Cash Officer
size: medium
kind: create
exercises: creating an app that moves money, under the two-approval house rule; approvals inside the app are themselves records
---

Hi all,

Branches order cash from the vault at head office by emailing me a filled-in
Word form. I then ring someone at the branch to confirm it's real, book it on
the armoured car run, and file the email. It works, until I'm away.

I'd like a small internal app for this:

- A branch staff member creates an order: branch, delivery date, and how
  much of each denomination (notes $100, $50, $20, $10, $5; coins in
  standard bags).
- A second person at the same branch approves it. The person who created it
  can't approve it.
- Treasury (me, or my backup Leilani for now) then confirms it, which is
  when it's booked for delivery.
- When the cash arrives, the branch records that it was received and
  whether the amount matched. If it didn't, they say what was different.
- Branches can also return surplus cash to the vault the same way, just in
  the other direction.

I need a list of orders for the coming week by delivery date, and each order
needs its full history: who created it, who approved, who confirmed, who
received, when.

The detailed rules (limits, cut-off times) are coming in a separate note.
