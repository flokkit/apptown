---
from: Graham Achterberg, Finance controller
size: medium
kind: feature
exercises: adapting to a changed input format while still reading the old one for past months
---

The card provider changed their export. From the October statement:

- Date and time are now one column, "Txn Timestamp", in day/month/year
  24-hour format. It used to be two columns.
- "Litres" is now "Quantity" and there's a "Unit" column which is always "L"
  so far. If it's ever not L, I want the row rejected with a message rather
  than guessed.
- Card numbers now come with spaces every four digits.
- There's a new "Merchant ID" column we don't need.

We'll still need to re-run September and earlier from the old files sometimes,
so the old format has to keep working.
