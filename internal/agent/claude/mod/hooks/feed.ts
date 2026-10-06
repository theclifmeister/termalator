import type { TerminatrWatch } from '../types'

// feed takes what is left of the last piece and a new piece of `tm
// watch --json` output: a piece may end inside a line or hold several.
// It answers the whole lines that parse, in order, and the rest.
export function feed(rest: string, text: string): { watches: TerminatrWatch[]; rest: string } {
  const lines = (rest + text).split('\n')
  const tail = lines.pop() ?? ''
  const watches: TerminatrWatch[] = []
  for (const l of lines) {
    if (l.trim() === '') continue
    try {
      watches.push(JSON.parse(l) as TerminatrWatch)
    } catch {
      // not a watch line: skip it
    }
  }
  return { watches, rest: tail }
}
