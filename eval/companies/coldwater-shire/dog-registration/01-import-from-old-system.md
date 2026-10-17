---
from: Tash Whitlock, Senior Ranger
size: long
kind: data
exercises: a messy one-off import where bad rows must be reported, not guessed, and imported records must be traceable to their source
---

We need the old register brought across before renewals go out in July.
Marcus got an export out of the old system: it's a spreadsheet with about
4,100 rows, one row per dog. My own spreadsheet of extra notes is a second
file with about 600 rows. Both are in the shared drive under Rangers >
Register migration.

Columns in the export: OwnerName, OwnerAddr1, OwnerAddr2, Town, Phone,
Email, DogName, Breed, Colour, Sex, DOB, Desexed, Chip, TagNo,
LastPaidYear, Status.

Things you need to know about this data, because it's a mess:

- **Owners are repeated on every row.** Same person, three dogs, three
  rows. Sometimes the name is spelled differently across their rows
  ("Margaret O'Dowd", "Margaret ODowd", "M O'Dowd"). Match owners on
  address *and* surname. Don't merge two people just because the names
  look alike; there are two unrelated Ian Fletchers in Harrow Flat.
- **Tag numbers** were typed in by hand for years. Some are "26-412",
  some "26/0412", some "412" with no year. If the year's missing, use
  LastPaidYear. If two dogs end up with the same tag, don't pick one.
  Put both on a list for me.
- **Dates of birth** are in at least three formats, and about 300 are
  "1/1/" something, which means somebody guessed the year. Keep them, but
  mark them as approximate.
- **Desexed** is Y, N, Yes, No, blank, or "?" Blank and "?" mean we
  don't know. Unknown isn't the same as No: desexed dogs pay less, and I
  don't want anyone billed the full fee because of a blank.
- **Status** has values like Active, Deceased, Moved, Surrendered, and
  some free text ("gone to daughter in Bendara"). Deceased and Moved dogs
  come across but must not get renewal reminders. Free text goes to me to
  sort.
- **Phone** numbers have spaces, brackets, the odd "ask for Bev". Store
  the number; put the leftover words in a note.
- About 150 rows have no email and no phone. Bring them across; they'll
  get a letter.
- My notes spreadsheet links by TagNo and has things like "dog
  aggressive to rangers – approach with care" and "owner deaf, text don't
  call". Those notes are important to us in the field. They are staff
  only and must never go in any letter or email to the owner.

What I want out of it:

1. A trial run first that changes nothing and gives me a summary: how
   many owners, how many dogs, how many problems of each kind, and a list
   of every row it couldn't bring across cleanly with the reason.
2. Then the real import once I've fixed what I can in the source files.
3. Every imported dog keeps a note of which row of which file it came
   from, so when someone rings up saying "your records are wrong" we can
   see what the old system said.
4. Tag numbers keep their original numbers. New tags issued after the
   import carry on from the highest number for that year.

Examples:

- Row: "Fletcher, Ian | 14 Quarry Rd | | Harrow Flat | ... | TagNo 412 |
  LastPaidYear 2025". Tag becomes 25-0412.
- Rows for "Margaret O'Dowd, 3 Pine St, Coldwater" and "M ODowd, 3 Pine
  St, Coldwater": one owner, two dogs.
- Two rows both tag 24-0077, different dogs, different owners: both go on
  my list, neither gets a tag until I decide.
