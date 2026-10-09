import type { Color, Elements, RenderSurface } from 'claude-code'

import type { TerminatrWatch } from '../types'
import { isPRBad, questionsText, steps } from './view'

// A part of the band's first row: its text and how it is painted.
type Part = { text: string; bold?: boolean; dimColor?: boolean; color?: Color }

// parts are the first row's: "T50 · steps 2/4 · #12 open, checks pass",
// each painted on its own. The counts are the status entry's alone.
export function parts(w: TerminatrWatch): Part[] {
  const out: Part[] = []
  if (w.task) out.push({ text: w.task.id, bold: true })
  const s = steps(w)
  if (s) out.push({ text: s, dimColor: true })
  if (w.pr) out.push(isPRBad(w.pr) ? { text: w.pr, color: 'error' } : { text: w.pr, dimColor: true })
  return out.map((p, i) => (i === 0 ? p : { ...p, text: ' · ' + p.text }))
}

// drawBand draws the band above the prompt, bodyColumns wide, for a
// watch line that shows (view.shows): the parts, then "now: current item"
// on the same row when the whole fits, on a row of its own otherwise;
// a row too long for the band is cut at its end. A thread waiting on
// the user says so on a last row. It takes the surface's elements, since
// $ never crosses an import. The engine adds the [-] that collapses it.
export function drawBand({ Box, Text }: Elements[RenderSurface], bodyColumns: number, w: TerminatrWatch) {
  const ps = parts(w)
  const current = w.task?.current ? 'now: ' + w.task.current : ''
  const ask = w.session.needs_you ? 'needs you: ' + w.session.needs_you : ''
  const headLen = ps.reduce((n, p) => n + p.text.length, 0)
  const isOneRow = !ask && headLen + 2 + current.length <= bodyColumns
  const head = (
    <Text wrap="truncate-end">
      {ps.map(p => (
        <Text bold={p.bold} dimColor={p.dimColor} color={p.color}>{p.text}</Text>
      ))}
      {isOneRow && current ? '  ' + current : ''}
    </Text>
  )
  return (
    <Box key="band" flexDirection="column">
      {head}
      {!isOneRow && current ? <Text wrap="truncate-end">{current}</Text> : null}
      {ask ? <Text color="warning" wrap="truncate-end">{ask}</Text> : null}
    </Box>
  )
}

// drawQuestions draws a coordinator's band: "2 questions waiting · a:
// Answer". A click on Answer, or a once the band has the focus (ctrl+x
// tab, or a click), runs onAnswer, which has the coordinator open them
// in its question dialog. It never blocks: the prompt box stays the
// user's, and an answer typed in chat works as before.
export function drawQuestions({ Box, Button, Text }: Elements[RenderSurface], n: number, onAnswer: () => void) {
  return (
    <Box key="questions" flexDirection="row">
      <Text color="warning" wrap="truncate-end">{questionsText(n) + ' · '}</Text>
      <Button key="answer" label="Answer" hotkey="a" plain onPress={onAnswer} />
    </Box>
  )
}
