// What the history section says when a load fails while charts are showing: they stay, marked as
// out of date, and say which range they really are - after a failed change of range the selector
// names the range asked for while the charts still show the one before.

export interface HistoryNote {
  stale: boolean
  text: string | null
}

export function historyNote(shown: string | null, wanted: string, failed: boolean, label: (range: string) => string): HistoryNote {
  if (!failed || shown == null) return { stale: false, text: null }
  if (shown !== wanted) {
    return { stale: true, text: `The last ${label(wanted)} could not be loaded - these charts are still the last ${label(shown)}. Trying again in a minute.` }
  }
  return { stale: true, text: `These charts could not be brought up to date - trying again in a minute.` }
}
