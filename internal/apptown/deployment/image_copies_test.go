package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

const (
	goneCopy = "registry.example/p/web@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	keptCopy = "registry.example/p/web@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// imageCopyWorld is a standing deployment on a Docker host that recorded two
// images; the host has lost goneCopy. during runs while the host answers the
// image question, before the reading is stored.
func imageCopyWorld(t *testing.T, owned []deploy.Owned, during func()) (*Service, *HostSnapshots, *Deployment, *Target) {
	t.Helper()
	svc, db, daemon, projectID, target := healthWorld(t)
	dep, err := svc.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range append([]deploy.Owned{{Kind: "image", ID: goneCopy}, {Kind: "image", ID: keptCopy}}, owned...) {
		if err := svc.Deployments.OwnerOf(dep).Created(t.Context(), o); err != nil {
			t.Fatal(err)
		}
	}
	daemon.SetCommandReply(func(c docker.Command) ([]byte, error, bool) {
		if len(c.Args) < 5 || c.Args[0] != "image" || c.Args[1] != "inspect" {
			return nil, nil, false
		}
		if during != nil {
			during()
		}
		missing := false
		for _, ref := range c.Args[4:] {
			if ref == goneCopy {
				fmt.Fprintf(c.Stderr, "Error: No such image: %s\n", ref)
				missing = true
				continue
			}
			fmt.Fprintf(c.Stdout, "[]\t[%q]\t%q\n", ref, dockerfake.ImageID(ref))
		}
		if missing {
			return nil, errors.New("exit status 1"), true
		}
		return nil, nil, true
	})
	return svc, NewHostSnapshots(db, svc.now), dep, target
}

func collectImageCopies(t *testing.T, svc *Service, s *HostSnapshots, target *Target) hostReading {
	t.Helper()
	refs, err := svc.providers.recordedImages(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	dk, err := svc.providers.hosts.DockerScope(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.collect(t.Context(), target, dk, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true, ImageRefs: refs}, nil, svc.Inventory); err != nil {
		t.Fatal(err)
	}
	r, err := s.reading(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ownedImages(t *testing.T, svc *Service, dep *Deployment) []string {
	t.Helper()
	owned, err := svc.Deployments.Owned(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range owned {
		if o.Kind == "image" {
			out = append(out, o.ID)
		}
	}
	return out
}

func imageCopyReceipts(t *testing.T, svc *Service) []audit.Entry {
	t.Helper()
	rows, err := audit.Recent(t.Context(), svc.db, audit.Filter{KindPrefix: "deployment.image-copies.retired"})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// An image removed from the host out of band (docker image prune -a) is a
// copy, not a missing resource: the reading claims nothing missing, and the
// record is retired with a receipt saying why. The image still there stays.
func TestAnImageRemovedFromTheHostIsRetiredNotMissing(t *testing.T) {
	t.Parallel()
	svc, s, dep, target := imageCopyWorld(t, nil, nil)
	r := collectImageCopies(t, svc, s, target)
	if len(r.Missing) != 0 {
		t.Fatalf("a gone image copy read missing: %+v", r.Missing)
	}
	if got := ownedImages(t, svc, dep); !slices.Equal(got, []string{keptCopy}) {
		t.Fatalf("owned images after the reading = %v, want only the one still on the host", got)
	}
	receipts := imageCopyReceipts(t, svc)
	if len(receipts) != 1 || receipts[0].Subject != dep.Name || receipts[0].ProjectID != dep.ProjectID {
		t.Fatalf("retirement receipts = %+v", receipts)
	}
	var payload struct {
		Target string   `json:"target"`
		Images []string `json:"images"`
		Note   string   `json:"note"`
	}
	if err := json.Unmarshal(receipts[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Target != target.Name || !slices.Equal(payload.Images, []string{goneCopy}) || payload.Note != "no longer on the host; pulled again when needed" {
		t.Fatalf("receipt payload = %+v", payload)
	}
	// A release that pulls the image again records it again.
	if err := svc.Deployments.OwnerOf(dep).Created(t.Context(), deploy.Owned{Kind: "image", ID: goneCopy}); err != nil {
		t.Fatal(err)
	}
	if got := ownedImages(t, svc, dep); len(got) != 2 {
		t.Fatalf("a re-pulled image was not recorded again: %v", got)
	}
}

// Only images are copies: a recorded container gone from the host still reads
// missing, and its record stays.
func TestAMissingContainerStillReadsMissing(t *testing.T) {
	t.Parallel()
	const container = "apptown-svc-p-main-postgres"
	svc, s, dep, target := imageCopyWorld(t, []deploy.Owned{{Kind: "container", ID: container, Name: container}}, nil)
	r := collectImageCopies(t, svc, s, target)
	if len(r.Missing) != 1 || r.Missing[0].Kind != "container" || r.Missing[0].Name != container {
		t.Fatalf("missing = %+v, want the container only", r.Missing)
	}
	owned, err := svc.Deployments.Owned(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(owned, func(o deploy.Owned) bool { return o.Kind == "container" && o.ID == container }) {
		t.Fatalf("the missing container's record was removed: %+v", owned)
	}
}

// A deploy already under way when the reading began pulls the image and
// records it again before the reading retires it: the record stays, with no
// receipt. Without the act mark on recording, the reading's act counter would
// be unchanged and it would delete the record of an image now on the host.
func TestAnImageRecordedAgainDuringTheReadIsKept(t *testing.T) {
	t.Parallel()
	var svc *Service
	var dep *Deployment
	recorded := false
	svc, s, dep, target := imageCopyWorld(t, nil, func() {
		if recorded {
			return
		}
		recorded = true
		if err := svc.Deployments.OwnerOf(dep).Created(context.WithoutCancel(t.Context()), deploy.Owned{Kind: "image", ID: goneCopy}); err != nil {
			t.Error(err)
		}
	})
	done, err := s.BeforeAct(t.Context(), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := collectImageCopies(t, svc, s, target)
	done()
	if len(r.Missing) != 0 {
		t.Fatalf("a gone image copy read missing: %+v", r.Missing)
	}
	if got := ownedImages(t, svc, dep); len(got) != 2 {
		t.Fatalf("an image recorded again during the reading was retired: %v", got)
	}
	if n := len(imageCopyReceipts(t, svc)); n != 0 {
		t.Fatalf("%d receipts for a refused retirement", n)
	}
}

// A row a reading left in place (not asked about, or its retirement refused)
// is not turned into a missing image by the infrastructure page's merge; a
// missing container still is.
func TestAnUnretiredImageCopyNeverReadsMissing(t *testing.T) {
	t.Parallel()
	image := deploy.Resource{Kind: "image", ID: goneCopy, Name: goneCopy}
	container := deploy.Resource{Kind: "container", Name: "apptown-svc-p-main-postgres"}
	out, _, _ := InventoryAnswer([]deploy.Resource{image, container}, InventoryEvidence{
		Items: []deploy.Resource{}, ObservedAt: time.Now(), Missing: []deploy.Resource{container},
	})
	if len(out) != 1 || out[0].Kind != "container" || out[0].Record != deploy.RecordMissing {
		t.Fatalf("inventory answer = %+v, want only the missing container", out)
	}
}
