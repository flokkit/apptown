---
from: Dr Ravi Subramaniam, Pharmacy and Compliance Lead
size: long
kind: feature
exercises: a long feature combining physical counts, register balances, discrepancy handling and sign-off, all without editing past entries
---

Every clinic has to do a physical count of every drug in the register once a
month, and the inspector will want to see that we did. On paper we write the
count at the bottom of the page and both people sign. I want this in the app.

## The count

At any time, a clinic can start a stock check. For each drug in each safe, the
app shows the drug but **not** the register balance (people count what they
expect to see otherwise). Two people count together: one counts, the other
watches; both must be vets or registered nurses, and they must be different
people. They enter the quantity found. For liquids, they estimate to the
nearest 0.1 ml by the vial markings and the app should say that's expected.

When they finish, the app compares the count with the register balance at the
moment the check was started.

## Results

- **Matches**: recorded as a stock check entry in the register for that drug,
  showing the count and both names. It doesn't change the balance.
- **Liquids within 0.5 ml or 5% (whichever is smaller)**: this is normal loss
  from drawing up. Recorded as a matched check with the difference noted. It
  does not change the balance on its own; the clinic's lead vet adds a
  correction for the difference with reason "measuring loss" if they agree.
- **Anything else is a discrepancy.** That includes any difference at all in
  tablets, patches or whole unopened vials. A discrepancy:
  - is recorded as a stock check entry showing both figures;
  - does **not** change the balance;
  - goes on my list straight away, and I get an email;
  - stays open until I or the clinic's lead vet records what we found (for
    example, "delivery of 10 not entered on 4 Nov, entry added") and closes it.

The balance only changes through ordinary entries and corrections, never
through the count itself. I want the book to show how we got there.

## Worked examples

- Fernlea, buprenorphine 0.3 mg/ml, register says 8.4 ml, counted 8.2 ml.
  Difference 0.2 ml, under 0.5 ml and under 5% (0.42 ml). Matched with
  difference noted.
- Carrow Bridge safe 2, methadone 10 mg/ml, register says 4.0 ml, counted
  3.6 ml. 5% of 4.0 is 0.2 ml, which is smaller than 0.5 ml, so the limit is
  0.2 ml. 0.4 ml is over: discrepancy.
- Lindenford, a tablet drug, register 56, counted 55. Discrepancy, even though
  it's only one.
- Millbeck, a drug the register says is at 0 and the count finds 0: matched.
  We still count drugs at zero.

## Edge cases

- Entries made during a count (it's a working clinic, someone may need a drug
  mid-count): the comparison uses the balance at the start of the check, but
  show any entries made during the count next to the result so whoever reviews
  it can see why.
- A check started but not finished by the end of the day: it stays open, and
  shows as unfinished on my list. It doesn't count as the month's check.
- The month's check is due by the last day of each calendar month. On the 25th,
  clinics that haven't done one get a reminder on their front page; after the
  month ends, it's overdue on my list.
