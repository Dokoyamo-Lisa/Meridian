// The globe's layout key: it changes whenever a pin is added, removed, moved, renamed or becomes
// exact, or a pin's servers change - a server replaced by another in the same city keeps the pin's
// place and label, and the pin must not keep the old one's id for hover and click. Same key: the
// globe only gets the news (updatePlace).

interface Pin {
  key: string
  lat: number
  lon: number
  label: string
  ids: number[]
  approx?: boolean
}

interface Hub {
  lat: number
  lon: number
  label: string
}

export function globeLayoutKey(places: Pin[], hub: Hub | null): string {
  return (
    places.map((p) => `${p.key}@${p.lat.toFixed(3)},${p.lon.toFixed(3)}${p.approx ? '~' : ''}=${p.label}[${p.ids.join(',')}]`).join('|') +
    (hub ? `#${hub.lat},${hub.lon}=${hub.label}` : '')
  )
}
