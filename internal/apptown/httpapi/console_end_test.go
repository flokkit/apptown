package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/console"
)

type enderFunc func(context.Context, string) error

func (f enderFunc) EndBy(ctx context.Context, id string) error { return f(ctx, id) }

// Only a chat that does not exist is 404, which the page reads as already
// gone; a failure to end it is not.
//
// Kills: answering every unclassified failure 404.
func TestEndingAChatAnswers404OnlyForNoSuchChat(t *testing.T) {
	t.Parallel()
	for err, want := range map[error]int{console.ErrNoSession: 404, errors.New("the database is unavailable"): 500} {
		w := httptest.NewRecorder()
		NewConsoleEnd(enderFunc(func(context.Context, string) error { return err })).ConsoleEnd(w, httptest.NewRequest("DELETE", "/", nil))
		if w.Code != want {
			t.Fatalf("%v: %d, want %d", err, w.Code, want)
		}
	}
}
