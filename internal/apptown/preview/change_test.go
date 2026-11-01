package preview

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

type hostFunc func(context.Context, Change, bool) (Result, error)

func (f hostFunc) BootPreview(ctx context.Context, ch Change, restart bool) (Result, error) {
	return f(ctx, ch, restart)
}

// The trusted boot (chat rehearsal) owns the change's boot interval for the
// host's whole call, stamps the selected project on what it returns, and
// settles the interval with the host's failure; it refuses an execution with
// no host or no project before taking the interval.
func TestBootChangeOwnsTheIntervalAndSettlesIt(t *testing.T) {
	t.Parallel()
	var runtime previewRuntime
	ch := Change{ID: idstest.N(7), ProjectID: idstest.N(3), SourceRevision: "selected"}
	for _, bad := range []struct {
		ch   Change
		host host
	}{{ch, nil}, {Change{ID: idstest.N(7)}, hostFunc(func(context.Context, Change, bool) (Result, error) {
		t.Fatal("an invalid execution reached the host")
		return Result{}, nil
	})}} {
		if _, err := runtime.bootChange(context.Background(), bad.ch, true, bad.host); err == nil {
			t.Fatalf("invalid execution admitted: %+v", bad.ch)
		}
	}
	if got := runtime.inspect(context.Background(), changeKey(idstest.N(7)), facts{}); got.State != cold || !got.At.IsZero() {
		t.Fatalf("a refused execution took the interval: %+v", got)
	}
	failed := errors.New("the app did not come up")
	host := hostFunc(func(_ context.Context, got Change, restart bool) (Result, error) {
		if got != ch || !restart {
			t.Fatalf("host selection: %+v restart=%t", got, restart)
		}
		if s := runtime.inspect(context.Background(), changeKey(idstest.N(7)), facts{URL: "https://old"}); s.State != stateStarting {
			t.Fatalf("the host ran outside the boot interval: %+v", s)
		}
		return Result{ProjectID: idstest.N(999), URL: "https://preview"}, failed
	})
	if out, err := runtime.bootChange(context.Background(), ch, true, host); !errors.Is(err, failed) || out != (Result{}) {
		t.Fatalf("a failed boot: %+v %v", out, err)
	}
	if got := runtime.inspect(context.Background(), changeKey(idstest.N(7)), facts{}); got.State != Broken || got.Reason != failed.Error() {
		t.Fatalf("settlement: %+v", got)
	}
	failed = nil
	out, err := runtime.bootChange(context.Background(), ch, true, host)
	if err != nil || out != (Result{ProjectID: idstest.N(3), URL: "https://preview"}) {
		t.Fatalf("a boot: %+v %v", out, err)
	}
}
