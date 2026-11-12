---
name: lang/ruby/dependency-updates
tags: dependencies,ruby
fixes: [{"kind":"update","appliesTo":["tool:ruby"],"ecosystem":"RubyGems","run":"bundle update --conservative","runFor":"bundle update --conservative {packages}","list":"ruby -e 'require \"json\";require \"bundler\"\nlock=Bundler::LockfileParser.new(Bundler.read_file(\"Gemfile.lock\"))\nlock.specs.sort_by{|s|[s.name,s.version.to_s,s.platform.to_s]}.each do |s|\n source=s.source\n origin=if source.respond_to?(:remotes);source.remotes.map(&:to_s).sort.join(\",\");else;source.to_s;end\n puts JSON.generate({ecosystem:\"RubyGems\",package:s.name,version:s.version.to_s,source:origin,attributes:{platform:s.platform.to_s}})\nend'","files":["Gemfile","Gemfile.lock"],"runMajor":"bundle update --major"}]
---

Aggressive updates use bundle update --major within Gemfile constraints; changing constraints needs a person. Routine updates refuse majors.
