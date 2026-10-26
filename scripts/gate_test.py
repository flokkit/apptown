#!/usr/bin/env python3
"""scripts/gate.py, the runner of `make check`'s stages, on stub stages.

Each test gives the gate a stub `make` whose stage names choose a behaviour
(pass, fail, a slow TERM cleanup, ignoring TERM, being killed), then checks
what the gate reports and which stage processes are left alive.

This file runs as `make check`'s last stage, alone, after the stages that
run together (check-gate). It may signal only processes it owns and can
still name: its own direct children that it has not yet reaped (until a
parent reaps a child, the child's pid, and the group it leads, belong to no
other process). It starts every process with posix_spawn in start_fixture
and is the only one to reap them, in reap, with INT and TERM held off from a
reap until it is recorded; so "not yet reaped" is something this code
knows, not something it infers. Every signal goes through signal_fixture,
which refuses a fixture it has reaped. A fixture gate leads a group of its
own, so a group signal a test sends reaches that gate alone. Every fixture
ends with its group killed and its leader reaped, whether its test passes
or fails, and on INT or TERM before this process exits.
"""
import ast
import os
import shutil
import signal
import sys
import tempfile
import textwrap
import time
import unittest

GATE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "gate.py")
STOPPING = {signal.SIGINT, signal.SIGTERM}
OUTPUT = tempfile.mkdtemp(prefix="gate-test.")
FIXTURES = []


class Fixture:
    def __init__(self, pid, own_group, grace, out):
        self.pid, self.own_group, self.grace, self.out = pid, own_group, grace, out
        self.returncode = None  # set by reap, and only there


def start_fixture(argv, env=None, own_group=False, grace=10):
    """The only way this file starts a process: a direct child, registered
    before this returns (INT and TERM are held off until then). own_group
    makes it lead a process group of its own. grace is how long stop_fixtures
    gives the group to end after TERM: a fixture that runs these tests itself
    needs longer than the grace its own fixtures get."""
    out = tempfile.mktemp(dir=OUTPUT)
    held = signal.pthread_sigmask(signal.SIG_BLOCK, STOPPING)
    try:
        pid = os.posix_spawnp(argv[0], argv, env if env is not None else os.environ, file_actions=[
            (os.POSIX_SPAWN_OPEN, 0, os.devnull, os.O_RDONLY, 0),
            (os.POSIX_SPAWN_OPEN, 1, out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644),
            # Python before 3.14 refuses setpgroup=None: leave it out instead.
            (os.POSIX_SPAWN_DUP2, 1, 2)], setsigmask=[], **({"setpgroup": 0} if own_group else {}))
        fixture = Fixture(pid, own_group, grace, out)
        FIXTURES.append(fixture)
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, held)
    return fixture


def signal_fixture(fixture, sig, group=False):
    """The only way this file sends a signal: to a fixture it has not
    reaped, or to the group that fixture leads."""
    if fixture.returncode is not None:
        return
    if group:
        assert fixture.own_group, "only a fixture leading its own group is signalled by group"
        try:
            os.killpg(fixture.pid, sig)
        except PermissionError:
            # macOS refuses a group whose only member is its exited, unreaped
            # leader: nothing in it is running.
            pass
    else:
        os.kill(fixture.pid, sig)


def exited(fixture):
    """Whether a fixture has exited, without reaping it."""
    if fixture.returncode is not None:
        return True
    return os.waitid(os.P_PID, fixture.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is not None


def reap(fixture):
    """The only reaper: waits for the fixture and records its status, with
    INT and TERM held off until it is recorded."""
    held = signal.pthread_sigmask(signal.SIG_BLOCK, STOPPING)
    try:
        if fixture.returncode is None:
            _, status = os.waitpid(fixture.pid, 0)
            fixture.returncode = os.waitstatus_to_exitcode(status)
            if fixture in FIXTURES:
                FIXTURES.remove(fixture)
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, held)


def end(fixture):
    """KILLs what is left of the fixture's group (or the fixture), then reaps
    it: its leader is still unreaped, so the group is still its own."""
    signal_fixture(fixture, signal.SIGKILL, group=fixture.own_group)
    reap(fixture)


def finish(fixture, timeout=120):
    """Waits for a fixture to exit on its own, ends whatever it left in its
    group, and returns its output."""
    deadline = time.time() + timeout
    while not exited(fixture):
        if time.time() > deadline:
            raise AssertionError("fixture %s did not exit within %ss" % (fixture.pid, timeout))
        time.sleep(0.05)
    end(fixture)
    with open(fixture.out, errors="replace") as f:
        return f.read()


def output(*argv):
    return finish(start_fixture(list(argv)))


def stop_fixtures():
    """Ends every fixture still running. Each that leads a group gets TERM
    for the group, so a fixture gate stops its stages (and a test run inside
    one ends its own fixtures); once its leader has exited, or its grace has
    passed, the group gets KILL and the leader is reaped."""
    stopping = list(FIXTURES)
    for fixture in stopping:
        if fixture.own_group:
            signal_fixture(fixture, signal.SIGTERM, group=True)
    started = time.time()
    for fixture in stopping:
        while fixture.own_group and not exited(fixture) and time.time() < started + fixture.grace:
            time.sleep(0.05)
        end(fixture)


def on_signal(signum, _frame):
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    stop_fixtures()
    if os.environ.get("SENTINEL"):
        open(os.environ["SENTINEL"] + ".done", "w").close()
    os._exit(128 + signum)


STUB = textwrap.dedent("""\
    #!/bin/sh
    # stub make: "$2" is the stage. Records its pid, then behaves as named.
    stage=$2
    echo $$ >> "$PIDS"
    case $stage in
    pass) echo fine ;;
    fail) echo "the reason it failed"; exit 7 ;;
    go-fail)
        # go test's shape: the failing package, then more that passed.
        printf -- '--- FAIL: TestTheBroken (0.01s)\\n    broken_test.go:9: wanted 2, got 3\\nFAIL\\nFAIL\\texample/broken\\t0.1s\\n'
        i=0; while [ $i -lt 70 ]; do echo "ok  	example/fine$i	0.1s"; i=$((i+1)); done
        echo FAIL; exit 1 ;;
    killed) kill -KILL $$ ;;
    demote-gate)
        # Moves the gate (this stub's parent) into the throttled band, as
        # Game Mode coming on part-way through a run does.
        taskpolicy -b -p $PPID && echo demoted ;;
    slow-cleanup)
        # Takes three seconds to clean up after TERM, as go test may.
        trap 'sleep 3; echo cleaned > "$PIDS.cleaned"; exit 1' TERM
        sleep 100 & echo $! >> "$PIDS"; wait ;;
    ignores-term)
        trap '' TERM
        sh -c 'trap "" TERM; echo $$ >> "$PIDS"; while :; do sleep 1; done' ;;
    gate-tests)
        # Every test in this file, as the check-gate stage runs them: the
        # gate's own child, so a stopped gate waits for its cleanup. It lets
        # the sentinel end when it does.
        exec python3 "$GATE_TEST" ;;
    sentinel)
        # Another stage of the same gate: it records any signal it is sent,
        # and ends (a cancelled gate need not wait for it).
        trap 'echo TERM >> "$SENTINEL"; exit 1' TERM
        trap 'echo INT >> "$SENTINEL"; exit 1' INT
        while [ ! -f "$SENTINEL.done" ]; do sleep 0.2; done ;;
    *) echo "unknown stub stage $stage"; exit 3 ;;
    esac
    """)


def alive(pid):
    """Whether a process (not necessarily ours) is running: signal 0 and ps
    look, and change nothing."""
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    state = output("ps", "-o", "stat=", "-p", str(pid)).strip()
    return state != "" and not state.startswith("Z")


class Gate(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.make = os.path.join(self.dir, "make")
        with open(self.make, "w") as f:
            f.write(STUB)
        os.chmod(self.make, 0o755)
        self.pids = os.path.join(self.dir, "pids")
        self.env = dict(os.environ, MAKE=self.make, PIDS=self.pids, GATE_LOG_DIR=os.path.join(self.dir, "logs"))

    def tearDown(self):
        stop_fixtures()
        shutil.rmtree(self.dir, ignore_errors=True)

    def stage_pids(self):
        try:
            with open(self.pids) as f:
                return [int(x) for x in f.read().split()]
        except FileNotFoundError:
            return []

    def run_gate(self, *stages):
        p = self.start_gate(*stages)
        return p, finish(p)

    def start_gate(self, *stages):
        return start_fixture(["python3", GATE, *stages], env=self.env, own_group=True)

    def wait_for_stages(self, n):
        deadline = time.time() + 10
        while len(self.stage_pids()) < n:
            self.assertLess(time.time(), deadline, "stages did not start")
            time.sleep(0.05)
        time.sleep(0.3)

    def assert_no_stage_alive(self):
        deadline = time.time() + 5
        while any(alive(pid) for pid in self.stage_pids()) and time.time() < deadline:
            time.sleep(0.1)
        self.assertEqual([pid for pid in self.stage_pids() if alive(pid)], [], "stage processes outlived the gate")

    def test_every_stage_runs_and_a_failure_fails_the_gate_with_its_log(self):
        p, out = self.run_gate("pass", "fail")
        self.assertEqual(p.returncode, 1, out)
        self.assertRegex(out, r"pass\s+exit 0 ")
        self.assertRegex(out, r"fail\s+exit 7 ")
        self.assertIn("the reason it failed", out)
        self.assertIn("gate FAILED: fail", out)
        self.assertNotIn("throttled band", out)
        p, out = self.run_gate("pass", "pass")
        self.assertEqual(p.returncode, 0, out)
        self.assertNotIn("throttled band", out)

    @unittest.skipUnless(sys.platform == "darwin", "the throttled band and taskpolicy are macOS's")
    def test_a_gate_in_the_throttled_band_starts_no_stage_and_says_why(self):
        # taskpolicy -b starts it as Game Mode leaves every app but the game.
        p = start_fixture(["taskpolicy", "-b", "python3", GATE, "pass"], env=self.env, own_group=True)
        out = finish(p)
        self.assertEqual(p.returncode, 3, out)
        self.assertIn("gate: refused: this gate runs in macOS's throttled band", out)
        self.assertIn("Game Mode", out)
        self.assertEqual(self.stage_pids(), [], "a refused gate started a stage")
        self.assertFalse(os.path.exists(self.env["GATE_LOG_DIR"]), "a refused gate made a log directory")
        # make check runs the check alone before its web build.
        p = start_fixture(["taskpolicy", "-b", "python3", GATE, "--refuse-if-throttled"], env=self.env)
        out = finish(p)
        self.assertEqual(p.returncode, 3, out)
        self.assertIn("gate: refused", out)
        p = start_fixture(["python3", GATE, "--refuse-if-throttled"], env=self.env)
        out = finish(p)
        self.assertEqual(p.returncode, 0, out)

    def runner(self):
        path = os.path.join(self.dir, "runner")
        with open(path, "w") as f:
            f.write('#!/bin/sh\nprintf "%s\\n" "$@" >> "$PIDS.argv"\nexec "$@"\n')
        os.chmod(path, 0o755)
        self.env["GATE_STAGE_RUNNER"] = path

    def test_optional_runner_preserves_arguments_and_failure(self):
        self.runner()
        p, out = self.run_gate("fail")
        self.assertEqual(p.returncode, 1, out)
        with open(self.pids + ".argv") as f:
            self.assertEqual(f.read().splitlines(), [self.make, "--no-print-directory", "fail"])
        self.assertIn("the reason it failed", out)

    def test_invalid_runner_starts_no_stage(self):
        for path in ("relative-runner", os.path.join(self.dir, "missing"), self.dir):
            self.env["GATE_STAGE_RUNNER"] = path
            p, out = self.run_gate("pass")
            self.assertEqual(p.returncode, 2, out)
            self.assertEqual(self.stage_pids(), [])

    def test_optional_runner_keeps_shutdown_ownership(self):
        self.runner()
        self.test_term_returns_only_after_a_slow_stage_has_cleaned_up()

    @unittest.skipUnless(sys.platform == "darwin", "the throttled band and taskpolicy are macOS's")
    def test_a_gate_demoted_part_way_says_so_and_keeps_its_result(self):
        p, out = self.run_gate("demote-gate")
        self.assertEqual(p.returncode, 0, out)
        self.assertIn("gate: this gate now runs in macOS's throttled band", out)
        self.assertIn("gate passed", out)
        p, out = self.run_gate("demote-gate", "fail")
        self.assertEqual(p.returncode, 1, out)
        self.assertIn("gate: this gate now runs in macOS's throttled band", out)
        self.assertIn("gate FAILED: fail", out)

    def test_a_failed_go_test_is_shown_even_when_passing_packages_follow_it(self):
        p, out = self.run_gate("go-fail")
        self.assertEqual(p.returncode, 1, out)
        failing = out.split("its failing Go tests ==", 1)[1].split("last 60 lines", 1)[0]
        self.assertIn("--- FAIL: TestTheBroken", failing)
        self.assertIn("wanted 2, got 3", failing)
        self.assertIn("FAIL\texample/broken", failing)
        self.assertNotIn("example/fine", failing)

    def test_a_killed_stage_fails_even_where_an_earlier_run_passed(self):
        # The same log directory, a passing run, then a stage killed by a
        # signal: its result is this run's, never the earlier one.
        p, out = self.run_gate("pass")
        self.assertEqual(p.returncode, 0, out)
        p, out = self.run_gate("killed")
        self.assertEqual(p.returncode, 1, out)
        self.assertRegex(out, r"killed\s+exit 137 ")

    def test_term_returns_only_after_a_slow_stage_has_cleaned_up(self):
        p = self.start_gate("slow-cleanup", "pass")
        self.wait_for_stages(2)
        signal_fixture(p, signal.SIGTERM)
        out = finish(p, timeout=30)
        self.assertEqual(p.returncode, 143, out)
        self.assertTrue(os.path.exists(self.pids + ".cleaned"), "the gate exited before the stage finished its cleanup")
        # The stage's background sleep, left by its cleanup, ended with the
        # fixture's group (PG5-02).
        self.assert_no_stage_alive()

    def test_int_stops_every_stage_before_the_gate_exits(self):
        p = self.start_gate("slow-cleanup")
        self.wait_for_stages(2)
        signal_fixture(p, signal.SIGINT)
        out = finish(p, timeout=30)
        self.assertEqual(p.returncode, 130, out)
        self.assertTrue(os.path.exists(self.pids + ".cleaned"))
        self.assert_no_stage_alive()

    def test_the_queues_kill_leaves_no_stage_even_one_ignoring_term(self):
        # As make-check-queued.sh stops a gate: TERM to its group, a grace,
        # then KILL to the group. The gate does not exit while a stage that
        # ignores TERM lives, and KILL reaches every stage.
        p = self.start_gate("ignores-term", "pass")
        self.wait_for_stages(3)
        signal_fixture(p, signal.SIGTERM, group=True)
        time.sleep(2)
        self.assertFalse(exited(p), "the gate exited while a stage ignoring TERM was still running")
        signal_fixture(p, signal.SIGKILL, group=True)
        finish(p, timeout=10)
        self.assert_no_stage_alive()

    def test_a_gate_signals_only_its_own_stages_not_its_callers_group(self):
        # Under the pre-push hook the gate shares git push's group. Its
        # caller (a shell here, which alone may signal the gate it started)
        # stops it with TERM: the caller's other process in the group must
        # not be signalled.
        script = textwrap.dedent("""\
            sleep 30 & other=$!
            python3 "$0" slow-cleanup & gate=$!
            while [ "$(wc -l < "$PIDS" 2>/dev/null || echo 0)" -lt 2 ]; do sleep 0.1; done
            kill -TERM $gate; wait $gate
            kill -0 $other && echo "the caller's other process is alive"
            kill $other
            """)
        p = start_fixture(["sh", "-c", script, GATE], env=self.env, own_group=True)
        out = finish(p, timeout=60)
        self.assertIn("the caller's other process is alive", out)
        self.assertTrue(os.path.exists(self.pids + ".cleaned"))

    def test_these_tests_never_signal_another_stage_of_the_gate_running_them(self):
        # check-gate runs inside the real gate, beside the gate's other
        # stages in one process group. Run every test here as a stage of a
        # gate beside a sentinel stage: the sentinel must receive no signal.
        if os.environ.get("SENTINEL"):
            self.skipTest("this is the run the test makes")
        sentinel = os.path.join(self.dir, "sentinel")
        p = start_fixture(["python3", GATE, "gate-tests", "sentinel"], own_group=True, grace=60,
                          env=dict(self.env, GATE_TEST=os.path.abspath(__file__), SENTINEL=sentinel,
                                   PIDS=os.path.join(self.dir, "outer-pids")))
        out = finish(p, timeout=300)
        self.assertEqual(p.returncode, 0, out)
        signals = open(sentinel).read() if os.path.exists(sentinel) else ""
        self.assertEqual(signals, "", "a test signalled another stage of its gate:\n" + out)

    def test_a_term_arriving_as_a_fixture_is_reaped_waits_until_the_reap_is_recorded(self):
        # PG5-01: a cancellation between the kernel's reap and its record
        # must not see the fixture as unreaped (its pid may already be
        # another process's). TERM is raised right after the real waitpid;
        # the handler must find the reap recorded.
        fixture = start_fixture(["true"])
        while not exited(fixture):
            time.sleep(0.01)
        seen = []
        real_waitpid = os.waitpid

        def waitpid_then_term(pid, options):
            result = real_waitpid(pid, options)
            signal.raise_signal(signal.SIGTERM)
            return result

        previous = signal.signal(signal.SIGTERM, lambda *_: seen.append(fixture.returncode))
        os.waitpid = waitpid_then_term
        try:
            reap(fixture)
        finally:
            os.waitpid = real_waitpid
            signal.signal(signal.SIGTERM, previous)
        self.assertEqual(seen, [0], "the handler ran before the reap was recorded")

    def test_this_file_starts_reaps_and_signals_processes_only_through_its_helpers(self):
        starters = {("subprocess", n) for n in ("Popen", "run", "call", "check_call", "check_output", "getoutput",
                                                 "getstatusoutput")}
        starters |= {("os", n) for n in ("system", "popen", "fork", "forkpty", "posix_spawn", "posix_spawnp",
                                         "spawnl", "spawnle", "spawnlp", "spawnlpe", "spawnv", "spawnve", "spawnvp",
                                         "spawnvpe", "execv", "execve", "execl", "execle", "execlp", "execlpe",
                                         "execvp", "execvpe")}
        signallers = {("os", "kill"), ("os", "killpg")}
        methods = {"send_signal", "terminate", "kill", "poll", "wait", "communicate"}
        reapers = {("os", n) for n in ("wait", "waitpid", "wait3", "wait4")}
        with open(os.path.abspath(__file__)) as f:
            tree = ast.parse(f.read())
        offenders = []

        def visit(node, inside):
            for child in ast.iter_child_nodes(node):
                name = child.name if isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef)) else inside
                if isinstance(child, ast.Call) and isinstance(child.func, ast.Attribute):
                    attr = child.func.attr
                    base = child.func.value.id if isinstance(child.func.value, ast.Name) else None
                    if (base, attr) in starters and inside != "start_fixture":
                        offenders.append("line %d starts a process" % child.lineno)
                    if (base, attr) in reapers and inside != "reap":
                        offenders.append("line %d reaps a process" % child.lineno)
                    # waitid reaps unless told WNOWAIT; only exited() may call it, with WNOWAIT.
                    if (base, attr) == ("os", "waitid") and (inside != "exited" or "WNOWAIT" not in ast.unparse(child)):
                        offenders.append("line %d may reap a process" % child.lineno)
                    sends = (base, attr) in signallers or (base not in ("os", "signal", "time") and attr in methods)
                    # alive() sends signal 0, which only looks.
                    looks = (base, attr) == ("os", "kill") and inside == "alive"
                    if sends and inside != "signal_fixture" and not looks:
                        offenders.append("line %d sends a signal" % child.lineno)
                if isinstance(child, ast.ImportFrom) and child.module in ("subprocess", "os"):
                    offenders.append("line %d: from %s import ..." % (child.lineno, child.module))
                visit(child, name)

        visit(tree, "")
        self.assertEqual(offenders, [])


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGINT, on_signal)
    try:
        unittest.main()
    finally:
        stop_fixtures()
        shutil.rmtree(OUTPUT, ignore_errors=True)
        if os.environ.get("SENTINEL"):
            open(os.environ["SENTINEL"] + ".done", "w").close()
