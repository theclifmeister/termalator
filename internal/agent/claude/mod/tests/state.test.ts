import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import { initialTurn, stateOf, step, waitKey } from '../hooks/turn'

const SOCKET = '/run/s/s-7/mod.sock'

type Report = { state: string; reason?: string; event: string }

// What the engine beneath answers: an AskUserQuestion call (run while
// the menu is open), a permission request's hooks.
type Beneath = {
  ask?: () => Promise<void>
  decide?: (tool: string) => { decision?: { behavior: 'allow' } }
  // wedged: the server takes a minute to answer each report.
  wedged?: boolean
}

// start runs session.start in a terminatr session with the mod's socket
// (or env), and answers the reports the mod posts there, as they come.
async function start($: Engine, on: On, env: Record<string, string> = { TERMINATR_MOD_SOCKET: SOCKET }, beneath: Beneath = {}) {
  const clock = mock.clock(on)
  mock.env(on, { TERMINATR_BIN: '/opt/tm', TERMINATR_SESSION: 's-7', ...env })
  const reports: Report[] = []
  const sockets: (string | undefined)[] = []
  on('http.fetch', async (_$, e) => {
    // No prompts queued: polls come back empty (deliver.test.ts).
    if (e.url.includes('/v1/prompts')) return { value: { status: 204, ok: true, headers: {}, text: '' } }
    sockets.push(e.init?.socketPath)
    reports.push(JSON.parse(e.init?.body ?? '{}') as Report)
    if (beneath.wedged) await clock.sleep(60_000)
    return { value: { status: 204, ok: true, headers: {}, text: '' } }
  })
  on('process.spawn', async function* () {
    return { value: { code: 0, signal: null } }
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))
  on('turn.start', async (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', async (_$, e) => ({ text: e.answer }))
  on('session.end', async (_$, e) => ({ sessionId: e.sessionId }))
  on('tool.call', { tool: 'AskUserQuestion' }, async () => {
    await beneath.ask?.()
    return { result: { answers: {} } }
  })
  on('classic.PermissionRequest', async (_$, e) => beneath.decide?.(e.tool_name) ?? {})
  on('classic.PostToolUse', async () => ({}))
  on('classic.PermissionDenied', async () => ({}))
  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  await clock.settle()
  return { clock, reports, sockets }
}

const states = (rs: Report[]) => rs.map(r => (r.reason ? `${r.state}/${r.reason}` : r.state))

const complete = (reason: 'answer' | 'aborted' | 'error', agentId?: string) => ({
  answer: '', durationMs: 5, isAborted: reason === 'aborted', turnId: 't1', reason, ...(agentId ? { agentId } : {}),
})

test('a turn: working, a question, a permission prompt, then idle; transitions only', async ($, on) => {
  let duringQuestion = ''
  const { clock, reports, sockets } = await start($, on, undefined, {
    ask: async () => {
      await clock.settle()
      duringQuestion = states(reports).at(-1) ?? ''
    },
  })

  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.turn.start({ text: 'go', turnId: 't1' }) // the same state: not sent again
  await $.tool.call({ tool: 'AskUserQuestion', questions: [] } as never)
  await $.classic.PermissionRequest({ tool_name: 'Bash', tool_input: { command: 'make' } })
  await $.classic.PostToolUse({ tool_name: 'Read', tool_input: {}, tool_response: {}, tool_use_id: 'u1' }) // another tool
  await clock.settle()
  expect(states(reports).at(-1)).toBe('blocked/permission')
  await $.classic.PostToolUse({ tool_name: 'Bash', tool_input: {}, tool_response: {}, tool_use_id: 'u2' })
  await $.turn.complete(complete('aborted'))
  await clock.settle()

  expect(duringQuestion).toBe('blocked/question')
  expect(states(reports)).toEqual(['idle', 'working', 'blocked/question', 'working', 'blocked/permission', 'working', 'idle/interrupted'])
  expect(reports.map(r => r.event)).toEqual([
    'session.start', 'turn.start', 'tool.call', 'tool.call', 'PermissionRequest', 'PostToolUse', 'turn.complete'])
  expect(sockets.every(s => s === SOCKET)).toBe(true)
})

test('a heartbeat every 10 s carries the state held', async ($, on) => {
  const { clock, reports } = await start($, on)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await clock.advance(10_000)
  await clock.advance(10_000)
  expect(reports.map(r => `${r.event}:${r.state}`)).toEqual(['session.start:idle', 'turn.start:working', 'beat:working', 'beat:working'])
})

test('a prompt a hook beneath decided opens no dialog; a denial closes one', async ($, on) => {
  const { clock, reports } = await start($, on, undefined, {
    decide: tool => (tool === 'Write' ? { decision: { behavior: 'allow' } } : {}),
  })
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.classic.PermissionRequest({ tool_name: 'Write', tool_input: {} })
  await $.classic.PermissionRequest({ tool_name: 'WebFetch', tool_input: {} })
  await $.classic.PermissionDenied({ tool_name: 'WebFetch', tool_input: {}, tool_use_id: 'u3', reason: 'no' })
  await clock.settle()
  expect(states(reports)).toEqual(['idle', 'working', 'blocked/permission', 'working'])
})

test("a subagent's run doesn't end the turn; /clear starts idle; exit is reported", async ($, on) => {
  const { clock, reports } = await start($, on)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.turn.complete(complete('answer', 'a-1'))
  await clock.settle()
  expect(states(reports).at(-1)).toBe('working')
  await $.session.end({ reason: 'clear', sessionId: 'x', resume: { id: 'x' } })
  await $.session.end({ reason: 'prompt_input_exit', sessionId: 'y', resume: { id: 'y' } })
  await clock.settle()
  expect(states(reports)).toEqual(['idle', 'working', 'idle', 'exited'])
})

test("a wedged server holds no hook up, and the exit waits half a second for it", async ($, on) => {
  const { clock, reports } = await start($, on, undefined, { wedged: true })
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.tool.call({ tool: 'AskUserQuestion', questions: [] } as never)
  await $.turn.complete(complete('answer'))
  let ended = false
  const end = $.session.end({ reason: 'other', sessionId: 'y', resume: { id: 'y' } }).then(() => (ended = true))
  await clock.advance(499)
  expect(ended).toBe(false)
  await clock.advance(1)
  await end
  // Behind the first report in flight, only the latest state waited.
  expect(states(reports)).toEqual(['idle'])
  await clock.advance(60_000)
  expect(states(reports)).toEqual(['idle', 'exited'])
})

test('outside a session with the mod socket it reports nothing', async ($, on) => {
  const { clock, reports } = await start($, on, {})
  await $.turn.start({ text: 'go', turnId: 't1' })
  await clock.advance(30_000)
  expect(reports).toEqual([])
})

test('the turn: what each event leaves, and the state it reports', () => {
  let t = step(initialTurn, { kind: 'turn.start' })
  expect(stateOf(t)).toEqual({ state: 'working', reason: '' })
  t = step(t, { kind: 'wait', key: waitKey('Bash'), reason: 'permission' })
  expect(stateOf(step(t, { kind: 'unwait', key: waitKey('Bash', 'a-1') }))).toEqual({ state: 'blocked', reason: 'permission' })
  expect(stateOf(step(t, { kind: 'turn.complete', reason: 'error' }))).toEqual({ state: 'idle', reason: 'error' })
  expect(stateOf(step(initialTurn, { kind: 'compact', on: true }))).toEqual({ state: 'working', reason: '' })
  expect(stateOf(step(t, { kind: 'end' }))).toEqual({ state: 'exited', reason: '' })
})
