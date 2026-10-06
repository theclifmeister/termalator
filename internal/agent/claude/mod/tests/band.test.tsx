import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import type { TerminatrWatch } from '../types'
import { ciToast, statusText } from '../hooks/view'

const thread = (over: Partial<TerminatrWatch> = {}): TerminatrWatch => ({
  session: { id: 's-7', role: 'thread', project: 'demo', thread: 't-0044', state: 'working' },
  task: { id: 'T50', title: 'Band', status: 'started', steps_done: 1, steps_total: 3, current: 'Write the band' },
  pr: '#12 open, checks pending',
  needs_you: 2,
  inbox: 0,
  queued_prompts: 0,
  ...over,
})

const coordinator = (needs: number, inbox: number): TerminatrWatch => ({
  session: { id: 's-1', role: 'coordinator', project: 'demo', state: 'idle' },
  task: null,
  pr: '',
  needs_you: needs,
  inbox,
  queued_prompts: 0,
})

// text is the band's Text whose own text is t, nested or not.
async function text(ui: { findAll: (q: { type: string }) => Promise<{ text: string; props: Record<string, unknown> }[]> }, t: string) {
  return (await ui.findAll({ type: 'Text' })).find(e => e.text === t)
}

// rows are the texts of the band's rows: the Texts right under its Box.
async function rows(ui: { drawn: () => Promise<unknown> }): Promise<string[]> {
  const text = (n: unknown): string =>
    typeof n === 'string' ? n : Array.isArray((n as { children?: unknown }).children) ? (n as { children: unknown[] }).children.map(text).join('') : ''
  const band = (await ui.drawn()) as { children: unknown[] }
  return band.children.map(text)
}

const band = (bodyColumns: number) => ({
  component: 'AbovePrompt' as const,
  props: { hasSurvey: false, isWorking: true, maxRows: 8, bodyColumns, scroll: { offset: 0, bodyRows: 7 }, view: {} },
})

// start runs session.start with a tm watch that prints lines, and
// answers what the mod pinned as its status entry and toasted.
async function start($: Engine, on: On, lines: TerminatrWatch[], env: Record<string, string> = {}) {
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7', ...env })
  const statuses: (string | undefined)[] = []
  const toasts: string[] = []
  let done = false
  on('process.spawn', async function* () {
    for (const l of lines) yield { stream: 'stdout' as const, text: JSON.stringify(l) + '\n' }
    done = true
    return { value: { code: 0, signal: null } }
  })
  on('ui.status', async (_$, e) => {
    statuses.push(e.text)
    return { value: undefined }
  })
  on('ui.toast', async (_$, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  // The engine's own band: a plugin that passes leaves it.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e) => {
    const { Text } = $.ui.resolve(e)
    return <Text key="engine">engine</Text>
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))
  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  for (let i = 0; i < 1000 && !done; i++) await Promise.resolve()
  for (let i = 0; i < 100; i++) await Promise.resolve()
  return { statuses, toasts }
}

test('a thread band shows task, steps, current item and PR on every surface that has it', async ($, on) => {
  await start($, on, [thread({ pr: '#12 open, 2 checks failed, behind main' })])
  for (const surface of ['terminal', 'desktop'] as const) {
    for (const cols of [140, 60]) {
      const ui = await $.ui.mount({ plugin: 'terminatr', surface, ...band(cols) })
      const head = 'T50 · steps 1/3 · #12 open, 2 checks failed, behind main'
      // From 140 columns it all fits on one row; at 60 the current item
      // takes a row of its own, and the first is cut at its end.
      expect(await rows(ui)).toEqual(cols === 140 ? [head + '  now: Write the band'] : [head, 'now: Write the band'])
      expect((await text(ui, 'T50'))?.props.bold).toBe(true)
      expect((await text(ui, ' · #12 open, 2 checks failed, behind main'))?.props.color).toBe('error')
      expect(await text(ui, ' · 2 need you')).toBeUndefined()
      const drawn = (await ui.drawn()) as { children: { props: { wrap?: string } }[] }
      expect(drawn.children.map(r => r.props.wrap)).toEqual(drawn.children.map(() => 'truncate-end'))
      await ui.unmount()
    }
  }
})

test('the status entry carries the same, short, and clears when the session exits', async ($, on) => {
  const { statuses } = await start($, on, [thread(), thread({ session: { id: 's-7', role: 'thread', state: 'exited' } })])
  expect(statuses).toEqual(['T50 steps 1/3 · #12 open, checks pending · 2 need you', undefined])
})

test('quiet with no task: a thread without one, a coordinator with nothing waiting', async ($, on) => {
  const { statuses } = await start($, on, [thread({ task: null, pr: '', needs_you: 0 })])
  expect(statuses).toEqual([undefined])
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ plugin: 'terminatr', surface, ...band(120) })
    expect(await ui.find({ key: 'band' })).toBeUndefined()
    expect(await ui.find({ type: 'Text', text: 'engine' })).toBeDefined()
    await ui.unmount()
  }
  expect(statusText(coordinator(0, 0))).toBeUndefined()
})

test('a coordinator shows what waits for the user in the status entry only', async ($, on) => {
  const { statuses } = await start($, on, [coordinator(3, 1)])
  expect(statuses).toEqual(['3 need you · 1 in inbox'])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', ...band(120) })
  expect(await ui.find({ key: 'band' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine' })).toBeDefined()
  await ui.unmount()
})

test('the band and the status entry never repeat the same text', async ($, on) => {
  const { statuses } = await start($, on, [thread({ inbox: 1 })])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', ...band(140) })
  const drawn = (await rows(ui)).join(' ')
  expect(statuses).toEqual(['T50 steps 1/3 · #12 open, checks pending · 2 need you · 1 in inbox'])
  for (const c of ['need you', 'needs you', 'in inbox']) expect(drawn).not.toContain(c)
  await ui.unmount()
})

test('a thread waiting on the user says so on a row of its own', async ($, on) => {
  await start($, on, [thread({ session: { id: 's-7', role: 'thread', state: 'blocked', needs_you: 'which port?' } })])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', ...band(140) })
  expect(await rows(ui)).toEqual([
    'T50 · steps 1/3 · #12 open, checks pending', 'now: Write the band', 'needs you: which port?'])
  await ui.unmount()
})

test('a toast when the CI run finishes, once', async ($, on) => {
  const { toasts } = await start($, on, [
    thread(),
    thread({ pr: '#12 open, checks pending', needs_you: 3 }),
    thread({ pr: '#12 open, 1 check failed' }),
    thread({ pr: '#12 open, 1 check failed' }),
    thread({ pr: '#12 open, checks pending' }),
    thread({ pr: '#12 open, checks pass, approved' }),
  ])
  expect(toasts).toEqual(['T50 #12: 1 check failed', 'T50 #12: checks passed'])
  expect(ciToast(null, thread({ pr: '#12 open, checks pass' }))).toBeUndefined()
})

test('[mods] band = false: no band, no status entry, no toast', async ($, on) => {
  const { statuses, toasts } = await start($, on, [thread(), thread({ pr: '#12 open, checks pass' })], { TERMINATR_BAND: 'off' })
  expect(statuses).toEqual([])
  expect(toasts).toEqual([])
  const ui = await $.ui.mount({ plugin: 'terminatr', surface: 'terminal', ...band(120) })
  expect(await ui.find({ key: 'band' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: 'engine' })).toBeDefined()
  await ui.unmount()
})

test('mobile and vscode: the band validates there too, the status entry carries it', async ($, on) => {
  const { statuses } = await start($, on, [thread()])
  expect(statuses).toEqual(['T50 steps 1/3 · #12 open, checks pending · 2 need you'])
  for (const surface of ['mobile', 'vscode'] as const) {
    const ui = await $.ui.mount({ plugin: 'terminatr', surface, ...band(40) })
    expect(await rows(ui)).toEqual(['T50 · steps 1/3 · #12 open, checks pending', 'now: Write the band'])
    await ui.unmount()
  }
})
