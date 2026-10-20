package agentenv

// RuntimeContractInstructions is injected into the steering of every implementation
// agent. This shared environment policy describes the runtime contract, This is the context whose
// absence shipped reading-list's outage: the agent that wrote that contract
// had a warm dev container and no way to know deploy containers are cold.
const RuntimeContractInstructions = `# Runtime contract

Your app is deployed COLD: nothing carries over from this dev session.
Declare how it runs in .forge/app.yaml — the forge-native contract. Two
sections: app (the containers your app IS) and needs (what it depends on).
Get the schema with ` + "`apptown config schema`" + ` and check your file with
` + "`apptown config validate`" + `.

SERVER APP (with a database, a cache, etc.):
  app:
    web:
      build: .              # forge builds your repo-root Dockerfile
      # image: <pushed ref> #   ...OR run a pushed image instead
      ports: ["8080"]       # CONTAINER port; AppTown assigns the host
      public: true          # the front door (exactly one part is public)
      dataDir: /data        # directory that survives redeploys
      health: { readiness: { http: /healthz } }  # answers 200 when ready
    # worker:               # more app-owned containers go here, same shape
    #   build: .
    #   command: ["node","worker.js"]
  init:                     # ordered, run-to-completion, BEFORE the app starts
    - name: migrate         #   (a migration, an asset build)
      build: .
      command: ["./migrate.sh"]
  needs:
    postgres: { version: "16" }
AppTown provisions each need per deployment (a local container here, a
managed resource in cloud) and injects its connection env — DATABASE_URL +
PG* (postgres), REDIS_URL (redis), AWS_* and S3_BUCKET (s3), reachable at
hostname = the capability name. READ that env; never hard-code a host or
credentials. A need takes typed params plus a properties: {} escape hatch
for esoteric provisioning params. Boot order is enforced: needs come up
healthy, then init containers run to completion, then the app parts start —
so a migration in init sees a ready database.

CLIENT-ONLY SITE OR GAME (static files, no server):
  app:
    game:
      static: .             # serve this dir; no build, no port needed
      public: true
The boot gate requires an app shell — a <script> or <canvas> in
index.html; a 404 or empty page fails.

LOCAL DEV: ` + "`apptown dev up`" + ` generates .forge/docker-compose.yml from this
contract (your app parts + the needs' local emulators, wired) and runs it —
that generated file is gitignored, never hand-edited. When your session has
its own Docker daemon (DOCKER_HOST=tcp://dind:2375), the app's published
ports answer on that host, not on localhost: curl http://dind:<port>/.

Toolchains resolve ONLY from mise.toml at the repo root — any tool your
build/start invokes (go, node, python...) MUST be pinned there; unpinned
tools DO NOT EXIST at deploy time. A boot gate runs the contract COLD
before review; a crash comes back as feedback and you rework. Rehearse the
boot with ` + "`apptown dev up`" + ` before finishing.

`

// startAppContainer launches the declared public app. bootStack owns its
// surrounding needs/init/health sequence; hostPort zero requests a preview port.
