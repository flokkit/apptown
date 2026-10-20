package preview

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/ids/idstest"
)

// The FIFO only pauses the contract read inside real preparation. Restore
// the pathname before releasing that read so later fingerprinting sees a
// normal file. Neither production code nor the worker is instrumented.
func TestADiscardDuringPreparationWins(t *testing.T) {
	for _, restart := range []bool{false, true} {
		label := "ordinary"
		if restart {
			label = "restart"
		}
		t.Run(label, func(t *testing.T) {
			s, w, _, dir, pid := workerPreviewWorld(t)
			ws := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
			writePreviewBootFixture(t, ws)
			contract := filepath.Join(ws, ".apptown", "app.yaml")
			body, err := os.ReadFile(contract)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(contract); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(contract, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: pid}, restart)
				done <- err
			}()
			opened := make(chan *os.File, 1)
			openErr := make(chan error, 1)
			go func() {
				f, err := os.OpenFile(contract, os.O_WRONLY, 0)
				if err != nil {
					openErr <- err
					return
				}
				opened <- f
			}()
			var pipe *os.File
			select {
			case pipe = <-opened:
			case err := <-openErr:
				t.Fatal(err)
			case err := <-done:
				t.Fatalf("boot ended before contract read: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("boot never read contract")
			}
			defer pipe.Close()
			if err := s.DiscardChangePreview(context.Background(), idstest.N(9)); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(contract); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(contract, body, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := pipe.Write(body); err != nil {
				t.Fatal(err)
			}
			pipe.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("boot did not finish")
			}
			if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
				t.Fatal(err)
			}
			if w.holds(docker.PreviewChangeName(idstest.N(9))) {
				t.Fatal("discard during preparation was forgotten; reconciliation kept the late boot")
			}
		})
	}
}

func TestALocalReplacementKeepsItsRecord(t *testing.T) {
	t.Parallel()
	s, _, _, dir, pid := previewBootWorld(t, "")
	repo := filepath.Join(dir, "workspaces", "project-p")
	writePreviewBootFixture(t, repo)
	first := gitCommitAll(t, repo, "first")
	ch := Change{ID: idstest.N(9), ProjectID: pid, SourceRevision: first}
	if _, err := s.RehearseChange(context.Background(), ch, false); err != nil {
		t.Fatal(err)
	}
	name := docker.PreviewChangeName(idstest.N(9))
	if _, ok := s.servedRecordOf(name); !ok {
		t.Fatal("fixture: first boot unrecorded")
	}
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM scratch\n# next commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ch.SourceRevision = gitCommitAll(t, repo, "replacement")
	out, err := s.RehearseChange(context.Background(), ch, false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.hosts.Local().Running(context.Background(), name) || s.servedIncarnation(context.Background(), name) != out.Incarnation {
		t.Fatal("fixture: replacement not running")
	}
	record, ok := s.servedRecordOf(name)
	if !ok || record.Source != ch.SourceRevision {
		t.Fatalf("successful local replacement has no current served record: found=%v record=%+v", ok, record)
	}
}

func TestARestartAndALaterReuseKeepTheirRecords(t *testing.T) {
	t.Parallel()
	s, w, _, dir, pid := workerPreviewWorld(t)
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	ch := Change{ID: idstest.N(9), ProjectID: pid}
	name := docker.PreviewChangeName(idstest.N(9))
	for _, restart := range []bool{false, false, true, false} {
		out, err := s.RehearseChange(context.Background(), ch, restart)
		if err != nil {
			t.Fatal(err)
		}
		rec, ok := s.servedRecordOf(name)
		if !ok || rec.Incarnation != out.Incarnation {
			t.Fatal("legitimate boot not recorded")
		}
		if err := s.ReconcileWorkerPreviews(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !w.holds(name) {
			t.Fatal("reconciliation removed recorded boot")
		}
		if !restart {
			if err := s.DiscardChangePreview(context.Background(), idstest.N(9)); err != nil {
				t.Fatal(err)
			}
		}
	}
}
