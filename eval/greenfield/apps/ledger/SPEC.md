# ledger — a small personal expense tracker

I want a little self-hosted expense tracker. Single user, no auth. It
should feel instant and never lose my data.

## Stage 1 — the core

Keep track of **accounts** (like "checking", "visa") and **transactions**
against them. A transaction is money in or out: amount, a payee, an
optional note, a date (default today), and a category from a fixed
starter set (groceries, rent, transport, fun, income, other).

Rules that matter to me:
- Amounts are dollars and cents. Store them exactly — no float drift.
  Positive = money in, negative = money out.
- An account has a running balance = sum of its transactions.
- Deleting an account with transactions should be refused (I'd rather be
  told than lose history).
- Payee is required and non-blank; amount can't be zero.

Serve a JSON API (this surface is fixed, since other tools will hit it):

    GET    /api/accounts                  → [{id, name, balance}]
    POST   /api/accounts    {name}        → 201 {id, name, balance:0}
    DELETE /api/accounts/{id}             → 204, or 409 if it has transactions
    GET    /api/transactions?account={id}&category={c}   (both filters optional)
    POST   /api/transactions {account_id, amount_cents, payee, category?, note?, date?}
    DELETE /api/transactions/{id}         → 204
    GET    /api/summary?month=YYYY-MM     → {income_cents, expense_cents, by_category:{...}}

In the summary, income_cents and expense_cents are both MAGNITUDES
(positive numbers): income_cents totals the money in, expense_cents
totals the money out. by_category keeps signed sums.

Errors as {"error": "..."} with sensible statuses (400 bad input, 404
missing, 409 conflict). Duplicate account names: 409. Unknown category:
400. Transactions listed newest-date first; ties by newest-created first.

Also give me a minimal HTML page at / that lists accounts and lets me add
a transaction — nothing fancy, it just has to work in a browser.

SQLite on disk (the app contract's data dir). Listen on $PORT.

## Stage 2 — budgets

Let me set a monthly budget per category:

    PUT /api/budgets/{category}  {month:"YYYY-MM", limit_cents}
    GET /api/budgets?month=YYYY-MM
      → [{category, limit_cents, spent_cents, remaining_cents}]

spent = |sum of negative amounts| in that category+month. remaining can
go negative; that's the point of a budget report. Unknown category 400.
Setting a budget again for the same category+month overwrites.

## Stage 3 — recurring transactions and CSV

Recurring: "rent, -180000 cents, monthly on the 1st".

    POST /api/recurring {account_id, amount_cents, payee, category, day_of_month}
    GET  /api/recurring
    POST /api/recurring/apply {month:"YYYY-MM"}
      → creates that month's instances (idempotent: applying twice
        creates nothing new), response {created: N}

Pseudocode for the idempotency, since it's the part people get wrong:

    for r in recurring:
        date = clamp(r.day_of_month to last day of month)
        if no transaction exists with (source_recurring=r.id, that month):
            create transaction(date=date, ..., source_recurring=r.id)

And CSV export: GET /api/export?account={id} returns text/csv with header
date,payee,category,amount_cents,note — RFC-quote fields containing
commas or quotes.
