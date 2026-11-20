---
name: lang/ruby/dependency-updates
tags: dependencies,ruby
fixes: [{"kind":"update","ecosystem":"RubyGems","files":["Gemfile","Gemfile.lock","*.gemspec"]}]
---

Run `bundle update`, or `bundle update --conservative NAME` for a targeted fix. Use `bundle update --major NAME` only for a gem `apptown janitor majors` says is due. Cover every bundle in the repository.
