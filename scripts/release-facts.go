//go:build ignore

// release-facts prints what a release's notes state about its build, read
// from the code itself so the notes cannot drift from it: the worker and CLI
// protocol numbers and the agent image's name for the build context given.
//
//	go run scripts/release-facts.go deploy/forge-agent
package main

import (
	"fmt"
	"os"

	"flokkit.io/forge/internal/version"
	"flokkit.io/forge/internal/worker"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/release-facts.go <agent build context>")
		os.Exit(2)
	}
	image, err := worker.AgentImage(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("WORKER_PROTOCOL=%d-%d\n", worker.ProtocolMin, worker.ProtocolMax)
	fmt.Printf("CLI_PROTOCOL=%d\n", version.CLIProtocol)
	fmt.Printf("AGENT_IMAGE=%s\n", image)
}
