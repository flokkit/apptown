// Bound repeated tool output before it reaches either the events file or live
// feed. Assistant messages and the separate session transcript stay whole.
const fs = require('node:fs');
const readline = require('node:readline');
const { once } = require('node:events');

function bound(text) {
  const bytes = Buffer.from(text);
  const limit = 16 * 1024;
  if (bytes.length <= limit) return text;
  let start = limit / 2, end = bytes.length - limit / 2;
  while ((bytes[start] & 0xc0) === 0x80) start--;
  while ((bytes[end] & 0xc0) === 0x80) end++;
  return bytes.subarray(0, start).toString() +
    `\n[AppTown cut ${end - start} bytes of tool output]\n` +
    bytes.subarray(end).toString();
}

function tools(value, inside = false) {
  if (typeof value === 'string') return inside ? bound(value) : value;
  if (!value || typeof value !== 'object') return value;
  if (Array.isArray(value)) return value.map(v => tools(v, inside));
  inside = inside || value.role === 'toolResult';
  for (const key of Object.keys(value)) {
    const result = key === 'toolResults' ||
      (value.type?.startsWith('tool_execution_') && (key === 'result' || key === 'partialResult'));
    value[key] = tools(value[key], inside || result);
  }
  return value;
}

(async () => {
  const file = fs.openSync(process.argv[1], 'w');
  try {
    const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
    for await (let line of lines) {
      let persist = true;
      // Pi may print non-event diagnostics. Keep them as they arrived.
      if (line.trimStart().startsWith('{')) {
        let event;
        try { event = JSON.parse(line); } catch { /* not a Pi event */ }
        if (event) {
          // Updates repeat cumulative output; the end event keeps the result.
          persist = event.type !== 'tool_execution_update';
          line = JSON.stringify(tools(event));
        }
      }
      line += '\n';
      if (persist) fs.writeSync(file, line);
      if (!process.stdout.write(line)) await once(process.stdout, 'drain');
    }
  } finally {
    fs.closeSync(file);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
