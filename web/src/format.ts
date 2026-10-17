// The formatters, once (debt review, finding 27: four relative-time
// formatters and two token formatters had drifted apart).

// Timestamps from the server are UTC. Most carry a zone marker (Z or an
// offset); SQLite-born ones ("2026-09-02 10:11:12" or "…T10:11:12") do not,
// and a bare stamp would be read as LOCAL time — hours off on a Sydney
// screen. Append Z only when nothing says the zone.
export function utcDate(ts: string): Date {
  const t = ts.trim().replace(' ', 'T')
  const zoned = /(Z|[+-]\d\d:?\d\d)$/i.test(t)
  return new Date(zoned ? t : t + 'Z')
}

// "just now", "5m ago", "3h ago", "2d ago". Empty for anything unparseable
// or in the future — callers put their own words there ("nothing shipped
// yet").
export function relTime(ts: string, now = Date.now()): string {
  if (!ts) return ''
  const d = (now - utcDate(ts).getTime()) / 1000
  if (!(d >= 0)) return ''
  if (d < 90) return 'just now'
  if (d < 3600) return `${Math.round(d / 60)}m ago`
  if (d < 86400) return `${Math.round(d / 3600)}h ago`
  return `${Math.round(d / 86400)}d ago`
}

// Relative inside a day, then the date: "09-02 10:11". For tables where a
// week-old row should say when, not how long ago.
export function fmtWhen(iso: string, now = Date.now()): string {
  const mins = (now - utcDate(iso).getTime()) / 60000
  if (mins >= 0 && mins < 60) return `${Math.max(1, Math.round(mins))}m ago`
  if (mins >= 0 && mins < 60 * 24) return `${Math.round(mins / 60)}h ago`
  return iso.slice(5, 16).replace('T', ' ')
}

export const fmtDuration = (s: number): string => {
  if (s < 90) return `${s}s`
  if (s < 5400) return `${Math.round(s / 60)}m`
  return `${(s / 3600).toFixed(1)}h`
}

export const fmtTokens = (n: number): string =>
  n >= 1e9 ? `${(n / 1e9).toFixed(1)}B` : n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(1)}k` : String(n)

export const fmtCost = (micros: number): string => `$${(micros / 1e6).toFixed(micros >= 1e6 ? 2 : 4)}`

// fmtBytes is a stored size a person reads (a backup, a copy): 812 B,
// 12 KB, 3.4 MB, 1.20 GB. Guidance sizes use fmtSize, which counts context.
export const fmtBytes = (n: number): string =>
  n < 1024 ? `${n} B` : n < 1024 * 1024 ? `${(n / 1024).toFixed(0)} KB` : n < 1024 * 1024 * 1024 ? `${(n / 1024 / 1024).toFixed(1)} MB` : `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`

// Guidance sizes: what a block costs an agent's context. 812 B · 4.2k · 21k.
export const fmtSize = (bytes: number): string =>
  bytes < 1024 ? `${bytes} B` : bytes < 10 * 1024 ? `${(bytes / 1024).toFixed(1)}k` : `${Math.round(bytes / 1024)}k`

// The tooltip that goes with fmtSize: a rough token count, since that is the
// unit the context actually fills in.
export const sizeTitle = (bytes: number): string => `${bytes.toLocaleString()} bytes, roughly ${fmtTokens(Math.round(bytes / 4))} tokens`

// "8 Nov 2026" in the reader's own zone: a calendar day, for a date a
// person plans by (kept until). Empty for no time.
export function fmtDay(ts: string): string {
  if (!ts) return ''
  const d = utcDate(ts)
  return isNaN(d.getTime()) ? '' : d.toLocaleDateString('en-AU', { day: 'numeric', month: 'short', year: 'numeric' })
}

// "1 Oct, 21:52" in the reader's own zone: when something happened, for a
// record a person reads (a change opened, a check ran). Empty for no time.
export function fmtStamp(ts: string): string {
  if (!ts) return ''
  const d = utcDate(ts)
  if (isNaN(d.getTime())) return ''
  const day = d.toLocaleDateString('en-AU', { day: 'numeric', month: 'short' })
  const time = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  return `${day}, ${time}`
}

// "14:02" when the moment is today in the reader's own zone, else
// "9 Oct, 14:02": when an answer shown was last read, for a mark that must
// stay short. Takes ms since the epoch, as a read's answeredAt does.
export function fmtLastRead(ms: number, now = Date.now()): string {
  const d = new Date(ms)
  if (isNaN(d.getTime())) return ''
  if (d.toDateString() === new Date(now).toDateString()) return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  return fmtStamp(d.toISOString())
}
