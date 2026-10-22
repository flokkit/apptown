// Loads a production dependency at start and serves the web workspace's build.
const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const ms = require("ms");
const page = fs.readFileSync(path.join(__dirname, "..", "web", "dist", "index.html"));
http.createServer((req, res) => {
  res.end(req.url === "/healthz" ? "ok " + ms(60000) : page);
}).listen(8080);
