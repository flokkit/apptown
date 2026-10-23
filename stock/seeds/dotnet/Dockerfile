# Packaging recipe for this way of working (built by AppTown at release).
# Generate the app with `dotnet new web -o App` (see the dotnet selection
# guidance). The SDK image builds; the aspnet runtime image runs. CMD, not
# ENTRYPOINT, so the init step in .apptown/app.yaml can run the same image
# with --init.
# Each RUN that downloads mounts the extra certificates AppTown gives a build
# (the secret apptown-trust) and runs them first; without them it runs as is.
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
WORKDIR /src
COPY App/ App/
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; dotnet publish App/App.csproj -c Release -o /out

# The runtime stage takes the distribution's security updates at build
# time. The aspnet image already has an unprivileged user named `app`;
# switch to it rather than creating another.
FROM mcr.microsoft.com/dotnet/aspnet:10.0
RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; apt-get update && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out .
ENV ASPNETCORE_URLS=http://0.0.0.0:8080
USER app
EXPOSE 8080
# The command first trusts the extra certificates AppTown gives the
# running app (APPTOWN_TRUST; nothing without it), then runs the app.
CMD ["sh", "-c", "eval \"${APPTOWN_TRUST:-}\"; exec \"$@\"", "sh", "dotnet", "App.dll"]
