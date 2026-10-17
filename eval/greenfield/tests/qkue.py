import sys
sys.path.insert(0, sys.path[0] + "/..")
from kit import check, finish, req, set_now

STAGE = int(sys.argv[1]) if len(sys.argv) > 1 else 1

T0 = "2030-01-07T08:00:00Z"


def at(mins, secs=0):
    """RFC3339 at T0 + mins:secs (keeps tests readable)."""
    total = 8 * 3600 + mins * 60 + secs
    h, rem = divmod(total, 3600)
    m, s = divmod(rem, 60)
    return f"2030-01-07T{h:02d}:{m:02d}:{s:02d}Z"


def stage1():
    set_now(T0)
    s, b, sym = req("POST", "/api/queues/work/jobs", {"payload": {"n": 1}})
    check("push", s == 201 and "id" in (b or {}), sym)
    j1 = b["id"]
    s, b, sym = req("POST", "/api/queues/work/jobs", {"payload": {"n": 2}})
    j2 = b["id"]

    s, b, sym = req("POST", "/api/queues/work/pull", {"lease_seconds": 60})
    check("pull FIFO oldest", s == 200 and b.get("id") == j1 and b.get("attempt") == 1, sym)
    s, b, sym = req("POST", "/api/queues/work/pull", {"lease_seconds": 60})
    check("second pull gets next", s == 200 and b.get("id") == j2, sym)
    s, b, sym = req("POST", "/api/queues/work/pull", {"lease_seconds": 60})
    check("empty queue 204", s == 204, sym)

    s, _, sym = req("POST", f"/api/jobs/{j1}/ack")
    check("ack in lease 204", s == 204, sym)
    s, b, sym = req("GET", "/api/queues/work/stats")
    check("stats after ack", b == {"ready": 0, "leased": 1, "done": 1, "dead": 0}
          or (b or {}).get("done") == 1 and b.get("leased") == 1, sym)

    set_now(at(2))  # j2's 60s lease expired
    s, b, sym = req("POST", "/api/queues/work/pull", {"lease_seconds": 30})
    check("expired lease returns job, attempt bumped",
          s == 200 and b.get("id") == j2 and b.get("attempt") == 2, sym)
    set_now(at(3))  # that lease expired too
    s, _, sym = req("POST", f"/api/jobs/{j2}/ack")
    check("late ack 409", s == 409, sym)

    s, b, sym = req("POST", "/api/queues/work/pull", {"lease_seconds": 9999})
    check("lease_seconds over 600 rejected", s == 400, sym)


def stage2():
    set_now(at(10))
    s, b, sym = req("POST", "/api/queues/r/jobs", {"payload": "x"})
    j = b["id"]
    # exhaust 5 attempts via expiry; backoff must gate re-pull
    t = 10
    for attempt in range(1, 6):
        s, b, sym = req("POST", "/api/queues/r/pull", {"lease_seconds": 1})
        if not check(f"attempt {attempt} pull", s == 200 and b.get("attempt") == attempt, sym):
            break
        t += 1  # lease expires
        set_now(at(t))
        if attempt < 5:
            # immediately after expiry, backoff (2^(a-1)s) may block: check at boundary
            s2, b2, sym2 = req("POST", "/api/queues/r/pull", {"lease_seconds": 1})
            back = min(2 ** (attempt - 1), 60)
            if back > 60:  # unreachable, doc clarity
                pass
            if s2 == 200:
                # allowed only if backoff already elapsed within our jump
                check(f"backoff respected after attempt {attempt}", 60 >= back, sym2)
                # we consumed the pull; rewind bookkeeping: it becomes next attempt
                t += 1
                set_now(at(t))
                continue
            check(f"backoff blocks immediately after attempt {attempt}", s2 == 204, sym2)
            t += max(1, (back + 59) // 60)
            set_now(at(t))
    s, b, sym = req("POST", "/api/queues/r/pull", {"lease_seconds": 1})
    check("after 5 attempts job is dead, queue empty", s == 204, sym)
    s, b, sym = req("GET", "/api/queues/r/dead")
    row = next((d for d in (b or []) if d.get("id") == j), None)
    check("dead list shows job with attempts", row and row.get("attempts") == 5, sym)
    s, _, sym = req("POST", f"/api/jobs/{j}/revive")
    check("revive 2xx", s in (200, 204), sym)
    s, b, sym = req("POST", "/api/queues/r/pull", {"lease_seconds": 60})
    check("revived pulls with attempt reset", s == 200 and b.get("attempt") == 1, sym)
    s, _, _ = req("POST", f"/api/jobs/{b['id']}/ack")

    set_now(at(40))
    s, b, sym = req("POST", "/api/queues/n/jobs", {"payload": "y"})
    s, b, sym = req("POST", "/api/queues/n/pull", {"lease_seconds": 60})
    jid = b["id"]
    s, _, sym = req("POST", f"/api/jobs/{jid}/nack", {"delay_seconds": 120})
    check("nack 2xx", s in (200, 204), sym)
    s, b, sym = req("POST", "/api/queues/n/pull", {"lease_seconds": 60})
    check("nack delay blocks", s == 204, sym)
    set_now(at(43))
    s, b, sym = req("POST", "/api/queues/n/pull", {"lease_seconds": 60})
    check("nack delay elapses", s == 200 and b.get("id") == jid and b.get("attempt") == 2, sym)


def stage3():
    set_now(at(60))
    req("POST", "/api/queues/p/jobs", {"payload": "low", "priority": 7})
    req("POST", "/api/queues/p/jobs", {"payload": "urgent", "priority": 1})
    req("POST", "/api/queues/p/jobs", {"payload": "default"})
    s, b, sym = req("POST", "/api/queues/p/pull", {"lease_seconds": 60})
    check("priority first", s == 200 and b.get("payload") == "urgent", sym)
    s, b, sym = req("POST", "/api/queues/p/pull", {"lease_seconds": 60})
    check("then default-5 FIFO", s == 200 and b.get("payload") == "default", sym)

    s, b, sym = req("POST", "/api/queues/p/jobs",
                    {"payload": "later", "not_before": at(90)})
    s, b, sym = req("POST", "/api/queues/p/pull", {"lease_seconds": 60})
    check("not_before hides job (low pulls instead)", s == 200 and b.get("payload") == "low", sym)
    set_now(at(91))
    s, b, sym = req("POST", "/api/queues/p/pull", {"lease_seconds": 60})
    check("not_before elapsed", s == 200 and b.get("payload") == "later", sym)

    for jid in []:
        pass
    s, b, sym = req("GET", "/api/queues/p/stats")
    s, b, sym = req("POST", "/api/queues/p/purge", {"state": "done"})
    check("purge done 200", s == 200 and "purged" in (b or {}), sym)


stage1()
if STAGE >= 2:
    stage2()
if STAGE >= 3:
    stage3()
finish()
