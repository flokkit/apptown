---
name: lang/selection/dotnet-aspnet
tags: lang,dotnet,csharp,aspnet
checks: [{"name": "trusts-forge-front-door", "expects": "the app trusts AppTown's front door: forwarded proto and host headers are applied (UseForwardedHeaders)", "severity": "high", "evaluator": "sh", "sh": "f=$(ls Program.cs */Program.cs 2>/dev/null | head -1); if [ -n \"$f\" ] && ! grep -q UseForwardedHeaders \"$f\"; then echo \"$f never applies forwarded headers — behind AppTown's HTTPS front door every absolute URL, redirect and origin check is wrong. Add ForwardedHeadersOptions (XForwardedFor|XForwardedProto|XForwardedHost, KnownNetworks/KnownProxies cleared) and app.UseForwardedHeaders() first.\"; exit 1; fi", "title": "Configure the application to trust AppTown’s front door"}, {"name": "cmd-not-entrypoint", "expects": "the Dockerfile ends in CMD, so init steps can run the same image with other arguments", "severity": "medium", "evaluator": "sh", "sh": "if [ -f Dockerfile ] && grep -q '^ENTRYPOINT' Dockerfile; then echo 'Dockerfile uses ENTRYPOINT — the init step (dotnet App.dll --init) is appended to it and breaks. Use CMD [\"dotnet\", \"App.dll\"].'; exit 1; fi", "title": "Make the container’s application command replaceable"}, {"name": "gitignore-names-build-output", "expects": "git would ignore what the .NET build writes (bin/, obj/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in bin/probe.dll obj/probe.dll; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "runtime-stage-not-build-image", "expects": "the Dockerfile's final stage is not FROM mcr.microsoft.com/dotnet/sdk (any tag), the image this pack's seed builds with: a build stage makes the artifact and the final stage starts from mcr.microsoft.com/dotnet/aspnet with only that artifact copied in. Judged from the FROM instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage name resolves to the image it names); a base image given by a build argument is not judged, an instruction with two heredocs, or heredoc-like text inside a quoted shell string whose end is never found, is reported as not judged", "severity": "high", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; img=$(awk 'function inst(i, s, a, d, c) { if (toupper($1) == \"FROM\") { i = 2; while ($i ~ /^--/) i++; s = $i; a = tolower(s); if (a in st) s = st[a]; if (toupper($(i+1)) == \"AS\") st[tolower($(i+2))] = s; last = s } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else print last }' Dockerfile); [ \"$img\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; r=${img%@*}; case ${r##*/} in *:*) r=${r%:*};; esac; r=${r#docker.io/}; r=${r#library/}; [ \"$r\" = mcr.microsoft.com/dotnet/sdk ] || exit 0; echo \"the final stage runs FROM $img, the image your pack's seed builds with: its compiler, toolchain and their known vulnerabilities ship with the app. Build in a stage FROM mcr.microsoft.com/dotnet/sdk, then copy only the dotnet publish output into a final stage FROM mcr.microsoft.com/dotnet/aspnet (lang/selection/dotnet-aspnet shows the recipe)\"; exit 1", "title": "Run the application from a runtime image, not the build image"}]
---
.NET services are ASP.NET Core on the current LTS line (.NET 9 today,
pinned in mise.toml), C#, minimal APIs first, EF Core on Postgres. The
pack seeds the packaging recipe, the ignore rules and the runtime
contract; the app comes from the SDK's own templates, which are always
current, never from a hand-kept skeleton.

**The first act on an empty repo** (the "build the first version" change):

    dotnet new web -o App
    dotnet new xunit -o App.Tests
    dotnet add App.Tests reference App
    dotnet add App.Tests package Microsoft.AspNetCore.Mvc.Testing
    dotnet add App package Npgsql.EntityFrameworkCore.PostgreSQL

The seeded Dockerfile expects the app project at `App/App.csproj` and
runs `dotnet App.dll` as the image's own unprivileged `app` user (the
aspnet image has it; do not create another). The seeded runtime contract
at `.forge/app.yaml` declares the web part, its `/healthz` readiness
path, the Postgres need and the init step that brings the schema up
before the app starts. AppTown fulfils the need and runs the init step
in every environment; keep both, and make `--init` exit (below), or the
init step starts the server instead of finishing and the app never boots:

    app:
      web:
        build: .
        ports: ["8080"]
        public: true
        health:
          readiness: { http: /healthz }
    init:
      - name: schema
        build: .
        command: ["dotnet", "App.dll", "--init"]
    needs:
      postgres:
        version: "16"

In `Program.cs`: map `/healthz`; when the arguments contain `--init`,
bring the schema up (`EnsureCreated` to start, `Migrate` once migrations
exist) and return before `app.Run()`; build the connection string from
the injected `PG*` variables (below); apply forwarded headers first
(below). Add one
xunit test through `WebApplicationFactory<Program>` (`public partial
class Program {}` at the end of Program.cs makes it reachable). The
checks on this note fail until the app trusts the front door and the
recipe ends in CMD.

- **Minimal APIs, then controllers when it earns them.** Start with
  `MapGet`/`MapPost` in `Program.cs`; move to controllers or Razor Pages
  when a surface has enough endpoints to want structure. Blazor is a
  decision, not a default — record it in an ADR.
- **EF Core on Npgsql, connected through the `PG*` variables.** The
  postgres need injects `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` and
  `PGPASSWORD` everywhere (a preview, a deployment, the agent's
  emulator). Build the connection string from exactly those, with
  Npgsql's builder:

      var db = new NpgsqlConnectionStringBuilder
      {
          Host = Environment.GetEnvironmentVariable("PGHOST"),
          Port = int.Parse(Environment.GetEnvironmentVariable("PGPORT") ?? "5432"),
          Database = Environment.GetEnvironmentVariable("PGDATABASE"),
          Username = Environment.GetEnvironmentVariable("PGUSER"),
          Password = Environment.GetEnvironmentVariable("PGPASSWORD"),
      }.ConnectionString;
      builder.Services.AddDbContext<AppDb>(o => o.UseNpgsql(db));

  Leave `SslMode` at Npgsql's default (it uses TLS where the database
  offers it: a managed database does, the local one does not). Never
  parse `DATABASE_URL` by hand: it says the same thing in a URL whose
  query options Npgsql does not read, and a hand parser gets the
  password's escaping and the TLS mode wrong. The starter creates the schema with `EnsureCreated` in its
  init step; once the schema must evolve without losing data, switch to
  migrations (`Database.Migrate()` in the same init step).
- **Migrations are generated, never written by hand.** Install the tool
  as a local tool (`dotnet new tool-manifest`, `dotnet tool install
  dotnet-ef`, commit `.config/dotnet-tools.json`), add
  `Microsoft.EntityFrameworkCore.Design` to the app, then
  `dotnet ef migrations add <Name> --project App` and commit the three
  files it writes. A hand-written migration lacks the `[DbContext]` and
  `[Migration]` attributes and the model snapshot, so `Migrate()` finds
  nothing to apply and the app starts on a missing table — the boot
  probe still passes because `/healthz` does not touch the database. After
  adding one, check that `/` (or a page that reads the table) answers.
- **Trust the front door.** AppTown terminates TLS and forwards proto
  and host. `UseForwardedHeaders` with proto, host and for, and the
  known-networks lists cleared, comes before anything else in the
  pipeline. Without it redirects go to http and antiforgery rejects
  every form.
- **Forms carry an antiforgery token; never turn the check off.** A
  minimal-API endpoint that binds a form (`[FromForm]`, `IFormCollection`,
  `IFormFile`) is validated automatically, but only when the service and
  the middleware are both there; without them every submit answers 500
  ("contains anti-forgery metadata, but a middleware was not found").
  Register both, the middleware after forwarded headers (and after
  authentication, when there is any):

      builder.Services.AddAntiforgery();
      // …
      app.UseForwardedHeaders();
      app.UseAntiforgery();

  and put the token in every form the app renders, from `IAntiforgery`:

      app.MapGet("/", (HttpContext http, IAntiforgery af) =>
      {
          var t = af.GetAndStoreTokens(http);
          return Results.Content($"""
              <form method="post" action="/items">
                <input type="hidden" name="{t.FormFieldName}" value="{t.RequestToken}">
                …
              </form>
              """, "text/html");
      });
      app.MapPost("/items", ([FromForm] string name) => …);

  (Razor Pages and MVC views add the hidden field to every `<form
  method="post">` themselves.) An HTMX or `fetch` post sends the same
  token in a `RequestVerificationToken` header. `.DisableAntiforgery()`
  removes the protection the form needs and is not a fix: a submit that
  fails validation means the token is missing from the form. After adding
  a form, submit it once against the running app and check the answer is
  the page or redirect you meant, not a 400 or 500.
- **One deployable per repo**: `dotnet publish` of the app project, run
  with `dotnet App.dll`. The SDK image builds; the final stage is
  `mcr.microsoft.com/dotnet/aspnet` with only the publish output (the
  seed). The SDK never ships: it is the compiler and NuGet, which the
  running app does not use. The Dockerfile ends in `CMD`, never
  `ENTRYPOINT`, so the init step can run the same image with `--init`.
  All environment-specific config comes from env vars (ASP.NET's
  configuration reads them natively, `ASPNETCORE_URLS` sets the port).
- **Packages through the mirror.** NuGet restores read the ambient
  `NuGet.Config` (injected as `RestoreConfigFile`); never write a
  registry URL into a `.csproj` or a repo-level `NuGet.Config`.
- **Tests are xunit** against the real app via `WebApplicationFactory`;
  `dotnet test App.Tests` runs them. The landing runs `make test`, and the
  seeded Makefile runs exactly that: keep the target running the whole
  suite (a second test project joins the target). The landing's database
  starts empty and `WebApplicationFactory` does not pass `--init`, so a
  test that reaches the database brings the schema up first through the
  same code the init step calls (`EnsureCreated` or `Migrate`).

Build output stays out of git: `.gitignore` names `bin/` and `obj/`
(the `[Bb]in/` spelling counts), so a build can never be committed by
accident.

**Fits a container limit.** .NET follows the container's memory limit by
itself; a small ASP.NET app uses about 64 MB, so declare
`resources: { memory: 128M }` in `.forge/app.yaml`, and set
`DOTNET_GCHeapHardLimitPercent` only when the app needs another share.
deploy/memory-declared says how to choose and check it.
