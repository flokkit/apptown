import { useState } from 'react'

// The workshop robot: a floating companion that makes "what is
// happening?" legible to a first-time user at a glance. Big, animated,
// minimizable — the tiny status chip stays for people who want the detail;
// this is the at-a-glance layer. Self-contained SVG + CSS keyframes (no assets, no network).
//
// States:
//   building  — bobbing, hammer arm swinging, sparks; the caption says
//               only "working…": made-up activities would claim work the
//               robot cannot see
//   your-turn — gentle float, blinking, waving; green antenna
//   napping   — connection lost; dimmed, zzz

export default function RobotBuddy({ busy, connected }: { busy: boolean; connected: boolean }) {
  const [min, setMin] = useState(() => localStorage.getItem('robot-min') === '1')

  const mode = !connected ? 'napping' : busy ? 'building' : 'idle'
  const caption = mode === 'napping' ? 'connection lost' : mode === 'building' ? 'working…' : 'your turn'

  if (min) {
    return (
      <button className={`robot-buddy robot-min robot-${mode}`} title={caption} aria-label={`robot helper: ${caption} · show it`}
        onClick={() => { setMin(false); localStorage.setItem('robot-min', '0') }}>
        <RobotFace mode={mode} small />
      </button>
    )
  }
  return (
    <div className={`robot-buddy robot-${mode}`}>
      <button className="robot-hide" title="tuck the robot into the corner" aria-label="tuck the robot into the corner"
        onClick={() => { setMin(true); localStorage.setItem('robot-min', '1') }}>—</button>
      <div className="robot-body-wrap">
        <RobotFace mode={mode} />
        {mode === 'building' && <span className="robot-sparks" aria-hidden>✦<i>✧</i><b>✦</b></span>}
        {mode === 'napping' && <span className="robot-zzz" aria-hidden>z<i>z</i><b>z</b></span>}
      </div>
      <div className="robot-caption">{caption}</div>
    </div>
  )
}

function RobotFace({ mode, small }: { mode: string; small?: boolean }) {
  const s = small ? 34 : 84
  return (
    <svg width={s} height={s} viewBox="0 0 100 100" className="robot-svg" aria-label="robot helper">
      {/* antenna */}
      <line x1="50" y1="16" x2="50" y2="6" stroke="currentColor" strokeWidth="3" />
      <circle className="robot-antenna" cx="50" cy="5" r="4.5" />
      {/* head */}
      <rect x="22" y="16" width="56" height="42" rx="12" fill="var(--panel2)" stroke="currentColor" strokeWidth="3" />
      {/* eyes — blink via CSS scaleY */}
      <g className="robot-eyes">
        <circle cx="38" cy="36" r="5.5" fill="currentColor" />
        <circle cx="62" cy="36" r="5.5" fill="currentColor" />
      </g>
      {/* mouth: smile when idle, small o when building, flat when napping */}
      {mode === 'idle' && <path d="M40 47 Q50 54 60 47" stroke="currentColor" strokeWidth="3" fill="none" strokeLinecap="round" />}
      {mode === 'building' && <circle cx="50" cy="49" r="3.5" stroke="currentColor" strokeWidth="2.5" fill="none" />}
      {mode === 'napping' && <line x1="42" y1="49" x2="58" y2="49" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />}
      {/* body */}
      <rect x="30" y="62" width="40" height="26" rx="8" fill="var(--panel2)" stroke="currentColor" strokeWidth="3" />
      <circle cx="50" cy="75" r="4" stroke="currentColor" strokeWidth="2" fill="none" />
      {/* left arm: waves when idle */}
      <g className={mode === 'idle' ? 'robot-arm-wave' : ''}>
        <line x1="30" y1="68" x2="16" y2="60" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round" />
      </g>
      {/* right arm: hammers when building */}
      <g className={mode === 'building' ? 'robot-arm-hammer' : ''}>
        <line x1="70" y1="68" x2="84" y2="60" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round" />
        {mode === 'building' && <rect x="82" y="52" width="10" height="7" rx="1.5" fill="currentColor" />}
      </g>
    </svg>
  )
}
