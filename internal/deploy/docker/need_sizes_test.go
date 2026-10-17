package docker

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"strings"
	"testing"
)

func TestResolvedNeedCommandAndCapArePreparedWithoutChangingInputs(t *testing.T) {
	c := &appcontract.Contract{Needs: []string{"mariadb"}, NeedSizes: map[string]appcontract.NeedSize{"mariadb": {Name: "small", Command: []string{"--small"}}}, NeedResources: map[string]appcontract.Resources{"mariadb": {MemoryBytes: 128 << 20}}}
	in := []deploy.ServiceSpec{{Name: "mariadb", Command: []string{"--default"}}}
	out, err := sizedServices(c, in)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Command[0] != "--small" || out[0].Resources.MemoryBytes != 128<<20 {
		t.Fatal("preset not carried in prepared services")
	}
	out[0].Command[0] = "mutated"
	if in[0].Command[0] != "--default" || c.NeedSizes["mariadb"].Command[0] != "--small" {
		t.Fatal("inputs changed")
	}
	c.NeedSizes["mariadb"] = appcontract.NeedSize{Name: "small"}
	if _, err := sizedServices(c, in); err == nil {
		t.Fatal("unresolved preset ignored")
	}
}
func TestStandingCommandChangeIsRefusedBeforeResizingRealData(t *testing.T) {
	for _, class := range []string{"production", "non-production", "", "synthetic"} {
		t.Run(class, func(t *testing.T) {
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				for _, arg := range req.Args {
					if strings.Contains(arg, "HostConfig.Memory") {
						return []byte("268435456 0"), nil
					}
					if strings.Contains(arg, "Config.Cmd") {
						return []byte(`["--old-buffers"]`), nil
					}
					if strings.Contains(arg, "Config.Image") {
						return []byte("mariadb:11"), nil
					}
				}
				return nil, nil
			}}
			svc := deploy.ServiceSpec{Name: "mariadb", Image: "mariadb:11", Command: []string{"--small"}, Resources: appcontract.Resources{MemoryBytes: 128 << 20}}
			_, err := EnsureStandingServices(context.Background(), Scope{Client: client}, "env-x-main", []deploy.ServiceSpec{svc}, class, Ownership{})
			if class == "synthetic" {
				if err != nil || !hasStandingCall(client.args, "rm -f") {
					t.Fatalf("synthetic replacement: %v %v", err, client.args)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "will not replace") {
					t.Fatalf("real-data command drift was not refused: %v", err)
				}
				if hasStandingCall(client.args, "update") || hasStandingCall(client.args, "rm -f") {
					t.Fatalf("changed a real-data service before refusal: %v", client.args)
				}
			}
		})
	}
}
func TestStandingCommandInspectionFailureIsNotAMatch(t *testing.T) {
	for _, raw := range []string{"", "invalid", "null", `[]`, `["--same"]`} {
		t.Run(raw, func(t *testing.T) {
			client := &fakeClient{reply: func(req Command) ([]byte, error) {
				for _, arg := range req.Args {
					if strings.Contains(arg, "Config.Cmd") {
						return []byte(raw), nil
					}
				}
				return nil, nil
			}}
			drift, err := ServiceDrift(t.Context(), Scope{Client: client}, "db", deploy.ServiceSpec{Name: "db", Command: []string{"--same"}})
			if raw == "" || raw == "invalid" {
				if err == nil {
					t.Fatal("unreadable command treated as match")
				}
			} else if err != nil || (len(drift) == 0) != (raw == `["--same"]`) {
				t.Fatalf("drift %v error %v", drift, err)
			}
		})
	}
	client := &fakeClient{reply: func(Command) ([]byte, error) { return nil, errors.New("daemon unavailable") }}
	if _, err := ServiceDrift(t.Context(), Scope{Client: client}, "db", deploy.ServiceSpec{Name: "db", Command: []string{"--same"}}); err == nil {
		t.Fatal("failed inspection treated as match")
	}
}
