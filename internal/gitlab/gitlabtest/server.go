// Package gitlabtest is a fake GitLab for tests of code that reaches GitLab
// through internal/gitlab. It serves the merge request, project and branch
// endpoints AppTown uses, keeps each merge request's state and the
// descriptions written to it, and records the merges asked of it. What a
// merge does to Git is the test's business: it supplies OnMerge.
package gitlabtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/gitlab"
)

// Token is the only token the fake accepts.
const Token = "test-token"

// Merge is one merge call as the fake received it, with the project
// settings in force when it arrived.
type Merge struct {
	Project  string
	IID      int
	SHA      string
	Message  string
	Squash   bool
	Settings gitlab.MergeSettings
}

// Refusal is an answer a hook gives GitLab's way: an HTTP status and message.
type Refusal struct {
	Status  int
	Message string
}

func (r *Refusal) Error() string { return fmt.Sprintf("HTTP %d: %s", r.Status, r.Message) }

// Server is the fake. Its hooks run with its lock held, so they may change
// the merge request they are handed but must not call the server.
type Server struct {
	URL string

	mu           sync.Mutex
	settings     gitlab.MergeSettings
	requests     map[string]*gitlab.MergeRequest
	descriptions map[string][]string
	merges       []Merge
	describeErr  *Refusal
	created      int

	// OnRead refreshes a merge request before it is served (a head that
	// follows a real remote, say).
	OnRead func(project string, mr *gitlab.MergeRequest)
	// OnMerge performs a merge. A nil error marks the request merged; a
	// *Refusal is answered as GitLab would, any other error as a 500.
	OnMerge func(m Merge, mr *gitlab.MergeRequest) error
	// OnBranch answers a branch's head commit.
	OnBranch func(project, branch string) (string, error)
}

// New starts a fake GitLab that closes when the test ends. Its projects merge
// fast-forward, never squash and do not rebase automatically until told
// otherwise.
func New(t testing.TB) *Server {
	t.Helper()
	no := false
	s := &Server{
		settings:     gitlab.MergeSettings{MergeMethod: "ff", SquashOption: "default_off", AutomaticRebase: &no},
		requests:     map[string]*gitlab.MergeRequest{},
		descriptions: map[string][]string{},
	}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	s.URL = server.URL
	return s
}

// Client is a provider client for this fake.
func (s *Server) Client() (*gitlab.Client, error) { return gitlab.New(s.URL, Token) }

// Settings sets how every project merges, as GitLab reports it.
func (s *Server) Settings(settings gitlab.MergeSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
}

// Put records a merge request as GitLab holds it.
func (s *Server) Put(project string, mr gitlab.MergeRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[key(project, mr.IID)] = &mr
}

// Update changes a recorded merge request.
func (s *Server) Update(project string, iid int, change func(*gitlab.MergeRequest)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mr := s.requests[key(project, iid)]; mr != nil {
		change(mr)
	}
}

// Request is a recorded merge request and whether it exists.
func (s *Server) Request(project string, iid int) (gitlab.MergeRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mr := s.requests[key(project, iid)]
	if mr == nil {
		return gitlab.MergeRequest{}, false
	}
	return *mr, true
}

// Descriptions are the descriptions a merge request was given, oldest first:
// the one it was opened with, then each replacement.
func (s *Server) Descriptions(project string, iid int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.descriptions[key(project, iid)]...)
}

// Merges are the merge calls received, in order, refused ones included.
func (s *Server) Merges() []Merge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Merge(nil), s.merges...)
}

// RefuseDescriptions makes every description update fail with r; nil
// accepts them again.
func (s *Server) RefuseDescriptions(r *Refusal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.describeErr = r
}

func key(project string, iid int) string { return project + "!" + strconv.Itoa(iid) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("PRIVATE-TOKEN") != Token {
		http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/"), "/")
	project, err := url.PathUnescape(parts[0])
	if err != nil || project == "" || !strings.HasPrefix(r.URL.EscapedPath(), "/api/v4/projects/") {
		http.Error(w, "404 Not Found", http.StatusNotFound)
		return
	}
	rest := parts[1:]
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		reply(w, s.settings)
	case len(rest) == 3 && rest[0] == "repository" && rest[1] == "branches" && r.Method == http.MethodGet:
		branch, _ := url.PathUnescape(rest[2])
		if s.OnBranch == nil {
			http.Error(w, "404 Branch Not Found", http.StatusNotFound)
			return
		}
		head, err := s.OnBranch(project, branch)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, map[string]any{"commit": map[string]string{"id": head}})
	case len(rest) == 1 && rest[0] == "merge_requests" && r.Method == http.MethodPost:
		s.create(w, r, project)
	case len(rest) >= 2 && rest[0] == "merge_requests":
		iid, err := strconv.Atoi(rest[1])
		mr := s.requests[key(project, iid)]
		if err != nil || mr == nil {
			http.Error(w, "404 Not found", http.StatusNotFound)
			return
		}
		switch {
		case len(rest) == 2 && r.Method == http.MethodGet:
			if s.OnRead != nil {
				s.OnRead(project, mr)
			}
			reply(w, mr)
		case len(rest) == 2 && r.Method == http.MethodPut:
			s.describe(w, r, project, mr)
		case len(rest) == 3 && rest[2] == "merge" && r.Method == http.MethodPut:
			s.merge(w, r, project, mr)
		default:
			http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "404 Not Found", http.StatusNotFound)
	}
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, project string) {
	var in struct {
		Source      string `json:"source_branch"`
		Target      string `json:"target_branch"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Source == "" || in.Target == "" {
		http.Error(w, "400 Bad request", http.StatusBadRequest)
		return
	}
	for _, mr := range s.requests {
		if mr.State == "opened" && mr.SourceBranch == in.Source {
			http.Error(w, `409 {"message":["Another open merge request already exists for this source branch"]}`, http.StatusConflict)
			return
		}
	}
	s.created++
	mr := &gitlab.MergeRequest{IID: s.created, State: "opened", SourceBranch: in.Source, TargetBranch: in.Target,
		WebURL: fmt.Sprintf("%s/%s/-/merge_requests/%d", s.URL, project, s.created)}
	s.requests[key(project, mr.IID)] = mr
	s.descriptions[key(project, mr.IID)] = []string{in.Description}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(mr)
}

func (s *Server) describe(w http.ResponseWriter, r *http.Request, project string, mr *gitlab.MergeRequest) {
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "400 Bad request", http.StatusBadRequest)
		return
	}
	description, ok := in["description"].(string)
	if !ok || len(in) != 1 {
		http.Error(w, "400 the fake updates descriptions only", http.StatusBadRequest)
		return
	}
	if s.describeErr != nil {
		fail(w, s.describeErr)
		return
	}
	k := key(project, mr.IID)
	s.descriptions[k] = append(s.descriptions[k], description)
	reply(w, mr)
}

func (s *Server) merge(w http.ResponseWriter, r *http.Request, project string, mr *gitlab.MergeRequest) {
	var in struct {
		SHA     string `json:"sha"`
		Message string `json:"merge_commit_message"`
		Squash  bool   `json:"squash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "400 Bad request", http.StatusBadRequest)
		return
	}
	call := Merge{Project: project, IID: mr.IID, SHA: in.SHA, Message: in.Message, Squash: in.Squash, Settings: s.settings}
	s.merges = append(s.merges, call)
	if s.OnRead != nil {
		s.OnRead(project, mr)
	}
	if mr.State != "opened" {
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if in.SHA != "" && in.SHA != mr.SHA {
		http.Error(w, "409 SHA does not match HEAD of source branch", http.StatusConflict)
		return
	}
	if s.OnMerge != nil {
		if err := s.OnMerge(call, mr); err != nil {
			fail(w, err)
			return
		}
	}
	mr.State = "merged"
	reply(w, mr)
}

func reply(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, err error) {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		http.Error(w, refusal.Message, refusal.Status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
