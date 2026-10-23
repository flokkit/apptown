# This project

An ASP.NET Core web service on .NET 10: the app in `App/`, its tests in
`App.Tests/`.

## Run

    make test
    dotnet run --project App
    docker build -t app . && docker run --rm -p 8080:8080 app

`make test` runs the test suite (`dotnet test App.Tests`). The service
listens on port 8080 and answers `GET /healthz` with 200 when it is ready.
