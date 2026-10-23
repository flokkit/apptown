// The app the dotnet selection guidance has an agent write on the first
// change, reduced to what the seed's recipe must carry: forwarded headers,
// /healthz, EF Core on Postgres from the PG* variables, and --init bringing
// the schema up and returning before the server starts.
using Microsoft.AspNetCore.HttpOverrides;
using Microsoft.EntityFrameworkCore;
using Npgsql;

var builder = WebApplication.CreateBuilder(args);
builder.Services.Configure<ForwardedHeadersOptions>(o =>
{
    o.ForwardedHeaders = ForwardedHeaders.XForwardedFor | ForwardedHeaders.XForwardedProto | ForwardedHeaders.XForwardedHost;
    o.KnownIPNetworks.Clear();
    o.KnownProxies.Clear();
});
var db = new NpgsqlConnectionStringBuilder
{
    Host = Environment.GetEnvironmentVariable("PGHOST"),
    Port = int.Parse(Environment.GetEnvironmentVariable("PGPORT") ?? "5432"),
    Database = Environment.GetEnvironmentVariable("PGDATABASE"),
    Username = Environment.GetEnvironmentVariable("PGUSER"),
    Password = Environment.GetEnvironmentVariable("PGPASSWORD"),
}.ConnectionString;
builder.Services.AddDbContext<AppDb>(o => o.UseNpgsql(db));
var app = builder.Build();

if (args.Contains("--init"))
{
    using var scope = app.Services.CreateScope();
    var ctx = scope.ServiceProvider.GetRequiredService<AppDb>();
    ctx.Database.EnsureCreated();
    if (!ctx.Notes.Any())
    {
        ctx.Notes.Add(new Note { Text = "seeded by init" });
        ctx.SaveChanges();
    }
    return;
}

app.UseForwardedHeaders();
app.MapGet("/healthz", () => "ok");
app.MapGet("/", (AppDb ctx) => string.Join("\n", ctx.Notes.OrderBy(n => n.Id).Select(n => n.Text)));
app.Run();

class AppDb(DbContextOptions<AppDb> options) : DbContext(options)
{
    public DbSet<Note> Notes => Set<Note>();
}

class Note
{
    public int Id { get; set; }
    public string Text { get; set; } = "";
}
