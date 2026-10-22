package agentenv

// ConsoleDockerInstructions: what the steering says about a change console's
// private Docker daemon.
func ConsoleDockerInstructions(disabled bool) string {
	availability := `- This change console has its own private Docker daemon. DOCKER_HOST is
  tcp://dind:2375: build images, run containers and inspect their logs with
  the normal docker CLI. The host Docker socket is never mounted here.
  /work is the same workspace on both sides; use docker run -v /work:/work
  to test its files. Published ports are at http://dind:<port>, not localhost.
  Buildx uses the preconfigured apptown builder with working DNS and the
  configured Docker Hub mirror. Build commands need explicit proxy build
  arguments when applicable; they do not inherit this shell's environment.
  Docker state survives an AppTown restart and an idle stop. After session
  cleanup or daemon recreation it may be empty: inspect before resuming.
  This grants no additional registry credentials.`
	if disabled {
		availability = `- Private Docker is disabled on this AppTown (-no-private-docker). This console
  has no daemon; use apptown preview for server-side container builds.`
	}
	return availability + `
- Keep the app's Dockerfile as its packaging recipe. apptown preview still
  cold-boots that recipe independently on the server for the operator.
  For direct local tool checks, declare pinned versions in mise.toml and
  run mise install; the declaration survives container recreation.`
}
