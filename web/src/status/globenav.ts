// Choosing a place on the globe, by pointer or keyboard: what a pin leads to, and the next pin.

export interface PinServer {
  id: number
  name: string
  online: boolean
}

export interface PinChoice {
  id: number
  name: string
  href: string
  online: boolean
}

// pinChoice is what selecting a pin does: one server opens it; several get a chooser that links to
// each - the server table may show them folded away (or, at full screen, not at all).
export function pinChoice(ids: number[], byId: (id: number) => PinServer | null | undefined): { go: string } | { choose: PinChoice[] } | null {
  const list = ids
    .map((id) => byId(id))
    .filter((s): s is PinServer => !!s)
    .map((s) => ({ id: s.id, name: s.name, href: `#/s/${s.id}`, online: s.online }))
  if (!list.length) return null
  if (list.length === 1) return { go: list[0].href }
  return { choose: list }
}

// pinStep is the pin the arrow keys go to from cur: the next (dir 1) or previous (-1), round the list.
export function pinStep(keys: string[], cur: string | null, dir: 1 | -1): string | null {
  if (!keys.length) return null
  const i = cur == null ? -1 : keys.indexOf(cur)
  if (i < 0) return dir > 0 ? keys[0] : keys[keys.length - 1]
  return keys[(i + dir + keys.length) % keys.length]
}

// pickSig is what an open chooser shows: its servers, their names and whether they are online.
export function pickSig(list: PinChoice[]): string {
  return list.map((x) => `${x.id}:${x.name}:${x.online ? 1 : 0}`).join('|')
}

// pickRefresh is what a status refresh does to an open chooser: keep it, rebuild it with what its
// pin holds now, or close it - the pin is gone, or is down to one server (or none).
export function pickRefresh(shown: string, now: ReturnType<typeof pinChoice>): 'keep' | 'rebuild' | 'close' {
  if (!now || !('choose' in now)) return 'close'
  return pickSig(now.choose) === shown ? 'keep' : 'rebuild'
}
