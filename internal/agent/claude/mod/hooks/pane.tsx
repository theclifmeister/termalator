// The coordinator's /tm pane (docs/SPEC.md §8.6, Mods): the project's
// dashboard beside the coordinator, on every surface the session draws
// on, a phone through Remote Control included. What waits for the user
// comes first (tasks to accept, threads' questions, red CI, blocked
// tasks), then the inbox, the threads, compact, and the tasks on deck.
//
// It follows `tm watch --project <slug> --json` for the session's life
// and keeps the latest line in $.state. Unless [mods] pane is off
// (TERMINATR_PANE=off) it opens by itself once the session starts, where
// it docks as a sidebar: the fullscreen layout, from 144 columns (the
// engine seats a pane opened unasked from there). /tm opens or closes it
// at any width, with no model turn.
//
// Its buttons never act themselves: each sends the coordinator a fixed
// line in the user's words ("accept T63", hooks/dashboard.ts), which the
// model can't fake, as it can't press a button. Report opens the
// thread's latest report in a pane of its own, with no turn.

import { atom, read, update } from 'claude-code'
import type { Elements, EngineInterface, On, RenderSurface, RenderViewport } from 'claude-code'

import type { TerminatrNeed, TerminatrProject, TerminatrThread } from '../types'
import {
  BUTTON_LABELS, MAX_READY, askLine, asked, isNarrow, needButtons, needDetail, needHead, needKey, oneLine, projectFeed, reportTextOf,
  sentWords, summary, threadLine, todoLine,
} from './dashboard'
import type { AskKind } from './dashboard'

const project = atom({ plugin: 'terminatr', key: 'project' } as const, null)
const asks = atom({ plugin: 'terminatr', key: 'asks' } as const, [])
const sendBack = atom({ plugin: 'terminatr', key: 'sendBack' } as const, '')
const report = atom({ plugin: 'terminatr', key: 'report' } as const, null)

// The panes' ids.
const PANE = 'tm'
const REPORT_PANE = 'tm-report'

// SEE_MS and SEE_TRIES bound the wait for the first drawing that says
// whether the layout docks a pane: 10 s, then it stays shut.
const SEE_MS = 250
const SEE_TRIES = 40

// RETRY_MS spaces the feed's restarts after tm watch ended (a server
// restart).
const RETRY_MS = 5_000

// fullscreen is what the last drawing said of the layout: true where a
// pane docks beside the transcript, undefined before any said.
let fullscreen: boolean | undefined

// sawViewport keeps what a drawing's viewport says of the layout; the
// band's hook (register.ts) hands it every one.
export function sawViewport(v: RenderViewport | undefined) {
  if (v?.isFullscreen !== undefined) fullscreen = v.isFullscreen
}

export function registerPane(on: On) {
  // Only where a person is at the prompt: a coordinator always is.
  on('session.start', { isInteractive: true }, async ($, e, next) => {
    const started = await next(e)
    const bin = await $.env.get('TERMINATR_BIN')
    const slug = await $.env.get('TERMINATR_PROJECT')
    if ((await $.env.get('TERMINATR_ROLE')) !== 'coordinator' || !bin || !slug) return started
    await $.command.register({ name: 'tm', description: "Show or hide the project's dashboard pane" })
    // An unload ends the loops mid-call: nothing to report.
    followProject($, bin, slug).catch(() => {})
    if ((await $.env.get('TERMINATR_PANE')) !== 'off') openUnasked($).catch(() => {})
    return started
  })

  on('command.run', { command: 'tm' }, async $ => {
    if ((await $.ui.panes()).some(p => p.id === PANE)) {
      await $.ui.close({ id: PANE })
      return { text: 'tm pane closed.' }
    }
    await $.ui.open({ id: PANE, title: 'tm' })
    return { text: 'tm pane opened.' }
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    sawViewport(e.viewport)
    const p = await read($, project)
    // The phone draws no field: a send-back note goes in the prompt box.
    const hasInput = e.surface !== 'mobile'
    return drawPane($, $.ui.resolve(e), hasInput, e.props.bodyColumns, p, await read($, asks), await read($, sendBack))
  })

  on('ui.render', { component: 'Pane', requestId: REPORT_PANE }, async ($, e) => {
    const { Box, Text, Button, Markdown } = $.ui.resolve(e)
    const r = await read($, report)
    return (
      <Box flexDirection="column">
        <Text bold wrap="truncate-end">{r ? `${r.thread} · ${r.title}` : 'No report'}</Text>
        {r ? <Markdown key="report" text={r.text} /> : null}
        <Button key="close-report" role="dismiss" onPress={() => void $.ui.close({ id: REPORT_PANE })}>Close</Button>
      </Box>
    )
  })
}

// openUnasked opens the pane once a drawing says the layout docks one;
// the engine seats it from 144 columns and holds it until then.
async function openUnasked($: EngineInterface) {
  for (let i = 0; i < SEE_TRIES && fullscreen === undefined; i++) await $.clock.sleep(SEE_MS)
  if (fullscreen !== true) return
  if ((await $.ui.panes()).some(p => p.id === PANE)) return
  await $.ui.open({ id: PANE, title: 'tm' })
}

// followProject keeps the project's latest line in $.state, starting tm
// watch again when it ends; the loop ends when the module unloads.
async function followProject($: EngineInterface, bin: string, slug: string) {
  for (;;) {
    let rest = ''
    try {
      for await (const { stream, text } of $.process.spawn({ argv: [bin, 'watch', '--project', slug, '--json'] })) {
        if (stream !== 'stdout') continue
        const got = projectFeed(rest, text)
        rest = got.rest
        const p = got.projects.at(-1)
        if (p) await update($, project, () => p)
      }
    } catch (err) {
      $.ui.log(`terminatr: tm watch --project: ${String(err)}`, { to: 'debug' })
    }
    await $.clock.sleep(RETRY_MS)
  }
}

// ask sends the coordinator a button's line as the user's words, and
// keeps it as sent while the task's status holds.
async function ask($: EngineInterface, kind: AskKind, task: string, status: string, extra: { note?: string; pr?: number } = {}) {
  const text = askLine(kind, task, extra)
  try {
    const r = await $.prompt.submit({ text, asUser: true })
    if (r.drop !== undefined) {
      $.ui.toast(`Not sent: ${r.drop}`)
      return
    }
  } catch (err) {
    $.ui.toast(`Not sent: ${String(err)}`)
    return
  }
  await update($, asks, l => [...l.filter(a => a.task !== task), { task, kind, status }])
  $.ui.toast(`Sent the coordinator: ${text}`)
}

// startSendBack asks for the note: in the pane where the surface draws a
// field, in the prompt box where it draws none (the phone).
async function startSendBack($: EngineInterface, task: string, hasInput: boolean) {
  if (hasInput) {
    await update($, sendBack, () => task)
    return
  }
  await $.prompt.fill({ text: `send ${task} back: ` })
  $.ui.toast('Say what to change after it, then send')
}

async function submitSendBack($: EngineInterface, task: string, status: string, note: string) {
  if (!oneLine(note)) {
    $.ui.toast('Say what to change first')
    return
  }
  await update($, sendBack, () => '')
  await ask($, 'send-back', task, status, { note })
}

// openReport shows thread's latest report in the report pane.
async function openReport($: EngineInterface, thread: string, title: string) {
  const bin = await $.env.get('TERMINATR_BIN')
  if (!bin) return
  let text = ''
  try {
    text = reportTextOf((await $.process.run([bin, 'thread', 'show', thread, '--json'])).stdout)
  } catch (err) {
    $.ui.log(`terminatr: tm thread show ${thread}: ${String(err)}`, { to: 'debug' })
  }
  await update($, report, () => ({ thread, title, text: text || '_No report yet._' }))
  await $.ui.open({ id: REPORT_PANE, title: `${thread} report`, focus: true, closeOnEscape: true })
}

// press runs a need's button.
function press($: EngineInterface, b: AskKind | 'report', n: TerminatrNeed, hasInput: boolean) {
  const task = n.task ?? ''
  const status = n.status ?? ''
  switch (b) {
    case 'report':
      return openReport($, n.thread ?? '', n.title)
    case 'send-back':
      return startSendBack($, task, hasInput)
    case 'merge':
      return ask($, 'merge', task, status, { pr: n.pr_number })
    default:
      return ask($, b, task, status)
  }
}

// drawPane draws the dashboard, bodyColumns wide, on the surface whose
// elements els are; hasInput says it draws a field.
function drawPane($: EngineInterface, els: Elements[RenderSurface], hasInput: boolean, bodyColumns: number, p: TerminatrProject | null,
  sent: Parameters<typeof asked>[3], editing: string) {
  const { Box, Text } = els
  if (!p) return <Text dimColor>Waiting for tm watch…</Text>
  const ready = p.ready.slice(0, MAX_READY)
  return (
    <Box flexDirection="column">
      <Text bold wrap="truncate-end">{p.project + ' · ' + summary(p)}</Text>
      {p.needs_you.length === 0 ? <Text dimColor>Nothing waits for you.</Text> : <Text bold color="warning">Needs you</Text>}
      {p.needs_you.map(n => drawNeed($, els, hasInput, bodyColumns, n, asked(n.task, n.status, n.asked, sent), editing))}
      {p.inbox.length > 0 ? <Text bold>Inbox</Text> : null}
      {p.inbox.map(it => <Text key={'inbox-' + it.id} dimColor wrap="truncate-end">{'  ' + it.summary}</Text>)}
      {p.threads.length > 0 ? <Text bold>Threads</Text> : null}
      {p.threads.map(t => drawThread($, els, bodyColumns, t))}
      {ready.length > 0 ? <Text bold>On deck</Text> : null}
      {ready.map(t => {
        const was = asked(t.task, t.status, t.asked, sent)
        return (
          <Box key={'ready-' + t.task} flexDirection="row" gap={1}>
            <Text wrap="truncate-end">{todoLine(t)}</Text>
            {was
              ? <Text dimColor>{sentWords(was)}</Text>
              : drawButton(els, 'delegate-' + t.task, BUTTON_LABELS.delegate, () => void ask($, 'delegate', t.task, t.status))}
          </Box>
        )
      })}
      {p.ready.length > MAX_READY ? <Text dimColor>{`  and ${p.ready.length - MAX_READY} more`}</Text> : null}
    </Box>
  )
}

function drawButton(els: Elements[RenderSurface], key: string, label: string, onPress: () => void) {
  const { Button } = els
  return <Button key={key} onPress={onPress}>{label}</Button>
}

// drawNeed draws one thing that waits for the user: its head, its
// question or PR, its buttons (or what was asked), and the send-back
// note's field while it is asked for.
function drawNeed($: EngineInterface, els: Elements[RenderSurface], hasInput: boolean, bodyColumns: number, n: TerminatrNeed,
  was: string, editing: string) {
  const { Box, Text } = els
  const head = needHead(n)
  const detail = needDetail(n)
  const id = needKey(n)
  const buttons = needButtons(n, was).map(b =>
    drawButton(els, `${b}-${id}`, BUTTON_LABELS[b], () => void press($, b, n, hasInput)))
  const headRow = (
    <Text wrap="truncate-end">
      <Text bold>{head.ref}</Text>
      <Text color={n.why === 'ci' || n.why === 'queue' ? 'error' : 'warning'}>{' ' + head.why}</Text>
      {' ' + head.title}
    </Text>
  )
  const isEditing = hasInput && n.task !== undefined && editing === n.task
  return (
    <Box key={'need-' + id} flexDirection="column">
      {isNarrow(bodyColumns)
        ? headRow
        : <Box flexDirection="row" gap={1}><Box flexGrow={1} flexShrink={1}>{headRow}</Box>{buttons}</Box>}
      {detail.text
        ? <Text wrap="truncate-end" color={detail.tone === 'bad' ? 'error' : detail.tone === 'question' ? 'warning' : undefined}
          dimColor={detail.tone === 'ok'}>{'  ' + detail.text}</Text>
        : null}
      {was ? <Text dimColor>{'  ' + sentWords(was)}</Text> : null}
      {isNarrow(bodyColumns) && buttons.length > 0 ? <Box flexDirection="row" flexWrap="wrap" gap={1}>{buttons}</Box> : null}
      {isEditing && 'Input' in els ? drawNote($, els, n) : null}
    </Box>
  )
}

// drawNote is the send-back note's field and its cancel button.
function drawNote($: EngineInterface, els: Elements['terminal'] | Elements['desktop'] | Elements['vscode'], n: TerminatrNeed) {
  const { Box, Input, Button } = els
  const task = n.task ?? ''
  return (
    <Box flexDirection="row" gap={1}>
      <Input key={'note-' + task} label="What to change:" autoFocus submitLabel="send back"
        onSubmit={value => void submitSendBack($, task, n.status ?? '', value)} />
      <Button key={'cancel-' + task} onPress={() => void update($, sendBack, () => '')}>Cancel</Button>
    </Box>
  )
}

// drawThread is a thread's compact row, pressed to open its report.
function drawThread($: EngineInterface, els: Elements[RenderSurface], bodyColumns: number, t: TerminatrThread) {
  const { Button } = els
  const line = threadLine(t)
  const room = Math.max(10, bodyColumns - 1)
  const label = line.length > room ? line.slice(0, room - 1) + '…' : line
  return (
    <Button key={'thread-' + t.id} plain dimColor={!t.session || t.done}
      onPress={() => void openReport($, t.id, t.task?.title ?? t.title)}>{label}</Button>
  )
}
