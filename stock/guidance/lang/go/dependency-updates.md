---
name: lang/go/dependency-updates
tags: dependencies,go
fixes: [{"kind":"update","appliesTo":["tool:go"],"ecosystem":"Go","run":"go get -u=patch ./... && go mod tidy","runFor":"go get -u=patch {packages} && go mod tidy","list":"python3 -c 'import json,sys,subprocess\ntext=subprocess.check_output([\"go\",\"list\",\"-m\",\"-json\",\"all\"],text=True);decoder=json.JSONDecoder();rows=[]\nwhile text.strip():\n text=text.lstrip();p,end=decoder.raw_decode(text);text=text[end:]\n if p.get(\"Main\"):continue\n source=p.get(\"Replace\",{});origin=source.get(\"Path\",p[\"Path\"])\n if source.get(\"Version\"):origin+=\"@\"+source[\"Version\"]\n rows.append(dict(ecosystem=\"Go\",package=p[\"Path\"],version=p[\"Version\"].removeprefix(\"v\"),source=origin,attributes={\"indirect\":p.get(\"Indirect\",False)}))\nfor p in sorted(rows,key=lambda p:p[\"package\"]):print(json.dumps(p,sort_keys=True))'","files":["go.mod","go.sum"]}]
---
