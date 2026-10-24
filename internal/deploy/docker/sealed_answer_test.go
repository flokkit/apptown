package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
)

// A sealed probe's status is the probe container's answer file: one that
// did not come back is no answer, never a status read from what the
// container printed.
//
// Kills: a status read from the probe's stdout.
func TestASealedProbesStatusIsItsAnswerFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		answer []byte
		want   int
		err    error
	}{
		{[]byte("204\n\n"), 204, nil},
		{nil, 0, ErrNoAnswer},
		{[]byte("garbage\n"), 0, ErrNoAnswer},
	} {
		results := &containerresulttest.Daemon{Answers: func(containerresulttest.Command) (containerresulttest.Answer, bool) {
			return containerresulttest.Answer{Answer: tc.answer}, true
		}}
		client := clientFunc(func(c Command) ([]byte, error) {
			out, err, _ := results.RunEnv(c.Args, c.Env)
			return out, err
		})
		got, err := (&Sealed{ProbeImage: "probe", Host: "app.example"}).Get(context.Background(), Scope{Client: client}, "net", "app", 8080, "/ready")
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Fatalf("answer %q: got %d, %v; want %d, %v", tc.answer, got, err, tc.want, tc.err)
		}
	}
}
