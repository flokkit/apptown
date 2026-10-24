package worker

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/stock"
)

// exitedAsAsked is a backend whose task has exited, and the request that
// asked is cancelled as it answers. Nothing else of a driver is used.
type exitedAsAsked struct {
	execution.TaskDriver
	cancel context.CancelFunc
}

func (d exitedAsAsked) Wait(context.Context, execution.TaskRef) (int, error) {
	d.cancel()
	return 0, nil
}

// A task that exited but whose private Docker could not be stopped reads
// "running" after a beat, so its client asks again; a client that has gone
// is not kept for the beat. In the bubble the beat would be a durable
// block: the handler must finish without any time passing.
func TestAWaitForAnUnquietTaskEndsWithItsRequest(t *testing.T) {
	t.Parallel()
	// A Docker that refuses every command; none is asked: the request has
	// ended before quiet runs it.
	fake := dockerFunc(func([]string) ([]byte, error) { return []byte("refused\n"), errors.New("exit status 1") })
	state := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s := &Server{ToolVersions: stock.Versions(), State: state, Docker: fake, Tasks: exitedAsAsked{cancel: cancel}}
		if err := s.writeRecord(record{Key: "agent-1", Private: &privateSet{Network: "apptown-private-agent-1", Services: []string{"apptown-private-agent-1-postgres"}}}); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/tasks/agent-1/wait", nil).WithContext(ctx)
		req.SetPathValue("key", "agent-1")
		w := httptest.NewRecorder()
		done := make(chan struct{})
		start := time.Now()
		go func() {
			s.wait(w, req)
			close(done)
		}()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the beat outlived the request that asked")
		}
		if waited := time.Since(start); waited != 0 {
			t.Fatalf("the handler waited %s for a request that had gone", waited)
		}
		if !strings.Contains(w.Body.String(), `"running":true`) {
			t.Fatalf("an unquiet task answered %s", w.Body.String())
		}
	})
}
