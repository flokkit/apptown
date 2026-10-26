package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// WorkerAvailability describes a fresh protocol observation, not readiness of
// every tool or image on the worker. An unreadable answer is not unreachable.
type WorkerAvailability uint8

const (
	WorkerUnknown WorkerAvailability = iota
	WorkerAvailable
	WorkerUnavailable
)

// Availability uses the existing authenticated profile endpoint. It does not
// pull or inspect an agent image, start work, or cache a startup profile as proof
// of current reachability. Errors are diagnostic; public views use the state.
func (w WorkerTasks) Availability(ctx context.Context) (WorkerAvailability, error) {
	resp, err := w.do(ctx, http.MethodGet, "/v1/profile", nil, "")
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, ErrWorkerUnavailable) {
			return WorkerUnavailable, err
		}
		return WorkerUnknown, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return WorkerUnknown, errors.New("worker availability response was not successful")
	}
	var profile WorkerProfile
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&profile); err != nil {
		return WorkerUnknown, err
	}
	if profile.Name == "" {
		return WorkerUnknown, errors.New("worker availability response has no profile")
	}
	return WorkerAvailable, nil
}
