---
from: Ravi Menon, IT and platform lead
size: medium
kind: ops
exercises: a new separate deployment with made-up data and no real driver records
---

Lucia's running driver inductions from next month and wants new drivers to
practise logging hours before they're on the real system. Can we have a
separate training copy of Driver Hours Log?

- Its own address (training-hours or similar), clearly marked TRAINING on every
  page so nobody logs real hours in it.
- Made-up drivers and made-up history only. No real driver records in it,
  not even names.
- Reset back to the starting data every night.
- The nightly check runs there too, but no emails go to Tomasz from it.

It gets the same updates as the real one, but a bad release to training mustn't
affect the real one.
