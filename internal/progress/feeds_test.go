package progress

import (
	"strconv"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestFeedOffsetsAndDetachedTail(t *testing.T) {
	var feeds Feeds
	feeds.Append(idstest.N(1), "before start")
	if _, ok := feeds.Read(idstest.N(1), 0); ok {
		t.Fatal("append must not create a feed")
	}
	feeds.Start(idstest.N(1))
	for i := 0; i < maxLines+20; i++ {
		feeds.Append(idstest.N(1), strconv.Itoa(i))
	}
	snapshot, ok := feeds.Read(idstest.N(1), 0)
	if !ok || len(snapshot.Lines) != maxLines || snapshot.Total != maxLines+20 || snapshot.Lines[0] != "20" {
		t.Fatalf("retained tail: %+v", snapshot)
	}
	snapshot.Lines[0] = "caller mutation"
	last, _ := feeds.Read(idstest.N(1), maxLines+19)
	if len(last.Lines) != 1 || last.Lines[0] != strconv.Itoa(maxLines+19) {
		t.Fatalf("absolute offset: %+v", last)
	}
	ahead, _ := feeds.Read(idstest.N(1), maxLines+100)
	if len(ahead.Lines) != 0 || ahead.Total != maxLines+20 {
		t.Fatalf("offset ahead of producer: %+v", ahead)
	}
	tail, ok := feeds.Finish(idstest.N(1))
	if !ok || tail.Lines[0] != "20" {
		t.Fatal("reader mutated stored output")
	}
	feeds.Append(idstest.N(1), "late output")
	if _, ok := feeds.Read(idstest.N(1), 0); ok {
		t.Fatal("finished feed was resurrected")
	}
	feeds.Start(idstest.N(1))
	reset, _ := feeds.Read(idstest.N(1), 0)
	if reset.Total != 0 {
		t.Fatal("new feed retained old offsets")
	}
	feeds.discard(idstest.N(1))
	feeds.discard(idstest.N(1))
	if _, ok := feeds.Finish(idstest.N(1)); ok {
		t.Fatal("discarded feed has a final tail")
	}
}

func TestConcurrentWritersAndPollers(t *testing.T) {
	var feeds Feeds
	feeds.Start(idstest.N(1))
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				feeds.Append(idstest.N(1), "line")
				snapshot, _ := feeds.Read(idstest.N(1), 0)
				if len(snapshot.Lines) > 0 {
					snapshot.Lines[0] = "private copy"
				}
			}
		}()
	}
	workers.Wait()
	tail, _ := feeds.Finish(idstest.N(1))
	if tail.Total != 800 || len(tail.Lines) != maxLines || tail.Lines[0] != "line" {
		t.Fatalf("concurrent output lost or aliased: %+v", tail)
	}
}

func TestOversizedAppendKeepsOnlyItsTail(t *testing.T) {
	var feeds Feeds
	feeds.Start(idstest.N(1))
	lines := make([]string, maxLines*3)
	for i := range lines {
		lines[i] = strconv.Itoa(i)
	}
	feeds.Append(idstest.N(1), lines...)
	lines[len(lines)-1] = "changed after append"
	tail, _ := feeds.Finish(idstest.N(1))
	if tail.Total != maxLines*3 || len(tail.Lines) != maxLines || tail.Lines[0] != strconv.Itoa(maxLines*2) || tail.Lines[maxLines-1] != strconv.Itoa(maxLines*3-1) {
		t.Fatalf("large append: %+v", tail)
	}
}
