---
name: lang/java/dependency-updates
tags: dependencies,java
fixes: [{"kind":"update","ecosystem":"Maven","files":["pom.xml"]}]
---

Use the Maven versions plugin across every module. Run `versions:use-latest-releases` with `-DallowMajorUpdates=false` at routine level; aggressive allows major releases. Update every module and sub-project.
