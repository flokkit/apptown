package apptown

import (
	"context"
	"strings"
	"testing"
)

func TestDockerClientReachesOwnersAndRemainsLocalToItsApp(t *testing.T) {
	t.Parallel()
	firstDaemon, secondDaemon := newFakeDocker(), newFakeDocker()
	firstDaemon.reply = func(args []string) (string, error, bool) {
		if strings.HasPrefix(strings.Join(args, " "), "image inspect") {
			return imageInspectAnswer(map[string]string{"apptown.pi-subagents.version": "0.61.0"}), nil, true
		}
		return "", nil, false
	}
	secondDaemon.reply = func(args []string) (string, error, bool) {
		if strings.HasPrefix(strings.Join(args, " "), "image inspect") {
			return imageInspectAnswer(nil), nil, true
		}
		return "", nil, false
	}
	input := Config{DockerClient: firstDaemon}
	first := testApp(t, input)
	home := piHomeFor(first) // retains a reference to the first App's scope
	input.DockerClient = secondDaemon
	second := testApp(t, input)

	if home.Delegation(context.Background()) == "" || piHomeFor(second).Delegation(context.Background()) != "" {
		t.Fatal("agent preparation did not observe its own daemon's image")
	}
	// The reaper captured its scope by value during New. Calling that owner
	// directly catches late fake installation that would leave it on the CLI.
	first.reaper.Containers(context.Background(), true)
	second.reaper.Containers(context.Background(), true)
	for _, daemon := range []*fakeDocker{firstDaemon, secondDaemon} {
		if calls := daemon.matching("ps -a "); len(calls) != 1 {
			t.Fatalf("constructed reaper did not query its own daemon: %v", daemon.joined())
		}
		if calls := daemon.matching("image inspect "); len(calls) != 1 {
			t.Fatalf("agent preparation did not query its own daemon: %v", daemon.joined())
		}
	}
}
