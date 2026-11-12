import { useEffect, useState } from 'react'
import { fmtDuration } from './format'

export function Elapsed({ startedAt }: { startedAt: string }) {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  const seconds = Math.max(0, Math.floor((now - Date.parse(startedAt)) / 1000))
  return <span>{seconds < 60 ? fmtDuration(seconds) : `${Math.floor(seconds / 60)}m ${seconds % 60}s`}</span>
}
