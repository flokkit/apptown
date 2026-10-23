---
name: lang/selection/dotnet-aspnet
tags: lang,dotnet,csharp,aspnet
checks: [{"name": "trusts-front-door", "expects": "the app trusts AppTown's front door: forwarded proto and host headers are applied (UseForwardedHeaders)", "severity": "high", "evaluator": "sh", "sh": "f=$(ls Program.cs */Program.cs 2>/dev/null | head -1); if [ -n \"$f\" ] && ! grep -q UseForwardedHeaders \"$f\"; then echo \"$f never applies forwarded headers — behind AppTown's HTTPS front door every absolute URL, redirect and origin check is wrong. Add ForwardedHeadersOptions (XForwardedFor|XForwardedProto|XForwardedHost, KnownIPNetworks/KnownProxies cleared) and app.UseForwardedHeaders() first.\"; exit 1; fi", "title": "Configure the application to trust AppTown’s front door"}, {"name": "cmd-not-entrypoint", "expects": "the Dockerfile ends in CMD, so init steps can run the same image with other arguments", "severity": "medium", "evaluator": "sh", "sh": "if [ -f Dockerfile ] && grep -q '^ENTRYPOINT' Dockerfile; then echo 'Dockerfile uses ENTRYPOINT — the init step (dotnet App.dll --init) is appended to it and breaks. Use CMD [\"dotnet\", \"App.dll\"].'; exit 1; fi", "title": "Make the container’s application command replaceable"}, {"name": "gitignore-names-build-output", "expects": "git would ignore what the .NET build writes (bin/, obj/), so build output can never be committed by accident. If the project commits that output deliberately, this check does not fit it: say so and stop rather than add the line, since an ignore rule over tracked files makes the repository contradict itself", "severity": "medium", "evaluator": "sh", "sh": "t=$(mktemp -d) && git -c init.defaultBranch=main init -q \"$t\" || exit 2; bad=0; for p in bin/probe.dll obj/probe.dll; do git --git-dir=\"$t/.git\" --work-tree=. -c core.excludesFile=/dev/null check-ignore -q \"$p\" || { echo \".gitignore does not cover ${p%/*}/\"; bad=1; }; done; exit $bad", "title": "Declare generated files in the repository’s ignore rules"}, {"name": "runtime-stage-not-build-image", "expects": "the Dockerfile's final stage is not FROM mcr.microsoft.com/dotnet/sdk (any tag), the image this pack's seed builds with: a build stage makes the artifact and the final stage starts from mcr.microsoft.com/dotnet/aspnet with only that artifact copied in. Judged from the FROM instructions as Docker reads them (continued lines, comments and a heredoc's body; a stage name resolves to the image it names); a base image given by a build argument is not judged, an instruction with two heredocs, or heredoc-like text inside a quoted shell string whose end is never found, is reported as not judged", "severity": "high", "evaluator": "sh", "sh": "[ -f Dockerfile ] || exit 0; img=$(awk 'function inst(i, s, a, d, c) { if (toupper($1) == \"FROM\") { i = 2; while ($i ~ /^--/) i++; s = $i; a = tolower(s); if (a in st) s = st[a]; if (toupper($(i+1)) == \"AS\") st[tolower($(i+2))] = s; last = s } else if (toupper($1) ~ /^(RUN|COPY|ADD)$/ && match($0, /(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_][^ \\t\"\\047<>|;&]*/)) { d = substr($0, RSTART, RLENGTH); c = $0; if (gsub(/(^|[^<])<<-?[ \\t]*[\"\\047]?[A-Za-z_]/, \"\", c) > 1) many = 1; sub(/^[^<]?<</, \"\", d); hs = (d ~ /^-/); sub(/^-?[ \\t]*[\"\\047]?/, \"\", d); hd = d } } { sub(/\\r$/, \"\") } hd != \"\" { t = $0; if (hs) sub(/^\\t+/, \"\", t); if (t == hd) hd = \"\"; next } /^[ \\t]*#/ { next } b != \"\" && /^[ \\t]*$/ { next } /\\\\[ \\t]*$/ { sub(/\\\\[ \\t]*$/, \"\"); b = b $0 \" \"; next } { $0 = b $0; b = \"\"; inst() } END { if (b != \"\") { $0 = b; inst() } if (many || hd != \"\") print \"<<\"; else print last }' Dockerfile); [ \"$img\" = \"<<\" ] && { echo 'not judged: a heredoc whose end this check cannot find (heredoc-like text inside a quoted string reads as one), or an instruction with more than one heredoc'; exit 2; }; r=${img%@*}; case ${r##*/} in *:*) r=${r%:*};; esac; r=${r#docker.io/}; r=${r#library/}; [ \"$r\" = mcr.microsoft.com/dotnet/sdk ] || exit 0; echo \"the final stage runs FROM $img, the image your pack's seed builds with: its compiler, toolchain and their known vulnerabilities ship with the app. Build in a stage FROM mcr.microsoft.com/dotnet/sdk, then copy only the dotnet publish output into a final stage FROM mcr.microsoft.com/dotnet/aspnet (lang/selection/dotnet-aspnet shows the recipe)\"; exit 1", "title": "Run the application from a runtime image, not the build image"}]
---
.NET services are ASP.NET Core on .NET 10, the long-term-support line
(supported until 14 November 2028; pinned in mise.toml), C#, minimal APIs
first, EF Core on Postgres. The pack seeds the
Dockerfile, ignore rules and contract; the app comes from the SDK's templates.

**The first act on an empty repo:**

    dotnet new web -o App
    dotnet new xunit -o App.Tests
    dotnet add App.Tests reference App
    dotnet add App.Tests package Microsoft.AspNetCore.Mvc.Testing
    dotnet add App package Npgsql.EntityFrameworkCore.PostgreSQL

The seeded Dockerfile expects `App/App.csproj` and runs `dotnet App.dll` as the
aspnet image's own `app` user (do not create another). Keep the seeded
`.apptown/app.yaml`:

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

In `Program.cs`: map `/healthz`; with `--init` in the arguments, bring the
schema up (`EnsureCreated` to start, `Migrate` once migrations exist) and
return before `app.Run()` (else the init step starts the server and the app
never boots); apply forwarded headers first. Add one xunit test through
`WebApplicationFactory<Program>` (`public partial class Program {}` at the end
of Program.cs).

- **Minimal APIs** (`MapGet`/`MapPost`) until a surface wants controllers or
  Razor Pages. Blazor is a decision, recorded in an ADR.
- **Connection from the `PG*` variables**, with Npgsql's builder; leave
  `SslMode` at its default; never parse `DATABASE_URL`:

      var db = new NpgsqlConnectionStringBuilder
      {
          Host = Environment.GetEnvironmentVariable("PGHOST"),
          Port = int.Parse(Environment.GetEnvironmentVariable("PGPORT") ?? "5432"),
          Database = Environment.GetEnvironmentVariable("PGDATABASE"),
          Username = Environment.GetEnvironmentVariable("PGUSER"),
          Password = Environment.GetEnvironmentVariable("PGPASSWORD"),
      }.ConnectionString;
      builder.Services.AddDbContext<AppDb>(o => o.UseNpgsql(db));

- **Migrations are generated, never hand-written.** `dotnet new tool-manifest`,
  `dotnet tool install dotnet-ef`, commit `.config/dotnet-tools.json`, add
  `Microsoft.EntityFrameworkCore.Design`, then
  `dotnet ef migrations add <Name> --project App` and commit the three files.
  A hand-written one is never applied. After adding one, check `/` (or a page
  that reads the table) answers.
- **Trust the front door:** `UseForwardedHeaders` with proto, host and for, and
  the known-networks lists cleared, before anything else in the pipeline.
- **Forms carry an antiforgery token; never turn the check off.** Register the
  service and the middleware (after forwarded headers, and after
  authentication when there is any), or every form submit answers 500:

      builder.Services.AddAntiforgery();
      // …
      app.UseForwardedHeaders();
      app.UseAntiforgery();

  and put the token in every form the app renders:

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

  Razor Pages and MVC views add it themselves; HTMX or `fetch` send it in a
  `RequestVerificationToken` header. Never `.DisableAntiforgery()`.
- **One deployable:** `dotnet publish` of the app project; the SDK image
  builds, the final stage is `mcr.microsoft.com/dotnet/aspnet` with only the
  publish output. The Dockerfile ends in `CMD`, never `ENTRYPOINT`, so the init
  step runs the same image with `--init`. Config from env vars
  (`ASPNETCORE_URLS` sets the port).
- **Packages through the mirror:** NuGet reads the ambient `NuGet.Config`;
  never a registry URL in a `.csproj` or a repo `NuGet.Config`.
- **Tests:** xunit against the real app via `WebApplicationFactory`;
  `make test` runs `dotnet test App.Tests` — keep it running the whole suite.
  The test database starts empty and `--init` is not passed, so a test that
  reaches the database brings the schema up first through the init step's code.

`.gitignore` names `bin/` and `obj/`.

**Memory:** .NET follows the limit; a small app uses about 64 MB, declare
`resources: { memory: 128Mi }`. Set `DOTNET_GCHeapHardLimitPercent` only when
the app needs another heap share.
