---
from: Graham Achterberg, Finance controller
size: long
kind: create
exercises: creating a batch job and report page from a long brief with matching rules, tolerances and worked examples
---

Fuel card reconciliation

We spend about $1.1 million a month on diesel across roughly 230 fuel cards.
Every month one of my team spends four days matching the fuel card statement
against the trips in the TMS to find fills that don't make sense. I want a job
that does the matching and a page that shows only the ones a person needs to
look at.

**Inputs**

- The fuel card statement: a CSV we download from the card provider's portal at
  the start of each month. One row per transaction: card number, transaction
  date and time, site name, product (diesel, AdBlue, unleaded, other), litres,
  price per litre, amount, and the odometer the driver keyed in (often wrong).
- The trips export from the TMS: one row per trip: trip number, truck rego,
  driver ID, depart time, arrive time, start depot, end point, planned km.
- The card list: which card is assigned to which truck rego. Some cards are
  "floating" (kept in the depot office, used for whatever truck needs it).
- Tank sizes per truck: most prime movers have 600 litres, B-double units
  1,000 litres, rigids 250 litres.

**Matching**

A fuel transaction matches a trip when:

1. the card is assigned to the truck on that trip (for a floating card, any
   truck that left that card's depot that day), and
2. the transaction time is within the trip, or up to 2 hours before departure
   or 2 hours after arrival (drivers fill up before they leave or on the way
   back to the yard).

**What counts as a mismatch** (each transaction gets at most one, in this
order):

1. **No trip**: no trip matches at all.
2. **Over tank**: litres more than the truck's tank size + 5%.
3. **Wrong product**: unleaded or "other" on a diesel truck. AdBlue is fine.
4. **Too soon**: a second fill on the same truck within 3 hours of the first,
   with the two together more than the tank size.
5. **Weekend no trip**: a fill on Saturday or Sunday on a truck with no trip
   that day (a special case of 1, but shown separately; it's the one we worry
   about most).

Everything else is matched and doesn't need looking at.

**Examples**

- Card 7781 (truck HF-204, 600 L). Trip 55102 departs Port Kellan 05:40,
  arrives Merrin Junction 14:10. Fill at 05:05, 410 L diesel → matched (within
  2 hours before departure).
- Same truck, same day, fill at 17:30 → no trip (more than 2 hours after
  arrival) → mismatch "no trip".
- Card 7790 (truck HF-311, 600 L) fill of 660 L → over tank (630 is the
  limit) → mismatch.
- Floating card 9001 (Tallow Creek) fill on a Tuesday at 11:00; trucks HF-118
  and HF-140 left Tallow Creek that day → matched, but show which trips it
  could belong to.
- Card 7781, 80 L unleaded → wrong product.
- Card 7781 on Sunday, no trips → weekend no trip.

**Output**

- A run per month. Re-running the same month with the same files gives the
  same answer; re-running with a corrected file replaces the old answer but the
  page shows it was re-run, when and by whom.
- A report page: totals at the top (transactions, matched, mismatches, dollars
  in mismatches), then the mismatches, filterable by depot, type and card.
- Each mismatch can be marked **explained** (with a note, e.g. "hire truck
  used card, approved by Hemi") or **referred** (sent to the depot manager).
  The note and who did it are kept.

The job runs when someone uploads the month's two files. No schedule for now.
Only my team and depot managers see the page; depot managers see their own
depot's cards.
