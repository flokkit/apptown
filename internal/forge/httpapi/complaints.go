package httpapi

import (
	"net/http"
	"strings"
	"time"

	"flokkit.io/forge/internal/forge/complaint"
	"flokkit.io/forge/internal/forge/identity"
)

type Complaints struct {
	complaints      *complaint.Box
	access          *identity.Service
	stepSteering    string
	consoleSteering string
}

func NewComplaints(complaints *complaint.Box, access *identity.Service, stepSteering string, consoleSteering string) Complaints {
	return Complaints{complaints: complaints, access: access, stepSteering: stepSteering, consoleSteering: consoleSteering}
}

// ListComplaints: GET /api/v1/complaints — newest first.
func (h Complaints) ListComplaints(w http.ResponseWriter, r *http.Request) {
	out, err := h.complaints.All(r.Context(), 300)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ComplaintList{Complaints: out})
}

// AnswerComplaint: POST /api/v1/complaints/{id}/answer {answer} — put
// the resolution ON the complaint. Answering marks it seen: an answered
// complaint is by definition dealt with. The answer is the operator's act
// and shows to every future reader (including agents' forge complaints).
func (h Complaints) AnswerComplaint(w http.ResponseWriter, r *http.Request) {
	if !RequireAdmin(h.access, w, r) {
		return
	}
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "an answer is required"}, func(in struct {
		Answer string `json:"answer"`
	}) {
		if strings.TrimSpace(in.Answer) == "" {
			WriteError(w, 400, "an answer is required")
			return
		}

		// Answered is its own state: the answer can be read before a person
		// archives it (New → Answered → Archived).
		if err := h.complaints.Answer(r.Context(), id, in.Answer, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// ArchiveComplaint: POST /api/v1/complaints/{id}/archive — out of the
// inbox without an answer: read, not worth a reply, or overtaken by events.
// The record stays; the archive is the history.
func (h Complaints) ArchiveComplaint(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if err := h.complaints.Archive(r.Context(), id, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ArchiveOldComplaints: POST /api/v1/complaints/archive-older {hours}
// — sweep the inbox of everything older than the window (default a day).
func (h Complaints) ArchiveOldComplaints(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Hours float64 `json:"hours"`
	}) {
		if in.Hours <= 0 {
			in.Hours = 24
		}
		n, err := h.complaints.ArchiveOlder(r.Context(), time.Duration(in.Hours*float64(time.Hour)), Actor(r))
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, ComplaintsArchived{OK: true, Archived: n})
	})
}

// DeleteComplaint: DELETE /api/v1/complaints/{id} — gone for good.
// The ledger keeps a line saying it was deleted and by whom; the text goes.
func (h Complaints) DeleteComplaint(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	if err := h.complaints.Delete(r.Context(), id, Actor(r)); err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ComplaintsSeen: POST /api/v1/complaints/seen — mark everything read.
func (h Complaints) ComplaintsSeen(w http.ResponseWriter, r *http.Request) {
	if err := h.complaints.MarkSeen(r.Context()); err != nil {
		WriteError(w, 500, "marking the complaints seen: %v", err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// ComplaintSteering: GET /api/v1/complaints/steering — the exact
// words agents are given about complaining. This text is PLATFORM, not
// guidance: it compiles into forged so no project can quietly mute its own
// agents' complaint channel. Shown to any signed-in person — the discipline
// agents work under should be inspectable by the people reading the box.
func (h Complaints) ComplaintSteering(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, 200, ComplaintSteeringText{
		Step:    h.stepSteering,
		Console: h.consoleSteering,
		Note: "Fixed platform text, compiled into AppTown (not editable guidance — a project must not be able to silence its own agents). " +
			"Every step's full steering, this included, is archived on its run as a receipt.",
	})
}

// ComplaintList is the complaint box, newest first.
type ComplaintList struct {
	Complaints []complaint.Complaint `json:"complaints"`
}

// ComplaintsArchived counts the complaints a sweep archived.
type ComplaintsArchived struct {
	OK       bool  `json:"ok"`
	Archived int64 `json:"archived"`
}

// ComplaintSteeringText is the fixed platform text agents are given about
// complaining.
type ComplaintSteeringText struct {
	Step    string `json:"step"`
	Console string `json:"console"`
	Note    string `json:"note"`
}
