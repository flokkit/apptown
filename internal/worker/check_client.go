package worker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// Checks is AppTown's check executor on a worker
// (design/operations/workers.md, "Checks on the worker"): Run
// answers a check container's request as the local runner does, having run
// it on the worker. A request the worker cannot honour (a mount of this
// machine's, a host route, a local process) is refused, never run here.
type Checks struct {
	URL    string
	Client *http.Client
	// Limits bound each check's container on the worker (AppTown's task
	// limits).
	Limits execution.Limits
}

// Run sends the staged tree and the spec, and reads the answer. The worker
// failing to answer is a run that did not happen (LaunchFailed), never a
// pass.
func (c Checks) Run(ctx context.Context, req execution.Request) execution.Outcome {
	if len(req.Mounts) > 0 || len(req.ExtraHosts) > 0 {
		return notRun(errors.New("a check on the worker takes no mounts or host routes of AppTown's"))
	}
	if req.Network != "" && req.Network != "none" {
		return notRun(fmt.Errorf("a check on the worker has no network or the worker's egress, not %q", req.Network))
	}
	if req.Timeout <= 0 || req.Image == "" || len(req.Argv) == 0 {
		return notRun(errors.New("a check on the worker needs an image, a command and a timeout"))
	}
	if err := execution.ValidFiles(req.Files); err != nil {
		return notRun(err)
	}
	if req.Workspace == "" && (req.Report != "" || req.History != nil || req.ExportImage != "") {
		return notRun(errors.New("a check with no tree has no report, history or image to export"))
	}
	spec := CheckSpec{
		Image: req.Image, Argv: req.Argv, Env: req.Env, NoEntrypoint: req.NoEntrypoint,
		Egress: req.Network == "", TimeoutSeconds: int(req.Timeout.Seconds() + 0.5),
		Writable: !req.WorkspaceRO, Report: req.Report, History: req.History != nil,
		ExportImage: req.ExportImage, NoTree: req.Workspace == "", Files: req.Files,
		Services: req.Services, Limits: c.Limits,
	}
	// The whole request, the transfers both ways and the wait between, ends
	// by the check's own timeout with the worker's allowance for them.
	ctx, cancel := context.WithTimeout(ctx, req.Timeout+2*checkGrace)
	defer cancel()
	body, content := io.Pipe()
	form := multipart.NewWriter(content)
	go func() { content.CloseWithError(writeCheck(ctx, form, spec, req)) }()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/checks", body)
	if err != nil {
		return notRun(err)
	}
	hr.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := c.Client.Do(hr)
	if err != nil {
		return notRun(fmt.Errorf("the worker did not answer the check: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return notRun(fmt.Errorf("the worker refused the check (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg))))
	}
	answer, err := readAnswer(resp.Body)
	if err != nil {
		return notRun(fmt.Errorf("the worker's answer to the check: %w", err))
	}
	out := execution.Outcome{Termination: answer.Termination, ExitCode: -1, Output: answer.Output,
		TimedOut: answer.Termination == execution.TimedOut, Report: answer.Report}
	if answer.Termination == execution.Exited {
		out.ExitCode = *answer.ExitCode
		if answer.RunErr != "" {
			out.CleanupErr = errors.New(answer.RunErr) // exited, and left something behind: no answer
		}
	} else {
		out.RunErr = errors.New(strs.FirstNonBlank(answer.RunErr, string(answer.Termination)))
	}
	if answer.ReportErr != "" {
		out.ReportErr = errors.New(answer.ReportErr)
	} else if req.Report != "" && len(answer.Report) > execution.MaxReport {
		out.Report, out.ReportErr = nil, fmt.Errorf("the report is over the %d limit", execution.MaxReport)
	}
	return out
}

func notRun(err error) execution.Outcome {
	return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: err}
}

// writeCheck writes the request's parts: the spec, a tar of the tree, and
// a bundle of the history's commits.
func writeCheck(ctx context.Context, form *multipart.Writer, spec CheckSpec, req execution.Request) error {
	part, err := form.CreateFormField("spec")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(part).Encode(spec); err != nil {
		return err
	}
	part, err = form.CreateFormFile("tree", "tree.tar")
	if err != nil {
		return err
	}
	if req.Workspace == "" {
		if err := tar.NewWriter(part).Close(); err != nil { // an empty tree
			return err
		}
	} else if err := tarTree(part, req.Workspace); err != nil {
		return fmt.Errorf("sending the check's tree: %w", err)
	}
	if req.History != nil {
		file, err := os.CreateTemp("", "apptown-check-history-*.bundle")
		if err != nil {
			return err
		}
		file.Close()
		defer os.Remove(file.Name())
		if err := gitexec.BundleCommits(ctx, req.History.Repository, req.History.Commits, file.Name()); err != nil {
			return err
		}
		bundle, err := os.Open(file.Name())
		if err != nil {
			return err
		}
		defer bundle.Close()
		if part, err = form.CreateFormFile("history", "history.bundle"); err != nil {
			return err
		}
		if _, err := io.Copy(part, bundle); err != nil {
			return err
		}
	}
	return form.Close()
}

// tarTree writes dir's regular files, directories and symbolic links, as
// they are (a link is sent as a link, never followed).
func tarTree(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // a socket or device is not part of a tree
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return errors.Join(err, f.Close())
	})
	return errors.Join(err, tw.Close())
}

// Image prepares an image on the worker (execution.ImageRequest): the tree a
// build's context is under goes with the request. A worker that does not
// answer is an error, never "no image".
func (c Checks) Image(ctx context.Context, req execution.ImageRequest) execution.ImageAnswer {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute+2*checkGrace)
	defer cancel()
	tree := req.Tree
	body, content := io.Pipe()
	form := multipart.NewWriter(content)
	go func() {
		content.CloseWithError(func() error {
			part, err := form.CreateFormField("spec")
			if err != nil {
				return err
			}
			wire := req
			wire.Tree = ""
			if err := json.NewEncoder(part).Encode(wire); err != nil {
				return err
			}
			if req.Context != "" {
				if part, err = form.CreateFormFile("tree", "tree.tar"); err != nil {
					return err
				}
				if err := tarTree(part, tree); err != nil {
					return fmt.Errorf("sending the build's tree: %w", err)
				}
			}
			return form.Close()
		}())
	}()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/check-images", body)
	if err != nil {
		return execution.ImageAnswer{Err: err.Error()}
	}
	hr.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := c.Client.Do(hr)
	if err != nil {
		return execution.ImageAnswer{Err: "the worker did not answer the image request: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return execution.ImageAnswer{Err: fmt.Sprintf("the worker refused the image request (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))}
	}
	var answer execution.ImageAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&answer); err != nil {
		return execution.ImageAnswer{Err: "the worker's answer to the image request: " + err.Error()}
	}
	return answer
}

// wireAnswer is CheckAnswer as read: an exit code that is absent is not 0.
type wireAnswer struct {
	CheckAnswer
	ExitCode *int `json:"exitCode"`
}

// readAnswer reads one whole, consistent answer: a known ending; for a
// normal exit, an exit code Docker can give (0 to 255); for any other
// ending, none; and nothing after it.
func readAnswer(body io.Reader) (wireAnswer, error) {
	dec := json.NewDecoder(io.LimitReader(body, 2*execution.MaxReport+4*maxCheckOutput))
	var a wireAnswer
	if err := dec.Decode(&a); err != nil {
		return a, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return a, errors.New("it goes on past its end")
	}
	switch a.Termination {
	case execution.Exited:
		if a.ExitCode == nil || *a.ExitCode < 0 || *a.ExitCode > 255 {
			return a, errors.New("a normal exit with no exit code a process can have")
		}
	case execution.TimedOut, execution.Canceled, execution.LaunchFailed, execution.OutOfMemory:
		if a.ExitCode != nil && *a.ExitCode != -1 {
			return a, fmt.Errorf("%s with an exit code", a.Termination)
		}
	default:
		return a, fmt.Errorf("no known ending (%q)", a.Termination)
	}
	return a, nil
}
