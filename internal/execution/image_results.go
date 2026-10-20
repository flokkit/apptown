package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
)

// ImageResult is an image a release build declares it will leave
// (design/operations/workers.md, "Releases built on the worker"): an OCI image layout, as `docker buildx build --output
// type=oci,dest=<Path>,tar=false` writes it, in the folder at Path under
// the workspace's state. The backend captures it after the task and
// everything beside it have stopped, the way it captures result files, and
// keeps it until the task is released. The forge reads it file by file
// (ImageFilePattern) and trusts none of it: what it publishes is what the
// registry reads back.
type ImageResult struct {
	Name string `json:"name"`
	// Path is under /apptown/state/, never in the tree: the source cannot
	// have put an image there.
	Path string `json:"path"`
	// Max is the admitted size, in bytes, of all the layout's files
	// together; a larger capture is ResultTooLarge.
	Max int64 `json:"max"`
}

// ImageFilePattern is the files of a captured image the forge reads: its
// index and its blobs. Nothing else in the layout is served.
var ImageFilePattern = regexp.MustCompile(`^(index\.json|blobs/sha256/[a-f0-9]{64})$`)

// ImageCapture is what became of a declared image when its task exited:
// present (a folder of regular files within Max, Size bytes), absent,
// refused (Reason: a symbolic link at, on the way to or inside the folder,
// something not a folder at the path, something not a regular file in it,
// or a copy that would leave the backend's disk short of space, in words)
// or too large (Size). None is ever turned into another; and once the
// task is released its files are gone (ErrImageGone), which is none of
// these.
type ImageCapture struct {
	State  ResultState `json:"state"`
	Reason string      `json:"reason,omitempty"` // refused only
	Size   int64       `json:"size,omitempty"`   // present and too large
}

// RefusedNotFolder: an image's path holds something other than a folder.
const RefusedNotFolder = "not-folder"

// ErrImageGone: the task was released, and the files of its images went
// with it. A read already open may finish; a new one is refused.
var ErrImageGone = errors.New("the task was released: its image files are gone")

// ValidImages says whether images may be declared: each named once, at its
// own clean path under /apptown/state/, with a positive admitted size.
func ValidImages(images []ImageResult) error {
	names, paths := map[string]bool{}, map[string]bool{}
	for _, img := range images {
		if !resultNamePattern.MatchString(img.Name) {
			return fmt.Errorf("invalid image name %q", img.Name)
		}
		if !cleanUnder(img.Path, StateRoot) {
			return fmt.Errorf("an image's path must be clean and under %s/: %q", StateRoot, img.Path)
		}
		if names[img.Name] || paths[img.Path] {
			return fmt.Errorf("the image %s (%s) is declared twice", img.Name, img.Path)
		}
		names[img.Name], paths[img.Path] = true, true
		if img.Max <= 0 {
			return fmt.Errorf("the image %s needs its admitted size", img.Name)
		}
	}
	return nil
}

// validBuild refuses a task declaring images that carries anything a
// secret could travel in: environment, inputs, or services (whose
// environment the request carries). A release build is given its source,
// its generated build files and its private daemon, and no credential: the
// forge alone publishes, with its own (E07). It works in a workspace of its
// own, whose state starts empty, so what is captured was left by it.
func (s TaskSpec) validBuild() error {
	if len(s.Images) == 0 {
		return nil
	}
	if err := ValidImages(s.Images); err != nil {
		return err
	}
	switch {
	case s.Source == nil || s.SharedWorkspace != "":
		return errors.New("a build's images are left in a workspace of its own, made from its source")
	case len(s.Env) > 0 || len(s.Inputs) > 0:
		return errors.New("a build carries no environment or inputs: nothing secret travels with it")
	case s.Private != nil && len(s.Private.Services) > 0:
		return errors.New("a build has no services: their environment would travel with it")
	}
	return nil
}

// Valid says whether a captured image is one of the four states, whole.
func (c ImageCapture) Valid() error {
	switch c.State {
	case ResultPresent, ResultAbsent, ResultTooLarge:
	case ResultRefused:
		if c.Reason == "" {
			return errors.New("a refused image without its reason")
		}
	default:
		return fmt.Errorf("unknown image state %q", c.State)
	}
	return nil
}

// ImageFiles opens a captured image's files (WorkerTasks.ImageFile).
type ImageFiles interface {
	ImageFile(ctx context.Context, ref TaskRef, image, file string) (io.ReadCloser, int64, error)
}

// ImageLayout is one captured image of a task read file by file, as the
// forge's publisher reads an OCI layout (oci.Layout): every file is opened
// on the backend when it is read, and checked by the reader.
type ImageLayout struct {
	Files ImageFiles
	Ref   TaskRef
	Image string
}

func (l ImageLayout) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	body, _, err := l.Files.ImageFile(ctx, l.Ref, l.Image, name)
	return body, err
}
