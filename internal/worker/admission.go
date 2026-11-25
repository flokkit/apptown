package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// DefaultTaskLimit fills the worker at the default four CPUs per task.
func DefaultTaskLimit() int { return max(1, runtime.NumCPU()/4) }

const admissionSlice = 20 * time.Second

func (s *Server) admissionLine() (*execution.AdmissionLine, error) {
	s.lineOnce.Do(func() {
		records, workspaces, err := s.onDisk()
		if err != nil {
			s.lineErr = err
			return
		}
		holders := workspaces
		for _, rec := range records {
			if rec.Workspace == "" {
				holders = append(holders, rec.Key)
			}
		}
		limit := s.TaskLimit
		if limit == 0 {
			limit = DefaultTaskLimit()
		}
		s.line = execution.NewAdmissionLine(limit, 2*admissionSlice, s.Now, holders)
	})
	return s.line, s.lineErr
}

// onDisk reads what the worker holds under a key: its task records not
// released and its workspace folders. The admission line's holders at start
// and the list AppTown reconciles (holdings) both come from it, so the slots
// held and what AppTown is shown cannot disagree.
func (s *Server) onDisk() ([]record, []string, error) {
	var workspaces []string
	entries, err := os.ReadDir(filepath.Join(s.State, "workspaces"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() && keyPattern.MatchString(e.Name()) {
			workspaces = append(workspaces, e.Name())
		}
	}
	var records []record
	entries, err = os.ReadDir(filepath.Join(s.State, "records"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !keyPattern.MatchString(key) {
			continue
		}
		rec, found, err := s.readRecord(key)
		if err != nil {
			return nil, nil, err
		}
		if found {
			records = append(records, rec)
		}
	}
	return records, workspaces, nil
}

// holdings is GET /v1/tasks: every task and workspace the worker holds,
// for AppTown to release those it no longer knows (a task of a process that
// stopped mid-call). The worker cannot judge that itself.
func (s *Server) holdings(w http.ResponseWriter, r *http.Request) {
	records, workspaces, err := s.onDisk()
	if err != nil {
		httpError(w, err)
		return
	}
	out := execution.WorkerHoldings{Tasks: []string{}, Workspaces: workspaces}
	if out.Workspaces == nil {
		out.Workspaces = []string{}
	}
	for _, rec := range records {
		out.Tasks = append(out.Tasks, rec.Key)
	}
	writeJSON(w, out)
}
func (s *Server) admission(w http.ResponseWriter, r *http.Request) {
	line, err := s.admissionLine()
	if err != nil {
		httpError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), admissionSlice)
	defer cancel()
	ahead, admitted, err := line.Admit(ctx, r.PathValue("key"), func() error { return s.admitNew(ctx) })
	if err != nil {
		execution.WriteRefusal(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, execution.AdmissionAnswer{Ahead: ahead, Admitted: admitted})
}
func (s *Server) consume(key string) error {
	line, err := s.admissionLine()
	if err != nil {
		return err
	}
	return line.Consume(key)
}

// holding refuses, as busy, a key that is not a live holder: work that
// shares a slot never acquires one.
func (s *Server) holding(key string) error {
	line, err := s.admissionLine()
	if err != nil {
		return err
	}
	if !line.Holds(key) {
		return fmt.Errorf("%w: %s holds no slot on this worker to share", execution.ErrBusy, key)
	}
	return nil
}
func (s *Server) freeSlot(key string) {
	// The line is initialized before any first write; initialize before release
	// too so reconstruction cannot restore the holder being removed.
	line, err := s.admissionLine()
	if err == nil {
		line.Release(key)
	}
}
