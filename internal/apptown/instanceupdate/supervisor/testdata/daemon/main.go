// The adapter probe performs no product/database/provider work. It proves the
// real registered child can authenticate and leaves one durable launch witness.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/flokkit/apptown/internal/apptown/instanceupdate/supervisor"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/ids"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if os.Getenv("U2_NEIGHBOR") == "true" {
		<-ctx.Done()
		return
	}
	socket := os.Getenv("APPTOWN_UPDATE_SUPERVISOR_SOCKET")
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(socket), "registration.json"))
	if err != nil {
		panic(err)
	}
	var r supervisor.Registration
	if err := json.Unmarshal(raw, &r); err != nil {
		panic(err)
	}
	in := supervisor.Intention{Request: ids.New(clock.Or(nil)()), Instance: r.Instance, ConfigurationDigest: r.ConfigurationDigest, CandidateSHA256: r.SupervisorSHA256, Principal: ids.New(clock.Or(nil)()), CredentialScope: "adapter-probe-not-product-authority"}
	deadline := clock.Or(nil)().Add(15 * time.Second)
	for {
		reply, err := supervisor.Call(ctx, socket, supervisor.Request{Protocol: supervisor.Protocol, Operation: "stage", Intention: &in})
		if err == nil && reply.Transaction != nil {
			break
		}
		if clock.Or(nil)().After(deadline) {
			panic(fmt.Errorf("registered peer could not stage: %w", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	body, _ := json.Marshal(struct {
		PID     int
		Request ids.ID
	}{os.Getpid(), in.Request})
	if err := os.WriteFile(filepath.Join(os.Getenv("APPTOWN_DATA"), "boot.json"), body, 0600); err != nil {
		panic(err)
	}
	<-ctx.Done()
	if seconds, _ := strconv.Atoi(os.Getenv("U2_STOP_DELAY")); seconds > 0 {
		time.Sleep(time.Duration(seconds) * time.Second)
	}
}
