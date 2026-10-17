package console

// The toolchain prelude is shared by every agent session: a workflow's agent
// step (ToolchainArgs) and a chat's runtime (ToolchainPrelude), on the
// forge's Docker or on a worker. One place, so no launcher can leave it out:
// a worker's chats once started without it and the project's declared Maven
// was "command not found".

// ToolchainPrelude installs the workspace's declared toolchain (mise.toml) before a
// session starts, wherever it runs (the forge's own Docker or a worker's), so `python`, `gofmt`, `node` are on PATH through the
// per-workspace shims without the agent having to know about mise
// (complaints #50, #59: the documented commands were "not found" until
// someone ran `mise install`). A failed install no longer passes silently:
// a session whose Ruby never installed went on to hand-write a Rails
// skeleton, and nobody saw why. The installer's output is kept in the
// container's /tmp, a failure is said on stderr (the step's log, a chat's
// terminal) with the output's last lines, and the session still starts
// with whatever toolchains the image has.
var ToolchainPrelude = toolchainPrelude("/work", "/tmp")

// ToolchainArgs wraps an argv so the prep runs first and the command then
// replaces the shell (signals and exit status pass straight through). The
// argv's last word is the agent's prompt: when the install failed, the
// agent is told so after it (toolchainInstallNote).
func ToolchainArgs(argv []string) []string { return toolchainArgs("/work", "/tmp", argv) }

// toolchainPrelude runs in the session's container: work is its folder,
// scratch a directory nothing of which outlives the container.
func toolchainPrelude(work, scratch string) string {
	log, failed := scratch+"/forge-toolchain-install.log", scratch+"/forge-toolchain-install-failed"
	return `rm -f ` + failed + `; cd ` + work + ` 2>/dev/null && [ -f mise.toml -o -f .mise.toml ] && { mise trust -q >/dev/null 2>&1; ` +
		`if ! mise install -y -q >` + log + ` 2>&1; then : >` + failed + `; ` +
		`echo "forge: the toolchain this project declares (mise.toml) did not install; its tools may be missing. The installer said (` + log + `):" >&2; ` +
		`tail -n 20 ` + log + ` >&2; fi; mise reshim >/dev/null 2>&1; }; true`
}

// toolchainInstallNote is what an agent is told, after its prompt, when the
// declared toolchain did not install.
func toolchainInstallNote(scratch string) string {
	return "(From the forge: the toolchain this project declares in mise.toml did not install before this session, so its tools may be missing. What the installer said is in " + scratch + "/forge-toolchain-install.log.)"
}

func toolchainArgs(work, scratch string, argv []string) []string {
	tell := `[ -f ` + scratch + `/forge-toolchain-install-failed ] && { i=0; n=$#; for a do i=$((i+1)); [ "$i" -eq 1 ] && set --; ` +
		`if [ "$i" -eq "$n" ]; then set -- "$@" "$a

` + toolchainInstallNote(scratch) + `"; else set -- "$@" "$a"; fi; done; }`
	return append([]string{"sh", "-c", toolchainPrelude(work, scratch) + "; " + tell + `; exec "$@"`, "sh"}, argv...)
}
