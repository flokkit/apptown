import { useEffect, useState } from 'react'
import type { ChangeView } from '../../api'
import type { FailureAdvice } from '../../wire.gen'
import { useGet } from '../../data'
export function useFailureAdvice(view: ChangeView) {
  const w = view.waitingOn
  const attempt = w.attempt ?? 0
  const path = w.kind === 'check' && w.step ? `/steps/${w.step}/advice?attempt=${attempt}` : null
  const [answered, setAnswered] = useState<string | null>(null)
  const q = useGet<FailureAdvice>(path, { poll: answered === path ? 0 : 3000, kind: 'immutable' })
  useEffect(() => {
    if (q.data?.text) setAnswered(path)
  }, [path, q.data?.text])
  const a = q.refreshFailed ? undefined : q.data
  const offered = [...(view.next ?? []), ...(view.detail?.next ?? [])]
  const recommend = a?.recommend && offered.some(n => n.act === a.recommend && (!['override', 'retry'].includes(n.act) || n.stepId === w.step)) ? a.recommend : ''
  return { text: a?.text ?? '', recommend }
}
export function recommendationWords(act: string) {
  return ({ 'send-back': 'Send it back to the helper', rework: 'Ask the helper to fix it', override: 'Accept it with an exception', retry: 'Try the check again' } as Record<string, string>)[act] ?? ''
}
