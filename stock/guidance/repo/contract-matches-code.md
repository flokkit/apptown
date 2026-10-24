---
name: repo/contract-matches-code
tags: repo,contract,platform
tier: platform
checks: [{"name": "contract-matches-code", "title": "Declare the app's code in its contract", "expects": "every package manifest in the repository (package.json, go.mod, pyproject.toml, Gemfile, Cargo.toml, pom.xml, a .csproj, composer.json, project.godot) is in the folder of a part whose tools: in .apptown/app.yaml name its tool, or in a folder listed under notAppCode:, so the checks for that tool judge it and none is not applicable by mistake", "severity": "high", "evaluator": "sh", "occurrences": "lines", "appliesTo": ["repository"], "sh": "t=$(mktemp) || exit 2; bad=0\nfind . \\( -name .git -o -name node_modules -o -name vendor -o -name testdata \\) -prune -o -type f \\( -name package.json -o -name go.mod -o -name pyproject.toml -o -name Gemfile -o -name Cargo.toml -o -name pom.xml -o -name '*.csproj' -o -name composer.json -o -name project.godot \\) -print | sed 's|^\\./||' | sort > \"$t\" || exit 2\nunder() { [ \"$2\" = . ] || [ \"$1\" = \"$2\" ] || case \"$1\" in \"$2\"/*) true;; *) false;; esac; }\nwhile IFS= read -r f; do\n  case \"${f##*/}\" in package.json) tool=node;; go.mod) tool=go;; pyproject.toml) tool=python;; Gemfile) tool=ruby;; Cargo.toml) tool=rust;; pom.xml) tool=java;; *.csproj) tool=dotnet;; composer.json) tool=php;; project.godot) tool=godot;; esac\n  d=$(dirname \"$f\"); ok=no\n  while IFS= read -r n; do [ -n \"$n\" ] && under \"$d\" \"$n\" && ok=yes; done <<NOTAPP\n$APPTOWN_NOT_APP_CODE\nNOTAPP\n  while IFS=\"$(printf '\\t')\" read -r name folder tools; do [ -n \"$name\" ] && under \"$d\" \"$folder\" && case \",$tools,\" in *\",$tool,\"*) ok=yes;; esac; done <<PARTS\n$APPTOWN_PARTS\nPARTS\n  [ $ok = yes ] && continue\n  bad=1\n  if [ ! -f .apptown/app.yaml ]; then echo \"$f: there is no .apptown/app.yaml, so no part says it is built with $tool: declare the app's parts and their tools, or list $d under notAppCode: if it is not the app's code\"\n  else echo \"$f: no part built with $tool holds $d: add $tool to the tools of the part that builds it in .apptown/app.yaml, or list $d under notAppCode: if it is not the app's code\"; fi\ndone < \"$t\"\nrm -f \"$t\"; exit $bad"}]
---
The contract says what each part is built with (`tools:` in
`.apptown/app.yaml`), and a tool's checks judge only the parts that name
it. Every package manifest (package.json, go.mod, pyproject.toml, Gemfile,
Cargo.toml, pom.xml, a .csproj, composer.json, project.godot) sits in the
folder of a part whose `tools:` name its tool, or below it, or under a
folder listed in `notAppCode:` (documentation, tooling). node_modules,
vendor and testdata are not looked in. For a finding, add the tool to the
part that builds it, or list the folder under `notAppCode:`.
