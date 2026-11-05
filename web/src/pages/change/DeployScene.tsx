/* Every movement reports a server stage; no timers or guessed progress. Unreadable
 * progress freezes the frame. 1.5px round lines, light washes, 300×150, phone first.
 * Existing variables only: busy = working, ok = live, bad = stopped; scene ink/ground.
 * Arrive in 200–450ms ease-out; small 2–3s idle loops; celebrate once under 1.5s.
 * Failure stops motion, tilts once in 350ms, and closes. Reduced motion keeps still frames.
 * The image names the current stage; the stage list remains text.
 */
import { createContext, useContext, useId, useRef, useState, type ReactNode } from 'react'
import type { DeployStage } from '../../wire.gen'

export const deployWords: Record<DeployStage, string> = {
  preparing: 'getting the code ready', building: 'building the app',
  starting: 'starting the app', checking: 'checking the app answers', live: 'live',
}
// Overview and its dialog keep separate drawings mounted. The first visible
// celebration claims the page so closing the dialog cannot play another one.
const Celebration = createContext<{ owner: string | null; claim: (id: string) => void } | null>(null)
export function DeploySceneScope({ children }: { children: ReactNode }) {
  const [owner, setOwner] = useState<string | null>(null)
  return <Celebration.Provider value={{ owner, claim: id => setOwner(old => old ?? id) }}>{children}</Celebration.Provider>
}

export type DeployOutcome = 'running' | 'passed' | 'failed' | 'paused'

export function DeployScene({ stage, failedStage, outcome, showStages = true }: {
  stage?: DeployStage; failedStage?: DeployStage; outcome: DeployOutcome; showStages?: boolean
}) {
  const celebration = useContext(Celebration)
  const instance = useId()
  const lastStage = useRef<DeployStage>()
  const buildingArrived = useRef(false)
  if (stage === 'building' && lastStage.current === 'preparing' && outcome === 'running') buildingArrived.current = true
  if (stage && stage !== 'building') buildingArrived.current = false
  if (stage) lastStage.current = stage
  const sawRunning = useRef(false)
  const [celebrated, setCelebrated] = useState(false)
  if (outcome === 'running' && stage && stage !== 'live') sawRunning.current = true
  const current = outcome === 'failed' ? failedStage : outcome === 'passed' ? 'live' : lastStage.current
  const stages = Object.keys(deployWords) as DeployStage[]
  const index = current ? stages.indexOf(current) + 1 : 0
  const celebrate = outcome === 'passed' && sawRunning.current && !celebrated && (!celebration?.owner || celebration.owner === instance)
  const label = outcome === 'failed' ? `Stopped while ${current ? deployWords[current] : 'deploying'}`
    : current === 'live' ? 'The app is live' : current ? `Deploying: ${deployWords[current]}` : 'Waiting for deployment progress'
  const classes = ['deploy-scene', ...stages.slice(0, index).map((_, i) => `g${i + 1}`),
    (outcome === 'running' || outcome === 'paused') && index < 5 ? `n${index}` : '', outcome === 'failed' ? 'fail' : '',
    outcome === 'paused' ? 'paused' : '', celebrate ? 'celebrate' : 'rest', buildingArrived.current ? 'arrive-building' : ''].filter(Boolean).join(' ')
  return <div className={classes} onAnimationStart={() => {
    if (celebrate) celebration?.claim(instance)
  }} onAnimationEnd={e => {
    if (e.animationName === 'deploy-burst') setCelebrated(true)
  }}>
    <div className="deploy-scene-panel"><svg viewBox="0 0 300 150" role="img" aria-label={label}>
          <line className="ink ground" x1="10" y1="130" x2="290" y2="130"></line>
          <rect className="ink plan" x="110" y="52" width="80" height="78"></rect>
          <g className="crane">
            <line className="ink" x1="240" y1="130" x2="240" y2="18"></line>
            <line className="ink" x1="234" y1="130" x2="246" y2="130"></line>
            <g transform="translate(240 20)"><g className="jib">
              <line className="ink" x1="0" y1="0" x2="-90" y2="0"></line>
              <line className="ink" x1="0" y1="0" x2="22" y2="0"></line>
              <g transform="translate(-80 0)"><g className="hook">
                <line className="ink" x1="0" y1="0" x2="0" y2="18"></line>
              </g></g>
            </g></g>
          </g>
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
          <rect className="ink door" x="143" y="112" width="14" height="18"></rect>
          <g className="visitor">
            <circle className="ink" cx="130" cy="113" r="3"></circle>
            <line className="ink" x1="130" y1="116" x2="130" y2="124"></line>
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
          <g className="spark">
            <line className="ink" x1="150" y1="2" x2="150" y2="0"></line>
            <line className="ink" x1="162" y1="6" x2="168" y2="2"></line>
            <line className="ink" x1="138" y1="6" x2="132" y2="2"></line>
            <line className="ink" x1="170" y1="16" x2="176" y2="16"></line>
            <line className="ink" x1="130" y1="16" x2="124" y2="16"></line>
          </g>
    </svg></div>
    {showStages && current && <ol className="deploy-stages" aria-label="Deployment stages">
      {stages.map((s, i) => {
        const stopped = s === current && outcome === 'failed'
        const finished = i < index - 1 || outcome === 'passed'
        const running = s === current && outcome === 'running'
        return <li key={s} aria-current={s === current ? 'step' : undefined}>
          <span className={`deploy-stage-mark ${stopped ? 'bad' : finished ? 'ok' : running ? 'busy' : 'muted'}`}>
            {running ? <span className="ch-now-spinner" role="status" aria-label="running" /> : stopped ? '✗' : finished ? '✓' : '·'}
          </span>
          <span>{deployWords[s][0].toUpperCase() + deployWords[s].slice(1)}</span>
          <small className="muted">{stopped ? 'stopped' : running ? 'now' : ''}</small>
        </li>
      })}
    </ol>}
  </div>
}
