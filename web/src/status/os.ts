// Operating system badges: a small mark in the system's own colour, drawn here from simple shapes
// (no logo files are loaded), and the plain-English name of what a server runs in.

import { s } from './dom'

interface OSMark {
  key: string
  name: string
  color: string
  draw: (c: string) => SVGElement[]
}

// spiral is an Archimedean spiral as a path: the swirl of the Debian mark, roughly.
function spiral(cx: number, cy: number, turns: number, r0: number, r1: number) {
  const n = Math.round(turns * 40)
  let d = ''
  for (let i = 0; i <= n; i++) {
    const a = (i / n) * turns * Math.PI * 2 + Math.PI * 0.15
    const r = r0 + ((r1 - r0) * i) / n
    d += `${i ? 'L' : 'M'}${(cx + Math.cos(a) * r).toFixed(2)} ${(cy + Math.sin(a) * r).toFixed(2)}`
  }
  return d
}

const stroke = (c: string, d: string, w = 2) => s('path', { d, fill: 'none', stroke: c, 'stroke-width': w, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' })
const fill = (c: string, d: string) => s('path', { d, fill: c })
const dot = (c: string, cx: number, cy: number, r: number) => s('circle', { cx, cy, r, fill: c })

const MARKS: OSMark[] = [
  { key: 'debian', name: 'Debian', color: '#d70a53', draw: (c) => [stroke(c, spiral(12.3, 11.6, 1.6, 1.2, 7.4), 2.1)] },
  {
    key: 'ubuntu',
    name: 'Ubuntu',
    color: '#e95420',
    draw: (c) => [
      s('circle', { cx: 12, cy: 12, r: 6.2, fill: 'none', stroke: c, 'stroke-width': 2.2 }),
      ...[0, 120, 240].map((deg) => {
        const a = ((deg - 90) * Math.PI) / 180
        return s('circle', { cx: 12 + Math.cos(a) * 7.2, cy: 12 + Math.sin(a) * 7.2, r: 2.3, fill: c, stroke: 'var(--surface)', 'stroke-width': 1.4 })
      }),
    ],
  },
  { key: 'alpine', name: 'Alpine', color: '#0d597f', draw: (c) => [fill(c, 'M2.5 18.5 9.2 7.8l3.6 5.6 2.4-3.4 6.3 8.5Z'), stroke('var(--surface)', 'M7.4 10.6l1.8 1.6 1.5-1.4', 1.2)] },
  {
    key: 'centos',
    name: 'CentOS',
    color: '#932279',
    draw: () =>
      [
        ['#9ccd2a', 0, -4.6],
        ['#932279', 4.6, 0],
        ['#efa724', 0, 4.6],
        ['#262577', -4.6, 0],
      ].map(([col, dx, dy]) => s('rect', { x: 12 + Number(dx) - 3.2, y: 12 + Number(dy) - 3.2, width: 6.4, height: 6.4, rx: 1.2, fill: String(col), transform: `rotate(45 ${12 + Number(dx)} ${12 + Number(dy)})` })),
  },
  { key: 'rocky', name: 'Rocky Linux', color: '#10b981', draw: (c) => [dot(c, 12, 12, 8.5), fill('var(--surface)', 'M4.6 15.6 9.4 10.4l3.2 3.2 2.4-2.2 4.4 4.6a8.5 8.5 0 0 1-14.8-.4Z')] },
  {
    key: 'alma',
    name: 'AlmaLinux',
    color: '#0069da',
    draw: () => ['#ff4649', '#ffc700', '#86da2f', '#24c2ff', '#0069da'].map((col, i) => {
      const a = ((i * 72 - 90) * Math.PI) / 180
      return dot(col, 12 + Math.cos(a) * 5.6, 12 + Math.sin(a) * 5.6, 2.6)
    }),
  },
  { key: 'fedora', name: 'Fedora', color: '#51a2da', draw: (c) => [dot(c, 12, 12, 8.6), stroke('#fff', 'M10.2 18v-7.4a2.6 2.6 0 0 1 4.6-1.6M8 12.6h5.6', 2)] },
  { key: 'arch', name: 'Arch Linux', color: '#1793d1', draw: (c) => [fill(c, 'M12 2.8 3.6 20.6c2.4-1.6 4.4-2.6 6.2-3a2.6 2.6 0 0 1 4.4 0c1.8.4 3.8 1.4 6.2 3Z')] },
  { key: 'opensuse', name: 'openSUSE', color: '#73ba25', draw: (c) => [fill(c, 'M3.5 13.5c0-4.4 3.8-7.5 8.4-7.5 3.3 0 5.6 1.4 7 3.4l1.6-.4-.6 3.6c.6 2.6-1.4 5.4-5 5.4H8.8c-3 0-5.3-1.8-5.3-4.5Z'), dot('var(--surface)', 15.6, 11.2, 1.8), dot(c, 16, 11.2, .8)] },
  { key: 'oracle', name: 'Oracle Linux', color: '#c74634', draw: (c) => [s('rect', { x: 3.5, y: 7.2, width: 17, height: 9.6, rx: 4.8, fill: 'none', stroke: c, 'stroke-width': 2.6 })] },
  { key: 'amazon', name: 'Amazon Linux', color: '#ff9900', draw: (c) => [stroke(c, 'M4.5 13.5c4.4 3 10.6 3 15 0', 2.2), stroke(c, 'M16.8 12.4l2.9 1.1-.9 2.9', 1.8)] },
  { key: 'mint', name: 'Linux Mint', color: '#87cf3e', draw: (c) => [s('rect', { x: 3.6, y: 3.6, width: 16.8, height: 16.8, rx: 4.6, fill: c }), stroke('#fff', 'M8 9v6.2a1.8 1.8 0 0 0 1.8 1.8h4.6a1.8 1.8 0 0 0 1.8-1.8V11a1.8 1.8 0 0 0-3.6 0v4.4M12.6 11a1.8 1.8 0 0 0-3.6 0', 1.5)] },
  {
    key: 'nixos',
    name: 'NixOS',
    color: '#5277c3',
    draw: (c) =>
      [0, 60, 120, 180, 240, 300].map((deg) => {
        const p = stroke(c, 'M12 12 12 4.2', 2.2)
        p.setAttribute('transform', `rotate(${deg} 12 12)`)
        return p
      }),
  },
  { key: 'raspbian', name: 'Raspberry Pi OS', color: '#c51a4a', draw: (c) => [dot(c, 9.4, 11.4, 3), dot(c, 14.6, 11.4, 3), dot(c, 12, 15.8, 3), dot(c, 12, 8.4, 2.2), stroke('#6cc04a', 'M9 5.4c1.4.4 2.4 1.2 3 2.4.6-1.2 1.6-2 3-2.4', 1.6)] },
  { key: 'manjaro', name: 'Manjaro', color: '#35bf5c', draw: (c) => [fill(c, 'M4 4h7v16H4Zm9 0h7v7h-7Zm0 9h7v7h-7Z')] },
  { key: 'gentoo', name: 'Gentoo', color: '#54487a', draw: (c) => [fill(c, 'M9.6 3.8c5.4-.6 10.6 2.6 10.6 6.4 0 3.2-5.4 4.4-6.6 6.6-1 2-3.6 3.6-6.4 3-3.6-.8-2.6-4.6.6-7.2-3.2-1.2-4.6-7.8 1.8-8.8Z'), dot('var(--surface)', 14.4, 9.2, 1.5)] },
  { key: 'kali', name: 'Kali Linux', color: '#557c94', draw: (c) => [stroke(c, 'M4 15.5c3-4.6 7.6-7 13-6.4l2.6-2.4M17 9.1c-1.6 2.2-1.6 5 .4 8.4', 1.9)] },
  { key: 'linux', name: 'Linux', color: '#8a94a3', draw: (c) => [s('rect', { x: 3.6, y: 5, width: 16.8, height: 14, rx: 2.6, fill: 'none', stroke: c, 'stroke-width': 1.8 }), stroke(c, 'M7.4 10l2.6 2.2-2.6 2.2M12.4 15h4', 1.8)] },
]

const MATCH: [RegExp, string][] = [
  [/debian/i, 'debian'],
  [/ubuntu/i, 'ubuntu'],
  [/alpine/i, 'alpine'],
  [/rocky/i, 'rocky'],
  [/alma/i, 'alma'],
  [/centos/i, 'centos'],
  [/fedora/i, 'fedora'],
  [/manjaro/i, 'manjaro'],
  [/arch/i, 'arch'],
  [/suse/i, 'opensuse'],
  [/oracle/i, 'oracle'],
  [/amazon/i, 'amazon'],
  [/mint/i, 'mint'],
  [/nixos/i, 'nixos'],
  [/raspbian|raspberry/i, 'raspbian'],
  [/gentoo/i, 'gentoo'],
  [/kali/i, 'kali'],
]

export function osMark(os: string | undefined): OSMark {
  const key = MATCH.find(([re]) => re.test(os || ''))?.[1] || 'linux'
  return MARKS.find((m) => m.key === key) || MARKS[MARKS.length - 1]
}

// osBadge is the mark of the system a server runs, on a soft disc of its colour. Next to the system's
// name it is decoration (hidden from screen readers); alone it names the system.
export function osBadge(os: string | undefined, cls = '', decorative = false): SVGElement {
  const m = osMark(os)
  const el = s('svg', { class: ('os-badge ' + cls).trim(), viewBox: '0 0 24 24', ...(decorative ? { 'aria-hidden': 'true' } : { role: 'img', 'aria-label': os || m.name }) })
  if (!decorative) el.append(s('title', {}, os || m.name))
  el.append(s('circle', { cx: 12, cy: 12, r: 12, fill: m.color, 'fill-opacity': 0.14 }), ...m.draw(m.color))
  return el
}

const VIRT: Record<string, string> = {
  kvm: 'KVM',
  xen: 'Xen',
  vmware: 'VMware',
  'hyper-v': 'Hyper-V',
  virtualbox: 'VirtualBox',
  parallels: 'Parallels',
  apple: 'Apple Virtualization',
  vm: 'A virtual machine',
  openvz: 'OpenVZ container',
  lxc: 'LXC container',
  'lxc-libvirt': 'LXC container',
  docker: 'Docker container',
  podman: 'Podman container',
  'systemd-nspawn': 'nspawn container',
  wsl: 'WSL',
  none: 'No hypervisor seen',
}

// virtName is what a server runs in, in words ('' when its agent does not say).
export function virtName(v: string | undefined): string {
  if (!v) return ''
  return VIRT[v] || v.charAt(0).toUpperCase() + v.slice(1)
}
