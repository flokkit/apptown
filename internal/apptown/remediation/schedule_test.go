package remediation

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestRemediationSchedulerCapacityAndRoundRobin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		free       int
		firstLimit int
		fail       bool
		want       []ids.ID
	}{
		{"round robin", 4, 3, false, []ids.ID{idstest.N(1), idstest.N(4), idstest.N(2), idstest.N(5)}},
		{"job limit", 4, 1, false, []ids.ID{idstest.N(1), idstest.N(4), idstest.N(5)}},
		{"committed dispatch failure consumes capacity", 1, 3, true, []ids.ID{idstest.N(1)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var started []ids.ID
			s := scheduler{
				queue: func(context.Context) (remediationQueue, error) {
					return remediationQueue{Free: test.free, Jobs: []queueJob{
						{Free: test.firstLimit, Items: []Item{{ID: idstest.N(1), ProjectID: idstest.N(1)}, {ID: idstest.N(2), ProjectID: idstest.N(2)}, {ID: idstest.N(3), ProjectID: idstest.N(3)}}},
						{Free: 3, Items: []Item{{ID: idstest.N(4), ProjectID: idstest.N(4)}, {ID: idstest.N(5), ProjectID: idstest.N(5)}, {ID: idstest.N(6), ProjectID: idstest.N(1)}}},
					}}, nil
				},
				cooling: func() bool { return false }, busy: func(context.Context, ids.ID) bool { return false },
				start: func(_ context.Context, item Item) (*workflow.Run, error) {
					started = append(started, item.ID)
					if test.fail {
						return &workflow.Run{}, dbtest.ErrInjectedFailure
					}
					return &workflow.Run{}, nil
				},
			}
			err := s.fill(context.Background())
			if !reflect.DeepEqual(started, test.want) || errors.Is(err, dbtest.ErrInjectedFailure) != test.fail {
				t.Fatalf("started %v, error %v; want %v", started, err, test.want)
			}
		})
	}
}

func TestRemediationSchedulerStopsWhenCooldownBegins(t *testing.T) {
	t.Parallel()
	for _, alreadyCooling := range []bool{false, true} {
		t.Run(map[bool]string{false: "during pass", true: "before pass"}[alreadyCooling], func(t *testing.T) {
			cooling := alreadyCooling
			reads, starts := 0, 0
			s := scheduler{
				queue: func(context.Context) (remediationQueue, error) {
					reads++
					return remediationQueue{Free: 2, Jobs: []queueJob{{Free: 2, Items: []Item{{ID: idstest.N(1)}, {ID: idstest.N(2)}}}}}, nil
				},
				cooling: func() bool { return cooling }, busy: func(context.Context, ids.ID) bool { return false },
				start: func(context.Context, Item) (*workflow.Run, error) {
					starts++
					cooling = true
					return &workflow.Run{}, nil
				},
			}
			if err := s.fill(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := 1
			if alreadyCooling {
				want = 0
			}
			if reads != want || starts != want {
				t.Fatalf("reads %d starts %d, want %d", reads, starts, want)
			}
		})
	}
}

func TestRemediationSchedulerStopsBetweenAdmissions(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var starts int
	s := scheduler{
		queue: func(context.Context) (remediationQueue, error) {
			return remediationQueue{Free: 2, Jobs: []queueJob{{Free: 2, Items: []Item{{ID: idstest.N(1)}, {ID: idstest.N(2)}}}}}, nil
		},
		cooling: func() bool { return false }, busy: func(context.Context, ids.ID) bool { return false },
		start: func(context.Context, Item) (*workflow.Run, error) { starts++; cancel(); return nil, nil },
	}
	if err := s.fill(ctx); !errors.Is(err, context.Canceled) || starts != 1 {
		t.Fatalf("scheduler ignored stop: starts=%d error=%v", starts, err)
	}
}
