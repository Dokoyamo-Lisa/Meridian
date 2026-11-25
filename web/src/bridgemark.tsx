// DrawBridge's mark: an arch over the water, two towers, and two bridge leaves hung on cables. Raised,
// the leaves stand up into the arch; lowered, they swing down and meet in the middle, land with a
// small bump, and a seam of light closes between them. The cables follow the leaves as they move.
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'

const PIVOT_L = { x: 52, y: 86 }
const PIVOT_R = { x: 148, y: 86 }
const LEN = 48 // a leaf's length: the two meet at x = 100
const RAISED = -66 // degrees, the leaves' angle when raised: leaning in, under the arch
const TOWER_TOP = 50

const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2)

// the leaves' angle at progress p (0 raised .. 1 lowered); lowering ends in a small bump as they land
function angleAt(p: number, lowering: boolean) {
  if (!lowering) return RAISED * (1 - ease(p))
  const k = 0.84
  if (p < k) return RAISED * (1 - ease(p / k)) + 2.2 * ease(p / k)
  const q = (p - k) / (1 - k)
  return 2.2 * Math.cos(q * Math.PI * 1.5) * (1 - q)
}

// a point of a leaf, given in the leaf's own frame (x along it from the pivot, y up is negative)
function onLeaf(pivot: { x: number; y: number }, deg: number, x: number, y: number, mirror: boolean) {
  const a = (deg * Math.PI) / 180
  const lx = x * Math.cos(a) - y * Math.sin(a)
  const ly = x * Math.sin(a) + y * Math.cos(a)
  return { x: pivot.x + (mirror ? -lx : lx), y: pivot.y + ly }
}

export function BridgeMark(props: { lowered: boolean; size?: number; class?: string; title?: string }) {
  const uid = useMemo(() => 'db' + Math.random().toString(36).slice(2, 8), [])
  // amt: how far down the leaves are, 0 raised .. 1 lowered; it moves at a steady pace and the
  // curve (angleAt) gives it weight
  const [amt, setAmt] = useState(0)
  const [lowering, setLowering] = useState(true)
  const cur = useRef(0)
  useEffect(() => {
    const target = props.lowered ? 1 : 0
    setLowering(props.lowered)
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      cur.current = target
      setAmt(target)
      return
    }
    const start = cur.current
    const dur = (props.lowered ? 1500 : 900) * Math.abs(target - start)
    if (!dur) return
    const t0 = performance.now()
    let raf = 0
    const tick = (now: number) => {
      const k = Math.min(1, (now - t0) / dur)
      cur.current = start + (target - start) * k
      setAmt(cur.current)
      if (k < 1) raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [props.lowered])

  const deg = angleAt(amt, lowering)
  const closed = Math.max(0, (amt - (lowering ? 0.8 : 0.9)) / (lowering ? 0.2 : 0.1))
  const leaf = (pivot: { x: number; y: number }, mirror: boolean) => {
    const pt = (x: number, y: number) => onLeaf(pivot, deg, x, y, mirror)
    const poly = [pt(-3, -4.6), pt(LEN, -2.4), pt(LEN, 2.4), pt(-3, 4.6)].map((q) => `${q.x.toFixed(2)},${q.y.toFixed(2)}`).join(' ')
    const g1 = pt(5, -1.1)
    const g2 = pt(LEN - 4, -0.6)
    const g3 = pt(5, 1.1)
    const g4 = pt(LEN - 4, 0.6)
    const rail = [pt(2, -10), pt(LEN - 2, -8)]
    const posts = [6, 15, 24, 33, 42].map((x) => [pt(x, -4.2 + (x / LEN) * 1.6), pt(x, -9.6 + (x / LEN) * 1.6)])
    const towerTop = { x: pivot.x + (mirror ? 15 : -15), y: TOWER_TOP }
    const cables = [0.42, 0.72, 0.98].map((f) => pt(LEN * f, -8.8 + f * 1.4))
    return (
      <g class="dbm-side">
        {cables.map((c) => (
          <line class="dbm-cable" x1={towerTop.x} y1={towerTop.y} x2={c.x} y2={c.y} />
        ))}
        <polyline class="dbm-rail" points={rail.map((q) => `${q.x},${q.y}`).join(' ')} />
        {posts.map(([a, b]) => (
          <line class="dbm-post" x1={a.x} y1={a.y} x2={b.x} y2={b.y} />
        ))}
        <polygon class="dbm-leaf" points={poly} fill={`url(#${uid}-metal)`} />
        <line class="dbm-groove" x1={g1.x} y1={g1.y} x2={g2.x} y2={g2.y} />
        <line class="dbm-groove" x1={g3.x} y1={g3.y} x2={g4.x} y2={g4.y} />
      </g>
    )
  }
  const tower = (x: number) => (
    <g class="dbm-tower">
      <path d={`M${x - 6} 100 L${x - 4.5} ${TOWER_TOP + 4} L${x} ${TOWER_TOP - 3} L${x + 4.5} ${TOWER_TOP + 4} L${x + 6} 100 Z`} fill={`url(#${uid}-metal)`} />
      <line class="dbm-groove" x1={x - 1.4} y1={TOWER_TOP + 6} x2={x - 1.6} y2={97} />
      <line class="dbm-groove" x1={x + 1.4} y1={TOWER_TOP + 6} x2={x + 1.6} y2={97} />
    </g>
  )
  return (
    <svg class={'dbm ' + (props.class || '')} viewBox="0 0 200 124" width={props.size || 120} height={((props.size || 120) * 124) / 200} role="img" aria-label={props.title || 'DrawBridge'}>
      <defs>
        <linearGradient id={`${uid}-metal`} x1="0" y1="0" x2="0.35" y2="1">
          <stop offset="0" stop-color="currentColor" stop-opacity="1" />
          <stop offset="0.55" stop-color="currentColor" stop-opacity="0.78" />
          <stop offset="1" stop-color="currentColor" stop-opacity="0.5" />
        </linearGradient>
        <radialGradient id={`${uid}-seam`}>
          <stop offset="0" stop-color="var(--accent-ink, #8fb8ff)" stop-opacity="1" />
          <stop offset="1" stop-color="var(--accent-ink, #8fb8ff)" stop-opacity="0" />
        </radialGradient>
      </defs>
      <path class="dbm-arch" d="M14 100 A86 86 0 0 1 186 100" />
      <path class="dbm-arch dbm-arch-in" d="M24 100 A76 76 0 0 1 176 100" />
      <line class="dbm-mast" x1="100" y1="14" x2="100" y2="40" />
      {tower(PIVOT_L.x - 15)}
      {tower(PIVOT_R.x + 15)}
      {/* the fixed spans from each tower to its leaf's hinge, with the hinge's drum */}
      <rect class="dbm-span" x={PIVOT_L.x - 16} y={PIVOT_L.y - 3} width="16" height="6" fill={`url(#${uid}-metal)`} />
      <rect class="dbm-span" x={PIVOT_R.x} y={PIVOT_R.y - 3} width="16" height="6" fill={`url(#${uid}-metal)`} />
      {leaf(PIVOT_L, false)}
      {leaf(PIVOT_R, true)}
      <circle class="dbm-hinge" cx={PIVOT_L.x} cy={PIVOT_L.y} r="3.4" />
      <circle class="dbm-hinge" cx={PIVOT_R.x} cy={PIVOT_R.y} r="3.4" />
      <ellipse class="dbm-seam" cx="100" cy={PIVOT_L.y} rx={4 + 14 * closed} ry={2 + 4 * closed} fill={`url(#${uid}-seam)`} opacity={closed} />
      <line class="dbm-water" x1="6" y1="100" x2="194" y2="100" />
      <line class="dbm-water dbm-ripple" x1="34" y1="106" x2="86" y2="106" />
      <line class="dbm-water dbm-ripple" x1="114" y1="106" x2="166" y2="106" />
      <line class="dbm-water dbm-ripple" x1="66" y1="112" x2="134" y2="112" />
    </svg>
  )
}
