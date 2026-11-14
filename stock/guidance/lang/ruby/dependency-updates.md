---
name: lang/ruby/dependency-updates
tags: dependencies,ruby
fixes: [{"kind":"update","ecosystem":"RubyGems","files":["Gemfile","Gemfile.lock","*.gemspec"]}]
---

Run `bundle update`, or `bundle update --conservative NAME` for a targeted fix. Use `--major` when the approved level allows major releases. Cover every bundle in the repository.
