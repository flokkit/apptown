// Package workertest starts a forge-worker in the test process, over real
// mutual TLS, for tests of the forge's worker path. Its certificates are made
// per test.
package workertest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/diskspace"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker"
)

// Installation is the installation a test worker serves.
const Installation = "inst-1"

// CA issues the certificates one test's worker and clients present.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func NewCA(t *testing.T) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test forge CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{cert: cert, key: key, pool: pool}
}

func (ca *CA) Issue(t *testing.T, name string, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Start serves a worker for Installation over mutual TLS, keeping its
// records under state and running its tasks on tasks.
func Start(t *testing.T, ca *CA, state string, tasks execution.TaskDriver) *httptest.Server {
	t.Helper()
	return StartWith(t, ca, state, tasks, nil)
}

// StartWith is Start with the worker's handler wrapped, for a test that
// makes the worker misbehave on the wire.
func StartWith(t *testing.T, ca *CA, state string, tasks execution.TaskDriver, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	return serve(t, ca, &worker.Server{Installation: Installation, State: state, Tasks: tasks}, wrap)
}

// StartServer serves srv over mutual TLS, as the worker for Installation.
func StartServer(t *testing.T, ca *CA, srv *worker.Server) *httptest.Server {
	t.Helper()
	return StartServerWith(t, ca, srv, nil)
}

// StartServerWith is StartServer with the worker's handler wrapped.
func StartServerWith(t *testing.T, ca *CA, srv *worker.Server, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	srv.Installation = Installation
	return serve(t, ca, srv, wrap)
}

// Disk is the state folder a test worker reads unless its test gives its
// own (worker.Server.StateDisk): a roomy, fixed reading, well above the
// floor. A test's worker never reads the disk of the machine the tests run
// on, so whether its work is admitted never depends on how full that disk
// is (soak finding 54). A test of the floor gives its own reading.
func Disk(string) (diskspace.Space, error) {
	return diskspace.Space{TotalBytes: 100 << 30, FreeBytes: 50 << 30}, nil
}

func serve(t *testing.T, ca *CA, w *worker.Server, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	if w.StateDisk == nil {
		w.StateDisk = Disk
	}
	handler := w.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = worker.TLSConfig(ca.Issue(t, "worker", x509.ExtKeyUsageServerAuth), ca.pool)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// Client presents a certificate naming name, or none when name is empty.
func Client(t *testing.T, ca *CA, name string) *http.Client {
	t.Helper()
	cfg := &tls.Config{RootCAs: ca.pool, MinVersion: tls.VersionTLS13}
	if name != "" {
		cfg.Certificates = []tls.Certificate{ca.Issue(t, name, x509.ExtKeyUsageClientAuth)}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: time.Minute}
}

// Driver is the forge's driver for a worker Start served.
func Driver(t *testing.T, ca *CA, srv *httptest.Server) execution.WorkerTasks {
	t.Helper()
	return execution.WorkerTasks{URL: srv.URL, Client: Client(t, ca, Installation)}
}

// ServerFiles writes the CA certificate and a worker certificate and key
// for 127.0.0.1 and localhost as PEM files in dir, for a worker process to
// be given. It returns their paths.
func (ca *CA) ServerFiles(t *testing.T, dir string) (caFile, certFile, keyFile string) {
	t.Helper()
	cert := ca.Issue(t, "worker", x509.ExtKeyUsageServerAuth)
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	caFile, certFile, keyFile = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "worker.pem"), filepath.Join(dir, "worker-key.pem")
	for path, block := range map[string]*pem.Block{caFile: {Type: "CERTIFICATE", Bytes: ca.cert.Raw}, certFile: {Type: "CERTIFICATE", Bytes: cert.Certificate[0]},
		keyFile: {Type: "PRIVATE KEY", Bytes: key}} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return caFile, certFile, keyFile
}

// Container is forge-worker running in its own Docker-in-Docker container:
// its own Docker daemon, nothing mounted from this machine. Its binary,
// certificates and task image are copied in.
type Container struct {
	URL       string
	t         *testing.T
	container string
	// host: the worker runs on this machine, driving its Docker directly
	// (StartOnHost); process is it, and start runs args.
	host    bool
	process *os.Process
	exited  chan struct{} // closed once process has exited
	binary  string
	args    []string
	log     string
}

func runDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := dockertest.CombinedOutput(args...)
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// buildWorker builds forge-worker at binary, with env added to the build's.
func buildWorker(t *testing.T, binary string, env ...string) {
	t.Helper()
	run := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{
		Argv: []string{"go", "build", "-o", binary, "flokkit.io/forge/cmd/forge-worker"}, Env: env, Timeout: 10 * time.Minute})
	if run.RunErr != nil {
		t.Fatalf("building forge-worker: %v\n%s", run.RunErr, run.Output)
	}
}

// StartInContainer builds forge-worker for Linux and runs it in a new
// docker:27-dind container, with busybox:latest loaded from this machine's
// Docker. The container is removed when the test ends. It needs a local
// Docker that can run privileged containers.
func StartInContainer(t *testing.T, ca *CA) *Container {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "forge-worker")
	buildWorker(t, binary, "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	caFile, certFile, keyFile := ca.ServerFiles(t, dir)
	name := fmt.Sprintf("forge-worker-it-%d", time.Now().UnixNano())
	// ZFS_MODULE_TIMEOUT=0, as for the product's private daemons: on a ZFS
	// host containerd's zfs probe otherwise outlasts dockerd's wait for it.
	runDocker(t, "run", "-d", "--privileged", "--name", name, "-e", "ZFS_MODULE_TIMEOUT=0", "-p", "127.0.0.1::8443", "docker:27-dind")
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", "-v", name) })
	if !poll.Until(context.Background(), 90*time.Second, time.Second, func() bool { return dockertest.Run("exec", name, "docker", "info") == nil }) {
		t.Fatal("the worker's Docker daemon never started")
	}
	w := &Container{t: t, container: name}
	runDocker(t, "exec", name, "mkdir", "-p", "/certs")
	for in, here := range map[string]string{"/forge-worker": binary, "/certs/ca.pem": caFile, "/certs/worker.pem": certFile, "/certs/worker-key.pem": keyFile} {
		runDocker(t, "cp", here, name+":"+in)
	}
	// The task image comes from this machine's Docker, not a registry.
	w.Load("busybox:latest")
	port := runDocker(t, "port", name, "8443/tcp")
	w.URL = "https://" + port[strings.LastIndex(port, "\n")+1:]
	w.start()
	return w
}

// StartOnHost builds forge-worker and runs it as a process on this
// machine, over a new state directory, driving this machine's Docker
// directly: a chat's containers are one level down, what its private daemon
// runs two, the supported topology. args are added to its flags. The
// process is stopped by its pid when the test ends; what the worker made on
// Docker is the test's to remove.
func StartOnHost(t *testing.T, ca *CA, args ...string) *Container {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "forge-worker")
	buildWorker(t, binary)
	caFile, certFile, keyFile := ca.ServerFiles(t, dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	w := &Container{t: t, host: true, binary: binary, URL: "https://" + addr, log: filepath.Join(dir, "worker.log"),
		args: append([]string{"-installation", Installation, "-state", filepath.Join(dir, "state"), "-listen", addr,
			"-cert", certFile, "-key", keyFile, "-client-ca", caFile, "-agent-context", ""}, args...)}
	t.Cleanup(w.stopProcess)
	w.start()
	return w
}

// stopProcess kills the worker process this fixture started, by its pid,
// and waits for it.
func (w *Container) stopProcess() {
	if w.process != nil {
		_ = w.process.Kill()
		<-w.exited
		w.process = nil
	}
}

// start runs forge-worker, recording its pid, and fails the test if it
// does not come to listen.
func (w *Container) start() {
	w.t.Helper()
	if err := w.launch(); err != nil {
		w.t.Fatal(err)
	}
}

// listenGuard bounds a worker's start only against a hang. The start ends
// when the worker listens or exits; how long a start takes depends on the
// machine's load (a worker in its own Docker-in-Docker container beside
// other packages' tests took over 30 s), so it is not a pass condition.
const listenGuard = 5 * time.Minute

// launch starts forge-worker and waits until it listens, or until it has
// exited, whose error carries the worker's output.
func (w *Container) launch() error {
	if w.host {
		logFile, err := os.OpenFile(w.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		cmd := exec.Command(w.binary, w.args...)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			logFile.Close()
			return err
		}
		logFile.Close()
		w.process = cmd.Process
		exited := make(chan struct{})
		w.exited = exited
		go func() { _ = cmd.Wait(); close(exited) }()
		return w.awaitListening(func() bool {
			select {
			case <-exited:
				return true
			default:
				return false
			}
		}, func() string { body, _ := os.ReadFile(w.log); return string(body) })
	}
	// The pid file names the worker of this start only: a restart's old
	// pid would read as this worker having exited.
	if out, err := dockertest.CombinedOutput("exec", w.container, "rm", "-f", "/worker.pid"); err != nil {
		return fmt.Errorf("clearing the worker's pid file: %v\n%s", err, out)
	}
	if out, err := dockertest.CombinedOutput("exec", "-d", w.container, "sh", "-c",
		"echo $$ > /worker.pid; exec /forge-worker -installation "+Installation+" -state /var/lib/forge-worker -cert /certs/worker.pem -key /certs/worker-key.pem -client-ca /certs/ca.pem > /worker.log 2>&1"); err != nil {
		return fmt.Errorf("starting forge-worker: %v\n%s", err, out)
	}
	// Exited: the container is gone, or the recorded pid is not running.
	// No pid file yet is a worker still starting.
	return w.awaitListening(func() bool {
		return dockertest.Run("exec", w.container, "sh", "-c", `[ ! -f /worker.pid ] || kill -0 "$(cat /worker.pid)"`) != nil
	}, func() string {
		out, err := dockertest.CombinedOutput("exec", w.container, "cat", "/worker.log")
		if err != nil {
			return fmt.Sprintf("(reading /worker.log: %v) %s", err, out)
		}
		return string(out)
	})
}

// awaitListening waits until something answers on the worker's port (TLS
// answers plain HTTP with an error), or exited reports the worker gone, or
// listenGuard passes.
func (w *Container) awaitListening(exited func() bool, log func() string) error {
	probe := &http.Client{Timeout: 5 * time.Second}
	var probeErr error
	listening, gone := false, false
	poll.Until(context.Background(), listenGuard, 250*time.Millisecond, func() bool {
		resp, err := probe.Get(strings.Replace(w.URL, "https://", "http://", 1))
		if err == nil {
			resp.Body.Close()
			listening = true
			return true
		}
		probeErr = err
		gone = exited()
		return gone
	})
	switch {
	case listening:
		return nil
	case gone:
		return fmt.Errorf("forge-worker exited before it listened (%v); its output:\n%s", probeErr, log())
	default:
		return fmt.Errorf("forge-worker did not listen within %s: %v; its output:\n%s", listenGuard, probeErr, log())
	}
}

// Load copies an image from this machine's Docker into the worker's; a
// worker on this machine has it already.
func (w *Container) Load(image string) {
	w.t.Helper()
	if w.host {
		return
	}
	// docker save streams into docker load; a load that ends early closes
	// the pipe, so the save cannot block on it.
	ctx := context.Background()
	pipe, into := io.Pipe()
	saved := make(chan error, 1)
	go func() {
		stderr, err := (docker.CLI{}).Run(ctx, "", docker.Command{Args: []string{"save", image}, Stdout: into})
		if err != nil {
			err = fmt.Errorf("%w: %s", err, stderr)
		}
		_ = into.CloseWithError(err)
		saved <- err
	}()
	out, err := (docker.CLI{}).Run(ctx, "", docker.Command{Args: []string{"exec", "-i", w.container, "docker", "load"}, Stdin: pipe})
	_ = pipe.Close()
	if err != nil {
		w.t.Fatalf("loading %s into the worker's daemon: %v\n%s", image, err, out)
	}
	if err := <-saved; err != nil {
		w.t.Fatal(err)
	}
}

// Docker runs a docker command against the worker's own daemon.
func (w *Container) Docker(args ...string) string {
	w.t.Helper()
	if w.host {
		return runDocker(w.t, args...)
	}
	return runDocker(w.t, append([]string{"exec", w.container, "docker"}, args...)...)
}

// Restart kills the worker process by its recorded pid, waits until that
// process is gone, and starts a new one.
func (w *Container) Restart() {
	w.t.Helper()
	if w.host {
		old := w.process.Pid
		w.stopProcess()
		w.start()
		if w.process.Pid == old {
			w.t.Fatalf("the worker was not restarted: still pid %d", old)
		}
		return
	}
	old := runDocker(w.t, "exec", w.container, "cat", "/worker.pid")
	runDocker(w.t, "exec", w.container, "kill", old)
	if !poll.Until(context.Background(), 30*time.Second, 100*time.Millisecond, func() bool { return dockertest.Run("exec", w.container, "kill", "-0", old) != nil }) {
		w.t.Fatalf("worker process %s did not exit", old)
	}
	w.start()
	if now := runDocker(w.t, "exec", w.container, "cat", "/worker.pid"); now == old {
		w.t.Fatalf("the worker was not restarted: still pid %s", now)
	}
}

// External is a forge-worker this test did not start: a real process on
// another machine, for the qualification of a worker profile
// (design/operations/workers.md, "Private Docker on the worker"). The test is the forge's side, on its own machine; the two share
// no files. It is named by the environment:
//   - FORGE_QUAL_WORKER_URL: the worker's address;
//   - FORGE_QUAL_WORKER_CA: the CA that signs the worker's certificate;
//   - FORGE_QUAL_WORKER_CERT, FORGE_QUAL_WORKER_KEY: the installation's
//     client certificate (Common Name Installation) and key;
//   - FORGE_QUAL_WORKER_STATE: the worker's -state, on its machine (it names
//     what the worker labels);
//   - FORGE_QUAL_WORKER_CTL: a command run with sh -c and a verb appended,
//     that acts on the worker's machine (restart: kill the worker by its
//     recorded pid and start it again over the same state; fresh: start
//     the worker, and a check worker, again on empty state, as a new
//     forge's workers);
//   - FORGE_QUAL_CHECK_WORKER_URL, optionally: a check worker there, with
//     the same certificates.
//
// DOCKER_HOST must name the worker's Docker, so that what the test
// observes is that machine's.
type External struct {
	URL, CheckURL, CA, Cert, Key, State, Control string
}

// ExternalWorker is the worker the environment names; the test is skipped
// when it names none.
func ExternalWorker(t *testing.T) *External {
	t.Helper()
	e := LookupExternal(t)
	if e == nil {
		t.Skip("FORGE_QUAL_WORKER_URL names no external worker")
	}
	return e
}

// LookupExternal is the worker the environment names, or nil.
func LookupExternal(t *testing.T) *External {
	t.Helper()
	e := &External{URL: os.Getenv("FORGE_QUAL_WORKER_URL"), CheckURL: os.Getenv("FORGE_QUAL_CHECK_WORKER_URL"), CA: os.Getenv("FORGE_QUAL_WORKER_CA"),
		Cert: os.Getenv("FORGE_QUAL_WORKER_CERT"), Key: os.Getenv("FORGE_QUAL_WORKER_KEY"), State: os.Getenv("FORGE_QUAL_WORKER_STATE"), Control: os.Getenv("FORGE_QUAL_WORKER_CTL")}
	if e.URL == "" {
		return nil
	}
	if e.CA == "" || e.Cert == "" || e.Key == "" || e.State == "" || e.Control == "" {
		t.Fatal("an external worker needs FORGE_QUAL_WORKER_CA, _CERT, _KEY, _STATE and _CTL")
	}
	return e
}

// Driver is the forge's driver for the external worker.
func (e *External) Driver(t *testing.T) execution.WorkerTasks {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(e.Cert, e.Key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(e.CA)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatalf("%s holds no certificate", e.CA)
	}
	cfg := &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	return execution.WorkerTasks{URL: e.URL, Client: &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: time.Minute}}
}

// OwnerLabel is the label the external worker puts on what it makes.
func (e *External) OwnerLabel() string {
	return (&worker.Server{Installation: Installation, State: e.State}).CheckOwnerLabel()
}

// Ctl runs the worker machine's control command with verb, and answers
// its output. A verb gets ten minutes; one that takes longer fails the test.
func (e *External) Ctl(t *testing.T, verb string) string {
	t.Helper()
	run := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{Argv: []string{"sh", "-c", e.Control + " " + verb}, Timeout: 10 * time.Minute})
	if run.RunErr != nil {
		t.Fatalf("%s %s: %v\n%s", e.Control, verb, run.RunErr, run.Output)
	}
	return strings.TrimSpace(string(run.Output))
}
