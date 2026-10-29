//go:build ignore

// release-facts prints what a release's notes state about its build, read
// from the code itself so the notes cannot drift from it: the worker and CLI
// protocol numbers and the agent image's name for the build context given.
//
//	go run scripts/release-facts.go deploy/apptown-agent
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/flokkit/apptown/internal/apptown/datalayout"
	"github.com/flokkit/apptown/internal/apptown/dbschema"

	"github.com/flokkit/apptown/internal/version"
	"github.com/flokkit/apptown/internal/worker"
)

func main() {
	asJSON := len(os.Args) == 3 && os.Args[1] == "--json"
	if asJSON {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/release-facts.go <agent build context>")
		os.Exit(2)
	}
	image, err := worker.AgentImage(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if asJSON {
		facts := map[string]any{
			"sourceSchema": map[string]int{"min": dbschema.Baseline(), "max": dbschema.Latest()},
			// ReadVersion accepts the original absent-stamp layout and current UUID layout.
			"sourceLayout": map[string]int{"min": 1, "max": datalayout.Version},
			"schemaResult": dbschema.Latest(), "layoutResult": datalayout.Version,
			"cliProtocol": version.CLIProtocol, "daemonWorkerProtocol": worker.Protocol,
			"workerProtocol": map[string]int{"min": worker.ProtocolMin, "max": worker.ProtocolMax},
		}
		if err := json.NewEncoder(os.Stdout).Encode(facts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("WORKER_PROTOCOL=%d-%d\n", worker.ProtocolMin, worker.ProtocolMax)
	fmt.Printf("CLI_PROTOCOL=%d\n", version.CLIProtocol)
	fmt.Printf("AGENT_IMAGE=%s\n", image)
}
