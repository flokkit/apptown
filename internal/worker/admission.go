package worker

import (
	"context"
	"errors"
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
		var holders []string
		entries, err := os.ReadDir(filepath.Join(s.State, "workspaces"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.lineErr = err
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				holders = append(holders, e.Name())
			}
		}
		entries, err = os.ReadDir(filepath.Join(s.State, "records"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			s.lineErr = err
			return
		}
		for _, e := range entries {
			key, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok {
				continue
			}
			rec, found, err := s.readRecord(key)
			if err != nil {
				s.lineErr = err
				return
			}
			if found && rec.Workspace == "" {
				holders = append(holders, key)
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
func (s *Server) freeSlot(key string) {
	// The line is initialized before any first write; initialize before release
	// too so reconstruction cannot restore the holder being removed.
	line, err := s.admissionLine()
	if err == nil {
		line.Release(key)
	}
}
