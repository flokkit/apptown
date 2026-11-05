import { Spinner } from '../../Spinner'
/* Every movement reports a server stage; no timers or guessed progress. Unreadable
 * progress freezes the frame. 1.5px round lines, light washes, 300×150, phone first.
 * Existing variables only: busy = working, ok = live, bad = stopped; scene ink/ground.
 * Arrive in 200–450ms ease-out; small 2–3s idle loops; celebrate once under 1.5s.
 * Failure closes calmly; checking knocks once more and turns away. Reduced motion keeps still frames.
 * The image names the current stage; the stage list remains text.
 */
import { useRef, useState, type CSSProperties } from 'react'
import type { DeployStage } from '../../wire.gen'

export const deployWords: Record<DeployStage, string> = {
  preparing: 'getting the code ready', 'backing-up': 'backing up the data', building: 'building the app',
  starting: 'starting the app', checking: 'checking the app answers', live: 'live',
}
export type DeployOutcome = 'running' | 'passed' | 'failed' | 'paused'

export function DeployScene({ stage, failedStage, outcome, showStages = true, compactStages = false }: {
  stage?: DeployStage; failedStage?: DeployStage; outcome: DeployOutcome; showStages?: boolean; compactStages?: boolean
}) {
  const lastStage = useRef<DeployStage>()
  if (stage) lastStage.current = stage
  const sawRunning = useRef(false)
  const [celebrated, setCelebrated] = useState(false)
  if (outcome === 'running' && stage && stage !== 'live') sawRunning.current = true
  const current = outcome === 'failed' ? failedStage : outcome === 'passed' ? 'live' : lastStage.current
  const stages = Object.keys(deployWords) as DeployStage[]
  const frames: DeployStage[] = ['preparing', 'building', 'starting', 'checking', 'live']
  const index = current ? frames.indexOf(current === 'backing-up' ? 'preparing' : current) + 1 : 0
  const celebrate = (outcome === 'passed' || outcome === 'running' && stage === 'live') && sawRunning.current && !celebrated
  const label = outcome === 'failed' ? `Stopped while ${current ? deployWords[current] : 'deploying'}`
    : current === 'live' ? 'The app is live' : current ? `Deploying: ${deployWords[current]}` : 'Waiting for deployment progress'
  const classes = ['deploy-scene', ...frames.slice(0, index).map((_, i) => `g${i + 1}`),
    (outcome === 'running' || outcome === 'paused') && current !== 'backing-up' && index < 5 ? `n${index}` : '', outcome === 'failed' ? 'fail' : '',
    outcome === 'paused' ? 'paused' : '', current === 'backing-up' ? 'backing-up' : '',
    current && stages.indexOf(current) >= stages.indexOf('backing-up') ? 'has-backup-shed' : '', celebrate ? 'celebrate' : 'rest'].filter(Boolean).join(' ')
  const stageRows = () => stages.map((s, i) => {
    const stopped = s === current && outcome === 'failed'
    const finished = (current ? i < stages.indexOf(current) : false) || outcome === 'passed'
    const running = s === current && outcome === 'running'
    return <li key={s} aria-current={s === current ? 'step' : undefined}>
      <span className={`deploy-stage-mark ${stopped ? 'bad' : finished ? 'ok' : running ? 'busy' : 'muted'}`}>
        {running ? <Spinner /> : stopped ? '✗' : finished ? '✓' : '·'}
      </span>
      <span>{compactStages ? ['Code', 'Backup', 'Build', 'Start', 'Check', 'Live'][i] : deployWords[s][0].toUpperCase() + deployWords[s].slice(1)}</span>
      <small className="muted">{stopped ? 'stopped' : running ? 'now' : ''}</small>
    </li>
  })
  return <div className={classes} onAnimationEnd={e => {
    if (e.animationName === 'deploy-burst') setCelebrated(true)
  }}>
    <div className="deploy-scene-panel"><svg viewBox="0 0 300 150" role="img" aria-label={label}>
          <line className="ink ground" x1="10" y1="130" x2="290" y2="130"></line>
          <rect className="ink plan" x="110" y="52" width="80" height="78"></rect>
          <g className="backup-shed" aria-hidden="true">
            <path className="ink" d="M16 108 L34 96 L52 108 V130 H16 Z" />
            <rect className="ink" x="27" y="114" width="14" height="16" />
          </g>
          <g className="backup-box" aria-hidden="true">
            <rect className="ink" x="27" y="118" width="14" height="12" />
            <path className="ink" d="M27 122 H41 M34 118 V122" />
          </g>
          <g className="crane">
            <line className="ink" x1="240" y1="130" x2="240" y2="18"></line>
            <line className="ink" x1="234" y1="130" x2="246" y2="130"></line>
            <g className="jib">
              <line className="ink" x1="110" y1="20" x2="262" y2="20" />
            </g>
            <g className="idle-rig"><g className="idle-trolley">
              <rect className="ink" x="224" y="18" width="8" height="4" />
              <line className="ink" x1="228" y1="22" x2="228" y2="36" />
              <path className="ink" d="M228 36 v2 q0 3 -3 2" />
            </g></g>
          </g>
          <g className="deliveries" aria-hidden="true">{[
            [112, 104, 38, 26], [150, 104, 38, 26], [112, 78, 38, 26],
            [150, 78, 38, 26], [112, 52, 38, 26], [150, 52, 38, 26], [106, 30, 88, 22],
          ].map(([x, y, width, height], i) => {
            const cx = x + width / 2
            // The trolley starts beside the mast with the hook raised. Only
            // its cable and hook move vertically; the jib always stays level.
            return <g className="delivery" key={i} style={{ '--delay': `${i}s`, '--lift': `${26 - y}px`, '--travel': `${228 - cx}px`, '--cable': 2 / (y - 24) } as CSSProperties}>
              <g className="trolley">
                <rect className="ink" x={cx - 4} y="18" width="8" height="4" />
                <g transform={`translate(${cx} 22)`}>
                  <line className="ink cable" x1="0" x2="0" y2={y - 24} />
                </g>
                <g className="hoist">
                  <path className="ink" d={`M${cx} ${y - 2} v2 q0 3 -3 2`} />
                </g>
                <g className="load">
                  {i === 6 ? <path className="ink" d="M106 52 L150 30 L194 52" /> : <rect className="ink" x={x} y={y} width={width} height={height} />}
                </g>
              </g>
            </g>
          })}</g>
          <rect className="ink blk b1" x="112" y="104" width="38" height="26"></rect>
          <rect className="ink blk b2" x="150" y="104" width="38" height="26"></rect>
          <rect className="ink blk b3" x="112" y="78" width="38" height="26"></rect>
          <rect className="ink blk b4" x="150" y="78" width="38" height="26"></rect>
          <rect className="ink blk b5" x="112" y="52" width="38" height="26"></rect>
          <rect className="ink blk b6" x="150" y="52" width="38" height="26"></rect>
          <path className="ink roof" d="M106 52 L150 30 L194 52"></path>
          <rect className="ink win w1" x="121" y="59" width="10" height="10"></rect>
          <rect className="ink win w2" x="169" y="59" width="10" height="10"></rect>
          <rect className="ink win w3" x="121" y="85" width="10" height="10"></rect>
          <rect className="ink win w4" x="169" y="85" width="10" height="10"></rect>
          <rect className="ink win w5" x="121" y="110" width="10" height="10"></rect>
          <rect className="ink win w6" x="169" y="110" width="10" height="10"></rect>
          <rect data-door={current === 'live' ? 'open' : 'closed'} className="ink door" x="143" y="112" width="14" height="18"></rect>
          <g className="visitor">
            <circle className="ink" cx="130" cy="113" r="3"></circle>
            <line className="ink" x1="130" y1="116" x2="130" y2="124"></line>
            <line className="ink" x1="130" y1="118" x2="138" y2="116"></line>
            <line className="ink" x1="130" y1="124" x2="127" y2="130"></line>
            <line className="ink" x1="130" y1="124" x2="133" y2="130"></line>
          </g>
          <rect className="ink tilt" x="150" y="40" width="38" height="12"></rect>
          <g className="sign">
            <rect className="ink" x="64" y="104" width="34" height="14"></rect>
            <line className="ink" x1="81" y1="118" x2="81" y2="130"></line>
            <text className="sign-label" x="81" y="114" textAnchor="middle">closed</text>
          </g>
          <line className="ink pole" x1="150" y1="30" x2="150" y2="8"></line>
          <path className="ink flag" d="M150 8 L166 12 L150 16 Z"></path>
          <g className="spark">{Array.from({ length: 12 }, (_, i) => {
            const angle = (i * 30) * Math.PI / 180
            return <line className="ink" key={i} x1={150 + Math.cos(angle) * 33} y1={34 + Math.sin(angle) * 24}
              x2={150 + Math.cos(angle) * 39} y2={34 + Math.sin(angle) * 29} />
          })}</g>
    </svg></div>
    {showStages && (current || compactStages) && <>
      <ol className={`deploy-stages${compactStages ? ' deploy-stages-compact' : ''}`} aria-label="Deployment stages">{stageRows()}</ol>
    </>}
  </div>
}
