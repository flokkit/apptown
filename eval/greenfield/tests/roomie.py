import sys
sys.path.insert(0, sys.path[0] + "/..")
from kit import check, finish, req, set_now

STAGE = int(sys.argv[1]) if len(sys.argv) > 1 else 1
# clock starts at 2030-01-07T08:00:00Z (Monday) via kit's clock server


def stage1():
    s, b, sym = req("POST", "/api/rooms", {"name": "small", "capacity": 4})
    small = (b or {}).get("id"); check("create room", s == 201, sym)
    s, b, sym = req("POST", "/api/rooms", {"name": "hall", "capacity": 20})
    hall = (b or {}).get("id")

    day = "2030-01-08"
    def book(room, start, end, who="ann", att=2):
        return req("POST", "/api/bookings",
                   {"room_id": room, "who": who, "attendees": att,
                    "start": f"{day}T{start}:00Z", "end": f"{day}T{end}:00Z"})

    s, b, sym = book(small, "10:00", "11:00")
    check("clean booking", s == 201, sym)
    first_id = (b or {}).get("id")
    s, b, sym = book(small, "10:30", "11:30")
    check("overlap 409 names the blocker",
          s == 409 and (b or {}).get("conflicts_with") == first_id, sym)
    s, b, sym = book(small, "11:00", "12:00")
    check("touching bookings fine (small room)", s == 201, sym)
    s, b, sym = book(small, "12:00", "12:10")
    check("under 15 min refused", s in (400, 422), sym)
    s, b, sym = book(small, "13:00", "17:30")
    check("over 4h refused", s in (400, 422), sym)
    s, b, sym = book(small, "13:07", "13:37")
    check("non-5-min boundary refused", s in (400, 422), sym)
    s, b, sym = book(small, "06:00", "07:30")
    check("before opening refused", s in (400, 422), sym)
    s, b, sym = book(small, "19:30", "20:30")
    check("past closing refused", s in (400, 422), sym)
    s, b, sym = book(small, "14:00", "15:00", att=5)
    check("over capacity 422", s == 422, sym)
    s, b, sym = req("POST", "/api/bookings",
                    {"room_id": small, "who": "old", "attendees": 1,
                     "start": "2030-01-06T10:00:00Z", "end": "2030-01-06T11:00:00Z"})
    check("past booking refused via CLOCK_URL", s == 422, sym)

    s, b, sym = req("GET", f"/api/bookings?room={small}&date={day}")
    check("list bookings for day", s == 200 and len(b or []) == 2, sym)
    s, _, sym = req("DELETE", f"/api/bookings/{first_id}")
    check("cancel 204", s == 204, sym)
    s, b, sym = book(small, "10:30", "11:00", who="late")
    check("cancelled slot reusable", s == 201, sym)


def stage2():
    s, b, sym = req("POST", "/api/rooms", {"name": "big", "capacity": 12})
    big = (b or {}).get("id")
    day = "2030-01-09"
    s, b, sym = req("POST", "/api/bookings",
                    {"room_id": big, "who": "a", "attendees": 3,
                     "start": f"{day}T10:00:00Z", "end": f"{day}T11:00:00Z"})
    check("big room clean booking", s == 201, sym)
    s, b, sym = req("POST", "/api/bookings",
                    {"room_id": big, "who": "b", "attendees": 3,
                     "start": f"{day}T11:05:00Z", "end": f"{day}T11:35:00Z"})
    check("buffer rule: 5-min gap in big room is a conflict", s == 409, sym)
    s, b, sym = req("POST", "/api/bookings",
                    {"room_id": big, "who": "b", "attendees": 3,
                     "start": f"{day}T11:10:00Z", "end": f"{day}T11:40:00Z"})
    check("buffer rule: 10-min gap ok", s == 201, sym)

    s, b, sym = req("POST", "/api/recurring",
                    {"room_id": big, "who": "standup", "weekday": "Wednesday",
                     "start_time": "09:00", "end_time": "09:30",
                     "attendees": 5, "weeks": 3})
    ok = s == 201 and len((b or {}).get("created", [])) == 3
    check("recurring creates 3 instances", ok, sym)
    s, b, sym = req("POST", "/api/recurring",
                    {"room_id": big, "who": "rival", "weekday": "Wednesday",
                     "start_time": "09:00", "end_time": "09:30",
                     "attendees": 5, "weeks": 2})
    ok = s == 201 and len((b or {}).get("created", [])) == 0 and len((b or {}).get("skipped", [])) == 2
    check("recurring skips conflicts and reports them", ok, sym)


def stage3():
    s, b, sym = req("POST", "/api/rooms", {"name": "util", "capacity": 6})
    room = (b or {}).get("id")
    day = "2030-01-10"
    req("POST", "/api/bookings", {"room_id": room, "who": "x", "attendees": 2,
                                  "start": f"{day}T09:00:00Z", "end": f"{day}T10:30:00Z"})
    s, b, sym = req("GET", f"/api/reports/utilization?date={day}")
    row = next((r for r in (b or []) if r.get("room_id") == room), None)
    check("utilization math 90/780=0.115",
          row and row.get("booked_minutes") == 90 and abs(row.get("utilization", 0) - 0.115) < 1e-9, sym)

    s, b, sym = req("POST", "/api/bookings", {"room_id": room, "who": "holder", "attendees": 2,
                                              "start": f"{day}T14:00:00Z", "end": f"{day}T15:00:00Z"})
    hold = (b or {}).get("id")
    s, b, sym = req("POST", "/api/waitlist", {"room_id": room, "who": "wl1", "attendees": 2,
                                              "start": f"{day}T14:00:00Z", "end": f"{day}T15:00:00Z"})
    check("waitlist accepts despite conflict", s == 201, sym)
    req("POST", "/api/waitlist", {"room_id": room, "who": "wl2", "attendees": 2,
                                  "start": f"{day}T14:00:00Z", "end": f"{day}T15:00:00Z"})
    s, _, sym = req("DELETE", f"/api/bookings/{hold}")
    s, b, sym = req("GET", f"/api/bookings?room={room}&date={day}")
    whos = [x.get("who") for x in (b or [])]
    check("cancel promotes FIFO waitlist entry", "wl1" in whos and "wl2" not in whos, sym)
    s, b, sym = req("GET", "/api/waitlist")
    whos = [x.get("who") for x in (b or [])]
    check("promoted entry left the waitlist", "wl1" not in whos and "wl2" in whos, sym)


stage1()
if STAGE >= 2:
    stage2()
if STAGE >= 3:
    stage3()
finish()
