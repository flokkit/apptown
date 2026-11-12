---
name: lang/python/dependency-updates
tags: dependencies,python
fixes: [{"kind":"update","appliesTo":["tool:python"],"ecosystem":"PyPI","run":"uv lock --upgrade","runFor":"for package in {packages}; do uv lock --upgrade-package \"$package\" || exit $?; done","list":"python3 -c 'import json,tomllib\nwith open(\"uv.lock\",\"rb\") as f:lock=tomllib.load(f)\nfor p in sorted(lock[\"package\"],key=lambda p:(p[\"name\"],p[\"version\"])):\n source=p.get(\"source\",{})\n if source.get(\"virtual\")==\".\" or source.get(\"editable\")==\".\":continue\n print(json.dumps(dict(ecosystem=\"PyPI\",package=p[\"name\"],version=p[\"version\"],source=json.dumps(source,sort_keys=True),attributes={\"resolution-markers\":p.get(\"resolution-markers\",[])}),sort_keys=True))'","files":["pyproject.toml","uv.lock"],"runMajor":"uv lock --upgrade"}]
---
