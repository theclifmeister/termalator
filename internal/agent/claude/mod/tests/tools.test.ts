import { expect, mock, test } from 'claude-code/testing'
import type { On } from 'claude-code'
import type { Engine } from 'claude-code/testing'

import { answer, body, TOOLS } from '../hooks/tools'

const SOCKET = '/run/s/s-7/mod.sock'

type Call = { url: string; body: unknown }

// start runs session.start in a terminatr session of role, with the
// mod's socket, answering POST /v1/tools/<name> with reply, and records
// the tools registered and the calls; beneath stands for the user's
// rules, and the tool's description as the engine has it.
async function start($: Engine, on: On, role: string, reply: (name: string) => { status: number; text: string } | Error) {
  const clock = mock.clock(on)
  mock.env(on, { TERMINATR_SESSION: 's-7', TERMINATR_MOD_SOCKET: SOCKET, TERMINATR_ROLE: role })
  const calls: Call[] = []
  const tools: string[] = []
  on('tool.register', async (_$, e) => {
    tools.push(`mcp__terminatr__${e.name}`)
    return { value: { tool: `mcp__terminatr__${e.name}` } }
  })
  on('tool.check', async (_$, e) => (e.tool === 'mcp__terminatr__done' ? { decision: 'deny', reason: 'no' } : { decision: 'ask' }))
  on('tool.describe', async (_$, e) => ({ description: e.description, isDeferred: true }))
  on('ui.log', async () => ({ value: undefined }))
  on('http.fetch', async (_$, e) => {
    expect(e.init?.socketPath).toBe(SOCKET)
    const m = /\/v1\/tools\/([a-z]+)$/.exec(e.url)
    if (!m) return { value: { status: e.url.includes('/v1/prompts') ? 410 : 204, ok: true, headers: {}, text: '' } }
    calls.push({ url: e.url, body: JSON.parse(e.init?.body ?? 'null') })
    const r = reply(m[1])
    if (r instanceof Error) return { deny: r.message }
    return { value: { status: r.status, ok: r.status < 300, headers: {}, text: r.text } }
  })
  on('session.start', async (_$, e) => ({ cwd: e.cwd }))
  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  await clock.settle()
  return { calls, tools: () => [...tools].sort() }
}

test("a thread gets its tools; each call goes to the server and answers what it said", async ($, on) => {
  const { calls, tools } = await start($, on, 'thread', name =>
    name === 'steps' ? { status: 422, text: '{"error":"T5 step 1 checked\\ntm: refused: no step 9"}' } : { status: 200, text: '{"text":"t-0001 handed in report 2"}' })
  expect(tools()).toEqual(['mcp__terminatr__done', 'mcp__terminatr__report', 'mcp__terminatr__status', 'mcp__terminatr__steps'])

  const r = await $.tool.call({ tool: 'mcp__terminatr__report', report: 'Did it.', next: ['Merge the PR'] })
  expect(r).toEqual({ result: 't-0001 handed in report 2' })
  expect(calls[0]).toEqual({ url: 'http://terminatr/v1/tools/report', body: { report: 'Did it.', next: ['Merge the PR'] } })

  // A refusal reaches the model as the tool's error, with what got done.
  const s = await $.tool.call({ tool: 'mcp__terminatr__steps', check: [1, 9] })
  expect(s.deny).toContain('no step 9')
  expect(s.deny).toContain('step 1 checked')
})

test("the tools run unasked unless a rule beneath denies them, and are listed up front", async ($, on) => {
  await start($, on, 'thread', () => ({ status: 200, text: '{"text":"ok"}' }))
  expect((await $.tool.check({ tool: 'mcp__terminatr__status', activity: 'x' })).decision).toBe('allow')
  expect((await $.tool.check({ tool: 'mcp__terminatr__done' })).decision).toBe('deny')
  const d = await $.tool.describe({ tool: 'mcp__terminatr__report', description: 'x', isDeferred: true, provider: { plugin: 'terminatr', tier: 'user' } })
  expect(d.isDeferred).toBe(false)
})

test('a coordinator gets no tools', async ($, on) => {
  const { tools } = await start($, on, 'coordinator', () => new Error('no route'))
  expect(tools()).toEqual([])
})

test("the server not answering is the tool's error, naming the shell command", async ($, on) => {
  await start($, on, 'thread', () => new Error('connect ENOENT'))
  const r = await $.tool.call({ tool: 'mcp__terminatr__status', needs_you: 'which port?' })
  expect(r.deny).toContain('connect ENOENT')
  expect(r.deny).toContain('tm status')
})

test('a body carries the tool fields alone; a reply reads as a result or a deny', () => {
  const status = TOOLS.find(t => t.name === 'status')!
  expect(JSON.parse(body(status, { tool: 'mcp__terminatr__status', tool_use_id: 'tu-1', agentId: 'a', percent: 40, activity: 'x' }))).toEqual({ percent: 40, activity: 'x' })
  expect(answer(200, '{"text":"t-0001: 40% self"}')).toEqual({ result: 't-0001: 40% self' })
  expect(answer(204, '')).toEqual({ result: 'done' })
  expect(answer(400, '{"error":"bad tool input: percent takes 0-100"}')).toEqual({ deny: 'bad tool input: percent takes 0-100' })
  expect(answer(502, 'bad gateway')).toEqual({ deny: '502 bad gateway' })
  for (const t of TOOLS) {
    expect(t.name).toMatch(/^[a-z_]+$/)
    expect(t.description.length).toBeLessThan(2048)
  }
})
