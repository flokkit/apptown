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
"""
import os
import signal
import sys
import tempfile
import time

STOPPING = {signal.SIGINT: 130, signal.SIGTERM: 143}
stopping = 0


def on_signal(signum, _frame):
    global stopping
    stopping = stopping or STOPPING[signum]


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
    make = os.environ.get("MAKE", "make")
    base = os.environ.get("GATE_LOG_DIR") or tempfile.gettempdir()
    os.makedirs(base, exist_ok=True)
    logs = tempfile.mkdtemp(prefix="forge-gate.", dir=base)
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
    sys.exit(main(sys.argv[1:]))
