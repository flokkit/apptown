package scm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/gitexec"
)

// A project can start from existing code: its hub begins with the default
// branch and tags of a Git bundle or an https repository, and everything
// else about the project starts fresh
// (design/operations/start-from-existing-code.md).

// ErrRepositoryEstablished refuses to seed a hub that already holds history:
// starting from existing code never replaces a project's repository.
var ErrRepositoryEstablished = fault.New(fault.Conflict, "the project already has a repository; only a new, empty project can start from existing code")

// SourceRefused is a source AppTown will not fetch from, with why.
type SourceRefused struct{ Why string }

func (e *SourceRefused) Error() string         { return e.Why }
func (e *SourceRefused) FaultKind() fault.Kind { return fault.Invalid }

func refused(format string, args ...any) error {
	return &SourceRefused{Why: fmt.Sprintf(format, args...)}
}

// URLPolicy decides which https repositories AppTown's server may fetch.
// AppTown sits inside networks its users may not: an address on the
// AppTown's own machine or private network is refused unless the operator
// allows them (an internal GitLab, say).
type URLPolicy struct {
	AllowPrivate bool
	// Resolve looks a host up; nil uses the system resolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Check admits raw as a remote: https on port 443, no user or password in
// it, and a host whose every address is public (unless allowed). The
// remote is pinned to the address checked, for the URL's own host and
// port, so git cannot be sent elsewhere by a second DNS answer.
func (p URLPolicy) Check(ctx context.Context, raw string) (gitexec.Remote, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" {
		return gitexec.Remote{}, refused("%q is not a URL", raw)
	}
	if u.Scheme != "https" {
		return gitexec.Remote{}, refused("only https URLs can be fetched, not %q", u.Scheme+":")
	}
	if u.User != nil {
		return gitexec.Remote{}, refused("put no user or password in the URL; name an AppTown secret for the credential instead")
	}
	host := u.Hostname()
	if host == "" {
		return gitexec.Remote{}, refused("the URL names no host")
	}
	// The pin covers one host and port; refusing other ports keeps every
	// connection git makes under it.
	if port := u.Port(); port != "" && port != "443" {
		return gitexec.Remote{}, refused("only the standard https port (443) can be fetched from, not %s", port)
	}
	addrs, err := p.lookup(ctx, host)
	if err != nil {
		return gitexec.Remote{}, refused("%s could not be looked up: %v", host, err)
	}
	if len(addrs) == 0 {
		return gitexec.Remote{}, refused("%s has no address", host)
	}
	var pinned netip.Addr
	for _, a := range addrs {
		a = a.Unmap()
		if why := nonPublic(a); why != "" && !p.AllowPrivate {
			return gitexec.Remote{}, refused("%s is at %s, %s; this AppTown fetches only from public addresses", host, a, why)
		}
		if !pinned.IsValid() || (a.Is4() && !pinned.Is4()) {
			pinned = a
		}
	}
	addr := pinned.String()
	if pinned.Is6() {
		addr = "[" + addr + "]"
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	return gitexec.Remote{URL: u.String(), Pin: u.Host + ":443:" + addr}, nil
}

func (p URLPolicy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	if p.Resolve != nil {
		return p.Resolve(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")
var thisNetwork = netip.MustParsePrefix("0.0.0.0/8")

// nonPublic says why an address is not on the public internet, "" when it is.
func nonPublic(a netip.Addr) string {
	switch {
	case a.IsLoopback():
		return "this machine (loopback)"
	case a.IsPrivate():
		return "a private network"
	case a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast():
		return "link-local"
	case a.IsUnspecified() || thisNetwork.Contains(a):
		return "unspecified"
	case a.IsMulticast() || a.IsInterfaceLocalMulticast():
		return "multicast"
	case cgnat.Contains(a):
		return "carrier-grade NAT"
	}
	return ""
}

// SeedFromRemote starts the empty hub at dir from r: its default branch
// (branch, or the remote's HEAD) and its tags, every object checked. It
// returns the default branch. A failure leaves no hub.
func SeedFromRemote(ctx context.Context, dir string, r gitexec.Remote, branch string) (string, error) {
	if err := emptyHub(ctx, dir); err != nil {
		return "", err
	}
	if branch == "" {
		head, err := gitexec.RemoteHead(ctx, r)
		if err != nil {
			return "", err
		}
		if head == "" {
			return "", refused("the repository names no default branch; give one with --branch")
		}
		branch = head
	}
	return seed(ctx, dir, branch, func() error {
		return gitexec.FetchRemote(ctx, dir, r, "+refs/heads/"+branch+":refs/heads/"+branch, "+refs/tags/*:refs/tags/*")
	})
}

// SeedFromBundle starts the empty hub at dir from the bundle file: its
// default branch and its tags. The default is branch when given, else the
// branch the bundle's HEAD names, else its only branch; a bundle that
// names no single default is refused with its branches listed.
func SeedFromBundle(ctx context.Context, dir, bundle, branch string) (string, error) {
	if err := emptyHub(ctx, dir); err != nil {
		return "", err
	}
	out, err := gitexec.HubOutput(ctx, os.TempDir(), nil, "bundle", "list-heads", bundle)
	if err != nil {
		return "", refused("that is not a Git bundle: %v", err)
	}
	branch, err = BundleDefault(string(out), branch)
	if err != nil {
		return "", err
	}
	return seed(ctx, dir, branch, func() error {
		if out, err := gitexec.HubRun(ctx, dir, "bundle", "verify", "--quiet", bundle); err != nil {
			return refused("the bundle cannot start a repository on its own (it needs history it does not carry, or it is damaged): %s", out)
		}
		if out, err := gitexec.HubRun(ctx, dir, "-c", "transfer.fsckObjects=true", "fetch", "--no-tags", "--update-head-ok", "--end-of-options", bundle,
			"+refs/heads/"+branch+":refs/heads/"+branch, "+refs/tags/*:refs/tags/*"); err != nil {
			return refused("the bundle could not be read: %s", out)
		}
		return nil
	})
}

// BundleDefault chooses a bundle's default branch from its list of heads
// ("<sha> <ref>" lines): want when given and present, else the branch its
// HEAD names, else its only branch.
func BundleDefault(heads, want string) (string, error) {
	branches := map[string]string{}
	head := ""
	for _, line := range strings.Split(strings.TrimSpace(heads), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if ref == "HEAD" {
			head = sha
		} else if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			branches[b] = sha
		}
	}
	names := make([]string, 0, len(branches))
	for b := range branches {
		names = append(names, b)
	}
	sort.Strings(names)
	if want != "" {
		if _, ok := branches[want]; !ok {
			return "", refused("the bundle has no branch %q (it has: %s)", want, listOrNone(names))
		}
		return want, nil
	}
	if head != "" {
		var at []string
		for _, b := range names {
			if branches[b] == head {
				at = append(at, b)
			}
		}
		if len(at) == 1 {
			return at[0], nil
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	return "", refused("the bundle does not say which branch is the default (it has: %s); name one with --branch", listOrNone(names))
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "no branches"
	}
	return strings.Join(xs, ", ")
}

// emptyHub refuses a hub with history and clears anything else at dir: the
// path is the project's own, and only a seed or an initialization writes it.
func emptyHub(ctx context.Context, dir string) error {
	if gitexec.IsRepo(dir) {
		if out, err := gitexec.HubRun(ctx, dir, "for-each-ref", "--count=1"); err != nil || strings.TrimSpace(out) != "" {
			return ErrRepositoryEstablished
		}
	}
	return os.RemoveAll(dir)
}

// seed makes the hub at dir on branch, runs fetch into it, and checks the
// branch out. Any failure removes the hub again.
func seed(ctx context.Context, dir, branch string, fetch func() error) (string, error) {
	if err := gitexec.CheckBranchName(branch); err != nil {
		return "", refused("%q is not a branch name", branch)
	}
	err := func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if out, err := gitexec.HubRun(ctx, dir, "init", "-q", "-b", branch); err != nil {
			return fmt.Errorf("git init: %s", out)
		}
		if err := fetch(); err != nil {
			return err
		}
		if gitexec.HubRef(ctx, dir, "refs/heads/"+branch) == "" {
			return refused("the source has no branch %q", branch)
		}
		if out, err := gitexec.HubRun(ctx, dir, "reset", "-q", "--hard"); err != nil {
			return fmt.Errorf("checking out %s: %s", branch, out)
		}
		return nil
	}()
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return branch, nil
}

// WriteBundle writes the hub's default branch and tags as a Git bundle,
// with HEAD naming that branch so the bundle says which is the default:
// the Download code door. No Change branch or AppTown ref is included.
func WriteBundle(ctx context.Context, dir, branch string, w io.Writer) error {
	if !gitexec.IsRepo(dir) || branch == "" {
		return ErrNoWorkspace
	}
	return gitexec.HubStream(ctx, dir, w, "bundle", "create", "-", "HEAD", "refs/heads/"+branch, "--tags")
}
