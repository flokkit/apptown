---
from: Fiona MacLeod, Risk and Compliance Officer
size: medium
kind: feature
exercises: a fraud-driven control change; edits after approval invalidate the approval, under the risk-approval house rule
---

Last week someone phoned Millbrook claiming to be from treasury and asked
the teller to "just bump up" an already-approved order by $40,000 because
of a public holiday rush. The teller edited the order in the app, it kept
its approval, and it was only caught because Arjun thought the total looked
odd when he confirmed it.

Please change it so that:

- Any edit to an order after the branch approval removes that approval and
  the order goes back to waiting for a second person at the branch.
- Any edit after treasury confirmation isn't allowed at all. Cancel and
  create a new order instead.
- The order history shows the before and after amounts for every edit.
- Treasury never asks branches to change orders by phone, so the app
  should show this line on the edit screen: "Treasury will never ask you
  to change an order by phone or email. If someone does, call the Risk
  team on extension 4410."

This is a control change on an app that moves money, so I'll be one of the
approvers.
