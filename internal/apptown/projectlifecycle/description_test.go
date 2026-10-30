package projectlifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type descriptionModel string

func (m descriptionModel) Resolve(context.Context, ids.ID, string) (string, string, string, error) {
	return string(m), "fast", "standard", nil
}

type descriptionAnswer func(context.Context, modelcall.Call) (string, *modelcall.Usage, error)

func (f descriptionAnswer) Ask(ctx context.Context, c modelcall.Call) (string, *modelcall.Usage, error) {
	return f(ctx, c)
}

type descriptionQueue struct{ requests []jobs.Request }

func (q *descriptionQueue) EnqueueIn(_ context.Context, _ *database.Tx, r jobs.Request) error {
	q.requests = append(q.requests, r)
	return nil
}

// The request survives both ordinary (MCP) and replayable (HTTP) creation,
// independently of model availability or publication. No request truncation.
func TestCreationKeepsOriginalRequestAndGeneratesOnlyDisplayWords(t *testing.T) {
	t.Parallel()
	for _, replayable := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "replayable"}[replayable], func(t *testing.T) {
			f, ctx := requestedOwner(t)
			q := new(descriptionQueue)
			f.lifecycle.jobs = q
			request := strings.Repeat("I need to know which driver has each truck, with service dates and alerts. ", 20)
			in := Draft{Slug: "trucks", Name: "Trucks", Actor: "boss", Blurb: request}
			if replayable {
				in.RequestID = requestID()
			}
			p, err := f.lifecycle.Create(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Blurb != "" || p.BlurbSource != "generated" || len(q.requests) != 1 {
				t.Fatalf("creation: %+v jobs=%+v", p, q.requests)
			}
			got, err := project.New(f.db, f.identity).OriginalRequest(ctx, p.ID)
			if err != nil || got != request {
				t.Fatalf("original request: %q %v", got, err)
			}
			d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(_ context.Context, c modelcall.Call) (string, *modelcall.Usage, error) {
				if c.ProjectID != p.ID || !strings.Contains(c.User, request) {
					t.Fatalf("lost request/project: %+v", c)
				}
				return "", nil, errors.New("model failed")
			})}
			if err := d.Refresh(ctx, p.ID); err != nil {
				t.Fatalf("model failure should finish quietly: %v", err)
			}
			held, _ := f.lifecycle.projects.ByID(ctx, p.ID)
			if held.Blurb != "" {
				t.Fatal("failed model wrote a description")
			}
			got, err = audit.CreationRequest(ctx, f.db, p.ID)
			if err != nil || got != request {
				t.Fatalf("failure lost request: %q %v", got, err)
			}
			d.Completion = descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
				return "Truck assignments and servicing", nil, nil
			})
			if err := d.Refresh(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			held, _ = f.lifecycle.projects.ByID(ctx, p.ID)
			if held.Blurb != "Truck assignments and servicing" || held.BlurbSource != "generated" || held.SettingsRevision != p.SettingsRevision+1 {
				t.Fatalf("publication: %+v", held)
			}
			same := held.Blurb
			repeated, err := f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Blurb: &same}, "boss")
			if err != nil || repeated.BlurbSource != "generated" || repeated.SettingsRevision != held.SettingsRevision {
				t.Fatalf("repeated description changed ownership: %+v %v", repeated, err)
			}
			name := "New name"
			if _, err := f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{ExpectedRevision: &p.SettingsRevision, Name: &name}, "boss"); !errors.Is(err, project.ErrSettingsMoved) {
				t.Fatalf("stale edit: %v", err)
			}
			if replayable {
				replay, err := f.lifecycle.Create(ctx, in)
				if err != nil || *replay != *p {
					t.Fatalf("creation replay: %+v %v", replay, err)
				}
				in.Blurb += "changed"
				if _, err := f.lifecycle.Create(ctx, in); !errors.Is(err, project.ErrRequestReused) {
					t.Fatalf("hash lost original input: %v", err)
				}
			}
		})
	}
}

func TestDescriptionEditSticksEvenWhenAModelReturnsLate(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	q := new(descriptionQueue)
	f.lifecycle.jobs = q
	p, err := f.lifecycle.Create(ctx, Draft{Slug: "trucks", Actor: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
		close(started)
		<-finish
		return "Generated truck description", nil, nil
	})}
	done := make(chan error, 1)
	go func() { done <- d.Refresh(ctx, p.ID) }()
	<-started
	text := "Our truck roster"
	edited, err := f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{ExpectedRevision: &p.SettingsRevision, Blurb: &text}, "boss")
	close(finish)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	held, _ := f.lifecycle.projects.ByID(ctx, p.ID)
	if held.Blurb != text || held.BlurbSource != "person" || held.SettingsRevision != edited.SettingsRevision {
		t.Fatalf("late model overwrote edit: %+v", held)
	}
	d.Completion = descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
		t.Fatal("person description called model")
		return "", nil, nil
	})
	if err := d.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	empty := ""
	cleared, err := f.lifecycle.UpdateSettings(ctx, p.ID, project.SettingsPatch{ExpectedRevision: &held.SettingsRevision, Blurb: &empty}, "boss")
	if err != nil || cleared.BlurbSource != "generated" || cleared.Blurb != "" || len(q.requests) != 2 {
		t.Fatalf("clear: %+v jobs=%+v %v", cleared, q.requests, err)
	}
}

func TestDescriptionNoModelAndInvalidAnswersKeepCurrentText(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{Slug: "test", Actor: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.lifecycle.projects.PublishDescription(ctx, p.ID, "Current description"); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{"", "One two three four five six seven eight nine", "Sentence..", "Two\nlines"} {
		d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) { return out, nil, nil })}
		if err := d.Refresh(ctx, p.ID); err == nil {
			t.Fatalf("accepted %q", out)
		}
	}
	d := Descriptions{Service: f.lifecycle, Models: descriptionModel(""), Completion: descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
		t.Fatal("no model called")
		return "", nil, nil
	})}
	if err := d.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	held, _ := f.lifecycle.projects.ByID(ctx, p.ID)
	if held.Blurb != "Current description" || held.SettingsRevision != 1 {
		t.Fatalf("failed generation wrote: %+v", held)
	}
}

func TestSeedQueuesDescriptionFromCommittedReadme(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, true)
	q := new(descriptionQueue)
	f.lifecycle.jobs = q
	p, _ := copySource(t, f)
	if len(q.requests) != 2 || !strings.Contains(q.requests[1].Key, p.ID.String()+"/seed/") {
		t.Fatalf("seed jobs: %+v", q.requests)
	}
	repo, err := p.Repository(f.lifecycle.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("Uncommitted misleading words"), 0600); err != nil {
		t.Fatal(err)
	}
	d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(_ context.Context, c modelcall.Call) (string, *modelcall.Usage, error) {
		if !strings.Contains(c.User, "the shop") || strings.Contains(c.User, "Uncommitted") {
			t.Fatalf("README evidence: %s", c.User)
		}
		return "Shop records", nil, nil
	})}
	if _, err := d.Run(t.Context(), q.requests[1].Key, nil); err != nil {
		t.Fatal(err)
	}
	held, err := f.lifecycle.projects.ByID(t.Context(), p.ID)
	if err != nil || held.Blurb != "Shop records" {
		t.Fatalf("seed description: %+v %v", held, err)
	}
}

// Model punctuation is normalised before the phrase's limits are checked.
func TestDescriptionNormalisesModelPunctuation(t *testing.T) {
	t.Parallel()
	f, ctx := requestedOwner(t)
	p, err := f.lifecycle.Create(ctx, Draft{Slug: "trucks", Actor: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{"Truck assignments and servicing.", `"Truck assignments and servicing"`, `  "Truck assignments and servicing."  `, "'Truck assignments and servicing.'", "“Truck assignments and servicing.”"} {
		t.Run(out, func(t *testing.T) {
			d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) { return out, nil, nil })}
			if err := d.Refresh(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			held, err := f.lifecycle.projects.ByID(ctx, p.ID)
			if err != nil || held.Blurb != "Truck assignments and servicing" {
				t.Fatalf("normalised description: %+v %v", held, err)
			}
		})
	}
}

// Run's error is what the job runner sends to the error reporter. Expected
// model unavailability completes quietly; a storage fault must still reach it.
func TestDescriptionJobReportsStorageFaultsButNotModelUnavailability(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"no model", "model failure", "database failure"} {
		t.Run(state, func(t *testing.T) {
			f, ctx := requestedOwner(t)
			p, err := f.lifecycle.Create(ctx, Draft{Slug: "trucks", Actor: "boss"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.lifecycle.projects.PublishDescription(ctx, p.ID, "Current description"); err != nil {
				t.Fatal(err)
			}
			d := Descriptions{Service: f.lifecycle, Models: descriptionModel("fixture/fast"), Completion: descriptionAnswer(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
				if state != "model failure" {
					t.Fatal("unexpected model call")
				}
				return "", nil, errors.New("provider unavailable")
			})}
			if state == "no model" {
				d.Models = descriptionModel("")
			}
			if state == "database failure" {
				if err := f.db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, err = d.Run(ctx, p.ID.String()+"/landed/test", nil)
			if state == "database failure" {
				if err == nil {
					t.Fatal("database fault was swallowed")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected outcome reported as fault: %v", err)
			}
			held, err := f.lifecycle.projects.ByID(ctx, p.ID)
			if err != nil || held.Blurb != "Current description" || held.SettingsRevision != 1 {
				t.Fatalf("unavailable model changed description: %+v %v", held, err)
			}
		})
	}
}
