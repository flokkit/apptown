// A build step that needs a devDependency, which must not ship.
const fs = require("node:fs");
const isNumber = require("is-number");
fs.mkdirSync("dist", { recursive: true });
fs.writeFileSync("dist/index.html", "<p>built " + isNumber(7) + "</p>\n");
