package docker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// The start sweep removes what an AppTown before 1.0 left that is
// disposable: an old preview, chat, step and task container, a chat's
// network and cache volume. It keeps a standing deployment's container
// (env), the built-in database (infra), a need volume (owner only),
// anything already labelled apptown.*, and anything unlabelled.
//
// Kills: a sweep that removes env or infra, a need volume, or a current
// thing; one that misses a task container, which carries no class.
func TestTheStartSweepRemovesOnlyWhatOldReleasesLeftToThrowAway(t *testing.T) {
	t.Parallel()
	lists := map[string]string{
		"ps": "p1\tpreview\tforge.class=preview,forge.owner=x\n" +
			"c1\tconsole\tforge.class=console,forge.writes=/srv/a\n" +
			"s1\tstep\tforge.class=step,forge.step=1\n" +
			"t1\t\tforge.task=k,forge.request=r\n" +
			"e1\tenv\tforge.class=env,forge.owner=env-shop-main\n" +
			"db\tinfra\tforge.class=infra,forge.instance=i\n" +
			"n1\t\tapptown.class=preview,apptown.owner=y\n" +
			"u1\t\tsomething=else\n",
		"network": "net1\t\tforge.console=chat-1\n" + "net2\t\t\n" + "net3\tservice\tapptown.class=service\n",
		"volume": "cache1\tconsole-cache\tforge.class=console-cache,forge.console=chat-1\n" +
			"need1\t\tforge.owner=env-shop-main\n" + "pg\tinfra\tforge.class=infra,forge.instance=i\n",
	}
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		a := req.Args
		switch {
		case a[0] == "ps":
			return []byte(lists["ps"]), nil
		case a[0] == "network" && a[1] == "ls":
			return []byte(lists["network"]), nil
		case a[0] == "volume" && a[1] == "ls":
			return []byte(lists["volume"]), nil
		}
		return nil, nil
	}}
	if err := SweepBefore1(context.Background(), Scope{Client: client}); err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, a := range client.args {
		if (a[0] == "rm") || (len(a) > 1 && a[1] == "rm") {
			removed = append(removed, a[len(a)-1])
		}
	}
	slices.Sort(removed)
	if want := []string{"c1", "cache1", "net1", "p1", "s1", "t1"}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
}

// A sweep that cannot list or remove what an old release left is an error
// naming it: a folder an old container still writes would otherwise read
// as free.
//
// Kills: a sweep that logs a failure and lets the start go on.
func TestAStartSweepThatCannotFinishFails(t *testing.T) {
	t.Parallel()
	client := &fakeClient{reply: func(req Command) ([]byte, error) {
		switch req.Args[0] {
		case "ps":
			return []byte("c1\tconsole\tforge.class=console\n"), nil
		case "rm":
			return []byte("Error response from daemon: cannot remove container c1: it is busy"), errors.New("exit status 1")
		}
		return nil, nil
	}}
	err := SweepBefore1(context.Background(), Scope{Client: client})
	if err == nil || !strings.Contains(err.Error(), "c1") {
		t.Fatalf("a failed removal: %v", err)
	}
}

// A container made before 1.0 carries its identity under forge.*; one made
// now under apptown.*. Both are read, the current key first, so take-down
// and resume recognise an app container a release before this one booted.
//
// Kills: an identity read only under the current keys (take-down of every
// deployment booted before the landing refused), or the old key preferred.
func TestIdentityIsReadUnderTheKeysEitherReleaseWrote(t *testing.T) {
	t.Parallel()
	old := map[string]string{"forge.environment": "41", "forge.generation": "3", "forge.artifact": "h"}
	if got := deploy.IdentityLabels(old); got[dockerlabel.Deployment] != "41" || got[dockerlabel.Generation] != "3" || got[dockerlabel.Artifact] != "h" {
		t.Fatalf("old keys: %v", got)
	}
	both := map[string]string{dockerlabel.Deployment: "new", "forge.environment": "41"}
	if got := deploy.IdentityLabels(both); got[dockerlabel.Deployment] != "new" {
		t.Fatalf("both keys: %v", got)
	}
	id := deploy.DockerIdentity{Deployment: "41", Generation: 3, Artifact: "h"}
	if err := VerifyIdentity(id, Runtime{Labels: old, Image: "img", Running: true}, "img"); err != nil {
		t.Fatalf("an app container booted before this release: %v", err)
	}
}
