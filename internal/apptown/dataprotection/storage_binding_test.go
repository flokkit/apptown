package dataprotection

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

func TestPhysicalCopyObservesExactContainerEngineAndMount(t *testing.T) {
	id := strings.Repeat("a", 64)
	image := "sha256:" + strings.Repeat("b", 64)
	for _, change := range []string{"", "unknown", "image", "mount"} {
		t.Run(change, func(t *testing.T) {
			daemon := dockerfake.New()
			daemon.SetReply(func(args []string) ([]byte, error, bool) {
				if args[0] != "inspect" || args[2] != "{{json .}}" {
					return nil, errors.New("unexpected effect"), true
				}
				if change == "unknown" {
					return nil, errors.New("unavailable daemon"), true
				}
				actual := image
				if change == "image" {
					actual = "redis:7-alpine"
				}
				volume := "redis-data"
				if change == "mount" {
					volume = "other-data"
				}
				out, err := json.Marshal(map[string]any{"Id": id, "Image": actual, "Mounts": []map[string]string{{"Type": "volume", "Name": volume, "Destination": "/data"}}})
				return out, err, true
			})
			kind := &dependency.Kind{}
			kind.Local.Data = "/data"
			p := backupPlanItem{Kind: "need", Name: "redis", Container: "redis-service", Volume: "redis-data", Declared: kind}
			got, writer, err := physicalCopySource(t.Context(), docker.Scope{Client: daemon}, p)
			if (err != nil) != (change != "") {
				t.Fatalf("writer=%q err=%v", writer, err)
			}
			if change == "" && (writer != image || got.Container != id) {
				t.Fatalf("export not pinned: %+v writer=%q", got, writer)
			}
		})
	}
}
