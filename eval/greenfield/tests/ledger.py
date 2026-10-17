import sys
sys.path.insert(0, sys.path[0] + "/..")
from kit import check, finish, req

STAGE = int(sys.argv[1]) if len(sys.argv) > 1 else 1


def stage1():
    # accounts CRUD
    s, b, sym = req("POST", "/api/accounts", {"name": "checking"})
    check("create account", s == 201 and b and b.get("name") == "checking" and b.get("balance") == 0, sym)
    acct = b["id"] if s == 201 else None
    s, b, sym = req("POST", "/api/accounts", {"name": "checking"})
    check("duplicate account name is 409", s == 409, sym)
    s, b, sym = req("POST", "/api/accounts", {"name": ""})
    check("blank account name is 400", s == 400, sym)

    # transactions
    s, b, sym = req("POST", "/api/transactions",
                    {"account_id": acct, "amount_cents": -1250, "payee": "cafe",
                     "category": "fun", "date": "2030-01-05"})
    check("create expense", s == 201, sym)
    s, b, sym = req("POST", "/api/transactions",
                    {"account_id": acct, "amount_cents": 500000, "payee": "job",
                     "category": "income", "date": "2030-01-03"})
    check("create income", s == 201, sym)
    s, b, sym = req("POST", "/api/transactions",
                    {"account_id": acct, "amount_cents": 0, "payee": "x"})
    check("zero amount refused 400", s == 400, sym)
    s, b, sym = req("POST", "/api/transactions",
                    {"account_id": acct, "amount_cents": 5, "payee": "  "})
    check("blank payee refused 400", s == 400, sym)
    s, b, sym = req("POST", "/api/transactions",
                    {"account_id": acct, "amount_cents": 5, "payee": "x",
                     "category": "yachts"})
    check("unknown category 400", s == 400, sym)
    s, b, sym = req("POST", "/api/transactions",
                    {"account_id": 99999, "amount_cents": 5, "payee": "x"})
    check("missing account 404", s == 404, sym)

    # balance + ordering
    s, b, sym = req("GET", "/api/accounts")
    bal = next((a["balance"] for a in (b or []) if a["id"] == acct), None)
    check("balance sums exactly", bal == 498750, sym + f" (balance={bal})")
    s, b, sym = req("GET", f"/api/transactions?account={acct}")
    dates = [t["date"] for t in (b or [])]
    check("transactions newest-date first", dates == sorted(dates, reverse=True), sym)

    # category filter
    s, b, sym = req("GET", f"/api/transactions?account={acct}&category=fun")
    check("category filter", s == 200 and len(b or []) == 1 and b[0]["payee"] == "cafe", sym)

    # deletion rules
    s, _, sym = req("DELETE", f"/api/accounts/{acct}")
    check("account with transactions refuses delete 409", s == 409, sym)
    s, b, sym = req("POST", "/api/accounts", {"name": "empty"})
    empty = b["id"] if s == 201 else None
    s, _, sym = req("DELETE", f"/api/accounts/{empty}")
    check("empty account deletes 204", s == 204, sym)
    s, _, sym = req("DELETE", "/api/transactions/424242")
    check("deleting missing transaction 404", s == 404, sym)

    # summary
    s, b, sym = req("GET", "/api/summary?month=2030-01")
    ok = (s == 200 and b and b.get("income_cents") == 500000
          and b.get("expense_cents") == 1250
          and (b.get("by_category") or {}).get("fun") in (-1250, 1250))
    check("monthly summary", ok, sym)
    s, b, sym = req("GET", "/api/summary?month=2029-12")
    check("empty month summary zeros", s == 200 and b.get("income_cents") == 0, sym)

    # HTML page exists
    s, text, sym = req("GET", "/", raw=True)
    check("root serves HTML", s == 200 and "<" in (text or ""), sym)


def stage2():
    s, b, sym = req("POST", "/api/accounts", {"name": "b2"})
    acct = b["id"]
    for amt, cat in ((-2000, "groceries"), (-3000, "groceries"), (-1000, "fun")):
        req("POST", "/api/transactions", {"account_id": acct, "amount_cents": amt,
                                          "payee": "shop", "category": cat,
                                          "date": "2030-03-10"})
    s, _, sym = req("PUT", "/api/budgets/groceries", {"month": "2030-03", "limit_cents": 4000})
    check("set budget", s in (200, 201, 204), sym)
    s, _, sym = req("PUT", "/api/budgets/yachts", {"month": "2030-03", "limit_cents": 1})
    check("budget unknown category 400", s == 400, sym)
    s, b, sym = req("GET", "/api/budgets?month=2030-03")
    row = next((r for r in (b or []) if r.get("category") == "groceries"), None)
    ok = row and row["limit_cents"] == 4000 and row["spent_cents"] == 5000 and row["remaining_cents"] == -1000
    check("budget math incl. negative remaining", ok, sym)
    s, _, sym = req("PUT", "/api/budgets/groceries", {"month": "2030-03", "limit_cents": 9000})
    s, b, sym = req("GET", "/api/budgets?month=2030-03")
    row = next((r for r in (b or []) if r.get("category") == "groceries"), None)
    check("budget overwrite", row and row["limit_cents"] == 9000, sym)


def stage3():
    s, b, sym = req("POST", "/api/accounts", {"name": "b3"})
    acct = b["id"]
    s, b, sym = req("POST", "/api/recurring",
                    {"account_id": acct, "amount_cents": -180000, "payee": "landlord",
                     "category": "rent", "day_of_month": 31})
    check("create recurring", s == 201, sym)
    s, b, sym = req("POST", "/api/recurring/apply", {"month": "2030-02"})
    check("apply creates one", s == 200 and b.get("created") == 1, sym)
    s, b, sym = req("POST", "/api/recurring/apply", {"month": "2030-02"})
    check("apply idempotent", s == 200 and b.get("created") == 0, sym)
    s, b, sym = req("GET", f"/api/transactions?account={acct}")
    t = (b or [None])[0]
    check("day clamped to Feb 28", t and t["date"] == "2030-02-28", sym)

    req("POST", "/api/transactions", {"account_id": acct, "amount_cents": -5,
                                      "payee": 'say "hi", ok', "category": "other",
                                      "date": "2030-02-02"})
    s, text, sym = req("GET", f"/api/export?account={acct}", raw=True)
    ok = (s == 200 and text.splitlines()[0] == "date,payee,category,amount_cents,note"
          and '"say ""hi"", ok"' in text)
    check("csv export with RFC quoting", ok, sym)


stage1()
if STAGE >= 2:
    stage2()
if STAGE >= 3:
    stage3()
finish()
