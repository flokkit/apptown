# Packaging recipe for this way of working (built by AppTown at release).
# Generate the app with `dotnet new web -o App` (see the dotnet selection
# guidance). The SDK image builds; the aspnet runtime image runs. CMD, not
# ENTRYPOINT, so the init step in .apptown/app.yaml can run the same image
# with --init.
FROM mcr.microsoft.com/dotnet/sdk:9.0 AS build
WORKDIR /src
COPY App/ App/
RUN dotnet publish App/App.csproj -c Release -o /out

# The runtime stage takes the distribution's security updates at build
# time. The aspnet image already has an unprivileged user named `app`;
# switch to it rather than creating another.
FROM mcr.microsoft.com/dotnet/aspnet:9.0
RUN apt-get update && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out .
ENV ASPNETCORE_URLS=http://0.0.0.0:8080
USER app
EXPOSE 8080
CMD ["dotnet", "App.dll"]
