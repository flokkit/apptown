package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/google/go-containerregistry/pkg/name"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ImageRequest asks for an application image a check runs in or scans
// (design/operations/workers.md, "Checks on the worker"): the
// image Tag names, when it is already there; else Pull pulled, with
// Registry's login for that one pull; else built from Context under Tree,
// when the caller allows a build. The machine that prepares it is the one
// that runs the check.
type ImageRequest struct {
	Tag        string
	Pull       string
	Tree       string // the staged tree a build's context is under
	Context    string // the build context, relative to Tree
	AllowBuild bool
	Registry   *RegistryLogin
	// Anonymous isolates registry authentication without changing the selected daemon.
	Anonymous bool
	Timeout   time.Duration
	// Scratch is where a login's Docker configuration is made, for the one
	// request: a folder its owner sweeps, so a request interrupted mid-pull
	// leaves no credentials behind for long. "" is the system's temporary
	// folder.
	Scratch string
}

// RegistryLogin is one registry's credentials, for one request.
type RegistryLogin struct{ Host, Username, Token string }

// ImageAnswer names the image by its ID, which is what a check then runs
// and what its evidence records; a tag is only a way to find it.
type ImageAnswer struct {
	ID     string            `json:"id,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	// NotBuilt: nothing was there, and the caller did not allow a build.
	NotBuilt bool   `json:"notBuilt,omitempty"`
	Err      string `json:"err,omitempty"`
}

// imageRef is what an image reference may look like: a name, tag, digest or
// ID. It cannot start with "-", so it is never read as one of Docker's
// options, whatever a request carries.
var imageRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,511}$`)

// ValidImage reports whether ref may be given to Docker as an image.
func ValidImage(ref string) bool { return imageRef.MatchString(ref) }

// CheckLabel marks what checks leave on a machine, so its sweep finds it.
const CheckLabel = dockerlabel.Class + "=check"

// DockerCLI runs one docker command (the arguments after the binary) on a
// machine's Docker, with stdin when given, and answers with its output.
type DockerCLI func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error)

// PrepareImage answers an ImageRequest on this runner's Docker.
func (r Runner) PrepareImage(ctx context.Context, req ImageRequest) ImageAnswer {
	if r.Refused != nil {
		return ImageAnswer{Err: r.Refused.Error()}
	}
	return PrepareImage(ctx, func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, r.dockerBinary(), args...)
		cmd.Stdin = stdin
		return cmd.CombinedOutput()
	}, req)
}

// PrepareImage answers an ImageRequest on the Docker that docker runs. A
// failure is Err, never an empty answer that reads as "no image".
func PrepareImage(ctx context.Context, docker DockerCLI, req ImageRequest) (answer ImageAnswer) {
	if req.Anonymous {
		if req.Registry != nil {
			return ImageAnswer{Err: "anonymous image preparation cannot include registry credentials"}
		}
		if os.Getenv("DOCKER_AUTH_CONFIG") != "" {
			return ImageAnswer{Err: "anonymous image preparation is unavailable while DOCKER_AUTH_CONFIG is set"}
		}
	}
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Minute
	}
	for _, ref := range []string{req.Tag, req.Pull} {
		if ref != "" && !ValidImage(ref) {
			return ImageAnswer{Err: fmt.Sprintf("%q is not an image reference", ref)}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	if req.Tag != "" {
		if a, found, err := inspectImage(ctx, docker, req.Tag); err != nil {
			return ImageAnswer{Err: err.Error()}
		} else if found {
			return a
		}
	}
	switch {
	case req.Pull != "":
		var config []string
		if req.Registry != nil || req.Anonymous {
			// Docker's configuration for this one request: the login made for
			// the pull goes with it. It names the same Docker as the ambient
			// configuration does, so the pull lands where the check runs.
			dir, err := requestConfig(ctx, docker, req.Scratch)
			if err != nil {
				return ImageAnswer{Err: err.Error()}
			}

			if req.Anonymous {
				defer func() {
					if err := os.RemoveAll(dir); err != nil {
						answer.ID = ""
						answer.Err = strings.TrimSpace(answer.Err + "; removing anonymous image configuration: " + err.Error())
					}
				}()
				if err := anonymousConfig(dir, req.Pull); err != nil {
					return ImageAnswer{Err: err.Error()}
				}
			} else {
				defer os.RemoveAll(dir)
			}
			config = []string{"--config", dir}
			if req.Registry != nil {
				if out, err := docker(ctx, strings.NewReader(req.Registry.Token), append(config, "login", req.Registry.Host, "-u", req.Registry.Username, "--password-stdin")...); err != nil {
					return ImageAnswer{Err: fmt.Sprintf("logging in to %s: %v: %s", req.Registry.Host, err, bounded(out))}
				}
			}
		}
		if out, err := docker(ctx, nil, append(config, "pull", req.Pull)...); err != nil {
			return ImageAnswer{Err: fmt.Sprintf("docker pull %s: %v: %s", req.Pull, err, bounded(out))}
		}
		if a, found, err := inspectImage(ctx, docker, req.Pull); found && err == nil {
			return a
		} else if err != nil {
			return ImageAnswer{Err: err.Error()}
		}
		return ImageAnswer{Err: "the pulled image " + req.Pull + " cannot be found"}
	case req.Context != "":
		if !req.AllowBuild {
			return ImageAnswer{NotBuilt: true}
		}
		if req.Tag == "" {
			return ImageAnswer{Err: "a build needs the name it goes by"}
		}
		root, err := os.OpenRoot(req.Tree)
		if err != nil {
			return ImageAnswer{Err: fmt.Sprintf("the build's tree: %v", err)}
		}
		fi, err := root.Stat(req.Context) // inside the tree, never through a link out of it
		root.Close()
		if err != nil || !fi.IsDir() {
			return ImageAnswer{Err: fmt.Sprintf("the build context %s is not a folder in the tree: %v", req.Context, err)}
		}
		// The extra certificates as the build's secret (internal/trust).
		build := append([]string{"build"}, trust.BuildArgs(true)...)
		build = append(append(build, "--label", CheckLabel), dockerlabel.BuiltArgs()...)
		build = append(build, "-t", req.Tag, filepath.Join(req.Tree, req.Context))
		if out, err := docker(ctx, nil, build...); err != nil {
			return ImageAnswer{Err: fmt.Sprintf("building the app image: %v: %s", err, bounded(out))}
		}
		if a, found, err := inspectImage(ctx, docker, req.Tag); found && err == nil {
			return a
		} else if err != nil {
			return ImageAnswer{Err: err.Error()}
		}
		return ImageAnswer{Err: "the built image " + req.Tag + " cannot be found"}
	}
	return ImageAnswer{} // asked only whether Tag is there, and it is not
}

// inspectImage answers whether ref is on the Docker: found, not found (the
// daemon says there is no such image), or an error (it could not be asked,
// or its answer could not be read). Only "not found" is a cache miss.
func inspectImage(ctx context.Context, docker DockerCLI, ref string) (ImageAnswer, bool, error) {
	out, err := docker(ctx, nil, "image", "inspect", "--format", "{{json .Id}} {{json (index .Config \"Labels\")}}", ref)
	if err != nil {
		said := strings.ToLower(string(out) + " " + err.Error())
		if strings.Contains(said, "no such image") || strings.Contains(said, "no such object") {
			return ImageAnswer{}, false, nil
		}
		return ImageAnswer{}, false, fmt.Errorf("asking Docker for %s: %v: %s", ref, err, bounded(out))
	}
	id, labels, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	var a ImageAnswer
	if json.Unmarshal([]byte(id), &a.ID) != nil || !strings.HasPrefix(a.ID, "sha256:") {
		return ImageAnswer{}, false, fmt.Errorf("Docker's answer for %s names no image ID: %s", ref, bounded(out))
	}
	_ = json.Unmarshal([]byte(labels), &a.Labels)
	return a, true, nil
}

// requestConfig makes a Docker configuration folder for one request, under
// scratch, that selects the same Docker context as the ambient
// configuration: the context's own records are shared, not copied, and the
// folder holds nothing else until the request's login writes to it.
func requestConfig(ctx context.Context, docker DockerCLI, scratch string) (string, error) {
	out, err := docker(ctx, nil, "context", "show")
	if err != nil {
		return "", fmt.Errorf("asking Docker which context it uses: %v: %s", err, bounded(out))
	}
	current := strings.TrimSpace(string(out))
	dir, err := os.MkdirTemp(scratch, "docker-config-*")
	if err != nil {
		return "", err
	}
	if current != "" && current != "default" {
		ambient, err := DockerConfigDir()
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.Symlink(filepath.Join(ambient, "contexts"), filepath.Join(dir, "contexts")); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		body, _ := json.Marshal(map[string]string{"currentContext": current})
		if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func bounded(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}

// A present empty auth entry also prevents Docker's automatic native-helper
// selection. An empty auth map alone would still discover a host helper.
func anonymousConfig(dir, reference string) error {
	ref, err := name.ParseReference(reference)
	if err != nil {
		return fmt.Errorf("anonymous image reference: %w", err)
	}
	host := ref.Context().RegistryStr()
	if host == "index.docker.io" || host == "docker.io" {
		host = "https://index.docker.io/v1/"
	}
	config := map[string]any{}
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			return err
		}
	}
	config["auths"] = map[string]any{host: map[string]string{}}
	config["credsStore"] = ""
	config["credHelpers"] = map[string]string{}
	raw, err = json.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
