package docker

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"regexp"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// Preview names and the stacks that live and die with a preview. A "stack
// id" is the owner of a throwaway or standing service stack: the value the
// forge.owner label carries on its services and init containers, and the
// suffix of its network:
//
//	pvsnap-change-<id>  the try-it preview of a change
//	pv<stepID>          a run's preview step
//	pvrel-<slug>-<N>    a look at release N of a project
//	env-<slug>-<env>    a standing deployment (persistent, never reaped)
//	<stepID>            an agent step's sidecars
const (
	previewPrefix      = string(Current) + "preview-"
	changeStackPrefix  = "pvsnap-change-"
	runStepStackPrefix = "pv"
	relStackPrefix     = "pvrel-"
)

func PreviewChangeName(changeID ids.ID) string {
	return fmt.Sprintf("%schange-%d", previewPrefix, changeID)
}

func PreviewRunName(runID ids.ID) string {
	return fmt.Sprintf("%srun-%d", previewPrefix, runID)
}

func PreviewLabelName(label string) string { return previewPrefix + label }

func ReleasePreviewLabel(slug string, number int64) string {
	return fmt.Sprintf("%s-release-%d", slug, number)
}

func ChangeStackID(changeID ids.ID) string { return fmt.Sprintf("%s%d", changeStackPrefix, changeID) }

func RunStepStackID(stepID ids.ID) string { return fmt.Sprintf("%s%d", runStepStackPrefix, stepID) }

func ReleaseStackID(slug string, number int64) string {
	return fmt.Sprintf("%s%s-%d", relStackPrefix, slug, number)
}

// previewLabel is the inverse of PreviewLabelName, under any prefix: the
// start-up sweep removes what an older release named.
func previewLabel(name string) (string, bool) {
	for _, p := range Prefixes {
		if rest, ok := strings.CutPrefix(name, string(p)+"preview-"); ok && rest != "" {
			return rest, true
		}
	}
	return "", false
}

// workPreviewLabel is a change's or a run's preview label.
var workPreviewLabel = regexp.MustCompile(`^(change|run)-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsWorkPreviewLabel says whether a preview label is a change's or a run's
// (change-<id>, run-<id>): the previews work boots, never a release's look.
func IsWorkPreviewLabel(label string) bool { return workPreviewLabel.MatchString(label) }

// IsWorkPreviewName is IsWorkPreviewLabel for a container name.
func IsWorkPreviewName(name string) bool {
	label, ok := previewLabel(name)
	return ok && IsWorkPreviewLabel(label)
}

// IsPreviewName says whether a container is a preview-tier app container
// (the vhost's cache policy and the orphan sweep read this).
func IsPreviewName(name string) bool {
	_, ok := previewLabel(name)
	return ok
}

// isPreviewStackID: a stack that lives and dies with a preview, exactly the
// three throwaway forms, never a deployment's or an agent step's stack.
func isPreviewStackID(id string) bool {
	if strings.HasPrefix(id, changeStackPrefix) || strings.HasPrefix(id, relStackPrefix) {
		return true
	}
	rest, ok := strings.CutPrefix(id, runStepStackPrefix)
	if !ok || rest == "" {
		return false
	}
	step, err := ids.Parse(rest)
	return err == nil && !step.IsZero()
}

// RemovePreview removes one preview's whole stack, and says whether
// anything of it could not be removed: its app container with its anonymous
// volumes and its data volume, then every container its stack owns (its
// services, its parts, an init container left standing) with theirs, and
// the stack's network. Every stop of a preview on this daemon comes here: a
// discard, a replacement, a restart, a boot that failed, an idle stop. Its
// build images stay; they are not part of what runs.
func RemovePreview(ctx context.Context, dk Scope, name, stack string) error {
	err := dk.RemoveDisposable(ctx, name)
	if stack != "" {
		err = errors.Join(err, dk.RemoveDisposableStack(ctx, stack))
	}
	return err
}

// StackLabel names, on a preview's app container, the stack that lives and
// dies with it: a run's preview stack (pv<step>) cannot be told from the
// preview's name.
const StackLabel = dockerlabel.Stack

// ReapPreviewOrphans removes every preview this daemon holds, as
// RemovePreview removes one. It decides from labels and exact names, never
// substrings: the preview-class app containers and the services and init
// containers whose owner is a throwaway preview stack (never a deployment's
// env-<slug>-<env> stack, never an agent step's), each with its anonymous
// volumes; those stacks' networks; and the previews' data volumes.
func ReapPreviewOrphans(ctx context.Context, dk Scope) {
	ctx = context.WithoutCancel(ctx) // a clean-up: it runs though the caller stops
	for _, id := range dk.Lines(ctx, "ps", "-aq", "--filter", "label="+dockerlabel.Class+"=preview") {
		dk.RemoveWithVolumes(ctx, id)
	}
	for _, class := range []string{"service", "step"} {
		for _, line := range dk.Lines(ctx, "ps", "-a", "--filter", "label="+dockerlabel.Class+"="+class,
			"--format", "{{.ID}}\t{{.Label \""+dockerlabel.Owner+"\"}}") {
			id, owner, _ := strings.Cut(line, "\t")
			if isPreviewStackID(owner) {
				dk.RemoveWithVolumes(ctx, id)
			}
		}
	}
	for _, n := range dk.Lines(ctx, "network", "ls", "--format", "{{.Name}}") {
		if stack, ok := StackOfNetwork(n); ok && isPreviewStackID(stack) {
			dk.RemoveNetwork(ctx, n)
		}
	}
	for _, v := range dk.Lines(ctx, "volume", "ls", "-q") {
		if c, ok := ContainerOfDataVolume(v); ok && IsPreviewName(c) {
			dk.RemoveVolume(ctx, v)
		}
	}
}
