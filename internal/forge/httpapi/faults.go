package httpapi

import (
	"net/http"

	"flokkit.io/forge/internal/fault"
)

// WriteFault answers an owner's error with the status its kind calls for:
// the one table every handler shares, so the same failure reads the same way
// on every route. An unclassified error is a 500.
func WriteFault(w http.ResponseWriter, err error) {
	WriteFaultOr(w, err, http.StatusInternalServerError)
}

// WriteFaultOr is WriteFault with the status to use when nothing in err's chain
// says what kind of failure it is.
func WriteFaultOr(w http.ResponseWriter, err error, unclassified int) {
	// A missing row the owner did not classify is not "not found": after a
	// write that returns its row it means the write was not recorded. Owners
	// mark their reads with fault.NotFound; anything else stays a failure.
	kind := fault.KindOf(err)
	if code := fault.CodeOf(err); code != "" {
		w.Header().Set("X-AppTown-Deny-Code", code)
	}
	if kind == fault.Internal {
		WriteError(w, unclassified, "%v", err)
		return
	}
	WriteError(w, FaultStatus(kind, unclassified), "%s", fault.Message(err))
}

// WriteRefusal answers err and reports true when its kind is anything but
// internal; otherwise it writes nothing, so the caller can still classify it.
func WriteRefusal(w http.ResponseWriter, err error) bool {
	if fault.KindOf(err) == fault.Internal {
		return false
	}
	WriteFault(w, err)
	return true
}

// FaultStatus is the HTTP status for a kind.
func FaultStatus(kind fault.Kind, unclassified int) int {
	switch kind {
	case fault.Invalid:
		return http.StatusBadRequest
	case fault.Unauthenticated:
		return http.StatusUnauthorized
	case fault.Forbidden:
		return http.StatusForbidden
	case fault.NotFound:
		return http.StatusNotFound
	case fault.Conflict:
		return http.StatusConflict
	case fault.Unprocessable:
		return http.StatusUnprocessableEntity
	case fault.Unavailable:
		return http.StatusServiceUnavailable
	}
	return unclassified
}
