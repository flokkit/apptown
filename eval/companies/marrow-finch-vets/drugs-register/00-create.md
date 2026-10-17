---
from: Dr Ravi Subramaniam, Pharmacy and Compliance Lead
size: long
kind: create
exercises: creating an append-only regulated record with balances, corrections as new entries, and a two-person witness
---

We keep the controlled drugs register in bound paper books, one per drug per
clinic, and I want to move it into a web app. This is a legal record. The
regulator's inspectors can ask to see it at any time, and if it's wrong it's
my registration on the line, so please read all of this.

## What's in the register

The drugs in the register are the ones in the strictest schedules (I'll call
them Schedule 2 and Schedule 3 here; Schedule 2 is the strong opioids and the
like, kept in the locked safe). Each clinic has one controlled drugs safe;
Carrow Bridge has two.

For each clinic, safe and drug (name, strength and form, e.g. "methadone
10 mg/ml injection"), the register is a list of entries. Every entry is one of:

- **Received**: from the supplier or another clinic. Supplier or clinic,
  invoice or transfer number, quantity.
- **Supplied/administered**: used on a patient. Patient number, pet name,
  owner surname, vet who authorised it, quantity.
- **Transferred out**: to another of our clinics. That clinic must record the
  matching "received".
- **Discarded**: expired, damaged, or the unused part of an opened vial.
  Needs a witness (below).
- **Correction**: see below.

Every entry records who made it, when it was made (the computer's time, not
typed in), the date and time the movement actually happened (typed in, because
people enter things after surgery), the quantity in the drug's unit (ml,
tablets, patches), and the **running balance** after the entry.

## The rules that matter

1. **Nothing is ever edited or deleted.** Not by staff, not by me, not by IT.
   If an entry is wrong, the fix is a new **correction** entry that names the
   entry it corrects, says what was wrong, and gives the right figure. Both
   stay visible forever. On paper we put a single line through and initial it;
   this is the same idea.
2. **The balance can't go below zero.** If an entry would make it negative,
   refuse it and say why. That nearly always means a delivery wasn't entered.
3. **Discards need two people.** The person discarding and a witness, who must
   be a different person, a vet or a registered nurse, and present. The
   witness confirms with their own login (or a PIN, whatever is quickest at the
   bench) at the time. A discard without a confirmed witness is not finished and
   must show as "awaiting witness", not count as done.
4. **Entries are in the order they happened at the bench**, but the balance is
   worked out in the order they were entered. If someone enters something late,
   that's fine, but show that it was entered late (more than 24 hours after the
   movement).
5. Only vets and registered nurses can make entries. Reception can look, not touch.

## Worked examples

- Wrenfield, methadone 10 mg/ml: balance 20.0 ml. Nurse records 0.6 ml
  administered to patient 48113 "Biscuit". Balance 19.4 ml.
- The vet realises it was 0.8 ml. She adds a correction to that entry:
  "recorded 0.6, actual 0.8". Balance 19.2 ml. The original 0.6 entry is still
  there, marked as corrected, and links to its correction.
- Opened vial with 1.2 ml left past its use-by. Nurse records a discard of
  1.2 ml. Balance stays 19.2 until the vet witnesses; then 18.0.
- Someone tries to record 25 ml used when the balance is 18.0. Refused.

## What I need to see

- For any clinic, safe and drug: the register page, newest at the bottom, like
  the book.
- For the group: anything awaiting a witness for more than an hour, any entry
  entered late, any correction in the last 30 days.

## Testing

I need to see a test that shows entries can't be changed or deleted, through
the screens or any other way in. That's not optional; it's what I'd show an
inspector if they asked how we know.
