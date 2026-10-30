---
name: frontend/server-rendered
tags: frontend,simplicity
---
The server renders HTML with the framework's own templates (Django
templates, ERB, Go's `html/template`, Razor, a Node template engine). HTMX
attributes, or Hotwire (Turbo and Stimulus through importmap) in Rails, add
interactivity where a page must feel live. No client-side state store, no
JSON API for your own UI, and no JavaScript build or Node toolchain in the
repository; adding one is a decision recorded in an ADR.

Rationale: the web platform already does most of what single-page apps
rebuild; hypermedia keeps state in one place, the server.
