"""Tests for run.py's own decisions; no AppTown is involved.

    python3 eval/companies/run_test.py
"""
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

import run

MIB = run.MIB


def req(app_dir, name, kind="feature", size="short", after=()):
    lines = ["---", "from: A Person, Role", f"size: {size}", f"kind: {kind}", "exercises: x"]
    lines += [f"after: {a}" for a in after]
    Path(app_dir).mkdir(parents=True, exist_ok=True)
    (Path(app_dir) / f"{name}.md").write_text("\n".join(lines + ["---", "", f"body of {name}"]) + "\n")


class Fake:
    """An AppTown that answers from a script and records what it was asked."""

    def __init__(self, answers=None, projects=(), capacity=None, deployments=None, user=""):
        self.calls, self.answers, self.user = [], list(answers or []), user
        self.projects, self.capacity = list(projects), capacity
        self.deployments = deployments if deployments is not None else [{"name": "main", "running": True, "url": "https://x.example"}]

    def run(self, *args):
        self.calls.append(args)
        return self.answers.pop(0) if self.answers else (0, {}, "")

    def text(self, *args):
        self.calls.append(("text",) + args)
        return "approving means: accept this change"

    def get(self, path):
        if path == "/api/v1/auth/me":
            return {"user": self.user} if self.user else {"mode": "internal"}
        if path == "/api/v1/projects":
            return [{"slug": s} for s in self.projects]
        if path.endswith("/capacity"):
            return self.capacity
        if path.endswith("/deployments"):
            if isinstance(self.deployments, Exception):
                raise self.deployments
            return self.deployments
        raise AssertionError(path)

    def call(self, method, path, body=None):
        self.calls.append((method, path))
        return {"pack": "django-htmx", "why": "it has forms"}


def company(tmp):
    """Two apps; a cross-app slug at different numbers; an after: dependency."""
    co = Path(tmp) / "acme"
    (co / "COMPANY.md").parent.mkdir(parents=True, exist_ok=True)
    (co / "COMPANY.md").write_text("A Person")
    req(co / "a", "00-create", "create")
    req(co / "a", "01-tweak")
    req(co / "a", "02-rename", "cross-app")
    req(co / "b", "00-create", "create")
    req(co / "b", "01-rename", "cross-app")
    req(co / "b", "02-later", after=["a/01"])
    return run.load_companies(tmp)


STACKS = {"waves": ["acme"], "apps": {"acme": {"a": {"pack": "go-small", "why": "a small service"},
                                                 "b": {"pack": "rails", "why": "reminders"}}}}


def state(tmp):
    return run.State(Path(tmp) / "state.json")


def mark(st, key, rid, outcome="shipped"):
    st.app(key)["requests"][rid] = {"intent": "opened", "change": "c-" + rid, "outcome": outcome, "kind": "x"}


class Ordering(unittest.TestCase):
    def test_no_later_request_before_every_create_in_the_company_has_finished(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            mark(st, "acme/a", "00-create")
            st.app("acme/b")["requests"]["00-create"] = {"intent": "opened", "change": "x"}
            self.assertEqual(run.next_requests(cos, st), [])

    def test_a_cross_app_request_waits_for_every_holder_and_then_goes_to_all(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            for k in ("acme/a", "acme/b"):
                mark(st, k, "00-create")
            got = [(k, r["id"]) for k, r in run.next_requests(cos, st)]
            # b's rename is ready, a's is not (a has 01 first): only a/01 goes.
            self.assertEqual(got, [("acme/a", "01-tweak")])
            mark(st, "acme/a", "01-tweak")
            got = sorted((k, r["id"]) for k, r in run.next_requests(cos, st))
            self.assertEqual(got, [("acme/a", "02-rename"), ("acme/b", "01-rename")])

    def test_after_holds_a_request_until_what_it_names_is_finished(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            for k in ("acme/a", "acme/b"):
                mark(st, k, "00-create")
            mark(st, "acme/b", "01-rename")
            st.app("acme/a")["requests"]["01-tweak"] = {"intent": "opened", "change": "x"}
            self.assertNotIn(("acme/b", "02-later"), [(k, r["id"]) for k, r in run.next_requests(cos, st)])
            mark(st, "acme/a", "01-tweak")
            self.assertIn(("acme/b", "02-later"), [(k, r["id"]) for k, r in run.next_requests(cos, st)])

    def test_one_open_request_per_app_and_an_unknown_one_holds_its_app(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            for k in ("acme/a", "acme/b"):
                mark(st, k, "00-create")
            st.app("acme/a")["requests"]["01-tweak"] = {"intent": "unknown"}
            self.assertNotIn("acme/a", [k for k, _ in run.next_requests(cos, st)])

    def test_the_shipped_set_has_no_after_naming_nothing_and_no_ordering_deadlock(self):
        cos = run.load_companies()
        for co, apps in cos.items():
            for app, reqs in apps.items():
                for r in reqs:
                    for d in r["after"]:
                        run._find(apps, d)   # raises when it names nothing
        # Everything opens eventually when each opened request finishes at once.
        with tempfile.TemporaryDirectory() as tmp:
            st = state(tmp)
            for co, apps in cos.items():
                for app, reqs in apps.items():
                    mark(st, f"{co}/{app}", reqs[0]["id"])
            total = sum(len(r) - 1 for apps in cos.values() for r in apps.values())
            opened = 0
            for _ in range(200):
                step = run.next_requests(cos, st)
                if not step:
                    break
                for k, r in step:
                    mark(st, k, r["id"])
                opened += len(step)
            self.assertEqual(opened, total)


class Resume(unittest.TestCase):
    def test_an_opened_request_is_never_opened_again(self):
        with tempfile.TemporaryDirectory() as tmp:
            st, f = state(tmp), Fake([(0, {"change": {"id": "c1", "name": "s#1"}}, "")])
            r = {"id": "01-x", "kind": "feature", "size": "short", "from": "", "body": "b"}
            self.assertTrue(run.open_request(f, st, "acme/a", "s", r))
            self.assertFalse(run.open_request(f, run.State(st.path), "acme/a", "s", r))
            self.assertEqual(len(f.calls), 1)

    def test_a_lost_answer_leaves_the_request_unknown_and_unsent_again(self):
        with tempfile.TemporaryDirectory() as tmp:
            st, f = state(tmp), Fake([(4, None, "answer lost")])
            r = {"id": "01-x", "kind": "data", "size": "long", "from": "", "body": "b"}
            run.open_request(f, st, "acme/a", "s", r)
            saved = json.loads(st.path.read_text())["apps"]["acme/a"]["requests"]["01-x"]
            self.assertEqual(saved["intent"], "unknown")
            self.assertFalse(run.open_request(f, run.State(st.path), "acme/a", "s", r))

    def test_intent_is_written_before_apptown_is_asked(self):
        with tempfile.TemporaryDirectory() as tmp:
            st = state(tmp)
            seen = {}

            class Peek(Fake):
                def run(self, *args):
                    seen["state"] = json.loads(st.path.read_text())
                    return (0, {"change": {"id": "c1"}}, "")

            run.open_request(Peek(), st, "acme/a", "s", {"id": "01-x", "kind": "fix", "size": "short", "from": "", "body": "b"})
            self.assertEqual(seen["state"]["apps"]["acme/a"]["requests"]["01-x"]["intent"], "opening")

    def test_reconcile_none_lets_it_be_opened_and_a_change_ref_resumes_it(self):
        with tempfile.TemporaryDirectory() as tmp:
            st = state(tmp)
            st.app("acme/a")["requests"]["01-x"] = {"intent": "unknown"}
            run.cmd_reconcile(st, SimpleNamespace(app="acme/a", request="01-x", change="none"))
            self.assertNotIn("01-x", st.app("acme/a")["requests"])
            st.app("acme/a")["requests"]["01-y"] = {"intent": "unknown"}
            run.cmd_reconcile(st, SimpleNamespace(app="acme/a", request="01-y", change="c9"))
            self.assertEqual(st.app("acme/a")["requests"]["01-y"]["change"], "c9")

    def test_a_project_already_on_apptown_is_adopted_not_created_again(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            f = Fake([(0, {"change": {"id": "c1"}}, "")], projects=["acme-a"])
            run.cmd_create(f, st, cos, SimpleNamespace(limit=1, target=None, accept_capacity_risk=None), STACKS)
            self.assertEqual(st.app("acme/a")["project"], "adopted")
            self.assertNotIn("new", [c[0] for c in f.calls if isinstance(c[0], str)])


class Driving(unittest.TestCase):
    def drive(self, answers, kind="feature"):
        tmp = tempfile.mkdtemp()
        st, f = state(tmp), Fake(answers)
        st.app("acme/a")["requests"]["01-x"] = {"intent": "opened", "change": "c1", "kind": kind}
        run.drive_one(f, st, "acme/a", "acme-a", "01-x", SimpleNamespace(target=None, accept_capacity_risk=None))
        return st.app("acme/a"), f.calls

    def test_a_question_is_read_then_approved_with_the_delegation_stated(self):
        a, calls = self.drive([(2, {"outcome": "needs_decision", "step": "s1"}, ""),
                               (0, {"decision": "approve"}, ""),
                               (0, {"outcome": "shipped"}, ""), (0, {"state": "shipped"}, "")])
        approve = [c for c in calls if c[0] == "approve"]
        self.assertEqual(approve[0], ("approve", "s1", "-y", "-m", run.approval_note(""), "--json"))
        self.assertIn(("text", "approve", "s1"), calls)
        self.assertEqual(a["requests"]["01-x"]["approvals"][0]["step"], "s1")
        self.assertEqual(a["requests"]["01-x"]["outcome"], "shipped")

    def test_done_work_that_is_not_landing_is_landed(self):
        a, calls = self.drive([(0, {"outcome": "done"}, ""), (0, {"state": "in_progress"}, ""),
                               (0, {"change": {"id": "c1"}}, ""),
                               (0, {"outcome": "shipped"}, ""), (0, {"state": "shipped"}, "")])
        self.assertIn(("land", "c1", "--json"), calls)
        self.assertEqual(a["requests"]["01-x"]["outcome"], "shipped")

    def test_a_blocked_change_stays_blocked_and_nothing_is_overridden(self):
        a, calls = self.drive([(1, {"outcome": "blocked", "detail": "tests failed"}, "")])
        self.assertEqual(a["requests"]["01-x"]["outcome"], "stopped")
        self.assertFalse(any(c[0] in ("override", "retry", "approve", "land") for c in calls))

    def test_a_change_apptown_closed_is_recorded_as_closed(self):
        a, _ = self.drive([(2, {"outcome": "ended"}, ""), (0, {"state": "abandoned", "closedAs": "nothing-to-change"}, "")])
        self.assertEqual(a["requests"]["01-x"]["outcome"], "closed")

    def test_a_shipped_create_records_its_address_and_stops_counting_as_pending(self):
        a, _ = self.drive([(0, {"outcome": "shipped"}, ""), (0, {"state": "shipped"}, "")], kind="create")
        self.assertEqual(a["deployed"], "https://x.example")
        self.assertFalse(a["pending"])


class Delegation(unittest.TestCase):
    QUESTION = [(2, {"outcome": "needs_decision", "step": "s1"}, ""), (0, {"decision": "approve"}, ""),
                (0, {"outcome": "shipped"}, ""), (0, {"state": "shipped"}, "")]

    def notes(self, f, **args):
        st = state(tempfile.mkdtemp())
        st.app("acme/a").update({"slug": "acme-a", "requests": {"01-x": {"intent": "opened", "change": "c1", "kind": "feature"}}})
        run.cmd_drive(f, st, {"acme": {"a": [{"id": "01-x"}]}}, SimpleNamespace(target=None, accept_capacity_risk=None, **args))
        return [c[4] for c in f.calls if c[0] == "approve" and "-m" in c]

    def test_approvals_name_the_user_the_token_acts_as(self):
        self.assertEqual(self.notes(Fake(self.QUESTION, user="operator")),
                         ["Approved on behalf of operator, delegated to the eval/companies runner"])

    def test_on_behalf_of_names_the_person_instead(self):
        self.assertEqual(self.notes(Fake(self.QUESTION, user="operator"), on_behalf_of="alice"),
                         ["Approved on behalf of alice, delegated to the eval/companies runner"])

    def test_with_no_known_user_the_note_names_only_the_runner(self):
        self.assertEqual(self.notes(Fake(self.QUESTION)), ["Approved by the eval/companies runner"])


class StateLocation(unittest.TestCase):
    def test_state_names_the_file(self):
        self.assertEqual(run.state_path("/x/s.json", "/d", "forge.example.com"), Path("/x/s.json"))

    def test_the_environment_names_a_directory_with_a_file_per_apptown(self):
        self.assertEqual(run.state_path(None, "/d", "forge.example.com"), Path("/d/forge.example.com.json"))

    def test_with_neither_the_runner_refuses(self):
        with self.assertRaises(SystemExit) as e:
            run.state_path(None, None, "forge.example.com")
        self.assertIn("--state", str(e.exception.code))


class LostAnswers(unittest.TestCase):
    """AC-C01, AC-C04: a mutation whose answer was lost is never resent and
    never counts as finished."""

    def setup(self, tmp):
        cos, st = company(tmp), state(tmp)
        for k in ("acme/a", "acme/b"):
            mark(st, k, "00-create")
        st.app("acme/a")["slug"] = "acme-a"
        st.app("acme/a")["requests"]["01-tweak"] = {"intent": "opened", "change": "c1", "kind": "feature"}
        return cos, st

    def drive(self, st, answers):
        f = Fake(answers)
        run.drive_one(f, st, "acme/a", "acme-a", "01-tweak", SimpleNamespace(target=None, accept_capacity_risk=None))
        return f.calls

    def test_a_lost_approval_answer_holds_the_app_and_is_not_voted_again(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = self.setup(tmp)
            self.drive(st, [(2, {"outcome": "needs_decision", "step": "s1"}, ""), (4, None, "answer lost")])
            r = st.app("acme/a")["requests"]["01-tweak"]
            self.assertFalse(run.finished(r))
            self.assertIn("held", r)
            self.assertNotIn("acme/a", [k for k, _ in run.next_requests(cos, st)])
            calls = self.drive(st, [(2, {"outcome": "needs_decision", "step": "s1"}, "")])
            self.assertFalse(any(c[0] == "approve" for c in calls))

    def test_a_vote_saved_but_never_answered_is_not_sent_again(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = self.setup(tmp)
            st.app("acme/a")["requests"]["01-tweak"]["approvals"] = [{"step": "s1", "meaning": "m", "answer": None}]
            calls = self.drive(st, [(2, {"outcome": "needs_decision", "step": "s1"}, "")])
            self.assertFalse(any(c[0] == "approve" for c in calls))
            self.assertIn("held", st.app("acme/a")["requests"]["01-tweak"])

    def test_once_apptown_has_moved_on_the_hold_is_released(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = self.setup(tmp)
            self.drive(st, [(2, {"outcome": "needs_decision", "step": "s1"}, ""), (4, None, "answer lost")])
            self.drive(st, [(0, {"outcome": "shipped"}, ""), (0, {"state": "shipped"}, "")])
            r = st.app("acme/a")["requests"]["01-tweak"]
            self.assertNotIn("held", r)
            self.assertEqual(r["outcome"], "shipped")

    def test_an_opening_interrupted_mid_send_can_be_reconciled(self):
        with tempfile.TemporaryDirectory() as tmp:
            st = state(tmp)

            class Dies(Fake):
                def run(self, *args):
                    raise KeyboardInterrupt

            with self.assertRaises(KeyboardInterrupt):
                run.open_request(Dies(), st, "acme/a", "s", {"id": "01-x", "kind": "fix", "size": "short", "from": "", "body": "b"})
            again = run.State(st.path)
            self.assertEqual(again.app("acme/a")["requests"]["01-x"]["intent"], "opening")
            run.cmd_reconcile(again, SimpleNamespace(app="acme/a", request="01-x", change="c1"))
            self.assertEqual(again.app("acme/a")["requests"]["01-x"]["intent"], "opened")


class Observation(unittest.TestCase):
    """AC-C03: only an observed running deployment releases an app's count."""

    def ship(self, deployments):
        tmp = tempfile.mkdtemp()
        st = state(tmp)
        a = st.app("acme/a")
        a.update({"slug": "acme-a", "pending": True})
        a["requests"]["00-create"] = {"intent": "opened", "change": "c1", "kind": "create"}
        f = Fake([(0, {"outcome": "shipped"}, ""), (0, {"state": "shipped"}, "")], deployments=deployments)
        run.drive_one(f, st, "acme/a", "acme-a", "00-create", SimpleNamespace(target=None, accept_capacity_risk=None))
        return st

    def test_a_failed_read_stays_unknown_and_counted(self):
        st = self.ship(OSError("connection reset"))
        a = st.app("acme/a")
        self.assertEqual(a["observed"], "unknown")
        self.assertTrue(a["pending"])

    def test_a_null_running_is_unknown_and_false_is_not_running_both_counted(self):
        for dep, seen in (([{"name": "main", "running": None}], "unknown"), ([{"name": "main", "running": False}], "not running")):
            a = self.ship(dep).app("acme/a")
            self.assertEqual(a["observed"], seen)
            self.assertTrue(a["pending"])

    def test_the_next_drive_looks_again_and_settles_it(self):
        st = self.ship(OSError("connection reset"))
        run.cmd_drive(Fake(), st, {"acme": {"a": [{"id": "00-create"}]}}, SimpleNamespace(target=None, accept_capacity_risk=None))
        a = st.app("acme/a")
        self.assertEqual((a["observed"], a["deployed"], a["pending"]), ("running", "https://x.example", False))


class Stacks(unittest.TestCase):
    """stacks.yaml chooses each app's stack; the picker is only measured."""

    def test_stacks_yaml_covers_every_app_with_a_stock_pack_and_every_company_once(self):
        stacks, packs = run.load_stacks(), run.stock_packs()
        cos = run.load_companies()
        for co, apps in cos.items():
            for app in apps:
                self.assertIn(app, stacks["apps"].get(co, {}), f"{co}/{app} has no stacks.yaml entry")
        for co, apps in stacks["apps"].items():
            self.assertIn(co, cos, f"stacks.yaml names a company with no directory: {co}")
            for app, e in apps.items():
                self.assertIn(app, cos[co], f"stacks.yaml names an app with no directory: {co}/{app}")
                self.assertIn(e["pack"], packs, f"{co}/{app}: {e['pack']} is not a stock pack")
        self.assertEqual(sorted(stacks["waves"]), sorted(cos), "every company in exactly one wave")

    def test_create_uses_the_chosen_pack_and_records_the_pickers_suggestion_beside_it(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            f = Fake([(0, {"id": "pa"}, ""), (0, {"change": {"id": "ca"}}, "")])
            run.cmd_create(f, st, cos, SimpleNamespace(limit=1, app=None, target=None, accept_capacity_risk=None), STACKS)
            self.assertIn(("new", "acme-a", "--pack", "go-small", "--json"), f.calls)
            a = st.app("acme/a")
            self.assertEqual((a["pack"], a["suggested"]), ("go-small", "django-htmx"))

    def test_an_app_with_no_stacks_entry_is_not_created(self):
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            f = Fake()
            run.cmd_create(f, st, cos, SimpleNamespace(limit=None, app=None, target=None, accept_capacity_risk=None),
                           {"waves": ["acme"], "apps": {}})
            self.assertFalse(any(c[0] in ("new", "build") for c in f.calls if isinstance(c[0], str)))

    def test_a_stacks_line_it_does_not_understand_is_an_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "stacks.yaml"
            p.write_text("waves:\n  - acme\napps:\n  acme:\n    a: { pack: go-small }\n")
            with self.assertRaises(ValueError):
                run.load_stacks(p)


class Message(unittest.TestCase):
    def test_the_request_comes_first_so_it_titles_the_change_and_the_asker_is_kept(self):
        m = run.message({"from": "Mateo Silva, PE teacher", "body": "I'd like a page."})
        self.assertTrue(m.startswith("I'd like a page."))
        self.assertTrue(m.endswith("Asked by Mateo Silva, PE teacher."))


class Capacity(unittest.TestCase):
    def cap(self, total, apptown_use, other=0, known=True):
        return {"memory": {"total": {"known": True, "value": total},
                           "inUse": {"apptown": {"known": known, "value": apptown_use}, "other": {"known": True, "value": other}}}}

    def test_apps_opened_but_not_running_count_against_the_room(self):
        total, used = 2048 * MIB, 1400 * MIB
        _, room0 = run.room_after(self.cap(total, used), 0)
        _, room3 = run.room_after(self.cap(total, used), 3)
        self.assertEqual(room0 - room3, 3 * run.ESTIMATE)

    def test_unknown_use_is_not_free_memory(self):
        self.assertIsNone(run.room_after(self.cap(2048 * MIB, 0, known=False), 0))

    def test_the_guard_refuses_below_the_reserve_and_an_accepted_risk_is_recorded(self):
        with tempfile.TemporaryDirectory() as tmp:
            st = state(tmp)
            for i in range(4):
                st.app(f"acme/p{i}")["pending"] = True
            f = Fake(capacity=self.cap(2048 * MIB, 1000 * MIB))
            ok, why = run.capacity_ok(f, st, "host", None)
            self.assertFalse(ok, why)
            ok, _ = run.capacity_ok(f, st, "host", "a small static app, by hand")
            self.assertTrue(ok)
            self.assertEqual(st.data["risks"][0]["why"], "a small static app, by hand")

    def test_landing_an_admitted_app_does_not_count_it_twice(self):
        # AC-C02: 2048 MiB host, 1000 MiB in use; two apps admitted, then landed.
        with tempfile.TemporaryDirectory() as tmp:
            cos, st = company(tmp), state(tmp)
            cap = self.cap(2048 * MIB, 1000 * MIB)
            f = Fake([(0, {"id": "pa"}, ""), (0, {"change": {"id": "ca"}}, ""),
                      (0, {"id": "pb"}, ""), (0, {"change": {"id": "cb"}}, "")], capacity=cap)
            run.cmd_create(f, st, cos, SimpleNamespace(limit=None, app=None, target="host", accept_capacity_risk=None), STACKS)
            st = run.State(st.path)
            self.assertEqual(sum(1 for a in st.data["apps"].values() if a.get("pending")), 2)
            g = Fake([(0, {"outcome": "done"}, ""), (0, {"state": "in_progress"}, ""), (0, {"change": {"id": "ca"}}, ""),
                      (0, {"outcome": "shipped"}, ""), (0, {"state": "shipped"}, "")], capacity=cap)
            run.drive_one(g, st, "acme/a", "acme-a", "00-create", SimpleNamespace(target="host", accept_capacity_risk=None))
            self.assertIn(("land", "ca", "--json"), g.calls)

    def test_an_elastic_target_is_not_checked(self):
        with tempfile.TemporaryDirectory() as tmp:
            ok, why = run.capacity_ok(Fake(capacity={"elastic": True}), state(tmp), "cloud", None)
            self.assertTrue(ok)
            self.assertIn("grows on demand", why)


if __name__ == "__main__":
    unittest.main()
