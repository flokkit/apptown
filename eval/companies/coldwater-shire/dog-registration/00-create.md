---
from: Tash Whitlock, Senior Ranger
size: medium
kind: create
exercises: creating a records app with yearly renewals, unique identifiers and a scheduled job
---

The dog register lives in an old system that the vendor stopped supporting
in 2023, plus a spreadsheet I keep because the old system can't do half of
what we need. Time for something we own.

What it needs to do:

- Owners: name, address, phone, email, postal address if different.
- Dogs: name, breed, colour, sex, date of birth (often a guess), desexed
  yes/no, microchip number, and the council tag number. One owner can have
  several dogs.
- Registration runs 1 July to 30 June. Each year a dog is either
  registered (paid) for that year or not.
- Tag numbers are issued by us. Format is the year then a running number,
  like 26-0412. A tag number is never reused, even after a dog dies or
  leaves the shire.
- Rangers and customer service can search by owner name, address, dog
  name, microchip or tag. When I pick up a dog at 6 am I need to find the
  owner from the tag on my phone in the ute.
- In early July, send every owner whose dog was registered last year an
  email saying renewal is due by 31 August. Then an overdue reminder for
  anyone not paid by 31 August.
- A list of overdue dogs by town so we can follow up.

Payments are taken at the counter or over the phone through the existing
payment system. For now staff just record "paid", the amount and the
receipt number. No online payment yet.
