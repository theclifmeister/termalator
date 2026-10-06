import { expect, mock, test } from 'claude-code/testing'
import type { On, RenderViewport } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import type { TerminatrProject } from '../types'
import { askLine, asked, contextLine, contextToast, itemParts, itemTone, needButtons, projectFeed, threadLine } from '../hooks/dashboard'

const project = (over: Partial<TerminatrProject> = {}): TerminatrProject => ({
  project: 'demo',
  needs_you: [
    { why: 'review', task: 'T63', title: 'Dashboard pane', status: 'review', thread: 't-0061', pr: '#120 open, checks pass',
      pr_url: 'https://x/120', pr_number: 120, mergeable: true },
    { why: 'review', task: 'T61', title: 'Old review', status: 'review', asked: 'accept' },
    { why: 'question', task: 'T66', title: 'Brief across clear', status: 'started', thread: 't-0058', question: 'which file?' },
    { why: 'ci', task: 'T67', title: 'CI log', status: 'started', thread: 't-0060', pr: '#119 open, 2 checks failed', pr_number: 119 },
    { why: 'blocked', task: 'T70', title: 'Stuck', status: 'blocked' },
  ],
  inbox: [
    { id: 'i1', kind: 'accept', subject: 'T61', summary: 'the user accepts T61', count: 1, what: 'the user accepts T61' },
    { id: 'i2', kind: 'report', subject: 't-0059', summary: 'T64 Tools (t-0059) handed in report 3', count: 3, task: 'T64', what: 'handed in report 3', title: 'Tools' },
  ],
  threads: [
    { id: 't-0059', title: 'tools', session: 's-9', state: 'working',
      task: { id: 'T64', title: 'Tools', status: 'started', steps_done: 3, steps_total: 5, current: 'mod tools' }, pr: '#121 open, checks pending' },
    { id: 't-0061', title: 'pane', task: { id: 'T63', title: 'Dashboard pane', status: 'review', steps_done: 7, steps_total: 7 }, done: true },
  ],
  ready: [{ task: 'T71', title: 'Next thing', status: 'ready' }],
  ...over,
})

type Seen = {
  spawned: string[][]
  commands: string[]
  opened: string[]
  closed: string[]
  submitted: { text: string; asUser?: boolean }[]
  fills: string[]
  toasts: string[]
}

// start runs session.start in a terminatr session of role, with a tm
// watch --project that prints lines, and records what the mod did.
async function start($: Engine, on: On, role: string, lines: TerminatrProject[], env: Record<string, string> = {}) {
  const clock = mock.clock(on)
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-1', TERMINATR_PROJECT: 'demo', TERMINATR_ROLE: role, ...env })
  const seen: Seen = { spawned: [], commands: [], opened: [], closed: [], submitted: [], fills: [], toasts: [] }
  const open = new Set<string>()
  on('process.spawn', async function* (_$, e) {
    seen.spawned.push([...e.argv])
    if (e.argv.includes('--project')) {
      for (const l of lines) yield { stream: 'stdout' as const, text: JSON.stringify(l) + '\n' }
    }
    return { value: { code: 0, signal: null } }
  })
  on('process.run', async (_$, e) => ({
    value: { exitCode: 0, stdout: JSON.stringify({ thread: {}, report_text: `## Report\nreport of ${e.argv[3]}` }), stderr: '', isStdoutTruncated: false, isStderrTruncated: false },
  }))
  on('command.register', async (_$, e) => {
    seen.commands.push(e.name)
    return { value: { command: e.name } }
  })
  on('ui.open', async (_$, e) => {
    seen.opened.push(e.id)
    open.add(e.id)
    return { value: { isPlaced: true as const } }
  })
  on('ui.close', async (_$, e) => {
    seen.closed.push(e.id)
    open.delete(e.id)
    return { value: undefined }
  })
  on('ui.panes', async () => ({
    value: [...open].map(id => ({ id, title: id, isShown: true, isFocused: false, isPlaced: true })),
  }))
  on('ui.status', async () => ({ value: undefined }))
  on('ui.toast', async (_$, e) => {
    seen.toasts.push(e.text)
    return { value: undefined }
  })
  on('prompt.submit', async (_$, e) => {
    seen.submitted.push({ text: e.text, asUser: e.origin.kind === 'plugin' ? e.origin.asUser : undefined })
    return { text: e.text }
  })
  on('prompt.fill', async (_$, e) => {
    seen.fills.push(e.text)
    return { isFilled: true }
  })
  on('ui.render', { component: 'AbovePrompt' }, async ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text key="engine">engine</Text>
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))
  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  await clock.settle()
  return { seen, clock }
}

// tmCommand is /tm as the person types it at the prompt.
const tmCommand = { command: 'tm', args: '', origin: { kind: 'composer' as const }, presentation: { isFullscreen: false, columns: 100 } }

const BAND = { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 120, scroll: { offset: 0, bodyRows: 7 }, view: {} }

// draw draws the band once, as the terminal does, saying whether its
// layout docks a pane.
async function draw($: Engine, isFullscreen: boolean) {
  const viewport: RenderViewport = { columns: 170, rows: 48, isFullscreen }
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', component: 'AbovePrompt', props: BAND, viewport })
  await ui.unmount()
}

const pane = (bodyColumns: number) => ({
  component: 'Pane' as const,
  requestId: 'tm',
  props: { title: 'tm', isFocused: false, bodyColumns, placement: 'dock' as const, scroll: { offset: 0, bodyRows: 40 }, view: {} },
})

// texts are the Texts and Buttons drawn, in order.
async function texts(ui: { findAll: (q: { type: string }) => Promise<{ type: string; text: string }[]> }) {
  const all = [...(await ui.findAll({ type: 'Text' })), ...(await ui.findAll({ type: 'Button' }))]
  return all.map(e => e.text)
}

// T82: a coordinator whose nudge is held while it idles.
const stuck = { why: 'queue' as const, session: 's-86', title: '1 queued prompt for the coordinator, held 3m0s: prompt box not empty' }

test('a held queue leads the pane, in red, naming its session, with no buttons', async ($, on) => {
  expect(needButtons(stuck, '')).toEqual([])
  await start($, on, 'coordinator', [project({ needs_you: [stuck, ...project().needs_you] })])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', ...pane(120) })
  const t = await texts(ui)
  const at = (s: string) => t.findIndex(x => x.includes(s))
  expect(t[at('s-86')]).toBe('s-86 stuck 1 queued prompt for the coordinator, held 3m0s: prompt box not empty')
  expect(at('Needs you')).toBeLessThan(at('s-86'))
  expect(at('s-86')).toBeLessThan(at('T63'))
  expect(t).toContain('  the coordinator hears of nothing until it clears; tm agent explain s-86 says why')
  await ui.unmount()
})

test('askLine is what the coordinator reads; asked holds while the status does', () => {
  expect(askLine('accept', 'T63')).toBe('accept T63')
  expect(askLine('send-back', 'T63', { note: '  fix\nthe  title ' })).toBe('send T63 back: fix the title')
  expect(askLine('merge', 'T63', { pr: 120 })).toBe('merge PR #120 for T63')
  expect(askLine('delegate', 'T70')).toBe('delegate T70')
  const sent = [{ task: 'T63', kind: 'accept', status: 'review' }]
  expect(asked('T63', 'review', undefined, sent)).toBe('accept')
  expect(asked('T63', 'started', undefined, sent)).toBe('')
  expect(asked('T63', 'started', 'send-back', sent)).toBe('send-back')
})

test('needButtons: review gets accept, send back and merge when mergeable; asked gets none', () => {
  const [ready, asked, question, ci, blocked] = project().needs_you
  expect(needButtons(ready!, '')).toEqual(['accept', 'send-back', 'merge', 'report'])
  expect(needButtons({ ...ready!, mergeable: false }, '')).toEqual(['accept', 'send-back', 'report'])
  expect(needButtons(asked!, 'accept')).toEqual([])
  expect(needButtons(question!, '')).toEqual(['report'])
  expect(needButtons(ci!, '')).toEqual(['report'])
  expect(needButtons(blocked!, '')).toEqual(['delegate'])
})

test('projectFeed joins split lines; threadLine is compact', () => {
  const l = JSON.stringify(project()) + '\n'
  let got = projectFeed('', l.slice(0, 30))
  expect(got.projects).toEqual([])
  got = projectFeed(got.rest, l.slice(30) + 'nope\n{"x":1}\n')
  expect(got.projects.map(p => p.project)).toEqual(['demo'])
  const [running, done] = project().threads
  expect(threadLine(running!)).toBe('t-0059 T64 working 3/5 ▸ mod tools · #121 open, checks pending')
  expect(threadLine(done!)).toBe('t-0061 T63 done 7/7')
})

test('a coordinator follows its project, registers /tm and opens the pane where it docks', async ($, on) => {
  const { seen, clock } = await start($, on, 'coordinator', [project()])
  expect(seen.spawned).toContainEqual(['/opt/tm', 'watch', '--project', 'demo', '--json'])
  expect(seen.commands).toEqual(['tm'])
  expect(seen.opened).toEqual([])
  await draw($, true)
  await clock.advance(300)
  expect(seen.opened).toEqual(['tm'])
})

test('on the main screen it waits for /tm', async ($, on) => {
  const { seen, clock } = await start($, on, 'coordinator', [project()])
  await draw($, false)
  await clock.advance(20_000)
  expect(seen.opened).toEqual([])
})

test('[mods] pane = false: no pane by itself, /tm still toggles it', async ($, on) => {
  const { seen, clock } = await start($, on, 'coordinator', [project()], { TERMINATR_PANE: 'off' })
  await draw($, true)
  await clock.advance(20_000)
  expect(seen.opened).toEqual([])
  expect((await $.command.run(tmCommand)).text).toBe('tm pane opened.')
  expect(seen.opened).toEqual(['tm'])
  expect((await $.command.run(tmCommand)).text).toBe('tm pane closed.')
  expect(seen.closed).toEqual(['tm'])
})

test('a thread gets no pane, no /tm and no project feed', async ($, on) => {
  const { seen, clock } = await start($, on, 'thread', [project()])
  await draw($, true)
  await clock.advance(20_000)
  expect(seen.commands).toEqual([])
  expect(seen.opened).toEqual([])
  expect(seen.spawned.some(a => a.includes('--project'))).toBe(false)
})

test('the pane lists what needs the user first, then inbox, threads and the deck, on every surface', async ($, on) => {
  await start($, on, 'coordinator', [project({ needs_you: [] }), project()])
  for (const surface of ['terminal', 'desktop', 'vscode', 'mobile'] as const) {
    for (const cols of [80, 40]) {
      const ui = await $.ui.mount({ plugin: 'terminatr', surface, ...pane(cols) })
      const t = await texts(ui)
      const at = (s: string) => t.findIndex(x => x.includes(s))
      expect(t[0]).toBe('demo · 5 need you · 2 in inbox · 2 threads')
      expect(at('Needs you')).toBeLessThan(at('T63'))
      expect(at('T63')).toBeLessThan(at('T61'))
      expect(at('T61')).toBeLessThan(at('T66'))
      expect(at('T66')).toBeLessThan(at('T67'))
      expect(at('T67')).toBeLessThan(at('T70'))
      expect(at('T70')).toBeLessThan(at('Inbox'))
      expect(at('Inbox')).toBeLessThan(at('Threads'))
      expect(at('Threads')).toBeLessThan(at('On deck'))
      expect(t).toContain('  “which file?”')
      expect(t).toContain('  report x3 T64 handed in report 3 Tools')
      expect(t).toContain('  asked the coordinator: accept')
      expect((await ui.find({ key: 'merge-T63' }))?.text).toBe('Merge')
      expect(await ui.find({ key: 'accept-T61' })).toBeUndefined()
      expect(await ui.find({ key: 'merge-T67' })).toBeUndefined()
      expect((await ui.find({ key: 'thread-t-0059' }))?.text).toContain('t-0059 T64 working 3/5')
      await ui.unmount()
    }
  }
})

test('buttons send the coordinator a line in the user\'s words', async ($, on) => {
  const { seen } = await start($, on, 'coordinator', [project()])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', ...pane(80) })
  await ui.press({ key: 'merge-T63' })
  await ui.press({ key: 'delegate-T70' })
  await ui.press({ key: 'delegate-T71' })
  expect(seen.submitted).toEqual([
    { text: 'merge PR #120 for T63', asUser: true },
    { text: 'delegate T70', asUser: true },
    { text: 'delegate T71', asUser: true },
  ])
  // Sent: the buttons give way to what was asked.
  expect(await ui.find({ key: 'accept-T63' })).toBeUndefined()
  expect(await ui.find({ key: 'delegate-T71' })).toBeUndefined()
  expect(seen.toasts).toContain('Sent the coordinator: delegate T70')
  await ui.unmount()
})

test('send back takes a note in the pane, or the prompt box on a phone', async ($, on) => {
  const { seen } = await start($, on, 'coordinator', [project()])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', ...pane(80) })
  await ui.press({ key: 'send-back-T63' })
  await ui.input({ key: 'note-T63', text: '   ' })
  expect(seen.submitted).toEqual([])
  await ui.input({ key: 'note-T63', text: 'rename the setting' })
  expect(seen.submitted).toEqual([{ text: 'send T63 back: rename the setting', asUser: true }])
  expect(await ui.find({ key: 'note-T63' })).toBeUndefined()
  await ui.unmount()
})

test('on a phone, which draws no field, send back puts the line in the prompt box', async ($, on) => {
  const { seen } = await start($, on, 'coordinator', [project()])
  const phone = await $.ui.mount({ plugin: 'terminatr', surface: 'mobile', ...pane(40) })
  await phone.press({ key: 'send-back-T63' })
  expect(seen.fills).toEqual(['send T63 back: '])
  expect(seen.submitted).toEqual([])
  await phone.unmount()
})

test('Report opens the thread\'s report in a pane of its own', async ($, on) => {
  const { seen } = await start($, on, 'coordinator', [project()])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'mobile', ...pane(40) })
  await ui.press({ key: 'report-T66' })
  expect(seen.opened).toContain('tm-report')
  await ui.unmount()
  const r = await $.ui.mount({ plugin: 'terminatr', surface: 'mobile', component: 'Pane', requestId: 'tm-report',
    props: { ...pane(40).props, title: 't-0058 report' } })
  expect((await r.find({ key: 'report' }))?.props.text).toBe('## Report\nreport of t-0058')
  await r.press({ key: 'close-report' })
  expect(seen.closed).toContain('tm-report')
  await r.unmount()
})

test('an inbox row reads count, task, what happened, then the title', () => {
  const [, report] = project().inbox
  expect(itemParts(report!)).toEqual({ head: 'x3 T64 handed in report 3', title: 'Tools' })
  expect(itemParts({ id: 'i', kind: 'idle', subject: 't-1', summary: 'plain', count: 1, what: 'plain' })).toEqual({ head: 'plain', title: '' })
  expect(['report', 'pr-conflict', 'accept'].map(itemTone)).toEqual(['success', 'error', 'warning'])
})

const ctx = (percent: number, hint = false) => ({ tokens: percent * 2000, window: 200_000, percent, threshold: 40, hint })

test('the context line is coloured by how full it is, with the hint from the threshold', () => {
  expect(contextLine(ctx(12))).toEqual({ text: 'context 24k / 200k · 12%', tone: 'ok', hint: '' })
  const warn = contextLine(ctx(42, true))
  expect(warn.tone).toBe('warning')
  expect(warn.hint).toContain('/clear')
  expect(contextLine(ctx(85, true)).tone).toBe('error')
  expect(contextLine({ tokens: 400_000, window: 1_000_000, percent: 40, threshold: 40, hint: true }).text).toBe('context 400k / 1.0M · 40%')
})

test('one toast per crossing of the threshold', () => {
  expect(contextToast(false, ctx(41, true))).toContain('41%')
  expect(contextToast(true, ctx(45, true))).toBeUndefined()
  expect(contextToast(false, ctx(10))).toBeUndefined()
  expect(contextToast(false, undefined)).toBeUndefined()
})
