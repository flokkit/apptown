"""deploy/apptown-start.sh, the apptown image's entrypoint, run as a plain
process with stand-ins for dind, docker, curl and apptownd on its PATH: apptownd's
exit status is the container's, even when apptownd ends before it answers; a
stop during Docker's start, or after the ready line, ends apptownd and then
Docker and exits cleanly; the ready line carries the setup token in the
trial only; the server branch execs apptownd with none of the trial's
defaults; a container that cannot run Docker is told so; the one-server
branch checks it was started on the host's network with its data folder at
the same path, and execs apptownd on loopback with its own defaults."""
import os
import shutil
import signal
import stat
import subprocess
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "deploy", "apptown-start.sh")
# A hang guard only; each case takes about a second alone. Stand-ins that
# loop end when the test removes its folder, so a failed case leaves none.
HANG_GUARD = 60

STUBS = {
    "dind": r"""echo "dind $*" >> "$CALLS"
trap 'echo dind-term >> "$CALLS"; exit 0' TERM
while [ -d "$STUB_DIR" ]; do sleep 0.1; done
""",
    # The host's Docker, as the one-server branch asks it: this container's
    # network mode and data mount, and the default bridge's gateway.
    "docker": r"""case "$1" in
inspect) case "$3" in *NetworkMode*) echo "${STUB_NETWORK:-host}" ;; *Mounts*) echo "${STUB_MOUNT:-}" ;; esac ;;
network) echo "172.17.0.1 fd00::1 " ;;
*) [ -e "$STUB_DIR/docker-ready" ] ;;
esac""",
    # The health check: records how it was asked (last call wins).
    "curl": r"""echo "$*" > "$STUB_DIR/curl-args"; [ -e "$STUB_DIR/apptownd-ready" ]""",
    "apptownd": r"""echo "apptownd data=$APPTOWN_DATA auth=${APPTOWN_AUTH:-} tier=${APPTOWN_TIER_DOMAIN:-} tls=${APPTOWN_LOCAL_TLS:-} public=${APPTOWN_PUBLIC_HOST:-}" >> "$CALLS"
echo "settings addr=${APPTOWN_ADDR:-} console=${APPTOWN_CONSOLE_ADDR:-} cli=${APPTOWN_CONSOLE_CLI:-} tmp=${TMPDIR:-} agent=${APPTOWN_AGENT_IMAGE:-}" >> "$CALLS"
case "$FORGED_MODE" in
exit37) exit 37 ;;
serve)
	mkdir -p "$APPTOWN_DATA/local-tls" && echo 0123abcd > "$APPTOWN_DATA/setup-token"
	[ "${APPTOWN_LOCAL_TLS:-}" = true ] && echo cert > "$APPTOWN_DATA/local-tls/ca.pem"
	trap 'echo apptownd-term >> "$CALLS"; exit 0' TERM
	touch "$STUB_DIR/apptownd-ready"
	while [ -d "$STUB_DIR" ]; do sleep 0.1; done ;;
esac
""",
}


class TrialEntrypoint(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.stubs = os.path.join(self.tmp, "bin")
        os.makedirs(self.stubs)
        for name, body in STUBS.items():
            path = os.path.join(self.stubs, name)
            with open(path, "w") as f:
                f.write("#!/bin/sh\n" + body)
            os.chmod(path, os.stat(path).st_mode | stat.S_IEXEC)
        self.calls = os.path.join(self.tmp, "calls")
        open(self.calls, "w").close()
        self.cgroup = os.path.join(self.tmp, "cgroup")
        os.makedirs(self.cgroup)

    def start(self, **env):
        e = {k: v for k, v in os.environ.items() if not k.startswith("FORGE_")}
        e.update(PATH=self.stubs + os.pathsep + os.environ["PATH"], CALLS=self.calls, STUB_DIR=self.tmp,
                 APPTOWN_ROOT=os.path.join(self.tmp, "root"), APPTOWN_CGROUP=self.cgroup, FORGED_MODE="serve")
        e.update(env)
        return subprocess.Popen(["sh", SCRIPT], env=e, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)

    def called(self):
        with open(self.calls) as f:
            return f.read().split("\n")

    def wait_for(self, what):
        deadline = time.monotonic() + HANG_GUARD
        while not any(line.startswith(what) for line in self.called()):
            self.assertLess(time.monotonic(), deadline, "never saw " + what)
            time.sleep(0.05)

    def docker_ready(self):
        open(os.path.join(self.tmp, "docker-ready"), "w").close()

    def test_forged_ending_before_it_answers_fails_the_container(self):
        self.docker_ready()
        p = self.start(FORGED_MODE="exit37")
        out, _ = p.communicate(timeout=HANG_GUARD)
        self.assertEqual(p.returncode, 37, out)
        self.assertIn("dind-term", self.called())
        self.assertNotIn("is ready", out)

    def test_a_stop_while_docker_starts_ends_cleanly(self):
        p = self.start()
        self.wait_for("dind ")
        p.send_signal(signal.SIGTERM)
        out, _ = p.communicate(timeout=HANG_GUARD)
        self.assertEqual(p.returncode, 0, out)
        self.assertIn("dind-term", self.called())
        self.assertFalse(any(line.startswith("apptownd") for line in self.called()), self.called())

    def test_a_stop_after_the_ready_line_ends_forged_then_docker(self):
        self.docker_ready()
        p = self.start()
        line = ""
        while "is ready" not in line:
            line = p.stdout.readline()
            self.assertTrue(line, "the script ended before the ready line")
        self.assertIn("Open https://localhost:7777/?setup=0123abcd to create the administrator.", line)
        p.send_signal(signal.SIGINT)
        out, _ = p.communicate(timeout=HANG_GUARD)
        self.assertEqual(p.returncode, 0, out)
        calls = self.called()
        self.assertLess(calls.index("apptownd-term"), calls.index("dind-term"), calls)
        apptownd = [c for c in calls if c.startswith("apptownd data=")][0]
        self.assertIn("auth=internal", apptownd)
        # HTTPS from AppTown's own authority: the trial's addresses, the
        # flag that serves them and a health check that trusts that authority.
        self.assertIn("tier=preview=https://preview.localhost:7777,main=https://main.localhost:7777", apptownd)
        self.assertIn("tls=true public=https://localhost:7777", apptownd)
        with open(os.path.join(self.tmp, "curl-args")) as f:
            self.assertRegex(f.read(), r"--cacert \S+/local-tls/ca\.pem https://localhost:7777/healthz")

    def test_the_server_runs_forged_alone_with_its_own_data_directory(self):
        p = self.start(APPTOWN_NO_LOCAL_DOCKER="true", FORGED_MODE="exit37")
        out, _ = p.communicate(timeout=HANG_GUARD)
        self.assertEqual(p.returncode, 37, out)
        self.assertEqual([c for c in self.called() if c and not c.startswith("settings")], ["apptownd data=/var/lib/apptown auth= tier= tls= public="])
        self.assertNotIn("setup=", out)

    def test_a_container_that_cannot_run_docker_is_told_so(self):
        # A missing tree is not writable, as a read-only one is not, even for root.
        p = self.start(APPTOWN_CGROUP=os.path.join(self.tmp, "no-cgroup"))
        out, _ = p.communicate(timeout=HANG_GUARD)
        self.assertEqual(p.returncode, 1, out)
        self.assertIn("--privileged", out)
        self.assertEqual([c for c in self.called() if c], [])


    def one_server(self, **env):
        data = os.path.join(self.tmp, "data")
        cli = os.path.join(self.tmp, "apptown-linux")
        with open(cli, "w") as f:
            f.write("the CLI")
        mountinfo = os.path.join(self.tmp, "mountinfo")
        with open(mountinfo, "w") as f:
            f.write("1 2 0:1 / / rw - overlay overlay rw\n")
            f.write("3 2 8:1 /var/lib/docker/containers/" + "ab" * 32 + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw\n")
        self.docker_ready()
        base = dict(APPTOWN_ONE_SERVER="true", TMPDIR="", APPTOWN_DATA=data, APPTOWN_CLI_SOURCE=cli, APPTOWN_MOUNTINFO=mountinfo,
                    STUB_MOUNT="bind " + data, FORGED_MODE="exit37")
        base.update(env)
        p = self.start(**base)
        out, _ = p.communicate(timeout=HANG_GUARD)
        return p.returncode, out, data

    def test_one_server_runs_apptownd_on_loopback_with_its_own_defaults(self):
        code, out, data = self.one_server()
        self.assertEqual(code, 37, out)
        calls = [c for c in self.called() if c]
        self.assertEqual(calls[0], "apptownd data=" + data + " auth=internal tier= tls= public=", calls)
        self.assertEqual(calls[1], "settings addr=127.0.0.1:7777 console=172.17.0.1:7778 cli=" + data + "/bin/apptown-linux tmp=" + data + "/tmp agent=apptown-agent", calls)
        with open(os.path.join(data, "bin", "apptown-linux")) as f:
            self.assertEqual(f.read(), "the CLI")
        self.assertTrue(os.path.isdir(os.path.join(data, "tmp")))
        self.assertFalse(any(c.startswith("dind") for c in calls), calls)
        self.assertNotIn("setup=", out)

    def test_one_server_refuses_a_container_off_the_hosts_network(self):
        code, out, _ = self.one_server(STUB_NETWORK="bridge")
        self.assertEqual(code, 1, out)
        self.assertIn("network_mode: host", out)
        self.assertEqual([c for c in self.called() if c], [])

    def test_one_server_refuses_data_at_another_path_on_the_host(self):
        code, out, data = self.one_server(STUB_MOUNT="bind /srv/elsewhere")
        self.assertEqual(code, 1, out)
        self.assertIn("same path inside and out", out)
        self.assertEqual([c for c in self.called() if c], [])
        code, out, _ = self.one_server(STUB_MOUNT="volume /var/lib/docker/volumes/apptown/_data")
        self.assertEqual(code, 1, out)


if __name__ == "__main__":
    unittest.main()
