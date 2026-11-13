# Create project

Open **Projects → + Create project**, name the app and describe what you want to
build. Keep the details in your request: AppTown keeps your full request in the
project's history. You can read it under **Application details → Original
request**. **Build the first version** uses that request as its brief. If there
is no request, the helper asks what to build before starting.

AppTown suggests a short description of up to eight words for project lists.
It uses the request, the README on main and recent landed changes, and refreshes
the description after changes land. Imports request another refresh once the
code has been imported. Generation runs in the background; landing does not wait
for a model response.

Without a model, a new project's description stays empty. If generation fails,
the current description stays. You can write your own in
[project settings](project-settings.md).

Choose the project's [packs](packs-and-what-if.md) in **Way of working**.
The `dependency-updates` pack starts ticked for monthly dependency updates.
You can untick it when choosing packs yourself.

The four steps are **What it does**, **Way of working**, **Code** and
**Where it runs**. Code starts empty, from a Git bundle or from an HTTPS Git
URL, with an optional GitLab copy. Where it runs asks who the app is for and
what data it holds before offering targets. Defaults are **Team only** and
**Test data**. Choose one target, or **Nowhere yet** to land without deploying.
An offered target may let you change audience or data inline to make it usable.
Add other deployments later from the project's Deployments page.

Create project opens the first change's Conversation tab and starts the build.
If any setup step was skipped, the log stays visible with **Build the first
version**. A saved setup with an unknown outcome stays available to check;
AppTown does not guess or repeat that step. Current repository, workflow,
guidance, policy and binding facts are in **Settings → Way of working**.
