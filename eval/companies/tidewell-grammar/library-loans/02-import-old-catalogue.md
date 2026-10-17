---
from: Teresa Nguyen, Librarian
size: medium
kind: data
exercises: an import from a spreadsheet described in words, with messy data and decisions about what to do with rows that don't fit
---

I've got the export from the old system. It's a spreadsheet with about 21,000
rows, one per copy (so three copies of the same book are three rows). Columns are
Title, Author, ISBN, Barcode, Location, Dewey, Status, Notes. Things you should
know before you trust it:

- Authors are mostly "Surname, First name" but some are "First Surname" and some
  have the illustrator in the same cell ("Fox, Mem / Vivas, Julie").
- About a third of the donated books have no ISBN. Some ISBNs are the 10-digit
  kind, some 13. A few have dashes.
- Barcodes are 8 digits starting with 000 or 001, but somebody opened the file in
  Excel at some point and a lot of them lost their leading zeros. There are no
  real barcodes shorter than 8 digits, so a short one means zeros got eaten.
- Location is "JNR" or "SNR", except about 300 rows that say "Ridgeview" or
  "Harbour Rd", which mean the same thing.
- Status uses "IN", "OUT", "LOST", "WD" (withdrawn). Withdrawn books shouldn't come
  across at all. "OUT" should come across as available, because we're doing a
  stocktake and loans will be re-entered.
- There are some duplicate barcodes. I don't know why. Don't pick one; give me a
  list and I'll sort them out with the books in front of me.

Anything that can't be read cleanly, please put in a list for me rather than
guessing. I'd rather fix 400 rows by hand than find out in March that half the
picture books are filed under the wrong campus.
