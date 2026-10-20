#!/usr/bin/env python3
"""Runs make targets concurrently as the stages of `make check`.

    MAKE=make python3 scripts/gate.py check-test check-vet ...

Each stage's make runs with its own log. As each ends the gate prints one
line (name, exit, seconds); at the end a summary, and the log tail of every
stage that failed. It exits 0 only if every stage exited 0.

Logs: a new directory for every run, inside GATE_LOG_DIR when it is set
(default: the temporary directory). A stage's result is its make's exit
status as this run reaped it; a make ended by a signal has failed.

Stopping. Each stage's make is a direct child of the gate, in the gate's
process group, so whoever started the gate in a group of its own
(make-check-queued.sh, a terminal) reaches every stage, KILL included. On
INT or TERM the gate sends TERM to each make it has not yet reaped and
waits for them all, then exits 130 or 143. A child's pid belongs to no other
process until its parent reaps it, and this gate reaps a make only after it
can no longer signal it, so it never signals anything but its own stages.
It starts each make with posix_spawn and is the only one to reap it (no
Popen, whose own polling could reap a make behind the gate's back).
Make passes TERM on to the command it is running; whatever that command
leaves running is reached by the caller's signal to the group (the queue's
TERM and KILL, a terminal's INT), not by the gate.
(It is Python, not sh, because a shell reaps its children on its own.)

Demotion. On macOS a gate whose own process runs in the kernel's throttled
band is refused before any stage starts (exit 3), and one demoted part-way
and still is at the end says so in its summary (priority, throttled).
"""
import ctypes
import os
import signal
import struct
import sys
import tempfile
import time

STOPPING = {signal.SIGINT: 130, signal.SIGTERM: 143}
stopping = 0


def on_signal(signum, _frame):
    global stopping
    stopping = stopping or STOPPING[signum]


# XNU clamps a throttled ("darwin background") thread to this priority
# (MAXPRI_THROTTLE, osfmk/kern/sched.h). macOS Game Mode puts every app but
# the game there while a game is the frontmost full-screen app, and on Apple
# Silicon such threads run on the efficiency cores only: on 18 October 2026
# a 16-core Mac then ran a one-thread loop 68 times slower, and every gate
# failed on go test's 10-minute timeouts, which read like broken code.
MAXPRI_THROTTLE = 4
REFUSED = 3


def priority():
    """This process's scheduling priority as the kernel reports it (what
    `ps -o pri` shows), or None where it cannot be read: not macOS, or the
    call failed. Read in-process, through libproc, so the gate starts no
    process it would then have to reap."""
    if sys.platform != "darwin":
        return None
    try:
        libproc = ctypes.CDLL("/usr/lib/libproc.dylib")
        libproc.proc_pidinfo.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_uint64, ctypes.c_void_p, ctypes.c_int]
        # struct proc_taskinfo: six uint64s, then int32s ending in pti_priority.
        buf = ctypes.create_string_buffer(96)
        n = libproc.proc_pidinfo(os.getpid(), 4, 0, buf, 96)  # PROC_PIDTASKINFO
    except OSError:
        return None
    if n != 96:
        return None
    return struct.unpack_from("i", buf, 92)[0]


def throttled(pri):
    return pri is not None and pri <= MAXPRI_THROTTLE


REFUSAL = """gate: refused: this gate runs in macOS's throttled band (priority %d): it gets
only the CPU the rest of the machine leaves, so its timeouts would fail as if
the code were broken. macOS Game Mode puts every other app there while a game
is the frontmost full-screen app; if one is, leave it or take it out of full
screen, then run the gate again."""


def refuse_if_throttled():
    """Whether this process is in the throttled band, saying so if it is."""
    pri = priority()
    if throttled(pri):
        print(REFUSAL % pri, file=sys.stderr, flush=True)
        return True
    if pri is None and sys.platform == "darwin":
        print("gate: could not read this gate's scheduling priority; running without the Game Mode check",
              file=sys.stderr, flush=True)
    return False


class Stage:
    def __init__(self, name, pid, begun):
        self.name, self.pid, self.begun, self.reaped = name, pid, begun, False

    def exited(self):
        """Whether its make has exited, without reaping it."""
        return os.waitid(os.P_PID, self.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is not None

    def reap(self):
        """Reaps the exited make: its exit status, 128+n for a signal."""
        _, status = os.waitpid(self.pid, 0)
        self.reaped = True
        code = os.waitstatus_to_exitcode(status)
        return 128 - code if code < 0 else code


def main(stages):
    if not stages:
        print("gate.py: no stages", file=sys.stderr)
        return 2
    if refuse_if_throttled():
        return REFUSED
    make = os.environ.get("MAKE", "make")
    base = os.environ.get("GATE_LOG_DIR") or tempfile.gettempdir()
    os.makedirs(base, exist_ok=True)
    logs = tempfile.mkdtemp(prefix="apptown-gate.", dir=base)
    for sig in STOPPING:
        signal.signal(sig, on_signal)

    started = time.time()
    running = []
    for stage in stages:
        log = os.path.join(logs, stage + ".log")
        pid = os.posix_spawnp(make, [make, "--no-print-directory", stage], os.environ, file_actions=[
            (os.POSIX_SPAWN_OPEN, 0, os.devnull, os.O_RDONLY, 0),
            (os.POSIX_SPAWN_OPEN, 1, log, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644),
            (os.POSIX_SPAWN_DUP2, 1, 2)])
        running.append(Stage(stage, pid, time.time()))
    print("gate: %d stages running; logs in %s" % (len(stages), logs), flush=True)

    results = {}
    forwarded = False
    while running:
        if stopping and not forwarded:
            print("gate: stopping every stage; logs in %s" % logs, file=sys.stderr, flush=True)
            for stage in running:
                if not stage.reaped:
                    os.kill(stage.pid, signal.SIGTERM)
            forwarded = True
        still = []
        for stage in running:
            if stage.exited():
                code = stage.reap()
                secs = int(time.time() - stage.begun)
                results[stage.name] = (code, secs)
                print("%-20s exit %-3s %4ss" % (stage.name, code, secs), flush=True)
            else:
                still.append(stage)
        running = still
        if running:
            time.sleep(0.5)

    if stopping:
        print("gate stopped by a signal; every stage's make has ended; logs in %s" % logs, file=sys.stderr)
        return stopping
    print("\n== gate summary (%ds) ==" % (time.time() - started))
    failed = []
    for stage in stages:
        code, secs = results[stage]
        print("%-20s exit %-4s %5ss" % (stage, code, secs))
        if code != 0:
            failed.append(stage)
    # Game Mode may have come on after the gate started. A game started and
    # left again within one run goes unnoticed; one still frontmost does not.
    pri = priority()
    if throttled(pri):
        print("\ngate: this gate now runs in macOS's throttled band (priority %d; Game Mode does this); "
              "timeouts in this run may be that rather than the code." % pri)
    for stage in failed:
        with open(os.path.join(logs, stage + ".log"), errors="replace") as f:
            lines = f.readlines()
        found = go_failures(lines)
        if found:
            print("\n== %s failed; its failing Go tests ==" % stage)
            print("".join(found), end="")
        print("\n== %s failed; last 60 lines of %s/%s.log ==" % (stage, logs, stage))
        print("".join(lines[-60:]), end="")
    if failed:
        print("\ngate FAILED: %s (logs in %s)" % (" ".join(failed), logs))
        return 1
    print("gate passed (logs in %s)" % logs)
    return 0


def go_failures(lines, follow=40):
    """The Go test failures in a stage's log: each "--- FAIL" line with the
    indented output under it, each panic with the lines after it, and each
    failing package's FAIL line. A stage's tail alone misses them when
    packages that passed are listed after the one that failed, and CI keeps
    no stage logs to read afterwards."""
    found = []
    i = 0
    while i < len(lines):
        line = lines[i]
        if line.lstrip().startswith("--- FAIL"):
            found.append(line)
            i += 1
            while i < len(lines) and lines[i].startswith((" ", "\t")) and not lines[i].lstrip().startswith("--- "):
                if len(found) < 2000:
                    found.append(lines[i])
                i += 1
            continue
        if line.startswith("panic:"):
            found.extend(lines[i:i + follow])
            i += follow
            continue
        if line.startswith("FAIL\t"):
            found.append(line)
        i += 1
    return found


if __name__ == "__main__":
    # --refuse-if-throttled: only the check, for make check to run before its
    # web build, so a demoted gate is refused at once rather than minutes in.
    if sys.argv[1:] == ["--refuse-if-throttled"]:
        sys.exit(REFUSED if refuse_if_throttled() else 0)
    sys.exit(main(sys.argv[1:]))
