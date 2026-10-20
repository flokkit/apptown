# List

A small list kept in your browser. It is the starting point for this
project; ask for changes in the chat to turn it into your own app.

The app runs entirely in the browser: there is no server, and what you
add is saved in this browser only.

## Run

    npm ci
    npm run dev      # open the address it prints
    npm test         # the tests, once
    npm run build    # checks the types, then writes the finished app to dist/

The Dockerfile builds the app into `dist/`, and AppTown serves those
files for previews and releases (`.forge/app.yaml`).
