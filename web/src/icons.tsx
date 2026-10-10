import type { Ref } from 'preact'
import { MarkMode, animClass, brand, logoSrc, markPieces } from './mark'
// Line icons on a 24-unit grid, drawn with the current text colour.

const paths: Record<string, string> = {
  overview: 'M3 13h6v8H3zM15 3h6v18h-6zM9 8h6v13H9z',
  server: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01',
  link: 'M10 14a4 4 0 0 0 5.66 0l3-3a4 4 0 0 0-5.66-5.66l-1 1M14 10a4 4 0 0 0-5.66 0l-3 3a4 4 0 0 0 5.66 5.66l1-1',
  activity: 'M3 12h4l3-8 4 16 3-8h4',
  users: 'M16 20v-1.5A3.5 3.5 0 0 0 12.5 15h-5A3.5 3.5 0 0 0 4 18.5V20M10 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM20 20v-1.5a3.5 3.5 0 0 0-2.5-3.35M15.5 4.15a3.5 3.5 0 0 1 0 6.7',
  settings: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z',
  plus: 'M12 5v14M5 12h14',
  close: 'M6 6l12 12M18 6L6 18',
  copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
  check: 'M5 12.5l4.5 4.5L19 7.5',
  qr: 'M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h2v2h-2zM18 14h2M14 18h2v2M18 18h2v2h-2',
  download: 'M12 4v11M7 10l5 5 5-5M5 20h14',
  refresh: 'M20 11a8 8 0 0 0-14.9-3.9L4 9M4 4v5h5M4 13a8 8 0 0 0 14.9 3.9L20 15M20 20v-5h-5',
  pause: 'M9 5v14M15 5v14',
  play: 'M7 4.5v15l12-7.5z',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
  edit: 'M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4',
  more: 'M5 12h.01M12 12h.01M19 12h.01',
  search: 'M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4',
  alert: 'M12 4L2.5 20h19zM12 10v4.5M12 17.5h.01',
  info: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5.5M12 7.5h.01',
  shield: 'M12 3l7.5 3v5.5c0 4.6-3.2 8.3-7.5 9.5-4.3-1.2-7.5-4.9-7.5-9.5V6z',
  key: 'M14.5 9.5a4.5 4.5 0 1 1-1.3-3.2M13.2 12.8L20 19.5M17 16.5l2-2',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.5 3.8 5.5 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.5-3.8-9S9.5 5.5 12 3z',
  back: 'M19 12H5M11 6l-6 6 6 6',
  external: 'M14 4h6v6M20 4l-9 9M18 14v6H4V6h6',
  lock: 'M5 11h14v10H5zM8 11V7.5a4 4 0 0 1 8 0V11',
  power: 'M12 3v8M6.6 6.6a8 8 0 1 0 10.8 0',
  terminal: 'M4 5h16v14H4zM7.5 9.5l3 2.5-3 2.5M12.5 15h4',
  chevron: 'M6 9l6 6 6-6',
  eye: 'M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  zap: 'M13 3L5 13.5h6L10.5 21 19 10.5h-6z',
  ban: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM5.7 5.7l12.6 12.6',
  clock: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7.5V12l3 2',
  route: 'M6 19a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM18 9a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM6 15V9.5A2.5 2.5 0 0 1 8.5 7H16M18 9v5.5a2.5 2.5 0 0 1-2.5 2.5H8',
  forward: 'M4 12h14M13 6l6 6-6 6',
  logout: 'M15 4h4v16h-4M10 8l-4 4 4 4M6 12h10',
  palette: 'M12 21a9 9 0 1 1 9-9c0 2.5-2 3.5-3.5 3.5H15a2 2 0 0 0-1.5 3.3c.4.5.5 1.2.1 1.7-.4.4-1 .5-1.6.5zM7.5 11.5h.01M10 7.5h.01M14.5 7.5h.01M17 11h.01',
  book: 'M4 5.5A2.5 2.5 0 0 1 6.5 3H20v15H6.5A2.5 2.5 0 0 0 4 20.5zM4 20.5A2.5 2.5 0 0 0 6.5 23H20v-5',
  cpu: 'M7 7h10v10H7zM10 10h4v4h-4zM9 3v4M15 3v4M9 17v4M15 17v4M3 9h4M3 15h4M17 9h4M17 15h4',
  map: 'M12 21s-6.5-5.6-6.5-11a6.5 6.5 0 0 1 13 0c0 5.4-6.5 11-6.5 11zM12 12.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5z',
}

export type IconName = keyof typeof paths

export function Icon({ name, size, class: cls }: { name: string; size?: 'sm'; class?: string }) {
  return (
    <svg class={['ic', size, cls].filter(Boolean).join(' ')} viewBox="0 0 24 24" aria-hidden="true">
      <path d={paths[name] || paths.info} />
    </svg>
  )
}

// LogoMark is the logo: a built-in mark - the rose (five petals, a bloom, the moon at its heart) or the
// umbrella seen from above - or the image uploaded in Settings › Panel › Logo. mode "once" plays the
// chosen animation once (sign-in pages), "loop" keeps playing it (loading); anim and mark preview another
// animation or built-in mark than the saved ones.
export function LogoMark(props: { mode?: MarkMode; size?: number; label?: string; anim?: string; mark?: string; markRef?: Ref<any> }) {
  const mode = props.mode || 'still'
  const cls = animClass(mode, props.anim)
  if (brand.custom) {
    return (
      <img
        ref={props.markRef}
        class={('logo-img logo ' + cls).trim()}
        src={logoSrc()}
        width={props.size}
        height={props.size}
        alt={props.label || ''}
        aria-hidden={props.label ? undefined : 'true'}
        draggable={false}
      />
    )
  }
  return (
    <svg
      ref={props.markRef}
      class={('umb-mark logo ' + cls).trim()}
      viewBox="0 0 24 24"
      width={props.size}
      height={props.size}
      role={props.label ? 'img' : undefined}
      aria-label={props.label}
      aria-hidden={props.label ? undefined : 'true'}
    >
      {markPieces(props.mark).map((w) => (
        <path class={'w' + (w.white ? ' white' : '')} d={w.d} fill={w.fill} style={{ '--i': String(w.i), '--dx': String(w.dx), '--dy': String(w.dy) } as any} />
      ))}
    </svg>
  )
}

export function Logo() {
  return <LogoMark />
}
